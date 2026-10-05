package inboxdb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
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

func TestInitBringsTheFirstInboxTableForward(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, init := range []func() error{
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
		func() error { _, err := db.ExecContext(t.Context(), before); return err },
		func() error { return inboxdb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) }, // and at the next startup
	} {
		if err := init(); err != nil {
			t.Fatal(err)
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

	if it.Status != inboxbus.New || it.Outcome != "" || !it.SpeciesID.Zero() || !it.SortedAt.IsZero() || it.Note != "by the gate" {
		t.Errorf("the photo from before is %+v", it)
	}
}
