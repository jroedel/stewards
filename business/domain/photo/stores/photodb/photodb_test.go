package photodb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The photos table as PR #17 deployed it, written out literally rather than
// derived from Init: the database on the server has exactly this, and Init
// has to bring it forward (CLAUDE.md, "A store with a later column owns a
// test that runs Init over the schema as it stood before").
const before = `
CREATE TABLE photos (
    id            TEXT    PRIMARY KEY,
    species_id    TEXT    REFERENCES species (id),
    place_id      TEXT    REFERENCES places (id),
    kind          TEXT    NOT NULL DEFAULT '',
    taken_year    INTEGER NOT NULL DEFAULT 0,
    taken_month   INTEGER NOT NULL DEFAULT 0,
    source        TEXT    NOT NULL,
    credit        TEXT    NOT NULL DEFAULT '',
    source_url    TEXT    NOT NULL DEFAULT '',
    license       TEXT    NOT NULL DEFAULT '',
    checked       INTEGER NOT NULL DEFAULT 0,
    format        TEXT    NOT NULL,
    large_width   INTEGER NOT NULL,
    large_height  INTEGER NOT NULL,
    small_width   INTEGER NOT NULL,
    small_height  INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX photos_species ON photos (species_id);
CREATE INDEX photos_place ON photos (place_id);

INSERT INTO species (id, slug, common_en, status, created_at, updated_at)
VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'winecup', 'Winecup', 'native', 1, 1);

INSERT INTO photos (id, species_id, source, format, large_width, large_height, small_width, small_height, created_at, updated_at)
VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ours', 'jpeg', 1600, 1200, 800, 600, 1, 1),
       ('cccccccccccccccccccccccccccccccc', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ours', 'jpeg', 1600, 1200, 800, 600, 2, 2);
`

func TestInitBringsTheFirstPhotosTableForward(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, init := range []func() error{
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatalf("the old schema: %v", err)
	}

	// Twice: every startup runs it.
	for range 2 {
		if err := photodb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init over the old table: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, photodb.Expected); err != nil {
		t.Fatal(err)
	}

	// The two photos from before both have no digest, and the unique index
	// does not count '' as a value they share.
	s := photodb.NewStore(db)
	if _, err := db.ExecContext(t.Context(), `UPDATE photos SET checked = 1`); err != nil {
		t.Fatal(err)
	}

	all, err := s.ForSpecies(t.Context(), mustID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if err != nil || len(all) != 2 || all[0].SHA256 != "" {
		t.Fatalf("the old photos read back: %d, %v", len(all), err)
	}
}

func mustID(t *testing.T, s string) types.ID {
	t.Helper()

	id, err := types.ParseID(s)
	if err != nil {
		t.Fatal(err)
	}

	return id
}
