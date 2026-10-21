package workdayapp

// The messages a steward sends about a day, to many at once, each as an
// email and as a text, and each shown in full before it goes:
//
//   - telling the list a day is scheduled, once per day;
//   - telling the people coming that its time has changed, or that it is
//     cancelled;
//   - a message of the steward's own to the people coming -- "we'll decide
//     at 9 am whether we're rained out" -- with an optional Spanish of their
//     own writing beside it (decided 2026-10-21: a rain-out call can't wait
//     for a translation);
//   - and the reminder the evening before, which nobody presses: RemindLoop.
//
// Every one is a noticebus batch, so a button pressed twice reaches nobody
// twice, and every one is listed on the day's screen with how it went.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// Sender is what this app needs to write to many: noticebus.
type Sender interface {
	SendBatch(ctx context.Context, key string, to []noticebus.Recipient, build func(noticebus.Recipient) noticebus.Notice)
	Batches(ctx context.Context, prefix string) ([]noticebus.Batch, error)
	Texts() bool
}

// List is the list, for telling it about a day.
type List interface {
	All(ctx context.Context) ([]subscriberbus.Subscriber, error)
}

// Links are the public addresses a message carries, which other apps own:
// the home page, a day's page, the link that takes a name off a day, and
// the link off the list.
type Links struct {
	Home   string
	Day    func(types.ID) string
	Cancel func(token string) string
	Leave  func(token string) string
}

// remindAt is when the reminder goes, the evening before a day, in the
// garden's zone: late enough that plans for tomorrow are being made, early
// enough that nobody is woken.
const remindAt = 18

// noteMax is the longest a steward's message may be, so that it goes by
// text as written: noticebus.TextLimit.
const noteMax = noticebus.TextLimit

// ------------------------------------------------------------------ the words

var (
	announce = notices.Words{
		Subject: types.Text{EN: "New stewardship day: {date}"},
		Email: types.Text{EN: "A stewardship day is scheduled on the Trail of the Saints:\n\n" +
			"{date}, {hours}\n{title}\n\n{details}" +
			"See who's coming, and say you'll come too:\n{day}\n\n" +
			"Everyone is welcome, and no experience is needed.\n\n" +
			"To stop these messages:\n{leave}\n\n" +
			"-- The garden stewards\n"},
		Text: types.Text{EN: "Garden stewards: new stewardship day {date}, {hours}: {title}.\n" +
			"Coming? {day}\n" +
			"Stop: {leave}"},
	}

	changed = notices.Words{
		Subject: types.Text{EN: "Changed: the stewardship day is now {date}, {hours}"},
		Email: types.Text{EN: "The stewardship day you signed up for has changed. It is now:\n\n" +
			"{date}, {hours}\n{title}\n\n" +
			"The details, and who else is coming:\n{day}\n\n" +
			"If you can't come at the new time, take your name off here:\n{cancel}\n\n" +
			"-- The garden stewards\n"},
		Text: types.Text{EN: "Garden stewards: the day you signed up for is now {date}, {hours}: {title}.\n" +
			"{day}\n" +
			"Can't come? {cancel}"},
	}

	cancelled = notices.Words{
		Subject: types.Text{EN: "Cancelled: the stewardship day on {date}"},
		Email: types.Text{EN: "We're sorry: the stewardship day on {date}, {hours} is cancelled.\n\n" +
			"Thank you for signing up. The days coming up are here:\n{days}\n\n" +
			"-- The garden stewards\n"},
	}

	reminder = notices.Words{
		Subject: types.Text{EN: "Tomorrow: {title}"},
		Email: types.Text{EN: "A reminder that you're signed up for tomorrow's stewardship day:\n\n" +
			"{date}, {hours}\n{title}\n\n{details}" +
			"Who else is coming:\n{day}\n\n" +
			"If you can't come after all, take your name off here:\n{cancel}\n\n" +
			"-- The garden stewards\n"},
		Text: types.Text{EN: "Garden stewards: see you tomorrow, {date}, {hours}: {title}.\n" +
			"{day}\n" +
			"Can't come? {cancel}"},
	}

	note = notices.Words{
		Subject: types.Text{EN: "About the stewardship day on {date}"},
		Email: types.Text{EN: "{note}\n\n" +
			"{date}, {hours}: {title}\n{day}\n\n" +
			"If you can't come after all, take your name off here:\n{cancel}\n\n" +
			"-- The garden stewards\n"},
		Text: types.Text{EN: "Garden stewards, about {date}: {note}"},
	}
)

