package photoapp

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// The check queue's Change sheet: for a photo that is not what it was sorted
// as, the few things a look at it can correct -- the plant, what it shows,
// whether it is in flower or in fruit, where it was taken -- on a sheet over
// the queue, saved with the check or without it, and on to the next photo.
// Before it, Change was the photo's whole screen, twelve fields long, and a
// photo filed under the wrong plant, which is the mistake the API makes,
// could not be moved at all: it had to be deleted and sent again.
//
// The sheet is a popover the browser opens from the Change button and
// closes, with no script, as a place card's "+ Add" is. A form sent with
// something to fix comes back with the sheet in the page instead, open, since
// a popover cannot be sent open; what is less often wrong -- the date, the
// source, the credit -- stays on the photo's own screen, linked from the
// sheet.

// changeForm is what the sheet says about a photo: what it says now, or
// what was sent.
type changeForm struct {
	SpeciesID         types.ID
	Kind              photobus.Kind
	InFlower, InFruit bool
	PlaceID           types.ID
}

// changeSheet is the sheet as the template shows it.
type changeSheet struct {
	// Recent is the plants as a button each: the photo's own first, then
	// those worked on lately. Species is every plant, for the search box,
	// chosen from only when the plant is not one of the buttons.
	Recent, Species []option
	Kinds           []option
	InFlower        bool
	InFruit         bool

	// Here is a photo of ours taken on the property, for which the place
	// can be said. Places is the list.
	Here   bool
	Places []option

	// Open is the sheet shown in the page, with Problems to fix.
	Open     bool
	Problems map[string]any
}

// recentPlants is how many plants the sheet offers as a button each: a row
// or two on a phone, the photo's own included.
const recentPlants = 6

// sheet is the Change sheet for p, saying what f says.
func (a app) sheet(ctx context.Context, p photobus.Photo, f changeForm, places []option, problems map[string]any) (changeSheet, error) {
	s := changeSheet{
		InFlower: f.InFlower, InFruit: f.InFruit,
		Here:     p.Source == photobus.Ours && !p.Elsewhere,
		Open:     len(problems) > 0,
		Problems: problems,
	}

	plants, err := a.species.All(ctx)
	if err != nil {
		return s, err
	}

	byID := map[types.ID]speciesbus.Species{}
	for _, sp := range plants {
		byID[sp.ID] = sp
	}

	recent, err := a.photos.RecentSpecies(ctx, recentPlants)
	if err != nil {
		return s, err
	}

	buttons := []types.ID{p.SpeciesID}
	for _, id := range recent {
		if len(buttons) < recentPlants && !slices.Contains(buttons, id) {
			buttons = append(buttons, id)
		}
	}

	onButton := false
	for _, id := range buttons {
		if sp, ok := byID[id]; ok {
			s.Recent = append(s.Recent, option{Value: id.String(), Label: sp.Common, Selected: id == f.SpeciesID})
			onButton = onButton || id == f.SpeciesID
		}
	}

	slices.SortFunc(plants, func(x, y speciesbus.Species) int {
		return cmp.Compare(strings.ToLower(x.Common.In(types.English)), strings.ToLower(y.Common.In(types.English)))
	})

	s.Species = []option{{Value: "", Label: words.AnotherPlant, Selected: onButton || f.SpeciesID.Zero()}}
	for _, sp := range plants {
		s.Species = append(s.Species, plantOption(sp, sp.ID == f.SpeciesID && !onButton))
	}

	for _, k := range photobus.Kinds {
		s.Kinds = append(s.Kinds, option{Value: string(k), Label: words.Buttons[k], Selected: k == f.Kind})
	}

	place := ""
	if !f.PlaceID.Zero() {
		place = f.PlaceID.String()
	}

	for _, o := range places {
		o.Selected = o.Value == place
		s.Places = append(s.Places, o)
	}

	return s, nil
}

