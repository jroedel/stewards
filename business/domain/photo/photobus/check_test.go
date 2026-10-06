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
