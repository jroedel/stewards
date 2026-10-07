package workdaybus_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// A day's title and details go through the translation memory as a place's
// words do (placebus has the whole story): moved at startup, and read back
// in both languages from every list.
func TestADayKeepsItsWordsAndTheMemoryTheirTranslation(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) },
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, types.Garden)
	now := func() time.Time { return clock }
	store := workdaydb.NewStore(db)

	f := morning(10, "Planting the rain garden")
	f.Title.ES = "Plantar el jardín de lluvia"

	made, err := workdaybus.NewBusiness(store, nil, now).Create(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}

	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	b := workdaybus.NewBusiness(store, memory, now)

	if n, err := b.MoveTranslations(t.Context()); err != nil || n != 1 {
		t.Fatalf("MoveTranslations = %d, %v", n, err)
	}

	if raw, _ := store.ByID(t.Context(), made.ID); raw.Title != (types.Text{EN: f.Title.EN}) {
		t.Errorf("stored %+v, want the English alone", raw.Title)
	}

	upcoming, err := b.Upcoming(t.Context())
	if err != nil || len(upcoming) != 1 || upcoming[0].Title != f.Title {
		t.Errorf("Upcoming = %+v, %v", upcoming, err)
	}

	f.Title.EN = "Planting the rain garden's middle band"

	changed, err := b.Update(t.Context(), made.ID, f)
	if err != nil || changed.Title != (types.Text{EN: f.Title.EN}) {
		t.Errorf("Update = %+v, %v; the old title's Spanish came along", changed.Title, err)
	}
}
