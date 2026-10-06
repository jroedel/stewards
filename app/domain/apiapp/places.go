package apiapp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Places are the backbone of the garden: named areas people stand in and
// work in. The API reads each as its card describes it, adds or changes one
// through placebus.Import, and puts one on the map through SetSpot. It
// removes none; see placebus.Import for why each is as it is.

// PlaceJSON is a place as the API shows it.
type PlaceJSON struct {
	Slug        string    `json:"slug"`
	Name        TextJSON  `json:"name"`
	Parent      string    `json:"parent,omitempty"`
	Purpose     TextJSON  `json:"purpose"`
	Conditions  TextJSON  `json:"conditions"`
	PhotoPoint  TextJSON  `json:"photo_point"`
	TrailAnchor string    `json:"trail_anchor,omitempty"`
	Sort        int       `json:"sort"`
	Spot        *SpotJSON `json:"spot,omitempty"`
	CardURL     string    `json:"card_url"`
}

// SpotJSON is a placebus.Spot: a point on the drawing of the property.
type SpotJSON struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// PlaceIn is the body of a PUT to a place: the whole of it, but where it is
// on the map.
type PlaceIn struct {
	// Slug may be sent, and must then be the one in the path.
	Slug string `json:"slug,omitempty"`

	Name        TextJSON `json:"name"`
	Parent      string   `json:"parent"`
	Purpose     TextJSON `json:"purpose"`
	Conditions  TextJSON `json:"conditions"`
	PhotoPoint  TextJSON `json:"photo_point"`
	TrailAnchor string   `json:"trail_anchor"`
	Sort        int      `json:"sort"`
}

func (a app) placeOf(p placebus.Place, slugs map[types.ID]string) PlaceJSON {
	out := PlaceJSON{
		Slug: p.Slug, Name: textOf(p.Name), Parent: slugs[p.ParentID],
		Purpose: textOf(p.Purpose), Conditions: textOf(p.Conditions), PhotoPoint: textOf(p.PhotoPoint),
		TrailAnchor: p.TrailAnchor, Sort: p.Sort,
		CardURL: a.base + "/places/" + p.Slug,
	}

	if p.Spot != nil {
		out.Spot = &SpotJSON{X: p.Spot.X, Y: p.Spot.Y}
	}

	return out
}

// placeSlugs is every place, and each one's slug by its ID, for naming a
// parent.
func (a app) placeSlugs(r *http.Request) ([]placebus.Place, map[types.ID]string, error) {
	all, err := a.places.All(r.Context())
	if err != nil {
		return nil, nil, err
	}

	slugs := map[types.ID]string{}
	for _, p := range all {
		slugs[p.ID] = p.Slug
	}

	return all, slugs, nil
}

func (a app) listPlaces(w http.ResponseWriter, r *http.Request) {
	all, slugs, err := a.placeSlugs(r)
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	out := []PlaceJSON{}
	for _, p := range all {
		out = append(out, a.placeOf(p, slugs))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{
		"places": out,
		"map":    map[string]int{"width": placebus.MapWidth, "height": placebus.MapHeight},
	})
}

func (a app) onePlace(w http.ResponseWriter, r *http.Request) {
	pl, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	all, slugs, err := a.placeSlugs(r)
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	inside := []string{}
	for _, p := range all {
		if p.ParentID == pl.ID {
			inside = append(inside, p.Slug)
		}
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"place": a.placeOf(pl, slugs), "inside": inside})
}

func (a app) putPlace(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	var in PlaceIn
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	if in.Slug != "" && in.Slug != slug {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("slug",
			fmt.Sprintf("The body says %q and the address says %q. A place's slug cannot change; send it to %s/places/%s.", in.Slug, slug, Prefix, in.Slug)))

		return
	}

	all, slugs, err := a.placeSlugs(r)
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	f := placebus.Fields{
		Slug: slug, Name: types.Text{EN: in.Name.EN, ES: in.Name.ES},
		Purpose:     types.Text{EN: in.Purpose.EN, ES: in.Purpose.ES},
		Conditions:  types.Text{EN: in.Conditions.EN, ES: in.Conditions.ES},
		PhotoPoint:  types.Text{EN: in.PhotoPoint.EN, ES: in.PhotoPoint.ES},
		TrailAnchor: in.TrailAnchor, Sort: in.Sort,
	}

	if in.Parent != "" {
		for _, p := range all {
			if p.Slug == in.Parent {
				f.ParentID = p.ID
			}
		}

		if f.ParentID.Zero() {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("parent",
				fmt.Sprintf("No place has the slug %q. GET %s/places lists them; send \"\" for a place that stands on its own.", in.Parent, Prefix)))

			return
		}
	}

	got, err := a.places.Import(r.Context(), f)

	invalid, isInvalid := errors.AsType[placebus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "importing a place", err)

		return
	}

	if got.Outcome != placebus.Unchanged {
		steward, _ := mid.StewardFrom(r.Context())
		a.log.InfoContext(r.Context(), "place imported", "place", got.Place.Slug, "outcome", got.Outcome, "user_id", steward.ID.String())
	}

	// A new place names itself as a parent's slug from now on.
	slugs[got.Place.ID] = got.Place.Slug

	status := http.StatusOK
	if got.Outcome == placebus.Created {
		status = http.StatusCreated
	}

	web.WriteJSON(w, status, map[string]any{"outcome": got.Outcome, "place": a.placeOf(got.Place, slugs)})
}

func (a app) putSpot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		X *int `json:"x"`
		Y *int `json:"y"`
	}
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	if in.X == nil || in.Y == nil {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("spot",
			fmt.Sprintf("Send both x and y, inside %d by %d. DELETE the spot to take the place off the map.", placebus.MapWidth, placebus.MapHeight)))

		return
	}

	a.setSpot(w, r, &placebus.Spot{X: *in.X, Y: *in.Y})
}

func (a app) deleteSpot(w http.ResponseWriter, r *http.Request) {
	a.setSpot(w, r, nil)
}

func (a app) setSpot(w http.ResponseWriter, r *http.Request, spot *placebus.Spot) {
	pl, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	p, err := a.places.SetSpot(r.Context(), pl.ID, spot)

	invalid, isInvalid := errors.AsType[placebus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "setting a place's spot", err)

		return
	}

	_, slugs, err := a.placeSlugs(r)
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	steward, _ := mid.StewardFrom(r.Context())
	a.log.InfoContext(r.Context(), "place spot set", "place", p.Slug, "on_map", p.Spot != nil, "user_id", steward.ID.String())

	web.WriteJSON(w, http.StatusOK, map[string]any{"place": a.placeOf(p, slugs)})
}
