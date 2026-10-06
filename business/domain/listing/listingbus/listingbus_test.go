package listingbus_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type garden struct {
	listings *listingbus.Business
	places   *placebus.Business
	species  *speciesbus.Business
	clock    *time.Time

	inflow  placebus.Place
	sedge   speciesbus.Species
	johnson speciesbus.Species
}

// Real stores for all three, because the rules that matter most here are the
// database's references.
func setup(t *testing.T) *garden {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
		func() error { return listingdb.Init(t.Context(), db) },
		func() error { return listingdb.Init(t.Context(), db) }, // at every startup
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, listingdb.Expected); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	g := &garden{
		listings: listingbus.NewBusiness(listingdb.NewStore(db), now),
		places:   placebus.NewBusiness(placedb.NewStore(db), nil),
		species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		clock:    &clock,
	}

	if g.inflow, err = g.places.Create(t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}}); err != nil {
		t.Fatal(err)
	}

	if g.sedge, err = g.species.Create(t.Context(), speciesbus.Fields{Slug: "woodland-creek-sedge", Common: types.Text{EN: "Woodland creek sedge"}}); err != nil {
		t.Fatal(err)
	}

	if g.johnson, err = g.species.Create(t.Context(), speciesbus.Fields{Slug: "johnsongrass", Common: types.Text{EN: "Johnsongrass"}, Status: speciesbus.StatusInvasive}); err != nil {
		t.Fatal(err)
	}

	return g
}

func TestAListingIsSetChangedAndReadBothWays(t *testing.T) {
	g := setup(t)

	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.sedge.ID, listingbus.Fields{
		Action: listingbus.Protect, Planned: true, Note: types.Text{EN: " Six, around the pipe outlets. "},
	}); err != nil {
		t.Fatal(err)
	}

	first := *g.clock
	*g.clock = first.Add(time.Hour)

	// Again, for the same pair: a change, not a second row.
	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.sedge.ID, listingbus.Fields{
		Action: listingbus.Protect, Planned: true, Note: types.Text{EN: "Eight, around the pipe outlets."},
	}); err != nil {
		t.Fatal(err)
	}

	here, err := g.listings.ForPlace(t.Context(), g.inflow.ID)
	if err != nil || len(here) != 1 {
		t.Fatalf("ForPlace = %+v, %v", here, err)
	}

	l := here[0]
	switch {
	case l.Note.EN != "Eight, around the pipe outlets.":
		t.Errorf("note %q", l.Note.EN)
	case !l.CreatedAt.Equal(first) || !l.UpdatedAt.Equal(first.Add(time.Hour)):
		t.Errorf("times: made %v, changed %v", l.CreatedAt, l.UpdatedAt)
	}

	where, err := g.listings.ForSpecies(t.Context(), g.sedge.ID)
	if err != nil || len(where) != 1 || where[0].PlaceID != g.inflow.ID {
		t.Errorf("ForSpecies = %+v, %v", where, err)
	}
}

// The mistake the Phase 1 test is about, refused at the source.
func TestAPlannedPlantIsNeverOneToPull(t *testing.T) {
	g := setup(t)

	_, err := g.listings.Set(t.Context(), g.inflow.ID, g.sedge.ID, listingbus.Fields{Action: listingbus.Pull, Planned: true})
	if invalid, ok := errors.AsType[listingbus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "not one to pull") {
		t.Errorf("planned and pull: %v", err)
	}

	for _, bad := range []listingbus.Action{"", "weed"} {
		if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.sedge.ID, listingbus.Fields{Action: bad}); err == nil {
			t.Errorf("action %q was accepted", bad)
		}
	}

	if here, _ := g.listings.ForPlace(t.Context(), g.inflow.ID); len(here) != 0 {
		t.Error("a refused listing was saved")
	}
}

func TestAListingMustNameARealPlaceAndPlant(t *testing.T) {
	g := setup(t)

	_, err := g.listings.Set(t.Context(), types.NewID(), g.sedge.ID, listingbus.Fields{Action: listingbus.Protect})
	if invalid, ok := errors.AsType[listingbus.Invalid](err); !ok || invalid.Field != "species" {
		t.Errorf("a place that does not exist: %v", err)
	}

	if err := g.listings.Remove(t.Context(), g.inflow.ID, g.sedge.ID); !errors.Is(err, listingbus.ErrNotFound) {
		t.Errorf("removing what was never listed: %v", err)
	}
}

// Neither a place nor a plant can be removed out from under advice a
// volunteer has been given; once the listing goes, both can.
func TestWhatIsListedCannotBeRemoved(t *testing.T) {
	g := setup(t)

	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.johnson.ID, listingbus.Fields{Action: listingbus.Pull}); err != nil {
		t.Fatal(err)
	}

	err := g.places.Delete(t.Context(), g.inflow.ID)
	if invalid, ok := errors.AsType[placebus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "still has plants listed") {
		t.Errorf("removing a place with a listing: %v", err)
	}

	err = g.species.Delete(t.Context(), g.johnson.ID)
	if invalid, ok := errors.AsType[speciesbus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "still listed at a place") {
		t.Errorf("removing a listed plant: %v", err)
	}

	if err := g.listings.Remove(t.Context(), g.inflow.ID, g.johnson.ID); err != nil {
		t.Fatal(err)
	}

	if err := g.species.Delete(t.Context(), g.johnson.ID); err != nil {
		t.Errorf("removing the plant once unlisted: %v", err)
	}

	if err := g.places.Delete(t.Context(), g.inflow.ID); err != nil {
		t.Errorf("removing the place once unlisted: %v", err)
	}
}

// A program may list a plant to pull, and take one off a place: removed the
// first time, nothing to do the second.
func TestAProgramPullsAndUnlists(t *testing.T) {
	g := setup(t)

	got, err := g.listings.Import(t.Context(), g.inflow.ID, g.johnson.ID, listingbus.Fields{Action: listingbus.Pull, Note: types.Text{EN: "Invasive."}})
	if err != nil || got.Outcome != listingbus.Created || got.Listing.Action != listingbus.Pull {
		t.Fatalf("pull: %+v %v", got, err)
	}

	if _, err := g.listings.Import(t.Context(), g.inflow.ID, g.johnson.ID, listingbus.Fields{Action: listingbus.Pull, Planned: true}); err == nil {
		t.Error("a program pulled a plant still to plant")
	}

	for i, want := range []listingbus.Outcome{listingbus.Removed, listingbus.Unchanged} {
		if got, err := g.listings.Unlist(t.Context(), g.inflow.ID, g.johnson.ID); err != nil || got != want {
			t.Errorf("unlist %d: %q %v, want %q", i+1, got, err, want)
		}
	}

	if all, _ := g.listings.ForPlace(t.Context(), g.inflow.ID); len(all) != 0 {
		t.Errorf("still listed: %+v", all)
	}
}