// The names of the batches, as the day's screen lists what went out.
var sentNames = struct {
	Announce, Change, Note, Remind types.Text
}{
	Announce: types.Text{EN: "Told the list"},
	Change:   types.Text{EN: "Told those coming about the change"},
	Note:     types.Text{EN: "A message to those coming"},
	Remind:   types.Text{EN: "The reminder the evening before"},
}

// The kinds, for the stewards' messages screen.
var kindWords = struct {
	AnnounceName, AnnounceWhen, ChangeName, ChangeWhen, CancelName, CancelWhen, RemindName, RemindWhen, NoteName, NoteWhen, NoteSample types.Text
}{
	AnnounceName: types.Text{EN: "A new day, to the list"},
	AnnounceWhen: types.Text{EN: "Sent when a steward presses Tell the list on a day, to everyone on the list; once per day."},
	ChangeName:   types.Text{EN: "A day's time has changed"},
	ChangeWhen:   types.Text{EN: "Offered when a steward changes the date or time of a day people have signed up for, and sent to them if the steward agrees."},
	CancelName:   types.Text{EN: "A day is cancelled"},
	CancelWhen:   types.Text{EN: "Sent to the people signed up for a day when a steward removes it, unless the steward unticks the box."},
	RemindName:   types.Text{EN: "The reminder the evening before"},
	RemindWhen:   types.Text{EN: "Sent at 6 pm the day before, to everyone who signed up before then. Nobody needs to press anything."},
	NoteName:     types.Text{EN: "A steward's own message"},
	NoteWhen:     types.Text{EN: "Written by a steward on a day's screen and sent to the people coming, with an optional Spanish beside it. The words below are an example."},
	NoteSample:   types.Text{EN: "We'll decide at 9 am whether we're rained out. Expect a message from us then.", ES: "Decidiremos a las 9 am si se cancela por la lluvia. Espera un mensaje nuestro a esa hora."},
}

// MessageWords is this app's messages, for the catalog.
var MessageWords = page.Catalog{
	announce.Catalog("the message telling the list a stewardship day is scheduled: an email in plain text, and a short text message"),
	changed.Catalog("the message telling the people signed up for a stewardship day that its time has changed: an email, and a short text"),
	cancelled.Catalog("the message telling the people signed up for a stewardship day that it is cancelled: an email, which is also sent as the text"),
	reminder.Catalog("the reminder the evening before a stewardship day, to the people signed up: an email, and a short text"),
	note.Catalog("the frame round a steward's own message to the people coming to a day; {note} is the steward's words"),
	{Where: "the stewards' screens: the messages sent about a day, and the screen of every message the app sends", Words: []any{sentNames, kindWords}},
	{Where: "the stewards' screens for sending a message about a day to many people", Words: tell},
}

