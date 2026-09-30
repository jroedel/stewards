// Package stewardapp is the screen where stewards add each other and turn
// each other's access off and on.
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

// Users is the slice of userbus this app uses.
type Users interface {
	All(ctx context.Context) ([]userbus.User, error)
	Create(ctx context.Context, email types.Email, name string) (userbus.User, error)
	SetEnabled(ctx context.Context, actor, id types.ID, enabled bool) (userbus.User, error)
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
}

type app struct{ cfg Config }

// Routes mounts the app, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	mux.Handle("GET "+path, guard(http.HandlerFunc(a.list)))
	mux.Handle("POST "+path, guard(http.HandlerFunc(a.add)))
	mux.Handle("POST "+path+"/{id}/access", guard(http.HandlerFunc(a.access)))
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
	Problems    map[string]string

	// Done says what just happened. Problem is a refusal about a row.
	Done, Problem string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	v := view{}

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("done") {
	case "sent":
		v.Done = "Added. We emailed them to say so, with the address of the sign-in page."
	case "added":
		v.Done = "Added. Tell them to sign in at " + a.cfg.BaseURL + "/sign-in with that address."
	case "off":
		v.Done = "Turned off. They are signed out everywhere and cannot sign in."
	case "on":
		v.Done = "Turned back on. They can ask for a sign-in link again."
	}

	a.show(w, r, http.StatusOK, v)
}

func (a app) add(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := view{Email: r.PostFormValue("email"), Name: r.PostFormValue("name"), Problems: map[string]string{}}

	email, err := types.ParseEmail(v.Email)
	if err != nil {
		v.Problems["email"] = "That does not look like an email address. Check it for a typo."
		a.show(w, r, http.StatusUnprocessableEntity, v)

		return
	}

	u, err := a.cfg.Users.Create(r.Context(), email, v.Name)

	invalid, isInvalid := errors.AsType[userbus.Invalid](err)

	switch {
	case errors.Is(err, userbus.ErrEmailTaken):
		v.Problems["email"] = "A steward already has that address."
		a.show(w, r, http.StatusUnprocessableEntity, v)

		return
	case isInvalid:
		v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
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

	signIn := a.cfg.BaseURL + "/sign-in"

	text := "You have been added as one of the garden stewards for the Schoenstatt Fathers' Trail of the Saints.\r\n\r\n" +
		"To sign in, open this page and give this address, " + u.Email.String() + ":\r\n\r\n" +
		signIn + "\r\n\r\n" +
		"We will email you a link. There is no password.\r\n\r\n" +
		"If you were not expecting this, you can ignore it; nothing happens unless you sign in.\r\n\r\n" +
		"-- The garden stewards\r\n"

	if err := a.cfg.Mail.Send(r.Context(), mail.Message{
		To:      u.Email.String(),
		Subject: "You are now a garden steward",
		Text:    text,
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
		a.show(w, r, http.StatusUnprocessableEntity, view{Problem: page.Sentence(invalid.Problem)})

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
	}

	v.SignIn = a.cfg.BaseURL + "/sign-in"

	a.cfg.Render.Render(w, r, status, "stewards", v)
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
