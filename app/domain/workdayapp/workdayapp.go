// Package workdayapp is the stewards' screen for stewardship days: the list
// of what is coming and what has just been, and the form that schedules a
// day, changes it, or takes it off the calendar.
//
// The public half -- the days on the home page, which is where the QR code on
// the trail's signs leads -- is homeapp's. It reads the same rules; neither
// app imports the other.
//
// The form asks for a date and two times rather than two date-and-time
// inputs. A work day is a morning or an afternoon on one date, and a phone's
// datetime picker, asked twice, is twice the chance of the end landing on the
// wrong day. The times are read in the garden's zone, so "8:00" is 8 am in
// Austin whatever the server's clock says.
package workdayapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// Path is the list of days.
const Path = "/steward/days"

// recentShown is how many past days the list keeps: enough to correct the
// last few, without the page growing every month.
const recentShown = 5

// Days is what this app needs from the work-day rules.
type Days interface {
	Upcoming(ctx context.Context) ([]workdaybus.Day, error)
	Recent(ctx context.Context, limit int) ([]workdaybus.Day, error)
	ByID(ctx context.Context, id types.ID) (workdaybus.Day, error)
	Create(ctx context.Context, f workdaybus.Fields) (workdaybus.Day, error)
	Update(ctx context.Context, id types.ID, f workdaybus.Fields) (workdaybus.Day, error)
	Delete(ctx context.Context, id types.ID) error
	Now() time.Time
}

// RSVPs is what this app needs from the sign-up rules: who is coming, with
// how to reach them, and taking a name off.
type RSVPs interface {
	ForDay(ctx context.Context, day types.ID) ([]rsvpbus.RSVP, error)
	Counts(ctx context.Context, days []types.ID) (map[types.ID]int, error)
	Remove(ctx context.Context, id types.ID) error
	Changes(ctx context.Context, from, to time.Time) (rsvpbus.Changes, error)
}

// Config is what this app needs. RSVPs may be nil, when the sign-up is not
// mounted, and the screens then say nothing of who is coming.
//
// Notices, List, Links and Background are for writing to many (messages.go),
// and with any of them nil -- or RSVPs -- nothing is sent and no send screen
// is offered. Background is the app's context, which a send outlives the
// request that pressed its button on.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Days   Days
	RSVPs  RSVPs

	Notices    Sender
	List       List
	Links      Links
	Background context.Context

	// Stewards is who chose the once-a-day summary (summary.go); nil, and
	// it is not sent.
	Stewards Stewards
}

type app struct{ cfg Config }

// Routes mounts the app, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + Path:                   a.list,
		"GET " + Path + "/new":          a.newForm,
		"POST " + Path:                  a.create,
		"GET " + Path + "/{id}/edit":    a.editForm,
		"POST " + Path + "/{id}":        a.update,
		"POST " + Path + "/{id}/delete": a.remove,

		"POST " + Path + "/{id}/coming/{rsvp}/remove": a.removeComing,

		"GET " + Path + "/{id}/announce":  a.announceForm,
		"POST " + Path + "/{id}/announce": a.announceSend,
		"GET " + Path + "/{id}/changed":   a.changedForm,
		"POST " + Path + "/{id}/changed":  a.changedSend,
		"POST " + Path + "/{id}/note":     a.noteSend,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type row struct {
	ID          string
	Date, Hours page.Phrase
	Title       types.Text
	Coming      int
}

type listView struct {
	Upcoming, Recent []row
	Done             any
}

// The words this app says from Go, in English; Claude translates them
// through the translation memory, and say looks them up (page/words.go).
// The rest are in the templates, through t.
type wording struct {
	Added, Saved, Removed, AddADay, Edit, Confirm, NoDate, BadStart, BadEnd types.Text
}

var words = wording{
	Added:    types.Text{EN: "Day added. It is on the home page now."},
	Saved:    types.Text{EN: "Changes saved."},
	Removed:  types.Text{EN: "Day removed from the calendar."},
	AddADay:  types.Text{EN: "Add a day"},
	Edit:     types.Text{EN: "Edit {date}"},
	Confirm:  types.Text{EN: "Tick the box to confirm, then press Remove again."},
	NoDate:   types.Text{EN: "Choose the date of the day."},
	BadStart: types.Text{EN: "Write the start time as hours and minutes, such as 08:00."},
	BadEnd:   types.Text{EN: "Write the end time as hours and minutes, such as 11:30."},
}

