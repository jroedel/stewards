// Package signupapp is the list for stewardship days, by email, text or
// both: the form on the home page posts here, the unsubscribe links land
// here, and the stewards see who is on the list here.
//
// The rules -- one step to sign up, the caps, a page that says the same
// thing whoever is asking -- are subscriberbus's; see there for why each one
// is. What this package adds is the welcome -- an email and a text, sent
// through noticebus to whichever the person gave -- and the pages around it.
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
	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// The public addresses. FormPath is what the home page's form posts to, and
// LeavePath is where a link off the list lands -- which a day's confirmation
// carries too, for someone who ticked the box to join the list (dayapp).
const (
	FormPath    = "/subscribe"
	sentPath    = "/subscribe/sent"
	confirmPath = "/subscribe/confirm"
	LeavePath   = "/unsubscribe"
	stewardPath = "/steward/subscribers"
)

// Honeypot is the name of the field people never fill in.
const Honeypot = "website"

// List is what this app needs from the subscriber rules.
type List interface {
	Request(ctx context.Context, c types.Contact, lang types.Lang) (subscriberbus.Request, error)
	Unsubscribe(ctx context.Context, token string) error
	Remove(ctx context.Context, id types.ID) error
	All(ctx context.Context) ([]subscriberbus.Subscriber, error)
}

// Sender is what this app needs to write to people: noticebus.
type Sender interface {
	Send(ctx context.Context, c types.Contact, n noticebus.Notice) noticebus.Result
	Texts() bool
}

// Config is what this app needs. Notices is required: the welcome is what
// tells a person they are on the list and how to leave it, so main mounts
// this app only when a mail relay is configured.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	List    List
	Notices Sender
	BaseURL string
}

type app struct{ cfg Config }

// Routes mounts the public pages, and the stewards' list behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	mux.HandleFunc("POST "+FormPath, a.request)
	mux.HandleFunc("GET "+sentPath, a.sent)
	mux.HandleFunc("GET "+confirmPath, a.confirmGone)
	mux.HandleFunc("GET "+LeavePath, a.leaveForm)
	mux.HandleFunc("POST "+LeavePath, a.leave)

	mux.Handle("GET "+stewardPath, guard(http.HandlerFunc(a.list)))
	mux.Handle("POST "+stewardPath+"/{id}/remove", guard(http.HandlerFunc(a.remove)))
}

// The copy a newcomer reads, in English; Claude translates it through the
// translation memory, and say looks it up (page/words.go).
type wording struct {
	Eyebrow types.Text

	FormTitle, FormHelp, FormHelpTexts, FormButton types.Text

	SentTitle, SentLead, SentSpam types.Text

	OldLinkTitle, OldLinkLead types.Text

	LeaveTitle, LeaveLead, LeaveButton, OffTitle, OffLead types.Text

	SeeDays types.Text
}

// Words is this app's copy held in Go, for the catalog the translation
// memory lists.
var Words = page.Catalog{
	{Where: "signing up to hear about stewardship days by email or text, and stopping it: read by a newcomer", Words: words},
	welcome.Catalog("the welcome a newcomer is sent on signing up for stewardship days: an email in plain text, and a text message, " +
		"which is short and has its two links on lines of their own"),
}

var words = wording{
	Eyebrow: types.Text{EN: "Stewardship days"},

	FormTitle:     types.Text{EN: "Hear about the next day"},
	FormHelp:      types.Text{EN: "Leave your email and we'll write when a stewardship day is scheduled. Nothing else, and you can stop any time."},
	FormHelpTexts: types.Text{EN: "Leave your email or mobile number and we'll let you know when a stewardship day is scheduled. Nothing else, and you can stop any time."},
	FormButton:    types.Text{EN: "Keep me posted"},

	SentTitle: types.Text{EN: "You're on the list"},
	SentLead:  types.Text{EN: "Thank you. We'll let you know when a stewardship day is scheduled, and we've sent you a welcome now with a link to stop any time."},
	SentSpam:  types.Text{EN: "If the welcome isn't there in a few minutes, look in your spam folder, or check what you typed and sign up again."},

	OldLinkTitle: types.Text{EN: "No need to confirm any more"},
	OldLinkLead:  types.Text{EN: "Signing up is one step now. If you're not on the list yet, sign up from the home page and you'll be on it straight away."},

	LeaveTitle:  types.Text{EN: "Stop these messages?"},
	LeaveLead:   types.Text{EN: "Press the button and we won't write to you about stewardship days any more, by email or by text."},
	LeaveButton: types.Text{EN: "Stop the messages"},
	OffTitle:    types.Text{EN: "You're off the list"},
	OffLead:     types.Text{EN: "We won't write to you about stewardship days any more. You're welcome on the trail any time."},

	SeeDays: types.Text{EN: "See the days scheduled"},
}

// ------------------------------------------------------------------ signing up

type formView struct {
	Copy    wording
	Contact page.ContactFields
}

