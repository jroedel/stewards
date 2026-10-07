package listingbus_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// A note's Spanish goes through the translation memory like a place's words
// do (placebus has the whole story). What is particular to a listing is that
// Set finds the note as it was shown by reading the place's list, so that is
// what this follows: moved at startup, then a note changed with its Spanish
// left as it was.
func TestANoteKeepsItsWordsAndTheMemoryTheirTranslation(t *testing.T) {
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
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	place, err := placebus.NewBusiness(placedb.NewStore(db), nil, nil).Create(t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}})
	if err != nil {
		t.Fatal(err)
	}

	sedge, err := speciesbus.NewBusiness(speciesdb.NewStore(db), nil, nil).Create(t.Context(), speciesbus.Fields{Slug: "sedge", Common: types.Text{EN: "Sedge"}})
	if err != nil {
		t.Fatal(err)
	}

	store := listingdb.NewStore(db)
	note := types.Text{EN: "Two at the back", ES: "Dos al fondo"}

	if _, err := listingbus.NewBusiness(store, nil, nil).Set(t.Context(), place.ID, sedge.ID, listingbus.Fields{Action: listingbus.Protect, Note: note}); err != nil {
		t.Fatal(err)
	}

	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	b := listingbus.NewBusiness(store, memory, nil)

	if n, err := b.MoveTranslations(t.Context()); err != nil || n != 1 {
		t.Fatalf("MoveTranslations = %d, %v", n, err)
	}

	if raw, _ := store.ForPlace(t.Context(), place.ID); len(raw) != 1 || raw[0].Note != (types.Text{EN: note.EN}) {
		t.Errorf("stored %+v, want the English alone", raw)
	}

	listed, err := b.ForSpecies(t.Context(), sedge.ID)
	if err != nil || len(listed) != 1 || listed[0].Note != note {
		t.Fatalf("ForSpecies = %+v, %v", listed, err)
	}

	changed := listingbus.Fields{Action: listingbus.Protect, Note: types.Text{EN: "Three at the back", ES: note.ES}}

	l, err := b.Set(t.Context(), place.ID, sedge.ID, changed)
	if err != nil {
		t.Fatal(err)
	}

	if l.Note != (types.Text{EN: "Three at the back"}) {
		t.Errorf("Set = %+v; the old note's Spanish came along", l.Note)
	}

	// Sent again with the English alone, by a program: unchanged.
	again, err := b.Import(t.Context(), place.ID, sedge.ID, listingbus.Fields{Action: listingbus.Protect, Note: types.Text{EN: "Three at the back"}})
	if err != nil || again.Outcome != listingbus.Unchanged {
		t.Errorf("Import = %+v, %v; want unchanged", again, err)
	}
}
