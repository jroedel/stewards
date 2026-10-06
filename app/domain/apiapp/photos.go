package apiapp

import (
	"errors"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// PhotoPatch is the body of a PATCH to a photo: each field that is there is a
// change, and each that is left out stays as it was. Pointers, because "not
// sent" and "sent empty" are different answers here -- a place of "" is the
// place taken away.
//
// Checked is read only so that sending it is refused with photobus.Amend's
// reason rather than as a field this endpoint has never heard of.
type PhotoPatch struct {
	Kind       *string `json:"kind"`
	Source     *string `json:"source"`
	Credit     *string `json:"credit"`
	SourceURL  *string `json:"source_url"`
	License    *string `json:"license"`
	TakenMonth *int    `json:"taken_month"`
	TakenYear  *int    `json:"taken_year"`
	TakenOn    *string `json:"taken_on"`
	InFlower   *bool   `json:"in_flower"`
	Place      *string `json:"place"`
	Elsewhere  *bool   `json:"elsewhere"`
	TakenWhere *string `json:"taken_where"`
	Checked    *bool   `json:"checked"`
}

// patchPhoto changes what is said about a plant photo. See photobus.Amend.
func (a app) patchPhoto(w http.ResponseWriter, r *http.Request) {
	var in PhotoPatch
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		a.noPhoto(w, r.PathValue("id"))

		return
	}

	p, err := a.photos.ByID(r.Context(), id)

	switch {
	case errors.Is(err, photobus.ErrNotFound):
		a.noPhoto(w, r.PathValue("id"))

		return
	case err != nil:
		a.fail(w, r, "reading a photo", err)

		return
	}

	places, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	slugs := map[types.ID]string{}
	ids := map[string]types.ID{}
	for _, pl := range places {
		slugs[pl.ID], ids[pl.Slug] = pl.Slug, pl.ID
	}

	// What it says now, with the changes laid over it. The check is the
	// rules' to keep or take away, so it starts unsent.
	f := photobus.FieldsOf(p)
	f.Checked = false

	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}

	if in.Kind != nil {
		f.Kind = photobus.Kind(*in.Kind)
	}
	if in.Source != nil {
		f.Source = photobus.Source(*in.Source)
	}
	set(&f.Credit, in.Credit)
	set(&f.SourceURL, in.SourceURL)
	set(&f.License, in.License)
	set(&f.TakenWhere, in.TakenWhere)
	// A month or a year sent without a day says the day is not known: it
	// would otherwise be overruled by the day the photo has, which they
	// follow.
	if in.TakenMonth != nil {
		f.TakenMonth, f.TakenAt = *in.TakenMonth, time.Time{}
	}
	if in.TakenYear != nil {
		f.TakenYear, f.TakenAt = *in.TakenYear, time.Time{}
	}
	if in.TakenOn != nil {
		day, err := photobus.Day(*in.TakenOn)
		if err != nil {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("taken_on", `taken_on is a day, such as "2026-04-03", or "" for not known.`))

			return
		}
		f.TakenAt = day
	}
	if in.InFlower != nil {
		f.InFlower = *in.InFlower
	}
	if in.Elsewhere != nil {
		f.Elsewhere = *in.Elsewhere
		if !f.Elsewhere {
			f.TakenWhere = ""
		}
	}
	if in.Checked != nil {
		f.Checked = *in.Checked
	}

	// A place chosen is where it was taken, here; one taken away leaves it
	// not said. Choosing one also makes a photo from elsewhere one from
	// here, as on the edit screen.
	if in.Place != nil {
		switch id, ok := ids[*in.Place]; {
		case *in.Place == "":
			f.PlaceID = types.ID{}
		case !ok:
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("place",
				"There is no place with that slug. GET "+Prefix+"/places lists them; send \"\" for not said."))

			return
		default:
			f.PlaceID = id
		}
	}

	got, err := a.photos.Amend(r.Context(), p.ID, f)

	invalid, isInvalid := errors.AsType[photobus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case errors.Is(err, photobus.ErrNotFound):
		a.noPhoto(w, r.PathValue("id"))

		return
	case err != nil:
		a.fail(w, r, "amending a photo", err)

		return
	}

	if got.Outcome == photobus.Updated {
		steward, _ := mid.StewardFrom(r.Context())
		a.log.InfoContext(r.Context(), "photo amended", "photo_id", p.ID.String(), "kind", got.Photo.Kind,
			"check_cleared", got.Unchecked, "user_id", steward.ID.String())
	}

	out := map[string]any{"outcome": got.Outcome, "photo": a.photoOf(got.Photo, slugs)}
	if got.Unchecked {
		out["check_cleared"] = true
	}

	web.WriteJSON(w, http.StatusOK, out)
}

func (a app) noPhoto(w http.ResponseWriter, id string) {
	web.WriteJSON(w, http.StatusNotFound, web.Problem("id",
		"No plant photo has the id "+id+". GET "+Prefix+"/species/{slug} lists a plant's photos with their ids. An inbox photo is sorted, not changed here."))
}

// SeasonJSON is a year of a plant's flowering record.
type SeasonJSON struct {
	Year        int           `json:"year"`
	FirstFlower *SightingJSON `json:"first_flower,omitempty"`
	LastFlower  *SightingJSON `json:"last_flower,omitempty"`
	FirstSeen   *SightingJSON `json:"first_seen,omitempty"`
	InFlower    int           `json:"in_flower"`
	Seen        int           `json:"seen"`
}

// SightingJSON is one date in the record, and the photo it is read from.
type SightingJSON struct {
	PhotoID    string `json:"photo_id"`
	TakenOn    string `json:"taken_on"`
	Place      string `json:"place,omitempty"`
	Elsewhere  bool   `json:"elsewhere"`
	TakenWhere string `json:"taken_where,omitempty"`
	Checked    bool   `json:"checked"`
}

func (a app) flowering(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.loadSpecies(w, r)
	if !ok {
		return
	}

	photos, err := a.photos.ForSpecies(r.Context(), sp.ID)
	if err != nil {
		a.fail(w, r, "reading a species' photos", err)

		return
	}

	places, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	slugs := map[types.ID]string{}
	for _, pl := range places {
		slugs[pl.ID] = pl.Slug
	}

	sighting := func(p photobus.Photo) *SightingJSON {
		if p.ID.Zero() {
			return nil
		}

		return &SightingJSON{
			PhotoID: p.ID.String(), TakenOn: photobus.DayOf(p.TakenAt), Place: slugs[p.PlaceID],
			Elsewhere: p.Elsewhere, TakenWhere: p.TakenWhere, Checked: p.Checked,
		}
	}

	years := []SeasonJSON{}
	for _, s := range photobus.Flowering(photos, false) {
		years = append(years, SeasonJSON{
			Year: s.Year, InFlower: s.InFlower, Seen: s.Seen,
			FirstFlower: sighting(s.FirstFlower), LastFlower: sighting(s.LastFlower), FirstSeen: sighting(s.FirstSeen),
		})
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"species": sp.Slug, "years": years})
}
