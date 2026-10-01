// Package homeapp is the two screens a newcomer starts from: the home page,
// and the list of places it leads to.
//
// The home page is where the QR code on the trail's signs leads, so it is
// written for someone standing on the path who has never been here and is
// wondering whether they could help. It says they are welcome without any
// skills, lists the stewardship days coming up, and offers the trail itself
// to explore. The days are workdaybus's; the stewards schedule them on their
// own screen (workdayapp), and the email sign-up near the top posts to
// signupapp.
//
// The list of places at /places is what the home page was before it: the Map
// screen's accessible twin, "Where are you working?" (design.md, section 6),
// without the drawn map above it. The map is a schematic drawn over the
// public trail map's geometry, and it comes with the place pages it links
// to; the list is the half that works for everybody from the first day,
// which is why design.md insists the map always has one.
package homeapp

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// PlacesPath is the list of places, where "Explore the trail" leads and a
// place card's back link returns to.
const PlacesPath = "/places"

// Places is what these screens need from the place rules.
type Places interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// Days is what the home page needs from the work-day rules.
type Days interface {
	Upcoming(ctx context.Context) ([]workdaybus.Day, error)
	Now() time.Time
}

// App serves the two screens.
type App struct {
	log    *slog.Logger
	render *page.Renderer
	places Places
	days   Days
	signUp bool
}

// New constructs one. signUp says whether to show the email sign-up form,
// which signupapp answers; it is off when that app is not mounted -- no
// relay, or sign-in off -- because a form that can never send its
// confirmation is worse than no form.
func New(log *slog.Logger, render *page.Renderer, places Places, days Days, signUp bool) *App {
	return &App{log: log, render: render, places: places, days: days, signUp: signUp}
}

// Routes mounts the app.
func (a *App) Routes(mux *http.ServeMux) {
	// "GET /{$}" is the root and nothing under it; a bare "GET /" would also
	// answer every unknown path with the home page instead of a 404.
	mux.HandleFunc("GET /{$}", a.home)

	// Exact, so it sits beside placeapp's /places/{slug} without taking it.
	mux.HandleFunc("GET "+PlacesPath, a.list)
}

// The copy on these screens. Spanish waits for a native speaker to write it
// (design.md, principle 6), and until then the page shows the English marked
// lang="en".
type wording struct {
	Eyebrow, Title, Lead types.Text

	Days, NoDays, Today, Now types.Text

	// The sign-up form's words, the same as signupapp's own copy of the
	// form; see the template.
	FormTitle, FormHelp, FormLabel, FormButton types.Text

	Explore, ExploreWhat types.Text
	TrailAsk, TrailGo    types.Text

	PlacesEyebrow, PlacesTitle, PlacesLead, Places, Empty types.Text
}

var words = wording{
	Eyebrow: types.Text{EN: "Trail of the Saints"},
	Title:   types.Text{EN: "Come help in the garden"},
	Lead: types.Text{EN: "On stewardship days we care for the ground along the trail together: planting, weeding, mulching, watering. " +
		"Everyone is welcome. You don't need any experience or special skills. We'll show you what to do, and there is work for every pair of hands."},

	Days:   types.Text{EN: "Upcoming stewardship days"},
	NoDays: types.Text{EN: "No days are scheduled just now. Check back soon."},
	Today:  types.Text{EN: "Today"},
	Now:    types.Text{EN: "Happening now"},

	FormTitle:  types.Text{EN: "Hear about the next day"},
	FormHelp:   types.Text{EN: "Leave your email and we'll write when a stewardship day is scheduled. Nothing else, and you can stop any time."},
	FormLabel:  types.Text{EN: "Your email"},
	FormButton: types.Text{EN: "Keep me posted"},

	Explore:     types.Text{EN: "Explore the trail"},
	ExploreWhat: types.Text{EN: "the places along it, and what grows in each"},
	TrailAsk:    types.Text{EN: "Walking the trail to pray?"},
	TrailGo:     types.Text{EN: "Stations and prayers"},

	PlacesEyebrow: types.Text{EN: "Garden stewards"},
	PlacesTitle:   types.Text{EN: "Where are you working?"},
	PlacesLead:    types.Text{EN: "Pick the place you are standing in."},
	Places:        types.Text{EN: "Places"},
	Empty:         types.Text{EN: "No places have been added yet."},
}

// ------------------------------------------------------------------ home

// day is one stewardship day as the home page shows it.
type day struct {
	Date, Hours    types.Text
	Title, Details types.Text

	// Today and Now are the "Today" tag design.md gives a work day:
	// whoever scans the sign on a work morning should learn the stewards
	// are out there. Now wins when both hold.
	Today, Now bool
}

type homeView struct {
	Copy   wording
	Days   []day
	SignUp bool
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	up, err := a.days.Upcoming(r.Context())
	if err != nil {
		a.fail(w, r, "listing days for the home page", err)

		return
	}

	now := a.days.Now()
	today := now.In(types.Garden).Format(time.DateOnly)

	v := homeView{Copy: words, SignUp: a.signUp}
	for _, d := range up {
		v.Days = append(v.Days, day{
			Date: page.Date(d.Starts), Hours: page.Hours(d.Starts, d.Ends),
			Title: d.Title, Details: d.Details,
			Today: d.Starts.In(types.Garden).Format(time.DateOnly) == today,
			Now:   !now.Before(d.Starts) && now.Before(d.Ends),
		})
	}

	a.render.Render(w, r, http.StatusOK, "home", v)
}

// ------------------------------------------------------------------ places

// row is one place in the list, top-level places only: a place's bands
// belong on its own card, not in the list of where you might be standing.
type row struct {
	Slug    string
	Name    types.Text
	Purpose types.Text
}

type placesView struct {
	Copy   wording
	Places []row
}

func (a *App) list(w http.ResponseWriter, r *http.Request) {
	all, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	v := placesView{Copy: words}
	for _, p := range all {
		if p.TopLevel() {
			v.Places = append(v.Places, row{Slug: p.Slug, Name: p.Name, Purpose: p.Purpose})
		}
	}

	a.render.Render(w, r, http.StatusOK, "places", v)
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "id", web.RequestIDFrom(r.Context()), "err", err)
	http.Error(w, "Something went wrong on our end. Try once more in a minute; if it happens again, tell the garden stewards what you were doing.", http.StatusInternalServerError)
}