// tell is the send screens' words held in Go.
var tell = struct {
	AnnounceHeading, AnnounceLead, AnnounceButton, AnnounceDone                    types.Text
	ChangeHeading, ChangeLead, ChangeButton, ChangeSkip                            types.Text
	NoteHeading, NoteLead, NoteButton, NoteNoMessage, NoteTooLong, NoteSpanishNone types.Text
	Sending, Waiting, Over, Nobody                                                 types.Text
}{
	AnnounceHeading: types.Text{EN: "Tell the list about {date}"},
	AnnounceLead:    types.Text{EN: "This goes to everyone on the list, in the language they signed up in, by email and by text as they asked. It can go once for each day."},
	AnnounceButton:  types.Text{EN: "Send to the list"},
	AnnounceDone:    types.Text{EN: "The list has been told about this day already."},

	ChangeHeading: types.Text{EN: "Tell the people coming about the change"},
	ChangeLead:    types.Text{EN: "The day's time has changed. This tells everyone signed up for it, with a link to take their name off if the new time doesn't suit."},
	ChangeButton:  types.Text{EN: "Tell them"},
	ChangeSkip:    types.Text{EN: "Don't tell them"},

	NoteHeading:     types.Text{EN: "A message to the people coming"},
	NoteLead:        types.Text{EN: "Check it as each person will get it. It goes to everyone signed up for the day."},
	NoteButton:      types.Text{EN: "Send it"},
	NoteNoMessage:   types.Text{EN: "Write the message first."},
	NoteTooLong:     types.Text{EN: "Keep it to 320 characters, so it goes as one or two texts."},
	NoteSpanishNone: types.Text{EN: "There is no Spanish, so the people who signed up in Spanish get the message as written."},

	Sending: types.Text{EN: "Sending now. The texts go one a second; reload this page to see how it is going."},
	Waiting: types.Text{EN: "The day's title or details are still waiting for their Spanish, so Spanish readers would get them in English. Ask Claude to translate first if you can wait."},
	Over:    types.Text{EN: "This day is over; there is nobody to tell."},
	Nobody:  types.Text{EN: "There is nobody to send this to yet."},
}

// ------------------------------------------------------------------ building them

// dayPairs is what every message about d puts into its words.
func (a app) dayPairs(d workdaybus.Day) []any {
	details := any("")
	if d.Details.Written() {
		details = page.List{Items: []any{d.Details, ""}, Sep: "\n\n"}
	}

	return []any{
		"date", page.Date(d.Starts), "hours", page.Hours(d.Starts, d.Ends),
		"title", d.Title, "details", details,
		"day", a.cfg.Links.Day(d.ID), "days", a.cfg.Links.Home,
	}
}

// tokens is each recipient's own link token, by their batch key.
type tokens map[string]string

// toList is everyone on the list, as recipients of a batch.
func toList(subs []subscriberbus.Subscriber) ([]noticebus.Recipient, tokens) {
	to, tok := make([]noticebus.Recipient, 0, len(subs)), tokens{}

	for _, s := range subs {
		key := "sub:" + s.ID.String()
		to = append(to, noticebus.Recipient{Key: key, Contact: s.Contact, Lang: s.Lang})
		tok[key] = s.Unsubscribe
	}

	return to, tok
}

// toComing is the people signed up for a day, as recipients of a batch.
func toComing(rs []rsvpbus.RSVP) ([]noticebus.Recipient, tokens) {
	to, tok := make([]noticebus.Recipient, 0, len(rs)), tokens{}

	for _, r := range rs {
		key := "rsvp:" + r.ID.String()
		to = append(to, noticebus.Recipient{Key: key, Contact: r.Contact, Lang: r.Lang})
		tok[key] = r.Cancel
	}

	return to, tok
}

// builder is how each recipient's notice is made: w, with d's details, the
// recipient's own link, and anything else.
func (a app) builder(w notices.Words, d workdaybus.Day, link string, tok tokens, extra func(types.Lang) []any) func(noticebus.Recipient) noticebus.Notice {
	return func(r noticebus.Recipient) noticebus.Notice {
		pairs := a.dayPairs(d)

		switch link {
		case "leave":
			pairs = append(pairs, "leave", a.cfg.Links.Leave(tok[r.Key]))
		case "cancel":
			pairs = append(pairs, "cancel", a.cfg.Links.Cancel(tok[r.Key]))
		}

		if extra != nil {
			pairs = append(pairs, extra(r.Lang)...)
		}

		return notices.Build(a.cfg.Render, r.Lang, w, pairs...)
	}
}

