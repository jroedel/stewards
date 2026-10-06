package apiapp

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// A listing is a plant at a place: what a volunteer does with it there, and
// whether it is part of the planting. The API reads a place's listings, adds
// or changes one through listingbus.Import -- pull included, since the
// stewards decided it may (see there) -- and takes one off through Unlist.

// ListingIn is the body of a PUT to a place's plant.
type ListingIn struct {
	Action  string   `json:"action"`
	Planned bool     `json:"planned"`
	Note    TextJSON `json:"note"`
}

// ListingJSON is a listing as the API shows it.
type ListingJSON struct {
	Species string   `json:"species"`
	Action  string   `json:"action"`
	Planned bool     `json:"planned"`
	Note    TextJSON `json:"note"`
	CardURL string   `json:"card_url"`
}

func (a app) placePlants(w http.ResponseWriter, r *http.Request) {
	pl, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	listed, err := a.listings.ForPlace(r.Context(), pl.ID)
	if err != nil {
		a.fail(w, r, "listing a place's plants", err)

		return
	}

	all, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing species", err)

		return
	}

	slugs := map[types.ID]string{}
	for _, sp := range all {
		slugs[sp.ID] = sp.Slug
	}

	out := []ListingJSON{}
	for _, l := range listed {
		out = append(out, a.listingOf(l, slugs[l.SpeciesID]))
	}

	slices.SortFunc(out, func(x, y ListingJSON) int { return cmp.Compare(x.Species, y.Species) })

	web.WriteJSON(w, http.StatusOK, map[string]any{"place": pl.Slug, "plants": out})
}

func (a app) putPlacePlant(w http.ResponseWriter, r *http.Request) {
	var in ListingIn
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	pl, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	sp, ok := a.speciesAt(w, r, r.PathValue("species"))
	if !ok {
		return
	}

	action := listingbus.Action(in.Action)
	if !slices.Contains(listingbus.Actions, action) {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("action",
			"Send protect, to leave it; pull, to take it out; or careful, to handle it with gloves on as the note says."))

		return
	}

	got, err := a.listings.Import(r.Context(), pl.ID, sp.ID, listingbus.Fields{
		Action: action, Planned: in.Planned, Note: types.Text{EN: in.Note.EN, ES: in.Note.ES},
	})

	invalid, isInvalid := errors.AsType[listingbus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "importing a listing", err)

		return
	}

	steward, _ := mid.StewardFrom(r.Context())
	if got.Outcome != listingbus.Unchanged {
		a.log.InfoContext(r.Context(), "listing imported", "place", pl.Slug, "species", sp.Slug,
			"action", got.Listing.Action, "planned", got.Listing.Planned, "outcome", got.Outcome, "user_id", steward.ID.String())
	}

	status := http.StatusOK
	if got.Outcome == listingbus.Created {
		status = http.StatusCreated
	}

	web.WriteJSON(w, status, map[string]any{
		"outcome": got.Outcome,
		"place":   pl.Slug,
		"listing": a.listingOf(got.Listing, sp.Slug),
	})
}

func (a app) listingOf(l listingbus.Listing, slug string) ListingJSON {
	return ListingJSON{
		Species: slug, Action: string(l.Action), Planned: l.Planned, Note: textOf(l.Note),
		CardURL: a.base + "/plants/" + slug,
	}
}

func (a app) loadPlace(w http.ResponseWriter, r *http.Request) (placebus.Place, bool) {
	slug := r.PathValue("slug")

	pl, err := a.places.BySlug(r.Context(), slug)

	switch {
	case errors.Is(err, placebus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("slug",
			fmt.Sprintf("No place has the slug %q. GET %s/places lists the ones there are; PUT %s/places/%s adds it.", slug, Prefix, Prefix, slug)))

		return placebus.Place{}, false
	case err != nil:
		a.fail(w, r, "reading a place", err)

		return placebus.Place{}, false
	}

	return pl, true
}

// speciesAt is loadSpecies for a route whose plant is {species} rather
// than {slug}.
func (a app) speciesAt(w http.ResponseWriter, r *http.Request, slug string) (speciesbus.Species, bool) {
	sp, err := a.species.BySlug(r.Context(), slug)

	switch {
	case errors.Is(err, speciesbus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("species",
			fmt.Sprintf("No plant has the slug %q. PUT %s/species/%s adds it first; GET %s/species lists the ones there are.", slug, Prefix, slug, Prefix)))

		return speciesbus.Species{}, false
	case err != nil:
		a.fail(w, r, "reading a species", err)

		return speciesbus.Species{}, false
	}

	return sp, true
}

// deletePlacePlant takes a plant off a place's list.
func (a app) deletePlacePlant(w http.ResponseWriter, r *http.Request) {
	pl, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	sp, ok := a.speciesAt(w, r, r.PathValue("species"))
	if !ok {
		return
	}

	outcome, err := a.listings.Unlist(r.Context(), pl.ID, sp.ID)
	if err != nil {
		a.fail(w, r, "taking a plant off a place", err)

		return
	}

	if outcome == listingbus.Removed {
		steward, _ := mid.StewardFrom(r.Context())
		a.log.InfoContext(r.Context(), "listing removed", "place", pl.Slug, "species", sp.Slug, "user_id", steward.ID.String())
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"outcome": outcome, "place": pl.Slug, "species": sp.Slug})
}

// actionNames is every action a listing can have, for the index.
func actionNames() []string {
	var out []string
	for _, a := range listingbus.Actions {
		out = append(out, string(a))
	}

	return out
}
