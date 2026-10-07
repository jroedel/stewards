package translationdb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/foundation/sqldb"
)

func TestSchemaMatchesWhatTheStoreReads(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := translationdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := sqldb.CheckSchema(t.Context(), db, translationdb.Expected); err != nil {
		t.Errorf("CheckSchema: %v", err)
	}

	// The CHECK holds even for a caller that skips the rules.
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	french := translationbus.Translation{
		Key: "x", Source: "Arrosez", From: "fr", Translated: "Water it", By: translationbus.ByClaude,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := translationdb.NewStore(db).Put(t.Context(), french); err == nil {
		t.Error("a translation from French was stored")
	}
}
