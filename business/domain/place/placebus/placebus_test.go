package placebus_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The rules run against the real store rather than a fake, so what is tested
// is what is saved. The places are the ones the plan names; nothing here is
// a real volunteer or a real photo.
func business(t *testing.T) *placebus.Business {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatalf("sqldb.Init: %v", err)
	}
	if err := placedb.Init(t.Context(), db); err != nil {
		t.Fatalf("placedb.Init: %v", err)
	}

	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	return placebus.NewBusiness(placedb.NewStore(db), func() time.Time {
		clock = clock.Add(time.Minute)
		return clock
	})
}

func named(slug, name string) placebus.Fields {
	return placebus.Fields{Slug: slug, Name: types.Text{EN: name}}
}

func mustCreate(t *testing.T, b *placebus.Business, f placebus.Fields) placebus.Place {
	t.Helper()

	p, err := b.Create(t.Context(), f)
	if err != nil {
		t.Fatalf("Create(%s): %v", f.Slug, err)
	}

	return p
}

// invalid asserts err is an Invalid naming field, which is what the app layer
// will switch on to put the sentence beside the right input.
func invalid(t *testing.T, err error, field string) {
	t.Helper()

	e, ok := errors.AsType[placebus.Invalid](err)
	if !ok {
		t.Fatalf("got %v, want an Invalid for %s", err, field)
	}

	if e.Field != field {
		t.Errorf("Invalid names %q, want %q (%s)", e.Field, field, e.Problem)
	}
}

func TestAPlaceIsCreatedTidied(t *testing.T) {
	b := business(t)

	p := mustCreate(t, b, placebus.Fields{
		Slug: " rain-garden ",
		Name: types.Text{EN: "  Rain garden ", ES: " Jardín de lluvia"},
	})

	if p.Slug != "rain-garden" || p.Name.EN != "Rain garden" || p.Name.ES != "Jardín de lluvia" {
		t.Errorf("not tidied: %+v", p)
	}

	if p.ID.Zero() || p.CreatedAt.IsZero() || !p.CreatedAt.Equal(p.UpdatedAt) {
		t.Errorf("identity or times not set: %+v", p)
	}
}

func TestAnAddressIsOneAPersonCanType(t *testing.T) {
	b := business(t)

	for _, slug := range []string{"", "Rain-Garden", "rain garden", "rain_garden", "-rain", "rain-", "rain--garden", "jardín", strings.Repeat("a", 49)} {
		_, err := b.Create(t.Context(), named(slug, "Rain garden"))
		invalid(t, err, "slug")
	}

	mustCreate(t, b, named("rain-garden-2", "Rain garden"))
}

func TestTwoPlacesCannotShareAnAddress(t *testing.T) {
	b := business(t)

	mustCreate(t, b, named("rain-garden", "Rain garden"))

	_, err := b.Create(t.Context(), named("rain-garden", "Another rain garden"))
	invalid(t, err, "slug")
}

func TestAPlaceNeedsAnEnglishName(t *testing.T) {
	b := business(t)

	_, err := b.Create(t.Context(), placebus.Fields{Slug: "x", Name: types.Text{ES: "Jardín"}})
	invalid(t, err, "name")

	_, err = b.Create(t.Context(), named("x", strings.Repeat("n", 61)))
	invalid(t, err, "name")
}

// The anchors are burned into plywood. A link to one that is not there is a
// dead link on a phone in the woods.
func TestOnlyTheStationsOnTheBoardsCanBeLinked(t *testing.T) {
	b := business(t)

	f := named("st-joseph", "St. Joseph")
	f.TrailAnchor = "joe"
	_, err := b.Create(t.Context(), f)
	invalid(t, err, "trail_anchor")

	f.TrailAnchor = "joseph"
	mustCreate(t, b, f)
}

