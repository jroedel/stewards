// Package signupapp is the email list for stewardship days: the form on the
// home page posts here, the confirmation and unsubscribe links land here, and
// the stewards see who is on the list here.
//
// The rules -- one step to sign up, the caps, a page that says the same
// thing whoever is asking -- are subscriberbus's; see there for why each one
// is. What this package adds is the welcome email and the pages around it.
//
// # The honeypot
//
// The form has a field named "website" that people never see: it is off
// screen, out of the tab order and hidden from screen readers. A bot filling
// in every field fills it in too, and its sign-up is answered with the usual
// "check your email" and nothing else. It catches only the laziest bots,
// and costs nothing; the caps are what hold against the rest.
//
// # Nothing here logs an address
//
// A subscriber is logged by their row's ID. The address is on the steward
// screen, behind sign-in, and in the mail itself, and nowhere else.
//
// # Why the unsubscribe link lands on a button
//
// Mail apps open the links in a message to check them before the reader
// does, which is why the sign-in link does the same (authapp). An
// unsubscribe link that acted on being opened would take people off the
// list who never asked.
//
// # The confirm address
//
// /subscribe/confirm is where the links in the first version's confirmation
// emails led, before sign-up became one step. It is kept as a page saying
// to sign up again from the home page, which now puts them straight on the
// list, so that a link in an inbox from those few days lands somewhere
// rather than on a 404.
package signupapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// The public addresses. FormPath is what the home page's form posts to.
const (
	FormPath    = "/subscribe"
	sentPath    = "/subscribe/sent"
	confirmPath = "/subscribe/confirm"
	leavePath   = "/unsubscribe"
	stewardPath = "/steward/subscribers"
)

// Honeypot is the name of the field people never fill in.
const Honeypot = "website"

// List is what this app needs from the subscriber rules.
type List interface {
	Request(ctx context.Context, email types.Email, lang types.Lang) (subscriberbus.Request, error)
	Unsubscribe(ctx context.Context, token string) error
	Remove(ctx context.Context, id types.ID) error
	All(ctx context.Context) ([]subscriberbus.Subscriber, error)
}

// Config is what this app needs. Mail is required: the welcome is what tells
// a person they are on the list and how to leave it, so main mounts this app
// only when a relay is configured.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	List    List
	Mail    mail.Sender
	BaseURL string
}

type app struct{ cfg Config }

// Routes mounts the public pages, and the stewards' list behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	mux.HandleFunc("POST "+FormPath, a.request)
	mux.HandleFunc("GET "+sentPath, a.sent)
	mux.HandleFunc("GET "+confirmPath, a.confirmGone)
	mux.HandleFunc("GET "+leavePath, a.leaveForm)
	mux.HandleFunc("POST "+leavePath, a.leave)

	mux.Handle("GET "+stewardPath, guard(http.HandlerFunc(a.list)))
	mux.Handle("POST "+stewardPath+"/{id}/remove", guard(http.HandlerFunc(a.remove)))
}

// The copy a newcomer reads. Spanish waits for a native speaker, as on the
// home page.
type wording struct {
	Eyebrow types.Text

	FormTitle, FormHelp, FormLabel, FormButton, NotAnEmail types.Text

	SentTitle, SentLead, SentSpam types.Text

	OldLinkTitle, OldLinkLead types.Text

	LeaveTitle, LeaveLead, LeaveButton, OffTitle, OffLead types.Text

	SeeDays types.Text
}

var words = wording{
	Eyebrow: types.Text{EN: "Stewardship days"},

	FormTitle:  types.Text{EN: "Hear about the next day"},
	FormHelp:   types.Text{EN: "Leave your email and we'll write when a stewardship day is scheduled. Nothing else, and you can stop any time."},
	FormLabel:  types.Text{EN: "Your email"},
	FormButton: types.Text{EN: "Keep me posted"},
	NotAnEmail: types.Text{EN: "That doesn't look like an email address. Check it for a typo."},

	SentTitle: types.Text{EN: "You're on the list"},
	SentLead:  types.Text{EN: "Thank you. We'll email you when a stewardship day is scheduled, and we've sent you a welcome now with a link to stop the emails any time."},
	SentSpam:  types.Text{EN: "If the welcome isn't there in a few minutes, look in your spam folder, or check the address for a typo and sign up again."},

	OldLinkTitle: types.Text{EN: "No need to confirm any more"},
	OldLinkLead:  types.Text{EN: "Signing up is one step now. If you're not on the list yet, sign up from the home page and you'll be on it straight away."},

	LeaveTitle:  types.Text{EN: "Stop these emails?"},
	LeaveLead:   types.Text{EN: "Press the button and we won't email you about stewardship days any more."},
	LeaveButton: types.Text{EN: "Stop the emails"},
	OffTitle:    types.Text{EN: "You're off the list"},
	OffLead:     types.Text{EN: "We won't email you about stewardship days any more. You're welcome on the trail any time."},

	SeeDays: types.Text{EN: "See the days scheduled"},
}

// ------------------------------------------------------------------ signing up

type formView struct {
	Copy    wording
	Email   string
	Problem types.Text
}

