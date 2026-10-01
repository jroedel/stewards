package placeapp

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// This file is the stewards' screen for what is listed at a place: which
// plants are planned here, and what to protect, pull or be careful with.
// /steward/places/{id}/plants. The plants themselves are made on the species
// screens; this only says what they are to this place.

type plantRow struct {
	SpeciesID, Name, Scientific string
	Actions                     []option
	Planned                     bool
	NoteEN, NoteES              string
}

type addView struct {
	Species        []option
	Actions        []option
	Planned        bool
	NoteEN, NoteES string
}

type plantsView struct {
	PlaceID, PlaceName, Slug string
	Rows                     []plantRow
	Add                      addView
	NoSpecies, AllListed     bool
	Done, Problem            string
}

func (a app) plants(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	v := plantsView{}

	switch r.URL.Query().Get("done") {
	case "saved":
		v.Done = "Saved."
	case "removed":
		v.Done = "Taken off this place's list."
	}

	a.showPlants(w, r, http.StatusOK, p, v, listingbus.Fields{Action: listingbus.Protect}, "")
}

// setPlant lists a plant here or changes how it is listed: the add form and
// each row's own form post here, since saving a listing is the same
// statement either way.
func (a app) setPlant(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	f := listingbus.Fields{
		Action:  listingbus.Action(r.PostFormValue("action")),
		Planned: r.PostFormValue("planned") == "yes",
		Note:    types.Text{EN: r.PostFormValue("note_en"), ES: r.PostFormValue("note_es")},
	}

	speciesID, err := types.ParseID(r.PostFormValue("species"))
	if err != nil {
		a.showPlants(w, r, http.StatusUnprocessableEntity, p, plantsView{Problem: "Choose the plant from the list."}, f, "")

		return
	}

	// From the card, an empty note leaves the plant's note as it is. The
	// card's Growing button and its "plant more" both send none, and change
	// only whether the plant is on To plant; a steward clearing a note does
	// it on this screen, where the note is in front of them.
	if fromCard := r.PostFormValue("return") == "card"; fromCard && f.Note.Trimmed().EN == "" && f.Note.Trimmed().ES == "" {
		listed, err := a.listings.ForPlace(r.Context(), p.ID)
		if err != nil {
			a.fail(w, r, "reading what is listed at a place", err)

			return
		}

		for _, l := range listed {
			if l.SpeciesID == speciesID {
				f.Note = l.Note
			}
		}
	}

	_, err = a.listings.Set(r.Context(), p.ID, speciesID, f)

	invalid, isInvalid := errors.AsType[listingbus.Invalid](err)

	switch {
	case err == nil && r.PostFormValue("return") == "card":
		http.Redirect(w, r, cardAnchor(p, f), http.StatusSeeOther)
	case err == nil:
		http.Redirect(w, r, "/steward/places/"+p.ID.String()+"/plants?done=saved", http.StatusSeeOther)
	case isInvalid:
		a.showPlants(w, r, http.StatusUnprocessableEntity, p, plantsView{Problem: page.Sentence(invalid.Problem)}, f, speciesID.String())
	default:
		a.fail(w, r, "listing a plant at a place", err)
	}
}

func (a app) removePlant(w http.ResponseWriter, r *http.Request) {
	p, ok := a.loadPlace(w, r)
	if !ok {
		return
	}

	speciesID, err := types.ParseID(r.PathValue("species"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	switch err := a.listings.Remove(r.Context(), p.ID, speciesID); {
	case err == nil, errors.Is(err, listingbus.ErrNotFound):
		// Already gone is the outcome that was asked for.
		http.Redirect(w, r, "/steward/places/"+p.ID.String()+"/plants?done=removed", http.StatusSeeOther)
	default:
		a.fail(w, r, "taking a plant off a place's list", err)
	}
}

// showPlants renders the page. f and chosen refill the add form after a
// refusal, so nothing typed is lost.
func (a app) showPlants(w http.ResponseWriter, r *http.Request, status int, p placebus.Place, v plantsView, f listingbus.Fields, chosen string) {
	listed, err := a.listings.ForPlace(r.Context(), p.ID)
	if err != nil {
		a.fail(w, r, "reading what is listed at a place", err)

		return
	}

	all, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing species", err)

		return
	}

	byID := map[types.ID]speciesbus.Species{}
	for _, sp := range all {
		byID[sp.ID] = sp
	}

	v.PlaceID, v.PlaceName, v.Slug = p.ID.String(), p.Name.EN, p.Slug
	v.NoSpecies = len(all) == 0

	here := map[types.ID]bool{}

	for _, l := range listed {
		sp := byID[l.SpeciesID]
		here[l.SpeciesID] = true

		v.Rows = append(v.Rows, plantRow{
			SpeciesID: l.SpeciesID.String(), Name: sp.Common.EN, Scientific: sp.Scientific,
			Actions: actionOptions(l.Action), Planned: l.Planned, NoteEN: l.Note.EN, NoteES: l.Note.ES,
		})
	}

	slices.SortFunc(v.Rows, func(x, y plantRow) int {
		return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})

	v.Add = addView{Actions: actionOptions(f.Action), Planned: f.Planned, NoteEN: f.Note.EN, NoteES: f.Note.ES}
	v.Add.Species = []option{{Value: "", Label: "Choose a plant", Selected: chosen == ""}}

	for _, sp := range all {
		if !here[sp.ID] {
			v.Add.Species = append(v.Add.Species, option{Value: sp.ID.String(), Label: sp.Common.EN, Selected: sp.ID.String() == chosen})
		}
	}

	v.AllListed = !v.NoSpecies && len(v.Add.Species) == 1

	a.render.Render(w, r, status, "place-plants", v)
}

// cardAnchor is where a card's "+ Add" comes back to: the place's card, at
// the list the plant went on. Made from the place and a fixed word, never
// from anything the form sent, so the form cannot be used to send a steward
// somewhere else. A refusal is not sent back: it is shown on the Plants
// screen with the form refilled, as for the screen's own form.
func cardAnchor(p placebus.Place, f listingbus.Fields) string {
	anchor := map[listingbus.Action]string{
		listingbus.Protect: "protect-h", listingbus.Pull: "pull-h", listingbus.Careful: "careful-h",
	}[f.Action]

	if f.Planned {
		anchor = "planned-h"
	}

	return "/places/" + p.Slug + "#" + anchor
}

func actionOptions(chosen listingbus.Action) []option {
	var out []option
	for _, act := range listingbus.Actions {
		out = append(out, option{Value: string(act), Label: act.Label(), Selected: act == chosen})
	}

	return out
}

// loadPlace reads the place named in the path, answering for it if there is
// none.
func (a app) loadPlace(w http.ResponseWriter, r *http.Request) (placebus.Place, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return placebus.Place{}, false
	}

	p, err := a.places.ByID(r.Context(), id)
	switch {
	case errors.Is(err, placebus.ErrNotFound):
		http.Error(w, "That place is not here any more. Go back to the list of places.", http.StatusNotFound)

		return placebus.Place{}, false
	case err != nil:
		a.fail(w, r, "reading a place", err)

		return placebus.Place{}, false
	}

	return p, true
}