// Notices is this app's messages, for the stewards' messages screen.
func Notices(cfg Config) []notices.Kind {
	a := app{cfg: cfg}
	d := notices.SampleDay()
	sample := tokens{"x": strings.Repeat("0", 64)}
	r := func(l types.Lang) noticebus.Recipient { return noticebus.Recipient{Key: "x", Lang: l} }

	noteText := func(l types.Lang) []any { return []any{"note", kindWords.NoteSample.In(l)} }

	return []notices.Kind{
		{Name: kindWords.AnnounceName, When: kindWords.AnnounceWhen, Words: announce,
			Sample: func(l types.Lang) noticebus.Notice { return a.builder(announce, d, "leave", sample, nil)(r(l)) }},
		{Name: kindWords.RemindName, When: kindWords.RemindWhen, Words: reminder,
			Sample: func(l types.Lang) noticebus.Notice { return a.builder(reminder, d, "cancel", sample, nil)(r(l)) }},
		{Name: kindWords.ChangeName, When: kindWords.ChangeWhen, Words: changed,
			Sample: func(l types.Lang) noticebus.Notice { return a.builder(changed, d, "cancel", sample, nil)(r(l)) }},
		{Name: kindWords.CancelName, When: kindWords.CancelWhen, Words: cancelled,
			Sample: func(l types.Lang) noticebus.Notice { return a.builder(cancelled, d, "", sample, nil)(r(l)) }},
		{Name: kindWords.NoteName, When: kindWords.NoteWhen, Words: note,
			Sample: func(l types.Lang) noticebus.Notice { return a.builder(note, d, "cancel", sample, noteText)(r(l)) }},
	}
}

// ------------------------------------------------------------------ the send screens

// tellView is a message about to go to many: both forms in both languages,
// and who it goes to.
type tellView struct {
	DayID   string
	Heading any
	Lead    types.Text

	Previews []notices.Preview

	// People is how many it goes to; Emails and Texts how many of each go,
	// and Spanish how many read Spanish.
	People, Emails, Texts, Spanish int

	// Waiting is the day's own words not translated yet, said as
	// WaitingText.
	Waiting     bool
	WaitingText types.Text

	// Action is where Send posts, Hidden what it carries, and Button what
	// it says; Skip, when set, is the way out without sending.
	Action string
	Hidden map[string]string
	Button types.Text
	Skip   types.Text

	// Done is a sentence in place of the button: told already, over, or
	// nobody to tell.
	Done types.Text

	// The note's own form, on its preview: what was typed, and why it will
	// not go.
	Note    *noteView
	Problem types.Text
	Notice  types.Text
}

type noteView struct {
	Message, Spanish string
}

// counts fills in who a batch reaches.
func (v *tellView) counts(to []noticebus.Recipient, texts bool) {
	v.People = len(to)

	for _, r := range to {
		if !r.Contact.Email.Zero() {
			v.Emails++
		}

		if !r.Contact.Phone.Zero() && texts {
			v.Texts++
		}

		if r.Lang == types.Spanish {
			v.Spanish++
		}
	}
}

// preview shows what build makes, in both languages.
func (a app) preview(v *tellView, w notices.Words, d workdaybus.Day, build func(noticebus.Recipient) noticebus.Notice) {
	for _, l := range []types.Lang{types.English, types.Spanish} {
		v.Previews = append(v.Previews, notices.PreviewOf(a.cfg.Render, l, w, build(noticebus.Recipient{Key: "preview", Lang: l})))
	}

	v.Waiting = v.Spanish > 0 && (d.Title.Shown(types.Spanish) != types.Spanish ||
		d.Details.Written() && d.Details.Shown(types.Spanish) != types.Spanish)
	v.WaitingText = tell.Waiting
}

// messaging reports whether this app can send to many: it needs the
// notices, the list and the sign-ups.
func (a app) messaging() bool {
	return a.cfg.Notices != nil && a.cfg.List != nil && a.cfg.RSVPs != nil
}

// over reports whether d has ended, by the rules' clock.
func (a app) over(d workdaybus.Day) bool { return !a.cfg.Days.Now().Before(d.Ends) }

