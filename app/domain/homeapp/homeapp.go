// Package homeapp is the home screen: "Where are you working?" (design.md,
// section 6, the Map screen).
//
// Today it is the Map screen's accessible twin, the list of places, without
// the drawn map above it. The map is a schematic drawn over the public trail
// map's geometry, and it comes with the place pages it links to; the list is
// the half that works for everybody from the first day, which is why
// design.md insists the map always has one.
package homeapp

import (
	"context"
	"embed"
	"log/slog"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// Places is what the home screen needs from the place rules.
type Places interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// App serves the home screen.
type App struct {
	log    *slog.Logger
	render *page.Renderer
	places Places
}

// New constructs one.
func New(log *slog.Logger, render *page.Renderer, places Places) *App {
	return &App{log: log, render: render, places: places}
}

// Routes mounts the app.
func (a *App) Routes(mux *http.ServeMux) {
	// "GET /{$}" is the root and nothing under it; a bare "GET /" would also
	// answer every unknown path with the home page instead of a 404.
	mux.HandleFunc("GET /{$}", a.home)
}

// The copy on this screen. English is in the mockups; Spanish waits for a
// native speaker to write it (design.md, principle 6), and until then the
// page shows the English marked lang="en".
type wording struct {
	Eyebrow, Title, Lead, Places, Empty, TrailAsk, TrailGo types.Text
}

var words = wording{
	Eyebrow:  types.Text{EN: "Garden stewards"},
	Title:    types.Text{EN: "Where are you working?"},
	Lead:     types.Text{EN: "Pick the place you are standing in."},
	Places:   types.Text{EN: "Places"},
	Empty:    types.Text{EN: "No places have been added yet."},
	TrailAsk: types.Text{EN: "Walking the trail to pray?"},
	TrailGo:  types.Text{EN: "Stations and prayers"},
}

// row is one place in the list, top-level places only: a place's bands
// belong on its own card, not in the list of where you might be standing.
type row struct {
	Name    types.Text
	Purpose types.Text
}

type view struct {
	Copy   wording
	Places []row
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	all, err := a.places.All(r.Context())
	if err != nil {
		a.log.ErrorContext(r.Context(), "listing places for the home screen",
			"id", web.RequestIDFrom(r.Context()), "err", err)
		http.Error(w, "Something went wrong on our end. Try once more in a minute; if it happens again, tell the garden stewards what you were doing.", http.StatusInternalServerError)

		return
	}

	v := view{Copy: words}
	for _, p := range all {
		if p.TopLevel() {
			v.Places = append(v.Places, row{Name: p.Name, Purpose: p.Purpose})
		}
	}

	a.render.Render(w, r, http.StatusOK, "home", v)
}
