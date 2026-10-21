// Package dayapp is a stewardship day's own page, for anyone: when it is,
// what the work is, who is coming, and the form that says "I'll be there".
//
// The home page lists the days and links here; so does every message about a
// day. The stewards' side of a day -- scheduling it, and seeing who is
// coming with their contacts -- is workdayapp's. Neither imports the other.
//
// # Who is coming, by name
//
// The page shows the name each person chose, to anyone who opens it (see
// rsvpbus for why, and why that is all right). The form says so above the
// name field, in the words a person reads before typing it.
//
// # What the form answers
//
// The same page whatever happened -- signed up now, signed up already, or a
// cap reached -- as the list's form does, so that it tells a stranger
// nothing about who is on a day except the names already shown. A problem
// with what was typed is the one thing said differently, beside its field.
package dayapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// The public addresses. CancelPath is where the link that takes a name off
// a day lands, which every message to the people coming carries.
const (
	CancelPath = "/days/cancel"
	honeypot   = "website"
)

// DayPath is a day's page, which the home page and every message about the
// day link to.
func DayPath(id types.ID) string { return "/days/" + id.String() }

// RSVPs is what this app needs from the sign-up rules.
type RSVPs interface {
	Request(ctx context.Context, day types.ID, name string, c types.Contact, lang types.Lang) (rsvpbus.Request, error)
	Upcoming(ctx context.Context, day types.ID) (workdaybus.Day, error)
	Coming(ctx context.Context, day types.ID) ([]string, error)
	ByCancel(ctx context.Context, token string) (rsvpbus.RSVP, error)
	Cancel(ctx context.Context, token string) error
}

// Days reads a day for the cancel page, which may be for a day now over.
type Days interface {
	ByID(ctx context.Context, id types.ID) (workdaybus.Day, error)
}

// List is the email list, for the box that joins it too.
type List interface {
	Request(ctx context.Context, c types.Contact, lang types.Lang) (subscriberbus.Request, error)
}

// Sender writes to people: noticebus.
type Sender interface {
	Send(ctx context.Context, c types.Contact, n noticebus.Notice) noticebus.Result
	Texts() bool
}

// Config is what this app needs. LeavePath is where a link off the list
// lands (signupapp's), for the confirmation of someone who ticked the box.
type Config struct {
	Log       *slog.Logger
	Render    *page.Renderer
	RSVPs     RSVPs
	Days      Days
	List      List
	Notices   Sender
	BaseURL   string
	LeavePath string
}

type app struct{ cfg Config }

// Routes mounts the public pages.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{cfg: cfg}

	mux.HandleFunc("GET /days/{id}", a.page)
	mux.HandleFunc("POST /days/{id}", a.request)
	mux.HandleFunc("GET "+CancelPath, a.cancelForm)
	mux.HandleFunc("POST "+CancelPath, a.cancel)
}

// ------------------------------------------------------------------ the words

type wording struct {
	Eyebrow, Coming, NobodyYet, Over, SeeDays types.Text

	FormTitle, FormHelp, NameLabel, NameHelp, AlsoList, Button types.Text

	Done types.Text

	// The name's problems, by rsvpbus.Invalid's Kind.
	NameBlank, NameLong, NameLink, NameLetters types.Text

	// When is a day named in a sentence: its date and its hours.
	When types.Text

	CancelTitle, CancelLead, CancelButton, CancelledTitle, CancelledLead, CancelGone types.Text
}