func (a app) announceForm(w http.ResponseWriter, r *http.Request) {
	d, to, tok, ok := a.announcing(w, r)
	if !ok {
		return
	}

	v := tellView{DayID: d.ID.String(), Heading: page.Put(tell.AnnounceHeading, "date", page.Date(d.Starts)), Lead: tell.AnnounceLead,
		Action: Path + "/" + d.ID.String() + "/announce", Button: tell.AnnounceButton}
	v.counts(to, a.cfg.Notices.Texts())
	a.preview(&v, announce, d, a.builder(announce, d, "leave", tok, nil))

	told, err := a.cfg.Notices.Batches(r.Context(), "announce:"+d.ID.String())
	if err != nil {
		a.fail(w, r, "reading what was sent", err)

		return
	}

	switch {
	case a.over(d):
		v.Done = tell.Over
	case len(told) > 0:
		v.Done = tell.AnnounceDone
	case len(to) == 0:
		v.Done = tell.Nobody
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "day-tell", v)
}

func (a app) announceSend(w http.ResponseWriter, r *http.Request) {
	d, to, tok, ok := a.announcing(w, r)
	if !ok {
		return
	}

	if a.over(d) {
		http.Redirect(w, r, Path+"/"+d.ID.String()+"/announce", http.StatusSeeOther)

		return
	}

	// Once per day by the batch key: a second press, or a second steward,
	// reaches nobody already told, and only someone who joined the list
	// since.
	a.cfg.Notices.SendBatch(a.cfg.Background, "announce:"+d.ID.String(), to, a.builder(announce, d, "leave", tok, nil))
	a.logBatch(r, "the list told about a day", d.ID, len(to))

	http.Redirect(w, r, Path+"/"+d.ID.String()+"/edit?done=sending#sent", http.StatusSeeOther)
}

func (a app) announcing(w http.ResponseWriter, r *http.Request) (workdaybus.Day, []noticebus.Recipient, tokens, bool) {
	d, ok := a.load(w, r)
	if !ok || !a.messaging() {
		if ok {
			http.NotFound(w, r)
		}

		return workdaybus.Day{}, nil, nil, false
	}

	subs, err := a.cfg.List.All(r.Context())
	if err != nil {
		a.fail(w, r, "reading the list", err)

		return workdaybus.Day{}, nil, nil, false
	}

	to, tok := toList(subs)

	return d, to, tok, true
}

// changeKey is the batch for a change: the day's version, from when the
// steward saved it, so one change is told once and the next change again.
func changeKey(d workdaybus.Day) string {
	return "change:" + d.ID.String() + ":" + strconv.FormatInt(d.UpdatedAt.UnixMilli(), 10)
}

func (a app) changedForm(w http.ResponseWriter, r *http.Request) {
	d, to, tok, ok := a.toComing(w, r)
	if !ok {
		return
	}

	v := tellView{DayID: d.ID.String(), Heading: tell.ChangeHeading, Lead: tell.ChangeLead,
		Action: Path + "/" + d.ID.String() + "/changed", Button: tell.ChangeButton, Skip: tell.ChangeSkip,
		Hidden: map[string]string{"v": strconv.FormatInt(d.UpdatedAt.UnixMilli(), 10)}}
	v.counts(to, a.cfg.Notices.Texts())
	a.preview(&v, changed, d, a.builder(changed, d, "cancel", tok, nil))

	switch {
	case a.over(d):
		v.Done = tell.Over
	case len(to) == 0:
		v.Done = tell.Nobody
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "day-tell", v)
}

func (a app) changedSend(w http.ResponseWriter, r *http.Request) {
	d, to, tok, ok := a.toComing(w, r)
	if !ok {
		return
	}

	// The version previewed, not the one saved since: if another steward
	// changed it again in between, the preview was of something else.
	if r.PostFormValue("v") != strconv.FormatInt(d.UpdatedAt.UnixMilli(), 10) {
		http.Redirect(w, r, Path+"/"+d.ID.String()+"/changed", http.StatusSeeOther)

		return
	}

	if !a.over(d) {
		a.cfg.Notices.SendBatch(a.cfg.Background, changeKey(d), to, a.builder(changed, d, "cancel", tok, nil))
		a.logBatch(r, "the people coming told of a change", d.ID, len(to))
	}

	http.Redirect(w, r, Path+"/"+d.ID.String()+"/edit?done=sending#sent", http.StatusSeeOther)
}

