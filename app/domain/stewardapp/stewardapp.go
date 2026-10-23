// Package stewardapp is the screen where stewards add each other and turn
// each other's access off and on, and choose what they are told about people
// signing up for the days; and the one where each makes their own API keys
// (keys.go).
//
// There is no signing yourself up: an account here can change what every
// volunteer is told to pull. A steward adds another by address, and the new
// steward is sent a short note saying so, with the address of the sign-in
// page. The note carries no credential -- they ask for their own link, the
// same way as everybody else -- so a note that lands in the wrong inbox signs
// nobody in.
package stewardapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

const path = "/steward/stewards"

// DayNoticesPath is the part of the stewards screen where each steward
// chooses what they are told about people signing up for the days: the link
// at the foot of every such note.
const DayNoticesPath = path + "#day-notices"

// Users is the slice of userbus this app uses.
type Users interface {
	All(ctx context.Context) ([]userbus.User, error)
	Create(ctx context.Context, email types.Email, name string) (userbus.User, error)
	SetEnabled(ctx context.Context, actor, id types.ID, enabled bool) (userbus.User, error)
	SetDayNotices(ctx context.Context, id types.ID, n userbus.DayNotices) error

	CreateAPIKey(ctx context.Context, userID types.ID, name string) (userbus.APIKey, string, error)
	APIKeys(ctx context.Context, userID types.ID) ([]userbus.APIKey, error)
	RevokeAPIKey(ctx context.Context, userID, id types.ID) error
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Users  Users

	// Mail may be nil, and then the page says to tell the new steward
	// yourself instead of saying a note was sent.
	Mail mail.Sender

	// BaseURL is the public origin, for the sign-in address in the note.
	BaseURL string

	// DayNotices is whether stewards can be told about people signing up
	// for the days: the sign-up is on, and there is mail to tell them by.
	// Without it the screen does not offer a choice that would do nothing.
	DayNotices bool
}

type app struct{ cfg Config }

// Routes mounts the app, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	mux.Handle("GET "+path, guard(http.HandlerFunc(a.list)))
	mux.Handle("POST "+path, guard(http.HandlerFunc(a.add)))
	mux.Handle("POST "+path+"/{id}/access", guard(http.HandlerFunc(a.access)))

	if cfg.DayNotices {
		mux.Handle("POST "+path+"/day-notices", guard(http.HandlerFunc(a.dayNotices)))
	}

	mountKeys(mux, a, guard)
}

// The words this app says from Go, in English; Claude translates them
// through the translation memory, and say looks them up (page/words.go).
// The rest are in the templates, through t.
type wording struct {
	Sent, Added, Off, On, NotAnEmail, Taken types.Text

	// DayNotices is what the choice about the days was saved as.
	DayNoticesEach, DayNoticesDaily, DayNoticesOff types.Text

	// The note a new steward is sent, in plain text.
	MailSubject, MailText types.Text
}

var words = wording{
	Sent:       types.Text{EN: "Added. We emailed them to say so, with the address of the sign-in page."},
	Added:      types.Text{EN: "Added. Tell them to sign in at {address} with that address."},
	Off:        types.Text{EN: "Turned off. They are signed out everywhere and cannot sign in."},
	On:         types.Text{EN: "Turned back on. They can ask for a sign-in link again."},
	NotAnEmail: types.Text{EN: "That does not look like an email address. Check it for a typo."},
	Taken:      types.Text{EN: "A steward already has that address."},

	DayNoticesEach:  types.Text{EN: "Saved. We'll email you each time someone signs up for a day or cancels."},
	DayNoticesDaily: types.Text{EN: "Saved. We'll email you at 8 each morning when someone has signed up or cancelled since the morning before."},
	DayNoticesOff:   types.Text{EN: "Saved. We won't email you about sign-ups. The Days screen still shows who is coming."},

	MailSubject: types.Text{EN: "You are now a garden steward"},
	MailText: types.Text{EN: "You have been added as one of the garden stewards for the Schoenstatt Fathers' Trail of the Saints.\n\n" +
		"To sign in, open this page and give this address, {address}:\n\n{link}\n\n" +
		"We will email you a link. There is no password.\n\n" +
		"If you were not expecting this, you can ignore it; nothing happens unless you sign in.\n\n" +
		"-- The garden stewards\n"},
}

// Words is this app's copy held in Go, for the catalog the translation
// memory lists.
var Words = page.Catalog{
	{Where: "the stewards' list of stewards, and the API keys screen; Mail is the note a new steward is emailed", Words: words},
	{Where: "the API keys screen, for a steward", Words: keyWords},
}

type row struct {
	ID, Email, Name string
	Enabled, You    bool
}

type view struct {
	Stewards []row
	SignIn   string // the sign-in address, to tell a new steward

	// What was just typed into the add form, and what was wrong with it.
	Email, Name string
	Problems    map[string]any

	// Done says what just happened. Problem is a refusal about a row.
	Done, Problem any

	// DayNotices is whether to show the choice about the days, and SignUps
	// the signed-in steward's choice.
	DayNotices bool
	SignUps    string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	v := view{}

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("done") {
	case "sent":
		v.Done = words.Sent
	case "added":
		v.Done = page.Put(words.Added, "address", a.cfg.BaseURL+"/sign-in")
	case "off":
		v.Done = words.Off
	case "on":
		v.Done = words.On
	case "each":
		v.Done = words.DayNoticesEach
	case "daily":
		v.Done = words.DayNoticesDaily
	case "quiet":
		v.Done = words.DayNoticesOff
	}

	a.show(w, r, http.StatusOK, v)
}

