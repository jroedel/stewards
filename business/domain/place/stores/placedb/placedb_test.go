package placedb_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

var now = time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)

func open(t *testing.T) (*sql.DB, *placedb.Store) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		// Twice, because Init runs at every startup.
		func() error { return placedb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	return db, placedb.NewStore(db)
}

func place(slug string) placebus.Place {
	return placebus.Place{
		ID:        types.NewID(),
		Slug:      slug,
		Name:      types.Text{EN: "Rain garden", ES: "Jardín de lluvia"},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestSchemaMatchesWhatTheStoreReads(t *testing.T) {
	db, _ := open(t)

	if err := sqldb.CheckSchema(t.Context(), db, placedb.Expected); err != nil {
		t.Errorf("CheckSchema: %v", err)
	}
}

// Every field, both halves of every Text, and the times to the millisecond.
func TestAPlaceRoundTrips(t *testing.T) {
	_, store := open(t)

	parent := place("rain-garden")
	if err := store.Create(t.Context(), parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}

	want := placebus.Place{
		ID:          types.NewID(),
		Slug:        "rain-garden-inflow",
		Name:        types.Text{EN: "Inflow", ES: "Entrada del agua"},
		ParentID:    parent.ID,
		Purpose:     types.Text{EN: "Deep roots to hold the soil"},
		Conditions:  types.Text{EN: "Wettest; takes the force of the water"},
		PhotoPoint:  types.Text{EN: "From the fire-pit bench"},
		TrailAnchor: "joseph",
		Sort:        3,
		Spot:        &placebus.Spot{X: 470, Y: 446},
		CreatedAt:   now,
		UpdatedAt:   now.Add(1500 * time.Millisecond),
	}

	if err := store.Create(t.Context(), want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.BySlug(t.Context(), "rain-garden-inflow")
	if err != nil {
		t.Fatalf("BySlug: %v", err)
	}

	// DeepEqual rather than ==, which would compare the Spot pointers.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back\n %+v\nwant\n %+v", got, want)
	}

	byID, err := store.ByID(t.Context(), want.ID)
	if err != nil || !reflect.DeepEqual(byID, want) {
		t.Errorf("ByID = %+v, %v", byID, err)
	}
}

// A top-level place stores NULL, not an empty string, so the foreign key has
// nothing to look up. An empty string would fail it.
func TestAPlaceOnItsOwnHasNoParent(t *testing.T) {
	_, store := open(t)

	p := place("fire-pit")
	if err := store.Create(t.Context(), p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.ByID(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}

	if !got.TopLevel() {
		t.Errorf("parent = %q, want none", got.ParentID)
	}
}

func TestASlugIsTakenOnce(t *testing.T) {
	_, store := open(t)

	if err := store.Create(t.Context(), place("rain-garden")); err != nil {
		t.Fatalf("first: %v", err)
	}

	err := store.Create(t.Context(), place("rain-garden"))
	if !errors.Is(err, placebus.ErrSlugTaken) {
		t.Errorf("second = %v, want ErrSlugTaken", err)
	}
}

// placebus refuses this first. The reference is the second lock, for any
// query that ever goes round placebus.
func TestAParentCannotBeDeletedFromUnderItsBands(t *testing.T) {
	_, store := open(t)

	parent := place("rain-garden")
	band := place("rain-garden-middle")
	band.ParentID = parent.ID

	for _, p := range []placebus.Place{parent, band} {
		if err := store.Create(t.Context(), p); err != nil {
			t.Fatalf("Create %s: %v", p.Slug, err)
		}
	}

	if err := store.Delete(t.Context(), parent.ID); err == nil {
		t.Error("a place with a band inside it was deleted")
	}
}

func TestMissingIsNotFound(t *testing.T) {
	_, store := open(t)

	if _, err := store.BySlug(t.Context(), "nowhere"); !errors.Is(err, placebus.ErrNotFound) {
		t.Errorf("BySlug = %v", err)
	}

	p := place("nowhere")
	if err := store.Update(t.Context(), p); !errors.Is(err, placebus.ErrNotFound) {
		t.Errorf("Update = %v", err)
	}

	if err := store.Delete(t.Context(), p.ID); !errors.Is(err, placebus.ErrNotFound) {
		t.Errorf("Delete = %v", err)
	}
}

// The places table as PR #10 deployed it, written out literally rather than
// derived from Init: the database on the server has exactly this, and Init
// has to bring it forward (CLAUDE.md, "A store with a later column owns a
// test that runs Init over the schema as it stood before").
const before = `
CREATE TABLE places (
    id              TEXT    PRIMARY KEY,
    slug            TEXT    NOT NULL UNIQUE,
    name_en         TEXT    NOT NULL,
    name_es         TEXT    NOT NULL DEFAULT '',
    parent_id       TEXT    REFERENCES places (id),
    purpose_en      TEXT    NOT NULL DEFAULT '',
    purpose_es      TEXT    NOT NULL DEFAULT '',
    conditions_en   TEXT    NOT NULL DEFAULT '',
    conditions_es   TEXT    NOT NULL DEFAULT '',
    photo_point_en  TEXT    NOT NULL DEFAULT '',
    photo_point_es  TEXT    NOT NULL DEFAULT '',
    trail_anchor    TEXT    NOT NULL DEFAULT '',
    sort            INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;

CREATE INDEX places_parent ON places (parent_id);

INSERT INTO places (id, slug, name_en, created_at, updated_at)
VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'fire-pit', 'Fire pit', 1, 1);
`

func TestInitBringsTheFirstPlacesTableForward(t *testing.T) {
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
		if err := placedb.Init(t.Context(), db); err != nil {
			t.Fatalf("Init over the old table: %v", err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, placedb.Expected); err != nil {
		t.Fatal(err)
	}

	// The place from before is off the map, not in its north-west corner,
	// and it can be put on it.
	store := placedb.NewStore(db)

	p, err := store.BySlug(t.Context(), "fire-pit")
	if err != nil || p.Spot != nil {
		t.Fatalf("the old place read back: %+v, %v; want it off the map", p.Spot, err)
	}

	p.Spot = &placebus.Spot{X: 600, Y: 420}
	if err := store.Update(t.Context(), p); err != nil {
		t.Fatal(err)
	}

	if p, err = store.BySlug(t.Context(), "fire-pit"); err != nil || p.Spot == nil || *p.Spot != (placebus.Spot{X: 600, Y: 420}) {
		t.Errorf("after putting it on the map: %+v, %v", p.Spot, err)
	}
}