// noteSend previews a steward's message, or sends the one previewed. The
// preview carries the words it showed in hidden fields beside a key, and
// Send posts those: what goes is exactly what was looked at, and the key
// makes a second press reach nobody twice.
func (a app) noteSend(w http.ResponseWriter, r *http.Request) {
	d, to, tok, ok := a.toComing(w, r)
	if !ok {
		return
	}

	get := r.PostFormValue
	n := noteView{Message: strings.TrimSpace(get("message")), Spanish: strings.TrimSpace(get("spanish"))}

	if get("action") == "send" && get("key") != "" && !a.over(d) {
		n = noteView{Message: strings.TrimSpace(get("sent_message")), Spanish: strings.TrimSpace(get("sent_spanish"))}

		if problem := noteProblem(n); !problem.Written() {
			a.cfg.Notices.SendBatch(a.cfg.Background, "note:"+d.ID.String()+":"+get("key"), to, a.builder(note, d, "cancel", tok, n.words))
			a.logBatch(r, "a steward's message to the people coming", d.ID, len(to))

			http.Redirect(w, r, Path+"/"+d.ID.String()+"/edit?done=sending#sent", http.StatusSeeOther)

			return
		}
	}

	v := tellView{DayID: d.ID.String(), Heading: tell.NoteHeading, Lead: tell.NoteLead,
		Action: Path + "/" + d.ID.String() + "/note", Button: tell.NoteButton, Note: &n}
	v.counts(to, a.cfg.Notices.Texts())

	status := http.StatusOK

	switch {
	case a.over(d):
		v.Done = tell.Over
	case len(to) == 0:
		v.Done = tell.Nobody
	default:
		if v.Problem = noteProblem(n); v.Problem.Written() {
			status = http.StatusUnprocessableEntity
			v.Done = v.Problem

			break
		}

		v.Hidden = map[string]string{"action": "send", "key": newKey(), "sent_message": n.Message, "sent_spanish": n.Spanish}
		a.preview(&v, note, d, a.builder(note, d, "cancel", tok, n.words))

		if n.Spanish == "" && v.Spanish > 0 {
			v.Notice = tell.NoteSpanishNone
		}
	}

	a.cfg.Render.Render(w, r, status, "day-tell", v)
}

// words is the note put into the frame, in l: the Spanish the steward wrote
// for a Spanish reader, and the message as written for everyone else.
func (n noteView) words(l types.Lang) []any {
	if l == types.Spanish && n.Spanish != "" {
		return []any{"note", n.Spanish}
	}

	return []any{"note", n.Message}
}

func noteProblem(n noteView) types.Text {
	switch {
	case n.Message == "":
		return tell.NoteNoMessage
	case utf8.RuneCountInString(n.Message) > noteMax || utf8.RuneCountInString(n.Spanish) > noteMax:
		return tell.NoteTooLong
	}

	return types.Text{}
}

func (a app) toComing(w http.ResponseWriter, r *http.Request) (workdaybus.Day, []noticebus.Recipient, tokens, bool) {
	d, ok := a.load(w, r)
	if !ok || !a.messaging() {
		if ok {
			http.NotFound(w, r)
		}

		return workdaybus.Day{}, nil, nil, false
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return workdaybus.Day{}, nil, nil, false
	}

	rs, err := a.cfg.RSVPs.ForDay(r.Context(), d.ID)
	if err != nil {
		a.fail(w, r, "reading who is coming", err)

		return workdaybus.Day{}, nil, nil, false
	}

	to, tok := toComing(rs)

	return d, to, tok, true
}

// tellCancelled sends the cancellation to the people who were coming to d,
// from the list read before the day -- and its sign-ups with it -- was
// removed.
func (a app) tellCancelled(r *http.Request, d workdaybus.Day, rs []rsvpbus.RSVP) {
	to, tok := toComing(rs)
	a.cfg.Notices.SendBatch(a.cfg.Background, "cancel:"+d.ID.String(), to, a.builder(cancelled, d, "", tok, nil))
	a.logBatch(r, "the people coming told of a cancellation", d.ID, len(to))
}

