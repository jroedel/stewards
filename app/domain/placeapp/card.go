package placeapp

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/types"
)

// This file is the place card: the page a volunteer opens from the list,
// at /places/<slug>. design.md, section 6: "What's here, what do I do?"
//
// It shows what a place holds today -- its name, what it is for, its
// conditions, its photo point, the smaller places inside it and, for a
// station's space, the way to that station's prayer. The rest of the card in
// the mockups (the dated photo, today's job, Protect and Pull, the bloom
// calendar) arrives with the data behind it, and until then the card says
// plainly that the plants are not listed yet, with the one piece of advice
// that keeps a native in the ground: not sure? Leave it.
//
// Public, like the list. The Phase 1 test is a volunteer with a phone and no
// account, so nothing here asks who you are; a steward who is signed in also
// gets a link to edit.

// trailURL is the public trail page. Its anchors are the QR codes on the
// boards, so a station's link is this plus "#" plus the anchor, and never
// anything else (design.md, section 1).
const trailURL = "https://schoenstatt-fathers.us/trail/"

// CardRoutes mounts the place card. Unlike Routes it is not behind sign-in,
// and it is mounted whether or not sign-in is configured.
func CardRoutes(mux *http.ServeMux, log *slog.Logger, render *page.Renderer, places Places) {
	a := app{log: log, render: render, places: places}

	mux.HandleFunc("GET /places/{slug}", a.card)
}

// The words on the card. English, with the Spanish to be written by a native
// speaker (design.md, principle 6); until then say marks the English.
type cardWording struct {
	Back, PartOf, Inside, Conditions, PhotoPoint, PhotoPointHelp,
	StationIntro, StationGo, NotListed, NotSure, Edit types.Text
}

var cardWords = cardWording{
	Back:           types.Text{EN: "All places"},
	PartOf:         types.Text{EN: "Part of"},
	Inside:         types.Text{EN: "Inside this place"},
	Conditions:     types.Text{EN: "Conditions"},
	PhotoPoint:     types.Text{EN: "Photo point"},
	PhotoPointHelp: types.Text{EN: "Where to stand for this place's photo each season."},
	StationIntro:   types.Text{EN: "A station on the Trail of the Saints."},
	StationGo:      types.Text{EN: "Open its prayer"},
	NotListed:      types.Text{EN: "The plants for this place are not listed yet."},
	NotSure:        types.Text{EN: "Not sure what something is? Leave it."},
	Edit:           types.Text{EN: "Edit this place"},
}

type cardRow struct {
	Slug    string
	Name    types.Text
	Purpose types.Text
}

type cardView struct {
	Copy cardWording

	Name, Purpose, Conditions, PhotoPoint types.Text

	// Parent is the place this one is inside, for a band; zero otherwise.
	Parent *cardRow
	Bands  []cardRow

	// Station and StationURL are set for a station's space.
	Station    types.Text
	StationURL string

	// EditURL is set only for a signed-in steward.
	EditURL string
}

func (a app) card(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	p, err := a.places.BySlug(ctx, r.PathValue("slug"))
	switch {
	case errors.Is(err, placebus.ErrNotFound):
		// A page rather than a bare 404, because the likely way here is a
		// link typed from a stake or a guide, by somebody standing in the
		// garden who needs the list more than an error.
		a.render.Render(w, r, http.StatusNotFound, "place-missing", cardWords)

		return
	case err != nil:
		a.fail(w, r, "reading a place for its card", err)

		return
	}

	v := cardView{
		Copy: cardWords,
		Name: p.Name, Purpose: p.Purpose, Conditions: p.Conditions, PhotoPoint: p.PhotoPoint,
	}

	if !p.TopLevel() {
		parent, err := a.places.ByID(ctx, p.ParentID)
		if err != nil {
			a.fail(w, r, "reading the place a band is inside", err)

			return
		}

		v.Parent = &cardRow{Slug: parent.Slug, Name: parent.Name}
	}

	bands, err := a.places.Children(ctx, p.ID)
	if err != nil {
		a.fail(w, r, "reading the places inside a place", err)

		return
	}

	for _, b := range bands {
		v.Bands = append(v.Bands, cardRow{Slug: b.Slug, Name: b.Name, Purpose: b.Purpose})
	}

	if name, ok := stationName[p.TrailAnchor]; ok {
		v.Station = types.Text{EN: name}
		v.StationURL = trailURL + "#" + p.TrailAnchor
	}

	if _, ok := mid.StewardFrom(ctx); ok {
		v.EditURL = "/steward/places/" + p.ID.String() + "/edit"
	}

	a.render.Render(w, r, http.StatusOK, "place", v)
}
