package speciesdb_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

var now = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

func open(t *testing.T) *speciesdb.Store {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	for range 2 { // Init runs at every startup
		if err := speciesdb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, speciesdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	return speciesdb.NewStore(db)
}

func frostweed() speciesbus.Species {
	return speciesbus.Species{
		ID: types.NewID(), Slug: "frostweed",
		Common:      types.Text{EN: "Frostweed", ES: "Hierba de escarcha"},
		Scientific:  "Verbesina virginica",
		Status:      speciesbus.StatusNative,
		Confirmed:   true,
		FlowerColor: types.Text{EN: "White"},
		Swatches:    []string{"#ffffff"},
		Bloom:       types.MonthsOf(time.September, time.October, time.November),
		Height:      speciesbus.Size{Min: 48, Max: 84},
		Width:       speciesbus.Size{Min: 24, Max: 36},
		Light:       speciesbus.PartShade | speciesbus.Shade,
		Water:       speciesbus.Moist,
		Note:        types.Text{EN: "Back of the middle band. Monarchs."},
		Sources: []speciesbus.Source{
			{Label: "Lady Bird Johnson Wildflower Center", URL: "https://www.wildflower.org/plants/result.php?id_plant=VEVI3"},
			{Label: "Nursery tag"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

// Every field, every source in its order, and the times to the millisecond.
func TestASpeciesRoundTripsWithItsSources(t *testing.T) {
	s := open(t)
	want := frostweed()

	if err := s.Create(t.Context(), want); err != nil {
		t.Fatal(err)
	}

	got, err := s.BySlug(t.Context(), "frostweed")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back\n%+v\nwant\n%+v", got, want)
	}

	all, err := s.All(t.Context())
	if err != nil || len(all) != 1 || !reflect.DeepEqual(all[0], want) {
		t.Errorf("All = %+v, %v", all, err)
	}
}

func TestAnUpdateReplacesTheSources(t *testing.T) {
	s := open(t)
	sp := frostweed()

	if err := s.Create(t.Context(), sp); err != nil {
		t.Fatal(err)
	}

	sp.Sources = []speciesbus.Source{{Label: "Native Plant Society of Texas"}}
	sp.Common.EN = "Frostweed (white crownbeard)"
	sp.UpdatedAt = now.Add(time.Hour)

	if err := s.Update(t.Context(), sp); err != nil {
		t.Fatal(err)
	}

	got, _ := s.ByID(t.Context(), sp.ID)
	if !reflect.DeepEqual(got, sp) {
		t.Errorf("after update\n%+v\nwant\n%+v", got, sp)
	}
}

func TestAnAddressIsTakenOnceAndRemovalTakesTheSources(t *testing.T) {
	s := open(t)
	sp := frostweed()

	if err := s.Create(t.Context(), sp); err != nil {
		t.Fatal(err)
	}

	again := frostweed()
	if err := s.Create(t.Context(), again); !errors.Is(err, speciesbus.ErrSlugTaken) {
		t.Errorf("a second frostweed: %v, want ErrSlugTaken", err)
	}

	// The refused insert must not have left its sources behind under the
	// new id; the transaction is all or nothing.
	if _, err := s.ByID(t.Context(), again.ID); !errors.Is(err, speciesbus.ErrNotFound) {
		t.Errorf("the refused species: %v", err)
	}

	if err := s.Delete(t.Context(), sp.ID); err != nil {
		t.Fatal(err)
	}

	if all, _ := s.All(t.Context()); len(all) != 0 {
		t.Errorf("%d species after removing the only one", len(all))
	}

	if err := s.Delete(t.Context(), sp.ID); !errors.Is(err, speciesbus.ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}

	// Making it again proves the sources went with it: the primary key on
	// (species_id, position) would refuse leftovers otherwise.
	sp.ID = types.NewID()
	if err := s.Create(t.Context(), sp); err != nil {
		t.Errorf("making it again: %v", err)
	}
}