var words = wording{
	Eyebrow:   types.Text{EN: "Stewardship day"},
	Coming:    types.Text{EN: "Who's coming"},
	NobodyYet: types.Text{EN: "Nobody has said yet. You could be the first."},
	Over:      types.Text{EN: "This day is over. Thank you to everyone who came."},
	SeeDays:   types.Text{EN: "See the days coming up"},

	FormTitle: types.Text{EN: "I'll be there"},
	FormHelp:  types.Text{EN: "Tell us you're coming, so we know how many to expect, and so we can let you know if anything changes."},
	NameLabel: types.Text{EN: "Your name, as others will see it"},
	NameHelp:  types.Text{EN: "Shown on this page to anyone who opens it, so friends can see who's coming. A first name is plenty."},
	AlsoList:  types.Text{EN: "Also tell me about future stewardship days"},
	Button:    types.Text{EN: "I'll be there"},

	Done: types.Text{EN: "You're signed up. We've sent you a note with a link to cancel, in case you can't come after all."},

	NameBlank:   types.Text{EN: "Write the name you'd like shown, such as your first name."},
	NameLong:    types.Text{EN: "Keep the name to 40 letters or fewer."},
	NameLink:    types.Text{EN: "Write a name, not an address or a link."},
	NameLetters: types.Text{EN: "Use letters in the name; a few marks like - and ' are fine."},

	When: types.Text{EN: "{date}, {hours}"},

	CancelTitle:    types.Text{EN: "Can't come after all?"},
	CancelLead:     types.Text{EN: "Press the button and we'll take your name off {day}. Thank you for letting us know."},
	CancelButton:   types.Text{EN: "Take my name off"},
	CancelledTitle: types.Text{EN: "Your name is off that day"},
	CancelledLead:  types.Text{EN: "Thank you for letting us know. You're welcome on another day, or on the trail any time."},
	CancelGone:     types.Text{EN: "That sign-up isn't there any more: it was cancelled already, or the day was taken off the calendar."},
}

// Words is this app's copy held in Go, for the catalog the translation
// memory lists.
var Words = page.Catalog{
	{Where: "a stewardship day's own page, where anyone sees who is coming and says they'll come too; read on a phone, often from the QR code on the trail", Words: words},
	confirmation.Catalog("the note sent to someone who says they'll come to a stewardship day: an email in plain text, and a text message, " +
		"which is short and has its two links on lines of their own"),
	{Where: "the line in that note for someone who also asked to hear about future days", Words: []types.Text{alsoOnList}},
}

// ------------------------------------------------------------------ the page

type pageView struct {
	Copy wording

	ID          string
	Date, Hours page.Phrase
	Title       types.Text
	Details     types.Text
	Over        bool

	Coming []string

	Name        string
	NameProblem any
	AlsoList    bool
	Contact     page.ContactFields
	Done        bool
}

func (a app) page(w http.ResponseWriter, r *http.Request) {
	v, ok := a.load(w, r)
	if !ok {
		return
	}

	v.Done = r.URL.Query().Get("done") == "coming"

	a.cfg.Render.Render(w, r, http.StatusOK, "day", v)
}

// load is the day and who is coming, or a 404 answered. A day that is over
// is shown, without the form: a link in an old message still lands
// somewhere that says what happened.
func (a app) load(w http.ResponseWriter, r *http.Request) (pageView, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return pageView{}, false
	}

	d, err := a.cfg.RSVPs.Upcoming(r.Context(), id)
	over := errors.Is(err, rsvpbus.ErrOver)

	switch {
	case errors.Is(err, rsvpbus.ErrNotFound):
		http.Error(w, "That day is not on the calendar any more. The days coming up are on the home page.", http.StatusNotFound)

		return pageView{}, false
	case err != nil && !over:
		a.fail(w, r, "reading a day", err)

		return pageView{}, false
	}

	coming, err := a.cfg.RSVPs.Coming(r.Context(), id)
	if err != nil {
		a.fail(w, r, "reading who is coming", err)

		return pageView{}, false
	}

	return pageView{
		Copy: words, ID: d.ID.String(),
		Date: page.Date(d.Starts), Hours: page.Hours(d.Starts, d.Ends),
		Title: d.Title, Details: d.Details, Over: over,
		Coming:   coming,
		AlsoList: true,
		Contact:  page.ContactFields{ID: "rsvp", Texts: a.cfg.Notices.Texts()},
	}, true
}