func (a app) add(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := view{Email: r.PostFormValue("email"), Name: r.PostFormValue("name"), Problems: map[string]any{}}

	email, err := types.ParseEmail(v.Email)
	if err != nil {
		v.Problems["email"] = words.NotAnEmail
		a.show(w, r, http.StatusUnprocessableEntity, v)

		return
	}

	u, err := a.cfg.Users.Create(r.Context(), email, v.Name)

	invalid, isInvalid := errors.AsType[userbus.Invalid](err)

	switch {
	case errors.Is(err, userbus.ErrEmailTaken):
		v.Problems["email"] = words.Taken
		a.show(w, r, http.StatusUnprocessableEntity, v)

		return
	case isInvalid:
		v.Problems[invalid.Field] = types.Text{EN: page.Sentence(invalid.Problem)}
		a.show(w, r, http.StatusUnprocessableEntity, v)

		return
	case err != nil:
		a.fail(w, r, "adding a steward", err)

		return
	}

	a.cfg.Log.Info("steward added", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "by", me.ID.String())

	done := "added"
	if a.tell(r, u) {
		done = "sent"
	}

	http.Redirect(w, r, path+"?done="+done, http.StatusSeeOther)
}

// tell sends the new steward a note, and reports whether it went. A failure
// is not the steward's to fix and does not undo the account; the page says
// to tell them yourself instead.
func (a app) tell(r *http.Request, u userbus.User) bool {
	if a.cfg.Mail == nil {
		return false
	}

	// In the language of the steward adding them: who they know is the
	// best guess at the language they read.
	l := mid.LangFrom(r.Context())
	text := a.cfg.Render.Plain(l, page.Put(words.MailText, "address", u.Email.String(), "link", a.cfg.BaseURL+"/sign-in"))

	if err := a.cfg.Mail.Send(r.Context(), mail.Message{
		To:      u.Email.String(),
		Subject: a.cfg.Render.Plain(l, words.MailSubject),
		Text:    strings.ReplaceAll(text, "\n", "\r\n"),
	}); err != nil {
		a.cfg.Log.Error("a new steward could not be told", "request_id", web.RequestIDFrom(r.Context()), "user_id", u.ID.String(), "error", err)

		return false
	}

	return true
}

func (a app) access(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	enabled := r.PostFormValue("enabled") == "yes"

	_, err = a.cfg.Users.SetEnabled(r.Context(), me.ID, id, enabled)

	invalid, isInvalid := errors.AsType[userbus.Invalid](err)

	switch {
	case errors.Is(err, userbus.ErrNotFound):
		http.Error(w, "That steward is not here any more. Go back to the list of stewards.", http.StatusNotFound)

		return
	case isInvalid:
		a.show(w, r, http.StatusUnprocessableEntity, view{Problem: types.Text{EN: page.Sentence(invalid.Problem)}})

		return
	case err != nil:
		a.fail(w, r, "changing a steward's access", err)

		return
	}

	done := "off"
	if enabled {
		done = "on"
	}

	http.Redirect(w, r, path+"?done="+done, http.StatusSeeOther)
}

// dayNotices saves what the signed-in steward is told about the days. Their
// own choice and nobody else's: the form carries no account, so there is no
// steward to change but the one pressing it.
func (a app) dayNotices(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	signUps, err := userbus.ParseSignUps(r.PostFormValue("signups"))
	if err != nil {
		http.Error(w, "That is not one of the choices. Open the page again and choose one.", http.StatusBadRequest)

		return
	}

	// In the language of the screen they chose on; see userbus.DayNotices.
	if err := a.cfg.Users.SetDayNotices(r.Context(), me.ID, userbus.DayNotices{SignUps: signUps, Lang: mid.LangFrom(r.Context())}); err != nil {
		a.fail(w, r, "saving what a steward is told about the days", err)

		return
	}

	a.cfg.Log.Info("a steward's notes about the days changed", "request_id", web.RequestIDFrom(r.Context()), "user_id", me.ID.String(), "signups", string(signUps))

	done := map[userbus.SignUps]string{userbus.SignUpsEach: "each", userbus.SignUpsDaily: "daily"}[signUps]
	if done == "" {
		done = "quiet"
	}

	http.Redirect(w, r, path+"?done="+done+"#day-notices", http.StatusSeeOther)
}

func (a app) show(w http.ResponseWriter, r *http.Request, status int, v view) {
	me, _ := mid.StewardFrom(r.Context())

	all, err := a.cfg.Users.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing stewards", err)

		return
	}

	for _, u := range all {
		v.Stewards = append(v.Stewards, row{
			ID: u.ID.String(), Email: u.Email.String(), Name: u.Name,
			Enabled: u.Enabled, You: u.ID == me.ID,
		})

		if u.ID == me.ID {
			v.SignUps = string(u.DayNotices.SignUps)
		}
	}

	v.SignIn = a.cfg.BaseURL + "/sign-in"
	v.DayNotices = a.cfg.DayNotices

	a.cfg.Render.Render(w, r, status, "stewards", v)
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
