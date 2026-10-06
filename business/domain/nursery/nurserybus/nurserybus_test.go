package nurserybus_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
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

	// Most recent first: Sunday, then the two on Saturday. Under whichever
	// spelling reached the register first, since the three raced to it.
	if !strings.EqualFold(all[0].Visit.Nursery, "Natural Gardener") || len(all[0].Lines) != 1 {
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

	if last, _ := b.LastNursery(t.Context(), saturday.AddDate(0, 0, 1)); !strings.EqualFold(last, "Natural Gardener") {
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

// ------------------------------------------------------------------ the register

func TestANurseryIsInTheRegisterOnce(t *testing.T) {
	b := setup(t)

	n, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{
		Name: "  Natural   Gardener ", Address: "100 Example Rd, Austin, TX 78735",
		Website: "naturalgardener.com", Phone: "512-555-0142", Note: "Natives along the back fence.",
	})
	if err != nil {
		t.Fatal(err)
	}

	if n.Name != "Natural Gardener" || n.Website != "https://naturalgardener.com" {
		t.Errorf("kept as %+v", n)
	}

	if _, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{Name: "natural gardener"}); !isInvalid(err, "name") {
		t.Errorf("the same nursery again: %v", err)
	}

	// A tag photo sorted under any spelling of it is a visit to it, under
	// the register's name; and adds nothing to the register.
	if _, err := b.Add(t.Context(), "NATURAL GARDENER", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Turk's cap"}); err != nil {
		t.Fatal(err)
	}

	all, _ := b.All(t.Context())
	if len(all) != 1 || all[0].Visit.NurseryID != n.ID || all[0].Visit.Nursery != "Natural Gardener" {
		t.Errorf("the visit: %+v", all)
	}

	if reg, _ := b.Register(t.Context()); len(reg) != 1 {
		t.Errorf("the register: %+v", reg)
	}
}

// Typing a new nursery's name when sorting a tag photo is how most will
// arrive: it joins the register there and then, with nothing else said yet.
func TestANurseryNamedWhenSortingJoinsTheRegister(t *testing.T) {
	b := setup(t)

	if _, err := b.Add(t.Context(), "Barton Springs Nursery", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Rock rose"}); err != nil {
		t.Fatal(err)
	}

	reg, err := b.Register(t.Context())
	if err != nil || len(reg) != 1 || reg[0].Name != "Barton Springs Nursery" || reg[0].Address != "" {
		t.Fatalf("the register: %+v, %v", reg, err)
	}
}

func TestRenamingANurseryRenamesItsVisits(t *testing.T) {
	b := setup(t)

	if _, err := b.Add(t.Context(), "Natual Gardener", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Turk's cap"}); err != nil {
		t.Fatal(err)
	}

	reg, _ := b.Register(t.Context())

	if _, err := b.UpdateNursery(t.Context(), reg[0].ID, nurserybus.NurseryFields{Name: "Natural Gardener"}); err != nil {
		t.Fatal(err)
	}

	if all, _ := b.All(t.Context()); all[0].Visit.Nursery != "Natural Gardener" {
		t.Errorf("the visit is still under %q", all[0].Visit.Nursery)
	}

	// The same nursery again that morning, under its right name, is the
	// same visit.
	if _, err := b.Add(t.Context(), "Natural Gardener", saturday.Add(time.Hour), types.ID{}, nurserybus.Fields{NameOnTag: "Rock rose"}); err != nil {
		t.Fatal(err)
	}

	if all, _ := b.All(t.Context()); len(all) != 1 || len(all[0].Lines) != 2 {
		t.Errorf("visits after the rename: %+v", all)
	}

	// The old spelling is free again, and another nursery cannot take the
	// new one.
	other, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{Name: "Natual Gardener"})
	if err != nil {
		t.Fatalf("the old spelling: %v", err)
	}

	// And a visit to it that same Saturday is its own, not lost against
	// the renamed nursery's visit under the spelling they once shared.
	if _, err := b.Add(t.Context(), "Natual Gardener", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Frostweed"}); err != nil {
		t.Fatalf("a visit to the nursery with the old spelling: %v", err)
	}

	if all, _ := b.All(t.Context()); len(all) != 2 {
		t.Errorf("%d visits, want one to each", len(all))
	}

	if _, err := b.UpdateNursery(t.Context(), other.ID, nurserybus.NurseryFields{Name: "natural gardener"}); !isInvalid(err, "name") {
		t.Errorf("renamed onto another nursery: %v", err)
	}

	if _, err := b.UpdateNursery(t.Context(), types.NewID(), nurserybus.NurseryFields{Name: "Anything"}); !errors.Is(err, nurserybus.ErrNotFound) {
		t.Errorf("a nursery that is not there: %v", err)
	}
}

