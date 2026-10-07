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

// The table as it first shipped, before note: written out as it was rather
// than derived from Init, so that Init is tested against a database that
// predates the column, as production's does (CLAUDE.md, "When there is a
// database").
const firstSchema = `
CREATE TABLE translations (
    key          TEXT    PRIMARY KEY,
    source       TEXT    NOT NULL,
    source_lang  TEXT    NOT NULL CHECK (source_lang IN ('en', 'es')),
    translation  TEXT    NOT NULL,
    origin       TEXT    NOT NULL,
    checked      INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
INSERT INTO translations VALUES ('k', 'Rain garden', 'en', 'Jardín de lluvia', 'given', 1, 1, 1);
`

func TestInitBringsTheFirstTableUpToDate(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.ExecContext(t.Context(), firstSchema); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := translationdb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init over the first table: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, translationdb.Expected); err != nil {
		t.Fatal(err)
	}

	all, err := translationdb.NewStore(db).All(t.Context())
	if err != nil || len(all) != 1 || all[0].Translated != "Jardín de lluvia" || !all[0].Checked || all[0].Note != "" {
		t.Errorf("All = %+v, %v", all, err)
	}
}
