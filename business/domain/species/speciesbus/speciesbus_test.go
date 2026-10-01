package speciesbus_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

func setup(t *testing.T) *speciesbus.Business {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := speciesdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	return speciesbus.NewBusiness(speciesdb.NewStore(db), nil)
}

func winecup() speciesbus.Fields {
	return speciesbus.Fields{
		Slug:       "winecup",
		Common:     types.Text{EN: " Winecup "},
		Scientific: " Callirhoe   involucrata ",
		Status:     speciesbus.StatusNative,
		Swatches:   []string{" 8E1B4B ", ""},
		Bloom:      types.MonthsOf(time.March, time.April, time.May, time.June),
		Height:     speciesbus.Size{Min: 6, Max: 12},
		Width:      speciesbus.Size{Max: 36},
		Light:      speciesbus.FullSun,
		Water:      speciesbus.Dry,
		Sources:    []speciesbus.Source{{Label: "  "}, {Label: "Lady Bird Johnson Wildflower Center", URL: " https://www.wildflower.org/ "}},
	}
}

// What a steward types is tidied before it is kept: spaces, case, the empty
// spare rows of a form, a size given as one number.
func TestWhatIsTypedIsTidied(t *testing.T) {
	b := setup(t)

	sp, err := b.Create(t.Context(), winecup())
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case sp.Common.EN != "Winecup", sp.Scientific != "Callirhoe involucrata":
		t.Errorf("names: %q, %q", sp.Common.EN, sp.Scientific)
	case len(sp.Swatches) != 1 || sp.Swatches[0] != "#8e1b4b":
		t.Errorf("swatches: %v", sp.Swatches)
	case sp.Width != speciesbus.Size{Min: 36, Max: 36}:
		t.Errorf("width: %+v", sp.Width)
	case len(sp.Sources) != 1 || sp.Sources[0].URL != "https://www.wildflower.org/":
		t.Errorf("sources: %+v", sp.Sources)
	}
}

// The rule the plan puts first: an ID is confirmed by its sources.
func TestAnIDIsConfirmedOnlyWithItsSources(t *testing.T) {
	b := setup(t)

	f := winecup()
	f.Confirmed = true
	f.Sources = nil

	_, err := b.Create(t.Context(), f)
	if invalid, ok := errors.AsType[speciesbus.Invalid](err); !ok || invalid.Field != "confirmed" {
		t.Fatalf("confirmed with no source: %v", err)
	}

	f = winecup()
	f.Confirmed = true
	f.Scientific = ""

	if _, err := b.Create(t.Context(), f); err == nil {
		t.Error("confirmed with no scientific name was accepted")
	}

	f = winecup()
	f.Confirmed = true

	if sp, err := b.Create(t.Context(), f); err != nil || !sp.Confirmed {
		t.Errorf("confirmed with a source: %v", err)
	}
}

func TestABadSpeciesIsRefusedWithTheFieldToFix(t *testing.T) {
	b := setup(t)

	for field, change := range map[string]func(*speciesbus.Fields){
		"slug":     func(f *speciesbus.Fields) { f.Slug = "Wine Cup" },
		"common":   func(f *speciesbus.Fields) { f.Common.EN = "" },
		"status":   func(f *speciesbus.Fields) { f.Status = "weed" },
		"swatches": func(f *speciesbus.Fields) { f.Swatches = []string{"purple"} },
		"height":   func(f *speciesbus.Fields) { f.Height = speciesbus.Size{Min: 24, Max: 12} },
		"width":    func(f *speciesbus.Fields) { f.Width = speciesbus.Size{Min: 1, Max: 1000} },
		"light":    func(f *speciesbus.Fields) { f.Light = 8 },
		"sources": func(f *speciesbus.Fields) {
			f.Sources = []speciesbus.Source{{Label: "A blog", URL: "javascript:alert(1)"}}
		},
		"bloom":      func(f *speciesbus.Fields) { f.Bloom = 1 << 12 },
		"scientific": func(f *speciesbus.Fields) { f.Scientific = strings.Repeat("x", 200) },
	} {
		f := winecup()
		change(&f)

		_, err := b.Create(t.Context(), f)
		if invalid, ok := errors.AsType[speciesbus.Invalid](err); !ok || invalid.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}

	if all, _ := b.All(t.Context()); len(all) != 0 {
		t.Errorf("%d refused species were saved", len(all))
	}
}

func TestTheAddressIsFixedAndTakenOnce(t *testing.T) {
	b := setup(t)

	sp, err := b.Create(t.Context(), winecup())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := b.Create(t.Context(), winecup()); err == nil || !strings.Contains(err.Error(), "another plant already uses") {
		t.Errorf("a second winecup: %v", err)
	}

	f := winecup()
	f.Slug = "somewhere-else"
	f.Common.EN = "Purple poppy mallow"

	got, err := b.Update(t.Context(), sp.ID, f)
	if err != nil || got.Slug != "winecup" || got.Common.EN != "Purple poppy mallow" {
		t.Errorf("after update: %+v, %v", got, err)
	}
}

func TestTheListIsByCommonName(t *testing.T) {
	b := setup(t)

	for _, n := range []struct{ slug, name string }{{"sedge", "woodland creek sedge"}, {"aster", "Fall aster"}, {"violet", "Missouri violet"}} {
		f := winecup()
		f.Slug, f.Common.EN = n.slug, n.name

		if _, err := b.Create(t.Context(), f); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := b.All(t.Context())

	var got []string
	for _, sp := range all {
		got = append(got, sp.Slug)
	}

	if strings.Join(got, " ") != "aster violet sedge" {
		t.Errorf("order: %v", got)
	}
}

func TestASizeIsWrittenTheWayANurseryWritesIt(t *testing.T) {
	for size, want := range map[speciesbus.Size]string{
		{Min: 18, Max: 24}: "18–24 in",
		{Min: 48, Max: 84}: "4–7 ft",
		{Min: 12, Max: 24}: "12–24 in",
		{Min: 30, Max: 48}: "30–48 in",
		{Min: 36, Max: 36}: "36 in",
		{Min: 60, Max: 60}: "5 ft",
		{}:                 "",
	} {
		if got := size.String(); got != want {
			t.Errorf("%+v: %q, want %q", size, got, want)
		}
	}
}
