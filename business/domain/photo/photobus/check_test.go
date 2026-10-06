package photobus_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// The queue: only what is unchecked, a plant's photos together and in the
// order of kinds, and the plant sent most recently first.
func TestTheCheckQueuesOrder(t *testing.T) {
	g := setup(t)

	turksCap, err := g.species.Create(t.Context(), speciesbus.Fields{Slug: "turks-cap", Common: types.Text{EN: "Turk's cap"}})
	if err != nil {
		t.Fatal(err)
	}

	size := 300
	add := func(sp speciesbus.Species, k photobus.Kind, checked bool) photobus.Photo {
		t.Helper()

		*g.clock = g.clock.Add(time.Minute)
		size += 10

		f := ours(k)
		f.Checked = checked
		p, err := g.photos.Add(t.Context(), sp.ID, f, photo(t, size, 200))
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	penstemonLeaf := add(g.penstemon, photobus.Leaf, false)
	capFlower := add(turksCap, photobus.Flower, false)
	capLeaf := add(turksCap, photobus.Leaf, false)
	add(g.penstemon, photobus.Young, true)
	penstemonFlower := add(g.penstemon, photobus.Flower, false)

	got, err := g.photos.Unchecked(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	ids := func(ps ...photobus.Photo) []types.ID {
		var out []types.ID
		for _, p := range ps {
			out = append(out, p.ID)
		}

		return out
	}

	// The penstemon's flower arrived last, so the penstemon comes first, its
	// leaf before its flower as on a card; the checked seedling not at all.
	want := ids(penstemonLeaf, penstemonFlower, capLeaf, capFlower)
	if !slices.Equal(ids(got...), want) {
		t.Errorf("queue %v, want %v", ids(got...), want)
	}
}

// Yes checks a photo and takes it out of the queue, Yes again changes
// nothing, and Undo puts it back.
func TestCheckingAndUndoing(t *testing.T) {
	g := setup(t)

	f := ours(photobus.Leaf)
	f.Checked = false
	p, err := g.photos.Add(t.Context(), g.penstemon.ID, f, photo(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}

	*g.clock = g.clock.Add(time.Hour)

	checked, err := g.photos.SetChecked(t.Context(), p.ID, true)
	if err != nil {
		t.Fatal(err)
	}

	if !checked.Checked || !checked.UpdatedAt.Equal(*g.clock) {
		t.Errorf("after Yes: checked %v, updated %v, want true at %v", checked.Checked, checked.UpdatedAt, *g.clock)
	}

	if checked.Kind != photobus.Leaf || checked.Source != photobus.Ours {
		t.Errorf("checking changed what the photo says: %+v", checked)
	}

	if queue, _ := g.photos.Unchecked(t.Context()); len(queue) != 0 {
		t.Errorf("a checked photo is still in the queue: %d left", len(queue))
	}

	*g.clock = g.clock.Add(time.Hour)

	again, err := g.photos.SetChecked(t.Context(), p.ID, true)
	if err != nil {
		t.Fatal(err)
	}

	if !again.UpdatedAt.Equal(checked.UpdatedAt) {
		t.Error("a second Yes saved the photo again")
	}

	undone, err := g.photos.SetChecked(t.Context(), p.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	if undone.Checked {
		t.Error("Undo left the photo checked")
	}

	if queue, _ := g.photos.Unchecked(t.Context()); len(queue) != 1 {
		t.Errorf("after Undo the queue holds %d, want the photo back", len(queue))
	}

	if _, err := g.photos.SetChecked(t.Context(), types.NewID(), true); !errors.Is(err, photobus.ErrNotFound) {
		t.Errorf("checking a photo that is not there: %v, want ErrNotFound", err)
	}
}

// The Change sheet's refile: a leaf filed under the wrong plant moves to the
// right one, saying a flower now and checked, in one write; and what cannot
// be moved is refused with a sentence about the plant.
func TestARefiledPhotoMovesToItsPlant(t *testing.T) {
	g := setup(t)

	turksCap, err := g.species.Create(t.Context(), speciesbus.Fields{Slug: "turks-cap", Common: types.Text{EN: "Turk's cap"}})
	if err != nil {
		t.Fatal(err)
	}

	f := ours(photobus.Leaf)
	f.Checked = false
	p, err := g.photos.Add(t.Context(), g.penstemon.ID, f, photo(t, 400, 300))
	if err != nil {
		t.Fatal(err)
	}

	*g.clock = g.clock.Add(time.Hour)

	f = photobus.FieldsOf(p)
	f.Kind, f.Checked = photobus.Flower, true
	moved, err := g.photos.Refile(t.Context(), p.ID, turksCap.ID, f)
	if err != nil {
		t.Fatal(err)
	}

	if moved.SpeciesID != turksCap.ID || moved.Kind != photobus.Flower || !moved.InFlower || !moved.Checked || !moved.UpdatedAt.Equal(*g.clock) {
		t.Errorf("refiled: %+v", moved)
	}

	if left, _ := g.photos.ForSpecies(t.Context(), g.penstemon.ID); len(left) != 0 {
		t.Errorf("the penstemon still has %d photos", len(left))
	}

	if got, _ := g.photos.ForSpecies(t.Context(), turksCap.ID); len(got) != 1 || got[0].ID != p.ID {
		t.Errorf("the Turk's cap has %v, want the photo", got)
	}

	for name, to := range map[string]types.ID{"no plant": {}, "a plant not there": types.NewID()} {
		if _, err := g.photos.Refile(t.Context(), p.ID, to, f); err == nil {
			t.Errorf("%s: refiled", name)
		} else if invalid, ok := errors.AsType[photobus.Invalid](err); !ok || invalid.Field != "species" {
			t.Errorf("%s: %v, want a problem with the species", name, err)
		}
	}

	// The penstemon has the same picture sent again: moving this one back
	// would make it twice.
	if _, err := g.photos.Add(t.Context(), g.penstemon.ID, ours(photobus.Leaf), photo(t, 400, 300)); err != nil {
		t.Fatal(err)
	}

	_, err = g.photos.Refile(t.Context(), p.ID, g.penstemon.ID, f)
	if invalid, ok := errors.AsType[photobus.Invalid](err); !ok || invalid.Field != "species" {
		t.Errorf("moving a picture to a plant that has it: %v, want a problem with the species", err)
	}
}

// The plants offered on the Change sheet: those whose photos changed last,
// each once, as many as asked for.
func TestThePlantsWorkedOnLately(t *testing.T) {
	g := setup(t)

	turksCap, err := g.species.Create(t.Context(), speciesbus.Fields{Slug: "turks-cap", Common: types.Text{EN: "Turk's cap"}})
	if err != nil {
		t.Fatal(err)
	}

	add := func(sp speciesbus.Species, size int) photobus.Photo {
		t.Helper()

		*g.clock = g.clock.Add(time.Minute)
		p, err := g.photos.Add(t.Context(), sp.ID, ours(photobus.Leaf), photo(t, size, 200))
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	add(g.penstemon, 300)
	capLeaf := add(turksCap, 310)
	add(g.penstemon, 320)

	got, err := g.photos.RecentSpecies(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}

	if want := []types.ID{g.penstemon.ID, turksCap.ID}; !slices.Equal(got, want) {
		t.Errorf("recent %v, want %v", got, want)
	}

	// A change to the Turk's cap's photo -- its check taken away -- makes
	// the Turk's cap the plant worked on last.
	*g.clock = g.clock.Add(time.Minute)
	if _, err := g.photos.SetChecked(t.Context(), capLeaf.ID, false); err != nil {
		t.Fatal(err)
	}

	if got, _ := g.photos.RecentSpecies(t.Context(), 1); len(got) != 1 || got[0] != turksCap.ID {
		t.Errorf("one asked for: %v", got)
	}
}