// Words is this app's copy held in Go, for the catalog the translation
// memory lists.
var Words = page.Catalog{{Where: "the stewards' screens for stewardship days, the days anyone may come and help", Words: words}}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	up, err := a.cfg.Days.Upcoming(r.Context())
	if err != nil {
		a.fail(w, r, "listing upcoming days", err)

		return
	}

	past, err := a.cfg.Days.Recent(r.Context(), recentShown)
	if err != nil {
		a.fail(w, r, "listing recent days", err)

		return
	}

	v := listView{Upcoming: rows(up), Recent: rows(past)}

	if err := a.count(r.Context(), v.Upcoming); err != nil {
		a.fail(w, r, "counting who is coming", err)

		return
	}

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("done") {
	case "added":
		v.Done = words.Added
	case "saved":
		v.Done = words.Saved
	case "removed":
		v.Done = words.Removed
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "steward-days", v)
}

// count fills in how many are coming to each row.
func (a app) count(ctx context.Context, rs []row) error {
	if a.cfg.RSVPs == nil || len(rs) == 0 {
		return nil
	}

	ids := make([]types.ID, 0, len(rs))
	for _, r := range rs {
		id, _ := types.ParseID(r.ID)
		ids = append(ids, id)
	}

	counts, err := a.cfg.RSVPs.Counts(ctx, ids)
	if err != nil {
		return err
	}

	for i, id := range ids {
		rs[i].Coming = counts[id]
	}

	return nil
}

func rows(days []workdaybus.Day) []row {
	out := make([]row, 0, len(days))
	for _, d := range days {
		out = append(out, row{ID: d.ID.String(), Date: page.Date(d.Starts), Hours: page.Hours(d.Starts, d.Ends), Title: d.Title})
	}

	return out
}

// ------------------------------------------------------------------ the form

// formView is the form exactly as typed, so a refusal gives back what was
// sent rather than what was saved.
type formView struct {
	ID      string
	Heading any

	Date, Starts, Ends string
	Title, Details     page.Box

	Problems      map[string]any
	DeleteProblem any

	// Coming is who has said they will come, for a day being edited; nil
	// when the sign-up is off.
	Coming  []coming
	Signups bool
	Removed bool

	// Messaging is whether this day's screen offers the sends to many;
	// Sent is what went out about it, Told whether the list has been told,
	// and Sending that a send was pressed a moment ago.
	Messaging bool
	Sent      []sentRow
	Told      bool
	Sending   bool
	Cancel    *notices.Preview
}

// coming is one person on a day, as a steward sees them: the name shown,
// and how to reach them.
type coming struct {
	ID, Name, Email, Phone string
}

// The inputs' own formats: what <input type="date"> and type="time" send.
const (
	dateLayout = "2006-01-02"
	timeLayout = "15:04"
)

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	// A morning, since that is when most work days are; the steward
	// changes it if not. The date is left for them to choose.
	a.cfg.Render.Render(w, r, http.StatusOK, "day-form", formView{Heading: words.AddADay, Starts: "08:00", Ends: "11:00"})
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	d, ok := a.load(w, r)
	if !ok {
		return
	}

	v := viewOf(d, mid.LangFrom(r.Context()))
	if !a.withComing(w, r, d.ID, &v) || !a.withSent(w, r, d, &v) {
		return
	}

	v.Removed = r.URL.Query().Get("done") == "removed"
	v.Sending = r.URL.Query().Get("done") == "sending"

	a.cfg.Render.Render(w, r, http.StatusOK, "day-form", v)
}

// withComing adds who is coming to v, or answers 500 and reports false.
func (a app) withComing(w http.ResponseWriter, r *http.Request, day types.ID, v *formView) bool {
	if a.cfg.RSVPs == nil {
		return true
	}

	rs, err := a.cfg.RSVPs.ForDay(r.Context(), day)
	if err != nil {
		a.fail(w, r, "reading who is coming", err)

		return false
	}

	v.Signups = true
	for _, rv := range rs {
		v.Coming = append(v.Coming, coming{
			ID: rv.ID.String(), Name: rv.Name,
			Email: rv.Contact.Email.String(), Phone: rv.Contact.Phone.National(),
		})
	}

	return true
}

