package workdaydb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

func TestSchemaMatchesWhatTheStoreReads(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := workdaydb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := sqldb.CheckSchema(t.Context(), db, workdaydb.Expected); err != nil {
		t.Errorf("CheckSchema: %v", err)
	}

	// The CHECK holds even for a caller that skips the rules.
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	backwards := workdaybus.Day{
		ID: types.NewID(), Starts: now, Ends: now.Add(-time.Hour), Title: types.Text{EN: "x"},
		CreatedAt: now, UpdatedAt: now,
	}

	if err := workdaydb.NewStore(db).Create(t.Context(), backwards); err == nil {
		t.Error("a day ending before it starts was stored")
	}
}
