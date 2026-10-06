package nurserydb_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/nursery/stores/nurserydb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Saturday 3 and 10 October 2026, at the garden's midnight, as visits keep
// their day: 1791003600000 and 1791608400000.

// before is the nursery tables as they stood before the register, written out
// as they were rather than derived from today's (CLAUDE.md), with three visits
// to two nurseries -- one spelled two ways -- and a line on each.
const before = `
CREATE TABLE nursery_visits (
    id           TEXT    PRIMARY KEY,
    nursery      TEXT    NOT NULL,
    nursery_key  TEXT    NOT NULL,
    day          INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (nursery_key, day)
) STRICT;

CREATE TABLE nursery_lines (
    id           TEXT    PRIMARY KEY,
    visit_id     TEXT    NOT NULL REFERENCES nursery_visits (id),
    species_id   TEXT,
    name_on_tag  TEXT    NOT NULL DEFAULT '',
    pot_size     TEXT    NOT NULL DEFAULT '',
    price_cents  INTEGER NOT NULL DEFAULT 0,
    count        INTEGER NOT NULL DEFAULT 0,
    note         TEXT    NOT NULL DEFAULT '',
    inbox_id     TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX nursery_lines_visit ON nursery_lines (visit_id);

INSERT INTO nursery_visits VALUES
    ('11111111111111111111111111111111', 'Natural Gardener', 'natural gardener', 1791003600000, 1791010000000),
    ('22222222222222222222222222222222', 'natural gardener', 'natural gardener', 1791608400000, 1791610000000),
    ('33333333333333333333333333333333', 'Barton Springs Nursery', 'barton springs nursery', 1791003600000, 1791010000001);

INSERT INTO nursery_lines (id, visit_id, name_on_tag, created_at, updated_at) VALUES
    ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '11111111111111111111111111111111', 'Turk''s cap', 1790000000000, 1790000000000),
    ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', '22222222222222222222222222222222', 'Rock rose', 1790600000000, 1790600000000),
    ('cccccccccccccccccccccccccccccccc', '33333333333333333333333333333333', 'Mealy blue sage', 1790000000001, 1790000000001);
`

func TestInitPutsTheVisitedNurseriesInTheRegister(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.ExecContext(t.Context(), before); err != nil {
		t.Fatalf("the old schema: %v", err)
	}

	// Twice: every startup runs it.
	for range 2 {
		if err := nurserydb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init over the old tables: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, nurserydb.Expected); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)
	b := nurserybus.NewBusiness(nurserydb.NewStore(db), func() time.Time { return clock })

	// Two nurseries, the one spelled two ways under its first visit's
	// spelling, with ids as the app makes them.
	reg, err := b.Register(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(reg) != 2 || reg[0].Name != "Barton Springs Nursery" || reg[1].Name != "Natural Gardener" {
		t.Fatalf("the register: %+v", reg)
	}

	if !reg[1].CreatedAt.Equal(time.UnixMilli(1791010000000)) {
		t.Errorf("the register's date for it is its first visit's: %v", reg[1].CreatedAt)
	}

	all, err := b.All(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 3 {
		t.Fatalf("%d visits, want all three", len(all))
	}

	for _, st := range all {
		want := map[string]types.ID{"Natural Gardener": reg[1].ID, "Barton Springs Nursery": reg[0].ID}[st.Visit.Nursery]
		if want.Zero() || st.Visit.NurseryID != want || len(st.Lines) != 1 {
			t.Errorf("visit %+v with %d lines", st.Visit, len(st.Lines))
		}
	}

	// A tag photo from the first of those days is still that visit: the
	// new index holds the visits from before.
	if _, err := b.Add(t.Context(), "Natural Gardener", time.UnixMilli(1791010000000).Add(2*time.Hour), types.ID{}, nurserybus.Fields{NameOnTag: "Frostweed"}); err != nil {
		t.Fatal(err)
	}

	if all, _ := b.All(t.Context()); len(all) != 3 {
		t.Errorf("%d visits after another line on an old one", len(all))
	}
}