func (a app) form() formView {
	return formView{Copy: words, Contact: page.ContactFields{ID: "signup", Texts: a.cfg.Notices.Texts()}}
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

	v := a.form()
	v.Contact.Email = r.PostFormValue("email")

	// A phone typed into a form that did not offer the field -- an old page
	// open since before texts were turned off -- is not one we can text.
	if v.Contact.Texts {
		v.Contact.Phone = r.PostFormValue("phone")
	}

	c, err := types.ParseContact(v.Contact.Email, v.Contact.Phone)
	if err != nil {
		v.Contact.ContactProblem(err)
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "signup-form", v)

		return
	}

	lang := mid.LangFrom(r.Context())

	req, err := a.cfg.List.Request(r.Context(), c, lang)
	if err != nil {
		a.fail(w, r, "signing up", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "sign-up", "request_id", web.RequestIDFrom(r.Context()),
		"outcome", req.Outcome, "subscriber_id", idOrNone(req.Subscriber.ID))

	// They are on the list whether or not the welcome goes; a failure is
	// logged for a steward to see, and not shown, since signing up again
	// would only find them on the list already and send nothing. The
	// welcome goes to what is new: a person adding a phone to their email
	// is welcomed by text alone.
	if req.Outcome == subscriberbus.Added {
		res := a.cfg.Notices.Send(r.Context(), req.New, a.welcome(lang, req.Subscriber.Unsubscribe))
		a.logSent(r, "welcome", req.Subscriber.ID, res)
	}

	http.Redirect(w, r, sentPath, http.StatusSeeOther)
}

func (a app) sent(w http.ResponseWriter, r *http.Request) {
	a.cfg.Render.Render(w, r, http.StatusOK, "signup-sent", a.form())
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
	return a.cfg.BaseURL + LeavePath + "?t=" + url.QueryEscape(token)
}

// ------------------------------------------------------------------ the welcome

// welcome is a message a stranger can cause -- anybody can type anybody's
// address or number -- so besides the thanks it says plainly what to do if
// it was not wanted, and the link off the list is in it from the first day.
//
// It is in the language of the page the person signed up on, which is the
// language the list keeps for them.
func (a app) welcome(l types.Lang, token string) noticebus.Notice {
	return notices.Build(a.cfg.Render, l, welcome, "days", a.cfg.BaseURL+"/", "leave", a.leaveURL(token))
}

// The email is long enough that the text is its own, shorter thing. The
// text says STOP as well as giving the link: replying STOP is what anyone
// who has had a text from a business expects to work, and it does -- though
// the reply goes to the number's other user, eumaeus, the next text here is
// refused and the number taken off (noticebus).
var welcome = notices.Words{
	Subject: types.Text{EN: "Thank you for signing up for stewardship days"},
	Email: types.Text{EN: "Thank you for signing up to receive notifications about future stewardship days on the Schoenstatt Fathers' Trail of the Saints.\n\n" +
		"You don't need any experience or special skills.\n\n" +
		"The days already scheduled are here:\n{days}\n\n" +
		"If you did not sign up for this, or want to stop hearing from us at any time, unsubscribe here:\n{leave}\n\n" +
		"-- The garden stewards\n"},
	Text: types.Text{EN: "Trail of the Saints garden stewards: thank you for signing up. We'll text you when a stewardship day is scheduled.\n" +
		"Days: {days}\n" +
		"To stop: {leave} or reply STOP"},
}

// Notices is this app's messages, for the stewards' screen that shows every
// message before it goes out.
func Notices(cfg Config) []notices.Kind {
	a := app{cfg: cfg}

	return []notices.Kind{{
		Name:  welcomeName,
		When:  welcomeWhen,
		Words: welcome,
		Sample: func(l types.Lang) noticebus.Notice {
			return a.welcome(l, strings.Repeat("0", 64))
		},
	}}
}

var (
	welcomeName = types.Text{EN: "Welcome to the list"}
	welcomeWhen = types.Text{EN: "Sent at once to someone who signs up on the home page, to the email and phone they gave. If they add a phone later, the phone gets it then."}
)

// NoticeWords is the copy of Notices' names, for the catalog.
var NoticeWords = page.Copy{
	Where: "the stewards' screen of every message the app sends: a message's name, and when it is sent",
	Words: []types.Text{welcomeName, welcomeWhen},
}

// logSent records what came of a message, half by half, by the
// subscriber's ID and never their address or number.
func (a app) logSent(r *http.Request, what string, id types.ID, res noticebus.Result) {
	attrs := []any{"request_id", web.RequestIDFrom(r.Context()), "subscriber_id", id.String(),
		"mailed", res.Mailed, "texted", res.Texted, "stopped", res.Stopped}

	if res.MailErr != nil || res.TextErr != nil {
		a.cfg.Log.ErrorContext(r.Context(), "a "+what+" did not all go", append(attrs, "mail_error", errText(res.MailErr), "text_error", errText(res.TextErr))...)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), what+" sent", attrs...)
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// ------------------------------------------------------------------ the stewards' list

type stewardRow struct {
	ID, Email, Phone string
	Since            page.Phrase
	Lang             types.Text
}

type stewardView struct {
	Rows    []stewardRow
	All     string // every address, for the Bcc line
	Removed bool
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
		v.Rows = append(v.Rows, stewardRow{
			ID: s.ID.String(), Email: s.Contact.Email.String(), Phone: s.Contact.Phone.National(),
			Lang: page.Language(s.Lang), Since: page.Date(s.CreatedAt),
		})

		if !s.Contact.Email.Zero() {
			all = append(all, s.Contact.Email.String())
		}
	}

	v.All = strings.Join(all, ", ")

	v.Removed = r.URL.Query().Get("done") == "removed"

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