// plantOption is a plant as the search box offers it: its common name with
// its scientific one, so that either finds it, and its name in the other
// language to be found by as well (the page package's find.mjs, data-also).
// Both names go in, since the list may be shown in either language.
func plantOption(sp speciesbus.Species, selected bool) option {
	o := option{Value: sp.ID.String(), Label: sp.Common, After: sp.Scientific, Selected: selected}

	if sp.Common.EN != "" && sp.Common.ES != "" && sp.Common.ES != sp.Common.EN {
		o.Also = sp.Common.EN + " " + sp.Common.ES
	}

	return o
}

// change saves the sheet: checked=yes is "Save and check it", checked=no
// "Save without checking". Either goes on to the photo after this one,
// worked out before the change, as Yes does; saved without the check, the
// photo stays in the queue and comes round again.
func (a app) change(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	ctx := r.Context()

	p, err := a.photos.ByID(ctx, id)
	if errors.Is(err, photobus.ErrNotFound) {
		http.Redirect(w, r, CheckPath, http.StatusSeeOther)

		return
	}

	if err != nil {
		a.fail(w, r, "reading a photo to change", err)

		return
	}

	problems := map[string]any{}
	form := changeForm{Kind: photobus.Kind(r.PostFormValue("kind")), InFlower: r.PostFormValue("in_flower") == "yes", InFruit: r.PostFormValue("in_fruit") == "yes", PlaceID: p.PlaceID}

	// One plant, by its button or from the list. Both, and different, is a
	// question only a page with no script can ask -- the script lets go of
	// the button as the list is chosen from -- and the answer is the
	// steward's.
	button, list := r.PostFormValue("species"), r.PostFormValue("species_other")
	switch {
	case button != "" && list != "" && button != list:
		problems["species"] = words.OnePlant
	case cmp.Or(button, list) == "":
		problems["species"] = words.WhichPlant
	default:
		if form.SpeciesID, err = types.ParseID(cmp.Or(button, list)); err != nil {
			problems["species"] = words.ChoosePlant
		}
	}

	if p.Source == photobus.Ours && !p.Elsewhere {
		form.PlaceID = types.ID{}
		if raw := r.PostFormValue("place"); raw != "" {
			if form.PlaceID, err = types.ParseID(raw); err != nil {
				problems["place"] = words.PlaceFromList
			}
		}
	}

	if !slices.Contains(photobus.Kinds, form.Kind) {
		problems["kind"] = words.ChooseKind
	}

	checked := r.PostFormValue("checked") == "yes"

	queue, err := a.photos.Unchecked(ctx)
	if err != nil {
		a.fail(w, r, "listing the photos to check", err)

		return
	}

	if len(problems) == 0 {
		f := photobus.FieldsOf(p)
		f.Kind, f.InFlower, f.InFruit, f.PlaceID, f.Checked = form.Kind, form.InFlower, form.InFruit, form.PlaceID, checked

		_, err = a.photos.Refile(ctx, id, form.SpeciesID, f)

		invalid, isInvalid := errors.AsType[photobus.Invalid](err)

		switch {
		case err == nil:
			done := "changed"
			if checked {
				done = "changed-checked"
			}

			to := CheckPath + "?done=" + done + "&last=" + id.String() + "&at=" + id.String()
			if i := slices.IndexFunc(queue, func(q photobus.Photo) bool { return q.ID == id }); i >= 0 {
				if next, ok := after(queue, i); ok {
					to = CheckPath + "?done=" + done + "&last=" + id.String() + "&at=" + next.ID.String()
				}
			}

			http.Redirect(w, r, to, http.StatusSeeOther)

			return
		case errors.Is(err, photobus.ErrNotFound):
			http.Redirect(w, r, CheckPath, http.StatusSeeOther)

			return
		case isInvalid && slices.Contains([]string{"species", "kind", "place"}, invalid.Field):
			problems[invalid.Field] = types.Text{EN: page.Sentence(invalid.Problem)}
		case isInvalid:
			problems["outcome"] = page.Put(words.OnItsScreen, "problem", types.Text{EN: page.Sentence(invalid.Problem)})
		default:
			a.fail(w, r, "changing a photo", err)

			return
		}
	}

	a.showQueue(w, r, http.StatusUnprocessableEntity, id.String(), checkView{
		Problem: words.NothingSaved,
		form:    &form,
		formOf:  id,
		probs:   problems,
	})
}