// withSent adds what went out about d, and the cancellation the delete form
// would send, or answers 500 and reports false.
func (a app) withSent(w http.ResponseWriter, r *http.Request, d workdaybus.Day, v *formView) bool {
	if !a.messaging() {
		return true
	}

	sent, told, err := a.sent(r.Context(), d)
	if err != nil {
		a.fail(w, r, "reading what was sent", err)

		return false
	}

	v.Messaging, v.Sent, v.Told = true, sent, told

	if len(v.Coming) > 0 {
		p := a.cancelPreview(mid.LangFrom(r.Context()), d)
		v.Cancel = &p
	}

	return true
}

// removeComing takes a name off a day: one that should not be on a public
// page, or someone who said in person they cannot come. The sign-up form
// needs no box ticked for it, unlike removing a day: it is one name, and
// they can sign up again.
func (a app) removeComing(w http.ResponseWriter, r *http.Request) {
	d, ok := a.load(w, r)
	if !ok {
		return
	}

	id, err := types.ParseID(r.PathValue("rsvp"))
	if err != nil || a.cfg.RSVPs == nil {
		http.NotFound(w, r)

		return
	}

	if err := a.cfg.RSVPs.Remove(r.Context(), id); err != nil && !errors.Is(err, rsvpbus.ErrNotFound) {
		a.fail(w, r, "removing a sign-up", err)

		return
	}

	me, _ := mid.StewardFrom(r.Context())
	a.cfg.Log.InfoContext(r.Context(), "a sign-up removed by a steward", "request_id", web.RequestIDFrom(r.Context()),
		"day_id", d.ID.String(), "rsvp_id", id.String(), "user_id", me.ID.String())

	http.Redirect(w, r, Path+"/"+d.ID.String()+"/edit?done=removed", http.StatusSeeOther)
}

func (a app) create(w http.ResponseWriter, r *http.Request) {
	f, v, ok := read(w, r)
	if !ok {
		return
	}

	v.Heading = words.AddADay

	if len(v.Problems) == 0 {
		_, err := a.cfg.Days.Create(r.Context(), f)
		if err == nil {
			http.Redirect(w, r, Path+"?done=added", http.StatusSeeOther)

			return
		}

		if !a.refused(w, r, &v, err) {
			return
		}
	}

	a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "day-form", v)
}

func (a app) update(w http.ResponseWriter, r *http.Request) {
	d, ok := a.load(w, r)
	if !ok {
		return
	}

	f, v, ok := read(w, r)
	if !ok {
		return
	}

	v.ID, v.Heading = d.ID.String(), heading(d)

	if len(v.Problems) == 0 {
		_, err := a.cfg.Days.Update(r.Context(), d.ID, f)
		if err == nil {
			// A new time for a day people have signed up for: offer to
			// tell them, with the message shown first.
			if moved := !f.Starts.Equal(d.Starts) || !f.Ends.Equal(d.Ends); moved && a.messaging() {
				if rs, err := a.cfg.RSVPs.ForDay(r.Context(), d.ID); err == nil && len(rs) > 0 {
					http.Redirect(w, r, Path+"/"+d.ID.String()+"/changed", http.StatusSeeOther)

					return
				}
			}

			http.Redirect(w, r, Path+"?done=saved", http.StatusSeeOther)

			return
		}

		if !a.refused(w, r, &v, err) {
			return
		}
	}

	a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "day-form", v)
}