func TestPlacesGoOneLevelDeep(t *testing.T) {
	b := business(t)

	garden := mustCreate(t, b, named("rain-garden", "Rain garden"))

	band := named("rain-garden-inflow", "Inflow")
	band.ParentID = garden.ID
	inflow := mustCreate(t, b, band)

	// A band inside a band.
	deeper := named("inflow-left", "Left of the inflow")
	deeper.ParentID = inflow.ID
	_, err := b.Create(t.Context(), deeper)
	invalid(t, err, "parent")

	// The rain garden, which has bands, moved inside somewhere else.
	firePit := mustCreate(t, b, named("fire-pit", "Fire pit"))
	moved := named("", "Rain garden")
	moved.ParentID = firePit.ID
	_, err = b.Update(t.Context(), garden.ID, moved)
	invalid(t, err, "parent")

	// A place inside itself.
	self := named("", "Fire pit")
	self.ParentID = firePit.ID
	_, err = b.Update(t.Context(), firePit.ID, self)
	invalid(t, err, "parent")

	// A parent that is not there.
	orphan := named("orphan", "Orphan")
	orphan.ParentID = types.NewID()
	_, err = b.Create(t.Context(), orphan)
	invalid(t, err, "parent")

	kids, err := b.Children(t.Context(), garden.ID)
	if err != nil || len(kids) != 1 || kids[0].ID != inflow.ID {
		t.Errorf("Children = %v, %v; want the inflow band", kids, err)
	}
}

// The address is printed on things. It is chosen once.
func TestUpdatingAPlaceNeverChangesItsAddress(t *testing.T) {
	b := business(t)

	p := mustCreate(t, b, named("rain-garden", "Rain garden"))

	f := named("somewhere-else", "The rain garden")
	f.Purpose = types.Text{EN: "The backdrop of the gathering space"}

	got, err := b.Update(t.Context(), p.ID, f)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if got.Slug != "rain-garden" {
		t.Errorf("slug became %q", got.Slug)
	}

	again, err := b.BySlug(t.Context(), "rain-garden")
	if err != nil {
		t.Fatalf("BySlug after update: %v", err)
	}

	if again.Name.EN != "The rain garden" || again.Purpose.EN == "" {
		t.Errorf("the update did not stick: %+v", again)
	}

	if !again.UpdatedAt.After(again.CreatedAt) {
		t.Errorf("updated_at %s is not after created_at %s", again.UpdatedAt, again.CreatedAt)
	}
}