// cancelPreview is the cancellation as the day's form shows it beside the
// box, in the steward's language.
func (a app) cancelPreview(l types.Lang, d workdaybus.Day) notices.Preview {
	return notices.PreviewOf(a.cfg.Render, l, cancelled, a.builder(cancelled, d, "", nil, nil)(noticebus.Recipient{Lang: l}))
}

// ------------------------------------------------------------------ what went out

type sentRow struct {
	Name                           types.Text
	When                           page.Phrase
	Sent, Failed, Stopped, Sending int
}

// sent is every batch about d, earliest first.
func (a app) sent(ctx context.Context, d workdaybus.Day) ([]sentRow, bool, error) {
	id := d.ID.String()

	var (
		out  []sentRow
		told bool
	)

	for _, kind := range []struct {
		prefix string
		name   types.Text
	}{
		{"announce:" + id, sentNames.Announce},
		{"change:" + id + ":", sentNames.Change},
		{"note:" + id + ":", sentNames.Note},
		{"remind:" + id, sentNames.Remind},
	} {
		batches, err := a.cfg.Notices.Batches(ctx, kind.prefix)
		if err != nil {
			return nil, false, err
		}

		for _, b := range batches {
			out = append(out, sentRow{
				Name: kind.name, When: page.Put(types.Text{EN: "{date}, {time}"}, "date", page.Date(b.Started), "time", page.Time(b.Started)),
				Sent: b.Sent, Failed: b.Failed, Stopped: b.Stopped, Sending: b.Sending,
			})
		}

		if strings.HasPrefix(kind.prefix, "announce:") && len(batches) > 0 {
			told = true
		}
	}

	return out, told, nil
}

// ------------------------------------------------------------------ the reminder

// RemindLoop sends the reminder the evening before each day, checking every
// few minutes until ctx ends. A tick that finds the reminder sent already
// sends nothing -- the batch key is the day's -- so the loop can run as
// often as is convenient, and a restart at 6:05 pm still sends it.
func RemindLoop(ctx context.Context, cfg Config) {
	a := app{cfg: cfg}

	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()

	for {
		if err := a.remind(ctx); err != nil && ctx.Err() == nil {
			cfg.Log.Warn("the reminders could not be sent", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// remind sends the reminder for each day that starts tomorrow, once it is
// remindAt today, to the people who signed up before then: someone who
// signs up at 8 pm has just had their confirmation.
func (a app) remind(ctx context.Context) error {
	if !a.messaging() {
		return nil
	}

	now := a.cfg.Days.Now().In(types.Garden)
	evening := time.Date(now.Year(), now.Month(), now.Day(), remindAt, 0, 0, 0, types.Garden)

	if now.Before(evening) {
		return nil
	}

	tomorrow := now.AddDate(0, 0, 1).Format(time.DateOnly)

	days, err := a.cfg.Days.Upcoming(ctx)
	if err != nil {
		return err
	}

	var errs []error

	for _, d := range days {
		if d.Starts.In(types.Garden).Format(time.DateOnly) != tomorrow {
			continue
		}

		rs, err := a.cfg.RSVPs.ForDay(ctx, d.ID)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		var before []rsvpbus.RSVP
		for _, r := range rs {
			if r.CreatedAt.Before(evening) {
				before = append(before, r)
			}
		}

		to, tok := toComing(before)
		a.cfg.Notices.SendBatch(ctx, "remind:"+d.ID.String(), to, a.builder(reminder, d, "cancel", tok, nil))
	}

	return errors.Join(errs...)
}

// ------------------------------------------------------------------ the parts

// newKey is a fresh batch key for a note: 8 random bytes in hex.
func newKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

func (a app) logBatch(r *http.Request, what string, day types.ID, n int) {
	me, _ := mid.StewardFrom(r.Context())
	a.cfg.Log.InfoContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()),
		"day_id", day.String(), "recipients", n, "user_id", me.ID.String())
}
