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
	"github.com/jroedel/stewards/app/sdk/page"
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
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Days   Days
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
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type row struct {
	ID          string
	Date, Hours page.Phrase
	Title       types.Text
}

type listView struct {
	Upcoming, Recent []row
	Done             string
}

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

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("done") {
	case "added":
		v.Done = "Day added. It is on the home page now."
	case "saved":
		v.Done = "Changes saved."
	case "removed":
		v.Done = "Day removed from the calendar."
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "steward-days", v)
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
	ID, Heading string

	Date, Starts, Ends string
	Title, Details     page.Box

	Problems      map[string]string
	DeleteProblem string
}

// The inputs' own formats: what <input type="date"> and type="time" send.
const (
	dateLayout = "2006-01-02"
	timeLayout = "15:04"
)

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	// A morning, since that is when most work days are; the steward
	// changes it if not. The date is left for them to choose.
	a.cfg.Render.Render(w, r, http.StatusOK, "day-form", formView{Heading: "Add a day", Starts: "08:00", Ends: "11:00"})
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	d, ok := a.load(w, r)
	if !ok {
		return
	}

	a.cfg.Render.Render(w, r, http.StatusOK, "day-form", viewOf(d, mid.LangFrom(r.Context())))
}

func (a app) create(w http.ResponseWriter, r *http.Request) {
	f, v, ok := read(w, r)
	if !ok {
		return
	}

	v.Heading = "Add a day"

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
		err := a.cfg.Days.Delete(r.Context(), d.ID)

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
	v.DeleteProblem = "Tick the box to confirm, then press Remove again."

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
		Problems: map[string]string{},
	}

	f := workdaybus.Fields{Title: v.Title.Text(l), Details: v.Details.Text(l)}

	day, err := time.ParseInLocation(dateLayout, v.Date, types.Garden)
	if err != nil {
		v.Problems["date"] = "Choose the date of the day."

		return f, v, true
	}

	// A time left empty is a zero time, and the rules say which one is
	// missing; a time the browser sent in some other shape is refused here.
	f.Starts, err = at(day, v.Starts)
	if err != nil {
		v.Problems["date"] = "Write the start time as hours and minutes, such as 08:00."
	}

	f.Ends, err = at(day, v.Ends)
	if err != nil {
		v.Problems["ends"] = "Write the end time as hours and minutes, such as 11:30."
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

	v.Problems[invalid.Field] = page.Sentence(invalid.Problem)

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

func heading(d workdaybus.Day) string { return "Edit " + page.Date(d.Starts).In(types.English) }

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
