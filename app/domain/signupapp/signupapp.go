// Package signupapp is the email list for stewardship days: the form on the
// home page posts here, the confirmation and unsubscribe links land here, and
// the stewards see who is on the list here.
//
// The rules -- double opt-in, the caps, a page that says the same thing
// whoever is asking -- are subscriberbus's; see there for why each one is.
// What this package adds is the two emails and the pages around them.
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
// # Why every link lands on a button
//
// Mail apps open the links in a message to check them before the reader
// does, which is why the sign-in link does the same (authapp). A
// confirmation used up by a scanner would put on the list somebody who never
// pressed anything; an unsubscribe link opened by one would take somebody off
// who never asked.
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
	Confirm(ctx context.Context, presented string) (subscriberbus.Subscriber, error)
	Unsubscribe(ctx context.Context, token string) error
	Remove(ctx context.Context, id types.ID) error
	Confirmed(ctx context.Context) ([]subscriberbus.Subscriber, error)
	PendingCount(ctx context.Context) (int, error)
}

// Config is what this app needs. Mail is required: a sign-up that cannot send
// its confirmation is one that can never finish, so main mounts this app only
// when a relay is configured.
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
	mux.HandleFunc("GET "+confirmPath, a.confirmForm)
	mux.HandleFunc("POST "+confirmPath, a.confirm)
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

	ConfirmTitle, ConfirmLead, ConfirmButton, WhyButton types.Text
	OnTitle, OnLead, LinkFailed, SignUpAgain            types.Text

	LeaveTitle, LeaveLead, LeaveButton, OffTitle, OffLead types.Text

	SeeDays, CannotSend types.Text
}

var words = wording{
	Eyebrow: types.Text{EN: "Stewardship days"},

	FormTitle:  types.Text{EN: "Hear about the next day"},
	FormHelp:   types.Text{EN: "Leave your email and we'll write when a stewardship day is scheduled. Nothing else, and you can stop any time."},
	FormLabel:  types.Text{EN: "Your email"},
	FormButton: types.Text{EN: "Keep me posted"},
	NotAnEmail: types.Text{EN: "That doesn't look like an email address. Check it for a typo."},

	SentTitle: types.Text{EN: "Check your email"},
	SentLead:  types.Text{EN: "We've sent you a link. Open it and tap the button to finish signing up."},
	SentSpam:  types.Text{EN: "If it isn't there in a few minutes, look in your spam folder."},

	ConfirmTitle:  types.Text{EN: "One more tap"},
	ConfirmLead:   types.Text{EN: "Press the button to finish signing up for news of stewardship days."},
	ConfirmButton: types.Text{EN: "Yes, keep me posted"},
	WhyButton:     types.Text{EN: "Why a button? Mail apps open the links in a message to check them. If opening the link were enough, a mail app could sign you up without you."},
	OnTitle:       types.Text{EN: "You're on the list"},
	OnLead:        types.Text{EN: "Thank you. We'll email you when a stewardship day is scheduled. Every email has a link to stop them."},
	LinkFailed:    types.Text{EN: "That link has expired or has already been used. If you're not on the list yet, sign up again from the home page."},
	SignUpAgain:   types.Text{EN: "Back to the home page"},

	LeaveTitle:  types.Text{EN: "Stop these emails?"},
	LeaveLead:   types.Text{EN: "Press the button and we won't email you about stewardship days any more."},
	LeaveButton: types.Text{EN: "Stop the emails"},
	OffTitle:    types.Text{EN: "You're off the list"},
	OffLead:     types.Text{EN: "We won't email you about stewardship days any more. You're welcome on the trail any time."},

	SeeDays:    types.Text{EN: "See the days scheduled"},
	CannotSend: types.Text{EN: "We couldn't send the email just now. Try again in a few minutes."},
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
		"outcome", req.Outcome, "subscriber_id", idOrNone(req.SubscriberID))

	if req.Outcome == subscriberbus.Sent {
		if err := a.cfg.Mail.Send(r.Context(), confirmation(email, a.cfg.BaseURL+confirmPath+"?t="+url.QueryEscape(req.Confirm))); err != nil {
			a.cfg.Log.ErrorContext(r.Context(), "a confirmation could not be sent", "request_id", web.RequestIDFrom(r.Context()),
				"subscriber_id", req.SubscriberID.String(), "error", err)
			a.cfg.Render.Render(w, r, http.StatusServiceUnavailable, "signup-form", formView{Copy: words, Email: typed, Problem: words.CannotSend})

			return
		}
	}

	http.Redirect(w, r, sentPath, http.StatusSeeOther)
}

