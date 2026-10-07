package speciesbus_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// A plant's names and notes go through the translation memory as a place's
// words do (placebus has the whole story): moved at startup, read back in
// both languages, and an import that sends the English alone is unchanged.
func TestAPlantKeepsItsWordsAndTheMemoryTheirTranslation(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	store := speciesdb.NewStore(db)

	f := winecup()
	f.Common = types.Text{EN: "Winecup", ES: "Copa de vino"}
	f.FlowerColor = types.Text{EN: "Magenta", ES: "Magenta"}

	if _, err := speciesbus.NewBusiness(store, nil, nil).Create(t.Context(), f); err != nil {
		t.Fatal(err)
	}

	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	b := speciesbus.NewBusiness(store, memory, nil)

	if n, err := b.MoveTranslations(t.Context()); err != nil || n != 1 {
		t.Fatalf("MoveTranslations = %d, %v", n, err)
	}

	if raw, _ := store.BySlug(t.Context(), "winecup"); raw.Common != (types.Text{EN: "Winecup"}) || raw.FlowerColor != (types.Text{EN: "Magenta"}) {
		t.Errorf("stored %+v and %+v, want the English alone", raw.Common, raw.FlowerColor)
	}

	all, err := b.All(t.Context())
	if err != nil || len(all) != 1 || all[0].Common != f.Common || all[0].FlowerColor != f.FlowerColor {
		t.Fatalf("All = %+v, %v", all, err)
	}

	f.Common.ES, f.FlowerColor.ES = "", ""

	if got, err := b.Import(t.Context(), f); err != nil || got.Outcome != speciesbus.Unchanged || got.Species.Common.ES != "Copa de vino" {
		t.Errorf("Import = %+v, %v; want unchanged, with its Spanish", got, err)
	}
}