func (a app) request(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	// The bot answer: the page a person would see, and nothing done.
	if r.PostFormValue(Honeypot) != "" {
		a.cfg.Log.InfoContext(r.Context(), "sign-up caught by the honeypot", "request_id", web.RequestIDFrom(r.Context()))
		http.Redirect(w, r, sentPath, http.StatusSeeOther)

		return
	}

	typed := r.PostFormValue("email")

	email, err := types.ParseEmail(typed)
	if err != nil {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "signup-form", formView{Copy: words, Email: typed, Problem: words.NotAnEmail})

		return
	}

	lang := mid.LangFrom(r.Context())

	req, err := a.cfg.List.Request(r.Context(), email, lang)
	if err != nil {
		a.fail(w, r, "signing up", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "sign-up", "request_id", web.RequestIDFrom(r.Context()),
		"outcome", req.Outcome, "subscriber_id", idOrNone(req.Subscriber.ID))

	// They are on the list whether or not the welcome goes; a failure is
	// logged for a steward to see, and not shown, since signing up again
	// would only find them on the list already and send nothing.
	if req.Outcome == subscriberbus.Added {
		if err := a.cfg.Mail.Send(r.Context(), welcome(req.Subscriber.Email, a.cfg.BaseURL, a.leaveURL(req.Subscriber.Unsubscribe))); err != nil {
			a.cfg.Log.ErrorContext(r.Context(), "a welcome could not be sent", "request_id", web.RequestIDFrom(r.Context()),
				"subscriber_id", req.Subscriber.ID.String(), "error", err)
		}
	}

	http.Redirect(w, r, sentPath, http.StatusSeeOther)
}

func (a app) sent(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "signup-sent", formView{Copy: words})
}

// ------------------------------------------------------------------ old confirm links

type linkView struct {
	Copy  wording
	Token string
	Done  bool
}

func (a app) confirmGone(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "signup-old-link", linkView{Copy: words})
}

// ------------------------------------------------------------------ leaving

func (a app) leaveForm(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "unsubscribe", linkView{Copy: words, Token: r.URL.Query().Get("t")})
}

func (a app) leave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the link again and try once more.", http.StatusBadRequest)

		return
	}

	if err := a.cfg.List.Unsubscribe(r.Context(), r.PostFormValue("t")); err != nil {
		a.fail(w, r, "unsubscribing", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "unsubscribed", "request_id", web.RequestIDFrom(r.Context()))
	a.cfg.Render.Render(w, r, http.StatusOK, "unsubscribe", linkView{Copy: words, Done: true})
}

func (a app) leaveURL(token string) string {
	return a.cfg.BaseURL + leavePath + "?t=" + url.QueryEscape(token)
}

// ------------------------------------------------------------------ the emails

// welcome is the one email a stranger can cause -- anybody can type
// anybody's address -- so besides the thanks it says plainly what to do if
// it was not wanted, and the link off the list is in it from the first day.
// The emails a steward writes by hand carry no such link, which is another
// reason this one must.
func welcome(to types.Email, base, leave string) mail.Message {
	return mail.Message{
		To:      to.String(),
		Subject: "Thank you for signing up for stewardship days",
		Text: "Thank you for signing up to receive notifications about future stewardship days on the Schoenstatt Fathers' Trail of the Saints.\r\n\r\n" +
			"You don't need any experience or special skills.\r\n\r\n" +
			"The days already scheduled are here:\r\n" + base + "/\r\n\r\n" +
			"If you did not sign up for this, or want to stop these emails at any time, unsubscribe here:\r\n" + leave + "\r\n\r\n" +
			"-- The garden stewards\r\n",
	}
}

// ------------------------------------------------------------------ the stewards' list

type stewardRow struct {
	ID, Email, Since, Lang string
}

type stewardView struct {
	Rows []stewardRow
	All  string // every address, for the Bcc line
	Done string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	on, err := a.cfg.List.All(r.Context())
	if err != nil {
		a.fail(w, r, "reading the list", err)

		return
	}

	var v stewardView

	var all []string

	for _, s := range on {
		lang := "English"
		if s.Lang == types.Spanish {
			lang = "Spanish"
		}

		v.Rows = append(v.Rows, stewardRow{
			ID: s.ID.String(), Email: s.Email.String(), Lang: lang,
			Since: page.Date(s.CreatedAt).EN,
		})
		all = append(all, s.Email.String())
	}

	v.All = strings.Join(all, ", ")

	if r.URL.Query().Get("done") == "removed" {
		v.Done = "Removed from the list."
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "steward-subscribers", v)
}

func (a app) remove(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if err := a.cfg.List.Remove(r.Context(), id); err != nil && !errors.Is(err, subscriberbus.ErrNotFound) {
		a.fail(w, r, "removing a subscriber", err)

		return
	}

	me, _ := mid.StewardFrom(r.Context())
	a.cfg.Log.InfoContext(r.Context(), "subscriber removed by a steward", "request_id", web.RequestIDFrom(r.Context()),
		"subscriber_id", id.String(), "user_id", me.ID.String())

	http.Redirect(w, r, stewardPath+"?done=removed", http.StatusSeeOther)
}

// ------------------------------------------------------------------ the parts

func idOrNone(id types.ID) string {
	if id.Zero() {
		return "none"
	}

	return id.String()
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