func (a app) request(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v, ok := a.load(w, r)
	if !ok {
		return
	}

	id, _ := types.ParseID(v.ID)
	done := DayPath(id) + "?done=coming"

	// The bot answer: the page a person would see, and nothing done.
	if r.PostFormValue(honeypot) != "" {
		a.cfg.Log.InfoContext(r.Context(), "a day's sign-up caught by the honeypot", "request_id", web.RequestIDFrom(r.Context()))
		http.Redirect(w, r, done, http.StatusSeeOther)

		return
	}

	if v.Over {
		a.cfg.Render.Render(w, r, http.StatusConflict, "day", v)

		return
	}

	v.Name = r.PostFormValue("name")
	v.AlsoList = r.PostFormValue("list") == "yes"
	v.Contact.Email = r.PostFormValue("email")

	if v.Contact.Texts {
		v.Contact.Phone = r.PostFormValue("phone")
	}

	// Both checked before either is refused, so a person fixes everything
	// in one go rather than one field per press.
	_, nameErr := rsvpbus.Name(v.Name)
	if invalid, ok := errors.AsType[rsvpbus.Invalid](nameErr); ok {
		v.NameProblem = map[string]types.Text{
			"blank": words.NameBlank, "long": words.NameLong, "link": words.NameLink, "letters": words.NameLetters,
		}[invalid.Kind]
	}

	c, err := types.ParseContact(v.Contact.Email, v.Contact.Phone)
	if err != nil {
		v.Contact.ContactProblem(err)
	}

	if nameErr != nil || err != nil {
		a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "day", v)

		return
	}

	lang := mid.LangFrom(r.Context())

	req, err := a.cfg.RSVPs.Request(r.Context(), id, v.Name, c, lang)
	if err != nil {
		a.fail(w, r, "signing up for a day", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "a day's sign-up", "request_id", web.RequestIDFrom(r.Context()),
		"day_id", v.ID, "outcome", req.Outcome, "rsvp_id", idOrNone(req.RSVP.ID))

	// The list, when the box is ticked: its own rules and caps, and no
	// welcome of its own -- the confirmation says they are on it, and
	// carries the way off, so that one sign-up is one message.
	var leave string

	if v.AlsoList {
		joined, err := a.cfg.List.Request(r.Context(), c, lang)
		if err != nil {
			a.cfg.Log.ErrorContext(r.Context(), "joining the list from a day's sign-up", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		} else if joined.Outcome != subscriberbus.Capped && joined.Subscriber.Unsubscribe != "" {
			leave = a.cfg.BaseURL + a.cfg.LeavePath + "?t=" + url.QueryEscape(joined.Subscriber.Unsubscribe)
		}
	}

	if req.Outcome == rsvpbus.Added {
		d, err := a.cfg.Days.ByID(r.Context(), id)
		if err == nil {
			res := a.cfg.Notices.Send(r.Context(), req.New, a.confirm(lang, d, req.RSVP, leave))
			a.logSent(r, req.RSVP.ID, res)
		}
	}

	http.Redirect(w, r, done, http.StatusSeeOther)
}

// ------------------------------------------------------------------ cancelling

type cancelView struct {
	Copy      wording
	Token     string
	Lead      page.Phrase
	Gone, Off bool
}

func (a app) cancelForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("t")
	v := cancelView{Copy: words, Token: token}

	rsvp, err := a.cfg.RSVPs.ByCancel(r.Context(), token)

	switch {
	case errors.Is(err, rsvpbus.ErrNotFound):
		v.Gone = true
	case err != nil:
		a.fail(w, r, "reading a sign-up", err)

		return
	default:
		d, err := a.cfg.Days.ByID(r.Context(), rsvp.DayID)
		if err != nil {
			v.Gone = true

			break
		}

		v.Lead = page.Put(words.CancelLead, "day", dayName(d))
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "day-cancel", v)
}