func (a app) sent(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "signup-sent", formView{Copy: words})
}

// ------------------------------------------------------------------ confirming

type linkView struct {
	Copy    wording
	Token   string
	Done    bool
	Problem types.Text
}

func (a app) confirmForm(w http.ResponseWriter, r *http.Request) {
	v := linkView{Copy: words, Token: r.URL.Query().Get("t")}
	if v.Token == "" {
		v.Problem = words.LinkFailed
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "signup-confirm", v)
}

func (a app) confirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the link again and try once more.", http.StatusBadRequest)

		return
	}

	s, err := a.cfg.List.Confirm(r.Context(), r.PostFormValue("t"))

	switch {
	case errors.Is(err, subscriberbus.ErrDenied):
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "signup-confirm", linkView{Copy: words, Problem: words.LinkFailed})

		return
	case err != nil:
		a.fail(w, r, "confirming a sign-up", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "sign-up confirmed", "request_id", web.RequestIDFrom(r.Context()), "subscriber_id", s.ID.String())

	// The welcome carries their unsubscribe link, so that from the first
	// day they hold a way off the list -- including off the emails a steward
	// writes by hand, which carry none. Not being sent is logged and not
	// shown: they are on the list either way.
	if err := a.cfg.Mail.Send(r.Context(), welcome(s.Email, a.cfg.BaseURL, a.leaveURL(s.Unsubscribe))); err != nil {
		a.cfg.Log.WarnContext(r.Context(), "a welcome could not be sent", "request_id", web.RequestIDFrom(r.Context()),
			"subscriber_id", s.ID.String(), "error", err)
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "signup-confirm", linkView{Copy: words, Done: true})
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

// confirmation is the one email a stranger can cause, so it says plainly
// what happens if it was not wanted: nothing.
func confirmation(to types.Email, link string) mail.Message {
	return mail.Message{
		To:      to.String(),
		Subject: "Confirm your email for stewardship days",
		Text: "Someone, we hope you, asked to hear about stewardship days on the Schoenstatt Fathers' Trail of the Saints.\r\n\r\n" +
			"To confirm, open this link and tap the button:\r\n\r\n" +
			link + "\r\n\r\n" +
			"The link works for seven days. If you didn't ask, ignore this email and you won't hear from us.\r\n\r\n" +
			"-- The garden stewards\r\n",
	}
}

func welcome(to types.Email, base, leave string) mail.Message {
	return mail.Message{
		To:      to.String(),
		Subject: "You're on the list for stewardship days",
		Text: "Thank you for signing up. We'll email you when a stewardship day is scheduled on the Schoenstatt Fathers' Trail of the Saints.\r\n\r\n" +
			"You don't need any experience or special skills. We'll show you what to do.\r\n\r\n" +
			"The days already scheduled are here:\r\n" + base + "/\r\n\r\n" +
			"To stop these emails at any time:\r\n" + leave + "\r\n\r\n" +
			"-- The garden stewards\r\n",
	}
}

// ------------------------------------------------------------------ the stewards' list

type stewardRow struct {
	ID, Email, Since, Lang string
}

type stewardView struct {
	Rows    []stewardRow
	All     string // every address, for the Bcc line
	Pending int
	Done    string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	on, err := a.cfg.List.Confirmed(r.Context())
	if err != nil {
		a.fail(w, r, "reading the list", err)

		return
	}

	pending, err := a.cfg.List.PendingCount(r.Context())
	if err != nil {
		a.fail(w, r, "counting pending sign-ups", err)

		return
	}

	v := stewardView{Pending: pending}

	var all []string

	for _, s := range on {
		lang := "English"
		if s.Lang == types.Spanish {
			lang = "Spanish"
		}

		v.Rows = append(v.Rows, stewardRow{
			ID: s.ID.String(), Email: s.Email.String(), Lang: lang,
			Since: page.Date(s.ConfirmedAt).EN,
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