// remove takes a day off the calendar, and only with the box ticked, for the
// reason placeapp's remove gives: there is no script here to ask first.
func (a app) remove(w http.ResponseWriter, r *http.Request) {
	d, ok := a.load(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	if r.PostFormValue("confirm") == "yes" {
		// Who was coming, read before the day goes and its sign-ups with
		// it, so the people on them can still be told.
		var coming []rsvpbus.RSVP

		if a.messaging() && r.PostFormValue("tell") == "yes" && !a.over(d) {
			rs, err := a.cfg.RSVPs.ForDay(r.Context(), d.ID)
			if err != nil {
				a.fail(w, r, "reading who is coming", err)

				return
			}

			coming = rs
		}

		err := a.cfg.Days.Delete(r.Context(), d.ID)
		if err == nil && len(coming) > 0 {
			a.tellCancelled(r, d, coming)
		}

		switch {
		case err == nil, errors.Is(err, workdaybus.ErrNotFound):
			// Gone either way: removed now, or by another steward a
			// moment ago.
			http.Redirect(w, r, Path+"?done=removed", http.StatusSeeOther)
		default:
			a.fail(w, r, "removing a day", err)
		}

		return
	}

	v := viewOf(d, mid.LangFrom(r.Context()))
	if !a.withComing(w, r, d.ID, &v) || !a.withSent(w, r, d, &v) {
		return
	}

	v.DeleteProblem = words.Confirm

	a.cfg.Render.Render(w, r, http.StatusUnprocessableEntity, "day-form", v)
}

// ------------------------------------------------------------------ the parts

func (a app) load(w http.ResponseWriter, r *http.Request) (workdaybus.Day, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return workdaybus.Day{}, false
	}

	d, err := a.cfg.Days.ByID(r.Context(), id)

	switch {
	case errors.Is(err, workdaybus.ErrNotFound):
		http.Error(w, "That day is not on the calendar any more. It may have been removed. Go back to the list of days.", http.StatusNotFound)

		return workdaybus.Day{}, false
	case err != nil:
		a.fail(w, r, "reading a day", err)

		return workdaybus.Day{}, false
	}

	return d, true
}

// read turns the posted form into Fields, and into a view holding what was
// typed. The refusals here are about the inputs' shape -- a date the browser
// should not have sent -- and every rule about what a day may be is
// workdaybus's.
func read(w http.ResponseWriter, r *http.Request) (workdaybus.Fields, formView, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return workdaybus.Fields{}, formView{}, false
	}

	get, l := r.PostFormValue, mid.LangFrom(r.Context())

	v := formView{
		Date: get("date"), Starts: get("starts"), Ends: get("ends"),
		Title: page.Typed(get("title"), l), Details: page.Typed(get("details"), l),
		Problems: map[string]any{},
	}

	f := workdaybus.Fields{Title: v.Title.Text(l), Details: v.Details.Text(l)}

	day, err := time.ParseInLocation(dateLayout, v.Date, types.Garden)
	if err != nil {
		v.Problems["date"] = words.NoDate

		return f, v, true
	}

	// A time left empty is a zero time, and the rules say which one is
	// missing; a time the browser sent in some other shape is refused here.
	f.Starts, err = at(day, v.Starts)
	if err != nil {
		v.Problems["date"] = words.BadStart
	}

	f.Ends, err = at(day, v.Ends)
	if err != nil {
		v.Problems["ends"] = words.BadEnd
	}

	return f, v, true
}

// at is the time of day hm on day, in the garden's zone; empty hm is the
// zero time.
func at(day time.Time, hm string) (time.Time, error) {
	if hm == "" {
		return time.Time{}, nil
	}

	t, err := time.Parse(timeLayout, hm)
	if err != nil {
		return time.Time{}, err
	}

	return time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, types.Garden), nil
}

// refused marks the problem a rule found beside its input and reports true,
// or answers 500 itself and reports false when no rule was involved.
func (a app) refused(w http.ResponseWriter, r *http.Request, v *formView, err error) bool {
	invalid, ok := errors.AsType[workdaybus.Invalid](err)
	if !ok {
		a.fail(w, r, "saving a day", err)

		return false
	}

	v.Problems[invalid.Field] = types.Text{EN: page.Sentence(invalid.Problem)}

	return true
}

// viewOf is d in the form, on a page in l.
func viewOf(d workdaybus.Day, l types.Lang) formView {
	s, e := d.Starts.In(types.Garden), d.Ends.In(types.Garden)

	return formView{
		ID: d.ID.String(), Heading: heading(d),
		Date: s.Format(dateLayout), Starts: s.Format(timeLayout), Ends: e.Format(timeLayout),
		Title: page.BoxOf(d.Title, l), Details: page.BoxOf(d.Details, l),
	}
}

func heading(d workdaybus.Day) page.Phrase { return page.Put(words.Edit, "date", page.Date(d.Starts)) }

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