func (a app) cancel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the link again and try once more.", http.StatusBadRequest)

		return
	}

	if err := a.cfg.RSVPs.Cancel(r.Context(), r.PostFormValue("t")); err != nil {
		a.fail(w, r, "cancelling a sign-up", err)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "a day's sign-up cancelled", "request_id", web.RequestIDFrom(r.Context()))
	a.cfg.Render.Render(w, r, http.StatusOK, "day-cancel", cancelView{Copy: words, Off: true})
}

// ------------------------------------------------------------------ the confirmation

// confirm is the note to someone who said they'll come. It carries the
// cancel link, so that a person who can't come after all can say so in one
// tap and the stewards know how many to expect.
func (a app) confirm(l types.Lang, d workdaybus.Day, r rsvpbus.RSVP, leave string) noticebus.Notice {
	list := any("")
	if leave != "" {
		list = page.Put(alsoOnList, "leave", leave)
	}

	return notices.Build(a.cfg.Render, l, confirmation,
		"name", r.Name, "date", page.Date(d.Starts), "hours", page.Hours(d.Starts, d.Ends), "title", d.Title,
		"day", a.cfg.BaseURL+DayPath(d.ID), "cancel", a.cancelURL(r.Cancel), "list", list)
}

func (a app) cancelURL(token string) string {
	return a.cfg.BaseURL + CancelPath + "?t=" + url.QueryEscape(token)
}

var confirmation = notices.Words{
	Subject: types.Text{EN: "See you on {date}"},
	Email: types.Text{EN: "Thank you, {name}. You're signed up for the stewardship day on {date}, {hours}: {title}.\n\n" +
		"Who else is coming, and the details:\n{day}\n\n" +
		"If you can't come after all, take your name off here, so we know how many to expect:\n{cancel}\n\n" +
		"{list}" +
		"-- The garden stewards\n"},
	Text: types.Text{EN: "Garden stewards: you're signed up for {date}, {hours}: {title}.\n" +
		"Details: {day}\n" +
		"Can't come? {cancel}\n" +
		"Reply STOP to stop texts"},
}

var alsoOnList = types.Text{EN: "You're also on our list for future stewardship days. To stop those messages:\n{leave}\n\n"}

// dayName is how a day is named in a sentence: its date and hours.
func dayName(d workdaybus.Day) page.Phrase {
	return page.Put(words.When, "date", page.Date(d.Starts), "hours", page.Hours(d.Starts, d.Ends))
}

// Notices is this app's messages, for the stewards' messages screen.
func Notices(cfg Config) []notices.Kind {
	a := app{cfg: cfg}

	return []notices.Kind{{
		Name: confirmName, When: confirmWhen, Words: confirmation,
		Sample: func(l types.Lang) noticebus.Notice {
			d := notices.SampleDay()
			return a.confirm(l, d, rsvpbus.RSVP{Name: "Ana", Cancel: "sample"}, cfg.BaseURL+cfg.LeavePath+"?t=sample")
		},
	}}
}

var (
	confirmName = types.Text{EN: "Signed up for a day"}
	confirmWhen = types.Text{EN: "Sent at once to someone who says they'll come to a stewardship day, to the email and phone they gave. The line about future days is there only when they ticked that box."}
)

// NoticeWords is the copy of Notices' names, for the catalog.
var NoticeWords = page.Copy{
	Where: "the stewards' screen of every message the app sends: a message's name, and when it is sent",
	Words: []types.Text{confirmName, confirmWhen},
}

// ------------------------------------------------------------------ the parts

func (a app) logSent(r *http.Request, id types.ID, res noticebus.Result) {
	attrs := []any{"request_id", web.RequestIDFrom(r.Context()), "rsvp_id", id.String(),
		"mailed", res.Mailed, "texted", res.Texted, "stopped", res.Stopped}

	if res.MailErr != nil || res.TextErr != nil {
		a.cfg.Log.ErrorContext(r.Context(), "a day's confirmation did not all go", append(attrs, "mail_error", errText(res.MailErr), "text_error", errText(res.TextErr))...)

		return
	}

	a.cfg.Log.InfoContext(r.Context(), "a day's confirmation sent", attrs...)
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

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