func TestANurseryWithVisitsStays(t *testing.T) {
	b := setup(t)

	if _, err := b.Add(t.Context(), "Natural Gardener", saturday, types.ID{}, nurserybus.Fields{NameOnTag: "Turk's cap"}); err != nil {
		t.Fatal(err)
	}

	unvisited, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{Name: "Added by mistake"})
	if err != nil {
		t.Fatal(err)
	}

	reg, _ := b.Register(t.Context())
	visited := reg[slices.IndexFunc(reg, func(n nurserybus.Nursery) bool { return n.Name == "Natural Gardener" })]

	if err := b.DeleteNursery(t.Context(), visited.ID); !errors.Is(err, nurserybus.ErrInUse) {
		t.Errorf("removing a visited nursery: %v", err)
	}

	if err := b.DeleteNursery(t.Context(), unvisited.ID); err != nil {
		t.Errorf("removing one never visited: %v", err)
	}

	if err := b.DeleteNursery(t.Context(), unvisited.ID); !errors.Is(err, nurserybus.ErrNotFound) {
		t.Errorf("removing it twice: %v", err)
	}

	if reg, _ := b.Register(t.Context()); len(reg) != 1 {
		t.Errorf("the register: %+v", reg)
	}
}

func TestANurseryIsCheckedAsGiven(t *testing.T) {
	b := setup(t)

	for name, tc := range map[string]struct {
		f     nurserybus.NurseryFields
		field string
	}{
		"no name":           {nurserybus.NurseryFields{Name: "   "}, "name"},
		"a long name":       {nurserybus.NurseryFields{Name: strings.Repeat("x", 101)}, "name"},
		"a script":          {nurserybus.NurseryFields{Name: "N", Website: "javascript:alert(1)"}, "website"},
		"not a web address": {nurserybus.NurseryFields{Name: "N", Website: "ftp://naturalgardener.com"}, "website"},
		"no host":           {nurserybus.NurseryFields{Name: "N", Website: "https://"}, "website"},
		"a long address":    {nurserybus.NurseryFields{Name: "N", Address: strings.Repeat("x", 201)}, "address"},
		"a long phone":      {nurserybus.NurseryFields{Name: "N", Phone: strings.Repeat("1", 41)}, "phone"},
		"a long note":       {nurserybus.NurseryFields{Name: "N", Note: strings.Repeat("x", 301)}, "note"},
	} {
		if _, err := b.CreateNursery(t.Context(), tc.f); !isInvalid(err, tc.field) {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	n, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{Name: "N", Website: "http://www.example.org/natives?x=1"})
	if err != nil || n.Website != "http://www.example.org/natives?x=1" {
		t.Errorf("a web address as given: %+v, %v", n, err)
	}
}

// The list to choose from when sorting: the nursery most recently walked
// round first, and those never visited after, by name.
func TestTheNurseriesToChooseFromAreTheLatestFirst(t *testing.T) {
	b := setup(t)

	for _, name := range []string{"Zilker Garden Shop", "Austin Natives"} {
		if _, err := b.CreateNursery(t.Context(), nurserybus.NurseryFields{Name: name}); err != nil {
			t.Fatal(err)
		}
	}

	for i, name := range []string{"Natural Gardener", "Barton Springs Nursery", "Natural Gardener"} {
		if _, err := b.Add(t.Context(), name, saturday.AddDate(0, 0, -7*(2-i)), types.ID{}, nurserybus.Fields{NameOnTag: "Rock rose"}); err != nil {
			t.Fatal(err)
		}
	}

	names, err := b.Nurseries(t.Context())
	if want := []string{"Natural Gardener", "Barton Springs Nursery", "Austin Natives", "Zilker Garden Shop"}; err != nil || !slices.Equal(names, want) {
		t.Errorf("nurseries %v, want %v", names, want)
	}
}

func isInvalid(err error, field string) bool {
	invalid, ok := errors.AsType[nurserybus.Invalid](err)

	return ok && invalid.Field == field
}