func TestAPlaceWithBandsIsNotDeletedWithThem(t *testing.T) {
	b := business(t)

	garden := mustCreate(t, b, named("rain-garden", "Rain garden"))
	band := named("rain-garden-middle", "Middle")
	band.ParentID = garden.ID
	middle := mustCreate(t, b, band)

	invalid(t, b.Delete(t.Context(), garden.ID), "place")

	if err := b.Delete(t.Context(), middle.ID); err != nil {
		t.Fatalf("deleting the band: %v", err)
	}

	if err := b.Delete(t.Context(), garden.ID); err != nil {
		t.Errorf("deleting the garden once it is empty: %v", err)
	}

	if _, err := b.BySlug(t.Context(), "rain-garden"); !errors.Is(err, placebus.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

// The list is the map's accessible twin, so its order is chosen: by Sort, and
// by name where the sort ties.
func TestTheListIsInTheOrderChosen(t *testing.T) {
	b := business(t)

	for _, f := range []placebus.Fields{
		{Slug: "switchbacks", Name: types.Text{EN: "Switchbacks"}, Sort: 2},
		{Slug: "rain-garden", Name: types.Text{EN: "Rain garden"}, Sort: 1},
		{Slug: "fire-pit", Name: types.Text{EN: "Fire pit"}, Sort: 1},
	} {
		mustCreate(t, b, f)
	}

	all, err := b.All(t.Context())
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	var got []string
	for _, p := range all {
		got = append(got, p.Slug)
	}

	if strings.Join(got, " ") != "fire-pit rain-garden switchbacks" {
		t.Errorf("order = %v", got)
	}
}

// A place goes on the map, moves, and comes off; a band never goes on it, and
// a place that becomes a band leaves it.
func TestAPlaceIsPutOnTheMapAndTakenOff(t *testing.T) {
	b := business(t)

	garden := mustCreate(t, b, named("rain-garden", "Rain garden"))
	if garden.Spot != nil {
		t.Fatalf("a new place is at %v, want off the map", *garden.Spot)
	}

	p, err := b.SetSpot(t.Context(), garden.ID, &placebus.Spot{X: 470, Y: 446})
	if err != nil {
		t.Fatalf("SetSpot: %v", err)
	}
	if got, err := b.ByID(t.Context(), p.ID); err != nil || got.Spot == nil || *got.Spot != (placebus.Spot{X: 470, Y: 446}) {
		t.Fatalf("read back %v, %v; want 470,446", got.Spot, err)
	}

	// Saving the names and notes leaves the spot where it was.
	if p, err = b.Update(t.Context(), garden.ID, named("", "Rain garden, by the fire pit")); err != nil || p.Spot == nil {
		t.Fatalf("Update: %v, spot %v; want the spot kept", err, p.Spot)
	}

	for _, off := range []placebus.Spot{{X: -1, Y: 10}, {X: 10, Y: placebus.MapHeight + 1}, {X: placebus.MapWidth + 1, Y: 0}} {
		_, err := b.SetSpot(t.Context(), garden.ID, &off)
		invalid(t, err, "spot")
	}

	band := named("rain-garden-inflow", "Inflow")
	band.ParentID = garden.ID
	inflow := mustCreate(t, b, band)
	_, err = b.SetSpot(t.Context(), inflow.ID, &placebus.Spot{X: 10, Y: 10})
	invalid(t, err, "spot")

	firePit := mustCreate(t, b, named("fire-pit", "Fire pit"))
	if _, err := b.SetSpot(t.Context(), firePit.ID, &placebus.Spot{X: 600, Y: 420}); err != nil {
		t.Fatal(err)
	}
	moved := named("", "Fire pit")
	moved.ParentID = garden.ID
	if p, err := b.Update(t.Context(), firePit.ID, moved); err != nil || p.Spot != nil {
		t.Errorf("a place moved inside another: %v, spot %v; want it off the map", err, p.Spot)
	}

	if p, err = b.SetSpot(t.Context(), garden.ID, nil); err != nil || p.Spot != nil {
		t.Errorf("taking it off the map: %v, spot %v", err, p.Spot)
	}
	if got, _ := b.ByID(t.Context(), garden.ID); got.Spot != nil {
		t.Errorf("read back at %v, want off the map", *got.Spot)
	}
}

// A program adds a place, sends it again for nothing, changes it, and its
// spot on the map survives the change.
func TestAProgramAddsAndChangesAPlace(t *testing.T) {
	b := business(t)

	f := placebus.Fields{Slug: "skinny-bed", Name: types.Text{EN: " Skinny bed "}, Conditions: types.Text{EN: "4 to 5 hours of morning sun."}}

	got, err := b.Import(t.Context(), f)
	if err != nil || got.Outcome != placebus.Created || got.Place.Name.EN != "Skinny bed" {
		t.Fatalf("created: %+v %v", got, err)
	}

	if _, err := b.SetSpot(t.Context(), got.Place.ID, &placebus.Spot{X: 100, Y: 100}); err != nil {
		t.Fatal(err)
	}

	if again, err := b.Import(t.Context(), f); err != nil || again.Outcome != placebus.Unchanged {
		t.Errorf("sent again: %+v %v", again, err)
	}

	f.Purpose = types.Text{EN: "Along the house's north wall."}
	changed, err := b.Import(t.Context(), f)
	if err != nil || changed.Outcome != placebus.Updated || changed.Place.Purpose.EN == "" || changed.Place.Spot == nil {
		t.Errorf("changed: %+v %v", changed, err)
	}

	if _, err := b.Import(t.Context(), placebus.Fields{Slug: "Not A Slug", Name: types.Text{EN: "x"}}); err == nil {
		t.Error("a slug nobody can type was taken")
	}
}
