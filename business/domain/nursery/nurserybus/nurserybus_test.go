package nurserybus_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/nursery/stores/nurserydb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

func setup(t *testing.T) *nurserybus.Business {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for range 2 { // and at the next startup
		if err := nurserydb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, nurserydb.Expected); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)

	return nurserybus.NewBusiness(nurserydb.NewStore(db), func() time.Time { return clock })
}

// Saturday morning, in Austin.
var saturday = time.Date(2026, 10, 3, 9, 30, 0, 0, types.Garden)

// One walk round one nursery is one visit, however its name is typed, and
// whichever of its tags is sorted first; even two at once.
func TestOneMorningAtOneNurseryIsOneVisit(t *testing.T) {
	b := setup(t)

	var wg sync.WaitGroup
	for i, name := range []string{"Natural Gardener", "natural  gardener", "NATURAL GARDENER"} {
		wg.Go(func() {
			if _, err := b.Add(t.Context(), name, saturday.Add(time.Duration(i)*time.Minute), types.ID{}, nurserybus.Fields{NameOnTag: "Turk's cap"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	// The same nursery on Sunday is another visit, and another nursery the
	// same morning is too.
	if _, err := b.Add(t.Context(), "Natural Gardener", saturday.AddDate(0, 0, 1), types.ID{}, nurserybus.Fields{NameOnTag: "Rock rose"}); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Add(t.Context(), "Barton Springs Nursery", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Rock rose"}); err != nil {
		t.Fatal(err)
	}

	all, err := b.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 3 {
		t.Fatalf("%d visits, want 3: %+v", len(all), all)
	}

	// Most recent first: Sunday, then the two on Saturday.
	if all[0].Visit.Nursery != "Natural Gardener" || len(all[0].Lines) != 1 {
		t.Errorf("first visit %+v", all[0])
	}

	var sat nurserybus.Stock
	for _, st := range all[1:] {
		if st.Visit.Nursery != "Barton Springs Nursery" {
			sat = st
		}
	}

	if len(sat.Lines) != 3 || !sat.Visit.Day.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, types.Garden)) {
		t.Errorf("Saturday at the Natural Gardener: %d lines, day %v", len(sat.Lines), sat.Visit.Day)
	}

	names, _ := b.Nurseries(t.Context())
	if len(names) != 2 {
		t.Errorf("nurseries %v", names)
	}

	if last, _ := b.LastNursery(t.Context(), saturday.AddDate(0, 0, 1)); last != "Natural Gardener" {
		t.Errorf("the nursery visited on Sunday: %q", last)
	}
}

// A visit late in the evening in Austin is that day's, not tomorrow's in UTC.
func TestADayIsTheGardensDay(t *testing.T) {
	late := time.Date(2026, 10, 3, 22, 0, 0, 0, types.Garden)

	if got := nurserybus.DayOf(late); !got.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, types.Garden)) {
		t.Errorf("10 pm on the 3rd is the day of %v", got.In(types.Garden))
	}
}

func TestALineNeedsATagOrAPlant(t *testing.T) {
	b := setup(t)

	for name, tc := range map[string]struct {
		nursery string
		f       nurserybus.Fields
		field   string
	}{
		"no nursery": {"", nurserybus.Fields{NameOnTag: "Turk's cap"}, "nursery"},
		"no tag":     {"Natural Gardener", nurserybus.Fields{PotSize: "1 gal"}, "name_on_tag"},
		"a count":    {"Natural Gardener", nurserybus.Fields{NameOnTag: "Turk's cap", Count: -1}, "count"},
	} {
		_, err := b.Add(t.Context(), tc.nursery, saturday, types.ID{}, tc.f)
		if invalid, ok := errors.AsType[nurserybus.Invalid](err); !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	// A plant matched and no tag read is enough.
	if _, err := b.Add(t.Context(), "Natural Gardener", saturday, types.ID{}, nurserybus.Fields{SpeciesID: types.NewID()}); err != nil {
		t.Errorf("a matched plant with no tag: %v", err)
	}
}

func TestALineIsCorrected(t *testing.T) {
	b := setup(t)

	l, err := b.Add(t.Context(), "Natural Gardener", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Turks cap", PotSize: "1 gal"})
	if err != nil {
		t.Fatal(err)
	}

	sp := types.NewID()

	if _, err := b.Update(t.Context(), l.ID, nurserybus.Fields{SpeciesID: sp, NameOnTag: "Turk's cap", PotSize: "1 gal", PriceCents: 1299, Count: 12}); err != nil {
		t.Fatal(err)
	}

	got, _ := b.Line(t.Context(), l.ID)
	if got.SpeciesID != sp || got.NameOnTag != "Turk's cap" || got.PriceCents != 1299 || got.Count != 12 || got.VisitID != l.VisitID {
		t.Errorf("corrected to %+v", got)
	}

	if _, err := b.Update(t.Context(), types.NewID(), nurserybus.Fields{NameOnTag: "x"}); !errors.Is(err, nurserybus.ErrNotFound) {
		t.Errorf("a line that is not there: %v", err)
	}
}

func TestAPriceIsReadAsWritten(t *testing.T) {
	for in, want := range map[string]int{"": 0, "12": 1200, "$12.99": 1299, " $ 7.5 ": 750, "0.99": 99} {
		if got, err := nurserybus.ParsePrice(in); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}

	for _, in := range []string{"twelve", "12.999", "-3", "1.2.3"} {
		if _, err := nurserybus.ParsePrice(in); err == nil {
			t.Errorf("%q was read as a price", in)
		}
	}

	if got := nurserybus.PriceWords(1299); got != "$12.99" {
		t.Errorf("1299 cents is %q", got)
	}
}
