package inboxdb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The inbox table as PR #35 deployed it, written out literally rather than
// derived from Init: the database on the server has exactly this, and Init
// has to bring it forward (CLAUDE.md, "A store with a later column owns a
// test that runs Init over the schema as it stood before").
const before = `
CREATE TABLE inbox (
    id            TEXT    PRIMARY KEY,
    from_user_id  TEXT    REFERENCES users (id),
    at            TEXT    NOT NULL,
    place_id      TEXT    REFERENCES places (id),
    note          TEXT    NOT NULL DEFAULT '',
    taken_at      INTEGER,
    lat           REAL,
    lon           REAL,
    status        TEXT    NOT NULL,
    format        TEXT    NOT NULL,
    sha256        TEXT    NOT NULL UNIQUE,
    large_width   INTEGER NOT NULL,
    large_height  INTEGER NOT NULL,
    small_width   INTEGER NOT NULL,
    small_height  INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX inbox_status ON inbox (status);
CREATE INDEX inbox_place ON inbox (place_id);

INSERT INTO inbox (id, at, note, status, format, sha256, large_width, large_height, small_width, small_height, created_at, updated_at)
VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'property', 'by the gate', 'new', 'jpeg', 'abc', 1600, 1200, 800, 600, 1, 1);
`

// And as PR #37 left it, with the sorting columns: what the server has now.
const afterSorting = before + `
ALTER TABLE inbox ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE inbox ADD COLUMN species_id TEXT;
ALTER TABLE inbox ADD COLUMN photo_id TEXT;
ALTER TABLE inbox ADD COLUMN sorted_by TEXT;
ALTER TABLE inbox ADD COLUMN sorted_at INTEGER;
`

func TestInitBringsTheFirstInboxTableForward(t *testing.T) {
	for name, ddl := range map[string]string{"as PR 35 made it": before, "as PR 37 left it": afterSorting} {
		t.Run(name, func(t *testing.T) { bringsForward(t, ddl) })
	}
}

func bringsForward(t *testing.T, ddl string) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for step, init := range []func() error{
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
		func() error { _, err := db.ExecContext(t.Context(), ddl); return err },
		func() error { return inboxdb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) }, // and at the next startup
	} {
		if err := init(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, inboxdb.Expected); err != nil {
		t.Fatal(err)
	}

	id, _ := types.ParseID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	it, err := inboxdb.NewStore(db).ByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	if it.Status != inboxbus.New || it.Outcome != "" || !it.SpeciesID.Zero() || !it.SortedAt.IsZero() || !it.LineID.Zero() || !it.PrunedAt.IsZero() || it.Note != "by the gate" {
		t.Errorf("the photo from before is %+v", it)
	}
}

// A plant's photo sorted from the inbox before photos kept the moment they
// were taken gets it from its inbox row at the next startup -- once: a day a
// steward has since cleared stays cleared.
func TestAPhotoSortedBeforeGetsItsDayFromTheInbox(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := t.Context()
	for _, init := range []func() error{
		func() error { return placedb.Init(ctx, db) },
		func() error { return speciesdb.Init(ctx, db) },
		func() error { return userdb.Init(ctx, db) },
		func() error { return photodb.Init(ctx, db) },
		func() error { return inboxdb.Init(ctx, db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	// As a deploy finds them: a photo with no taken_at at all, and the
	// inbox row it was sorted from, which knew when.
	const taken = int64(1775210400000) // 2026-04-03 10:00 UTC
	for _, q := range []string{
		`INSERT INTO species (id, slug, common_en, status, created_at, updated_at) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'winecup', 'Winecup', 'native', 1, 1)`,
		`INSERT INTO photos (id, species_id, source, format, large_width, large_height, small_width, small_height, created_at, updated_at, kind)
		 VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ours', 'jpeg', 1600, 1200, 800, 600, 1, 1, 'leaf')`,
		`INSERT INTO inbox (id, at, taken_at, status, format, sha256, large_width, large_height, small_width, small_height, created_at, updated_at, photo_id, sorted_at)
		 VALUES ('cccccccccccccccccccccccccccccccc', 'property', 1775210400000, 'sorted', 'jpeg', 'x', 1600, 1200, 800, 600, 1, 1, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 2)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	day := func() any {
		var v any
		if err := db.QueryRowContext(ctx, `SELECT taken_at FROM photos`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if err := inboxdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := day(); got != taken {
		t.Errorf("after the startup: %v, want %d", got, taken)
	}

	if _, err := db.ExecContext(ctx, `UPDATE photos SET taken_at = 0`); err != nil {
		t.Fatal(err)
	}
	if err := inboxdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := day(); got != int64(0) {
		t.Errorf("a day cleared since came back: %v", got)
	}
}
