package placebus_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// A place's words go through the translation memory: the store keeps what was
// written, the memory its translation, and a place is read back with both.
// Two businesses share one database -- one with no memory, as the app was
// before it, and one with -- so the test can write a place the old way and
// watch the startup move it.
func TestAPlaceKeepsItsWordsAndTheMemoryTheirTranslation(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	store := placedb.NewStore(db)
	old := placebus.NewBusiness(store, nil, nil)

	before := mustCreate(t, old, placebus.Fields{
		Slug: "rain-garden",
		Name: types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}, Purpose: types.Text{EN: "Catches the roof water"},
	})

	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	b := placebus.NewBusiness(store, memory, nil)

	// The startup moves the Spanish once, and then finds nothing.
	for _, want := range []int{1, 0} {
		if n, err := b.MoveTranslations(t.Context()); err != nil || n != want {
			t.Fatalf("MoveTranslations = %d, %v; want %d", n, err, want)
		}
	}

	raw, err := store.ByID(t.Context(), before.ID)
	if err != nil {
		t.Fatal(err)
	}

	if raw.Name != (types.Text{EN: "Rain garden"}) || !raw.UpdatedAt.Equal(before.UpdatedAt.Truncate(time.Millisecond)) {
		t.Errorf("stored %+v at %v, want the English alone and the time untouched", raw.Name, raw.UpdatedAt)
	}

	got, err := b.BySlug(t.Context(), "rain-garden")
	if err != nil || got.Name != before.Name {
		t.Fatalf("read back %+v, %v; want %+v", got.Name, err, before.Name)
	}

	// A program sends the place back with its English alone: it said
	// nothing about the Spanish, which stays, and nothing changed.
	imported, err := b.Import(t.Context(), placebus.Fields{
		Slug: "rain-garden", Name: types.Text{EN: "Rain garden"}, Purpose: types.Text{EN: "Catches the roof water"},
	})
	if err != nil || imported.Outcome != placebus.Unchanged || imported.Place.Name != before.Name {
		t.Errorf("import: %+v, %v; want unchanged with its Spanish", imported, err)
	}

	// A steward renames it and leaves the Spanish box as it was: the new
	// name waits for Claude, rather than carry the old name's Spanish.
	f := placebus.FieldsOf(got)
	f.Name.EN = "Rain garden basin"

	renamed, err := b.Update(t.Context(), got.ID, f)
	if err != nil {
		t.Fatal(err)
	}

	if renamed.Name != (types.Text{EN: "Rain garden basin"}) {
		t.Errorf("renamed to %+v; the old Spanish came along", renamed.Name)
	}

	// The Spanish written together with it is kept.
	f.Name.ES = "Cuenca del jardín de lluvia"

	if renamed, err = b.Update(t.Context(), got.ID, f); err != nil || renamed.Name != f.Name {
		t.Errorf("renamed to %+v, %v; want %+v", renamed.Name, err, f.Name)
	}

	all, err := b.All(t.Context())
	if err != nil || len(all) != 1 || all[0].Name != f.Name {
		t.Errorf("All = %+v, %v", all, err)
	}

	// A new place saying the same words is shown the same translation.
	twin := mustCreate(t, b, placebus.Fields{Slug: "basin", Name: types.Text{EN: "Rain garden basin"}})
	if twin.Name != f.Name {
		t.Errorf("a second place with the same name: %+v, want %+v", twin.Name, f.Name)
	}

	// And the old way of storing is gone: nothing left for a startup to move.
	if n, err := b.MoveTranslations(t.Context()); err != nil || n != 0 {
		t.Errorf("MoveTranslations after = %d, %v", n, err)
	}

}
