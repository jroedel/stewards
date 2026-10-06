// Package nurserydb stores nursery stock: the register of nurseries, their
// visits, and the lines of what each nursery had.
//
// A nursery is unique by its name ignoring case, and a visit by its nursery
// and its day, so that two tag photos sorted at once from the same morning at
// the same nursery land on one nursery and one visit: each found or made by
// one INSERT … ON CONFLICT DO NOTHING and a read, never a read and then a
// write (CLAUDE.md).
//
// The register came after the visits. A visit kept its nursery's name and the
// name's lower case, which were what made it unique, and gained nursery_id;
// the name and key are still written, in step with the register's, so that a
// binary from before the register, rolled back onto this database, reads
// visits that make sense.
//
// A line's species_id and inbox_id are plain columns, without references, as
// the inbox's are: a stock list is a record of what a nursery had, and must
// not stop a steward removing a plant added twice or an inbox photo.
package nurserydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of nurserybus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ nurserybus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"nurseries":      {"id", "name", "name_key", "address", "website", "phone", "note", "created_at", "updated_at"},
	"nursery_visits": {"id", "nursery", "nursery_key", "day", "created_at", "nursery_id"},
	"nursery_lines": {
		"id", "visit_id", "species_id", "name_on_tag", "pot_size", "price_cents",
		"count", "note", "inbox_id", "created_at", "updated_at",
	},
}

// Init creates the tables. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS nursery_visits (
    id           TEXT    PRIMARY KEY,
    nursery      TEXT    NOT NULL,

    -- The name in lower case, which is what makes a visit the same one.
    nursery_key  TEXT    NOT NULL,

    -- The garden's midnight at the start of the day, Unix milliseconds.
    day          INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,

    UNIQUE (nursery_key, day)
) STRICT;

CREATE TABLE IF NOT EXISTS nursery_lines (
    id           TEXT    PRIMARY KEY,
    visit_id     TEXT    NOT NULL REFERENCES nursery_visits (id),
    species_id   TEXT,
    name_on_tag  TEXT    NOT NULL DEFAULT '',
    pot_size     TEXT    NOT NULL DEFAULT '',

    -- 0 for not noted.
    price_cents  INTEGER NOT NULL DEFAULT 0,
    count        INTEGER NOT NULL DEFAULT 0,
    note         TEXT    NOT NULL DEFAULT '',
    inbox_id     TEXT,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS nursery_lines_visit ON nursery_lines (visit_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the nursery stock tables: %w", err)
	}

	// The register, and each visit's place in it. nursery_id is a later
	// column, so it arrives by ALTER and nothing that mentions it is in the
	// CREATE block above (CLAUDE.md). Nullable, as an added column with a
	// reference must be; every visit has one once the statements below have
	// run, and every visit made since sets it.
	const register = `
CREATE TABLE IF NOT EXISTS nurseries (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,

    -- The name in lower case, which is what makes a nursery the same one.
    name_key    TEXT    NOT NULL UNIQUE,
    address     TEXT    NOT NULL DEFAULT '',
    website     TEXT    NOT NULL DEFAULT '',
    phone       TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;
`
	if _, err := db.ExecContext(ctx, register); err != nil {
		return fmt.Errorf("creating the register of nurseries: %w", err)
	}

	if err := sqldb.AddColumn(ctx, db, "nursery_visits", "nursery_id", "TEXT REFERENCES nurseries (id)"); err != nil {
		return err
	}

	// Every visit from before the register puts its nursery in it, under the
	// spelling of its first visit, and points at it. Idempotent: a nursery
	// already there is left as it is, and a visit already pointing is not
	// touched. The new ids are made here as types.ID makes them, 32
	// lower-case hex characters, because the rows are made in SQL.
	const backfill = `
INSERT INTO nurseries (id, name, name_key, created_at, updated_at)
SELECT lower(hex(randomblob(16))), nursery, nursery_key, MIN(created_at), MIN(created_at)
FROM nursery_visits WHERE nursery_id IS NULL GROUP BY nursery_key
ON CONFLICT DO NOTHING;

UPDATE nursery_visits SET nursery_id = (SELECT id FROM nurseries WHERE name_key = nursery_visits.nursery_key)
WHERE nursery_id IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS nursery_visits_nursery_day ON nursery_visits (nursery_id, day);
`
	// That index is what Visit's read relies on: one visit per nursery and
	// day. The old UNIQUE (nursery_key, day) says the same while every
	// visit's key is its nursery's, which UpdateNursery keeps so -- and
	// must, or a new nursery under a renamed one's old spelling could not
	// be visited on a day the renamed one was.
	if _, err := db.ExecContext(ctx, backfill); err != nil {
		return fmt.Errorf("putting the visited nurseries in the register: %w", err)
	}

	return nil
}

// ------------------------------------------------------------------ the register

const nurseryColumns = `id, name, address, website, phone, note, created_at, updated_at`

// EnsureNursery finds a nursery by its name ignoring case, or adds n.
func (s *Store) EnsureNursery(ctx context.Context, n nurserybus.Nursery) (nurserybus.Nursery, error) {
	key := strings.ToLower(n.Name)

	if _, err := s.db.ExecContext(ctx, `INSERT INTO nurseries (`+nurseryColumns+`, name_key)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		n.ID.String(), n.Name, n.Address, n.Website, n.Phone, n.Note, n.CreatedAt.UnixMilli(), n.UpdatedAt.UnixMilli(), key); err != nil {
		return nurserybus.Nursery{}, fmt.Errorf("adding a nursery: %w", err)
	}

	got, err := scanNursery(s.db.QueryRowContext(ctx, `SELECT `+nurseryColumns+` FROM nurseries WHERE name_key = ?`, key))
	if err != nil {
		return nurserybus.Nursery{}, fmt.Errorf("reading a nursery: %w", err)
	}

	return got, nil
}

// CreateNursery inserts a nursery, or nurserybus.ErrDuplicate for one whose
// name is taken.
func (s *Store) CreateNursery(ctx context.Context, n nurserybus.Nursery) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO nurseries (`+nurseryColumns+`, name_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID.String(), n.Name, n.Address, n.Website, n.Phone, n.Note, n.CreatedAt.UnixMilli(), n.UpdatedAt.UnixMilli(), strings.ToLower(n.Name))

	switch {
	case sqldb.IsUniqueViolation(err):
		return nurserybus.ErrDuplicate
	case err != nil:
		return fmt.Errorf("adding a nursery: %w", err)
	}

	return nil
}

// UpdateNursery writes a nursery, and its name onto its visits, together.
func (s *Store) UpdateNursery(ctx context.Context, n nurserybus.Nursery) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("changing a nursery: %w", err)
	}
	defer tx.Rollback()

	key := strings.ToLower(n.Name)

	res, err := tx.ExecContext(ctx, `UPDATE nurseries SET
    name = ?, name_key = ?, address = ?, website = ?, phone = ?, note = ?, updated_at = ?
WHERE id = ?`,
		n.Name, key, n.Address, n.Website, n.Phone, n.Note, n.UpdatedAt.UnixMilli(), n.ID.String())

	switch {
	case sqldb.IsUniqueViolation(err):
		return nurserybus.ErrDuplicate
	case err != nil:
		return fmt.Errorf("changing a nursery: %w", err)
	}

	changed, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("counting the rows changed: %w", err)
	case changed == 0:
		return nurserybus.ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `UPDATE nursery_visits SET nursery = ?, nursery_key = ? WHERE nursery_id = ?`, n.Name, key, n.ID.String()); err != nil {
		return fmt.Errorf("renaming a nursery's visits: %w", err)
	}

	return tx.Commit()
}

// DeleteNursery removes a nursery that has no visits.
func (s *Store) DeleteNursery(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nurseries WHERE id = ?
AND NOT EXISTS (SELECT 1 FROM nursery_visits WHERE nursery_id = ?)`, id.String(), id.String())
	if err != nil {
		return fmt.Errorf("removing a nursery: %w", err)
	}

	if removed, err := res.RowsAffected(); err != nil || removed > 0 {
		return err
	}

	// Nothing removed: either it is not there, or it has visits.
	if _, err := s.NurseryByID(ctx, id); err != nil {
		return err
	}

	return nurserybus.ErrInUse
}

// NurseryByID is one nursery, or nurserybus.ErrNotFound.
func (s *Store) NurseryByID(ctx context.Context, id types.ID) (nurserybus.Nursery, error) {
	n, err := scanNursery(s.db.QueryRowContext(ctx, `SELECT `+nurseryColumns+` FROM nurseries WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nurserybus.Nursery{}, nurserybus.ErrNotFound
	}

	return n, err
}

// Register is every nursery, by name.
func (s *Store) Register(ctx context.Context) ([]nurserybus.Nursery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nurseryColumns+` FROM nurseries ORDER BY name_key`)
	if err != nil {
		return nil, fmt.Errorf("listing nurseries: %w", err)
	}
	defer rows.Close()

	var all []nurserybus.Nursery
	for rows.Next() {
		n, err := scanNursery(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, n)
	}

	return all, rows.Err()
}

// ------------------------------------------------------------------ visits

// Visit finds the visit for a nursery on a day, or makes it.
func (s *Store) Visit(ctx context.Context, v nurserybus.Visit) (nurserybus.Visit, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO nursery_visits (id, nursery, nursery_key, nursery_id, day, created_at)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		v.ID.String(), v.Nursery, strings.ToLower(v.Nursery), v.NurseryID.String(), v.Day.UnixMilli(), v.CreatedAt.UnixMilli()); err != nil {
		return nurserybus.Visit{}, fmt.Errorf("recording a nursery visit: %w", err)
	}

	got, err := scanVisit(s.db.QueryRowContext(ctx, visitSelect+` WHERE v.nursery_id = ? AND v.day = ?`,
		v.NurseryID.String(), v.Day.UnixMilli()))
	if err != nil {
		return nurserybus.Visit{}, fmt.Errorf("reading a nursery visit: %w", err)
	}

	return got, nil
}

// visitSelect reads a visit with its nursery's name as the register has it.
const visitSelect = `SELECT v.id, v.nursery_id, n.name, v.day, v.created_at
FROM nursery_visits v JOIN nurseries n ON n.id = v.nursery_id`

// Visits is every visit, the most recent first.
func (s *Store) Visits(ctx context.Context) ([]nurserybus.Visit, error) {
	rows, err := s.db.QueryContext(ctx, visitSelect+` ORDER BY v.day DESC, v.created_at DESC, v.id`)
	if err != nil {
		return nil, fmt.Errorf("listing nursery visits: %w", err)
	}
	defer rows.Close()

	var all []nurserybus.Visit
	for rows.Next() {
		v, err := scanVisit(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, v)
	}

	return all, rows.Err()
}

const lineColumns = `id, visit_id, species_id, name_on_tag, pot_size, price_cents, count, note, inbox_id, created_at, updated_at`

// CreateLine inserts a line.
func (s *Store) CreateLine(ctx context.Context, l nurserybus.Line) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO nursery_lines (`+lineColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID.String(), l.VisitID.String(), orNull(l.SpeciesID), l.NameOnTag, l.PotSize, l.PriceCents, l.Count, l.Note,
		orNull(l.InboxID), l.CreatedAt.UnixMilli(), l.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting a line of nursery stock: %w", err)
	}

	return nil
}

// UpdateLine writes what may be corrected: everything but the visit and the
// photo.
func (s *Store) UpdateLine(ctx context.Context, l nurserybus.Line) error {
	res, err := s.db.ExecContext(ctx, `UPDATE nursery_lines SET
    species_id = ?, name_on_tag = ?, pot_size = ?, price_cents = ?, count = ?, note = ?, updated_at = ?
WHERE id = ?`,
		orNull(l.SpeciesID), l.NameOnTag, l.PotSize, l.PriceCents, l.Count, l.Note, l.UpdatedAt.UnixMilli(), l.ID.String())
	if err != nil {
		return fmt.Errorf("updating a line of nursery stock: %w", err)
	}

	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("counting the rows changed: %w", err)
	case n == 0:
		return nurserybus.ErrNotFound
	}

	return nil
}

// Line is one line, or nurserybus.ErrNotFound.
func (s *Store) Line(ctx context.Context, id types.ID) (nurserybus.Line, error) {
	l, err := scanLine(s.db.QueryRowContext(ctx, `SELECT `+lineColumns+` FROM nursery_lines WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return nurserybus.Line{}, nurserybus.ErrNotFound
	}

	return l, err
}

// Lines is every line, in the order they were added.
func (s *Store) Lines(ctx context.Context) ([]nurserybus.Line, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+lineColumns+` FROM nursery_lines ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing nursery stock: %w", err)
	}
	defer rows.Close()

	var all []nurserybus.Line
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, l)
	}

	return all, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanNursery(row scanner) (nurserybus.Nursery, error) {
	var (
		n                nurserybus.Nursery
		id               string
		created, updated int64
	)

	if err := row.Scan(&id, &n.Name, &n.Address, &n.Website, &n.Phone, &n.Note, &created, &updated); err != nil {
		return nurserybus.Nursery{}, err
	}

	var err error
	if n.ID, err = types.ParseID(id); err != nil {
		return nurserybus.Nursery{}, fmt.Errorf("a nursery has an unreadable id %q: %w", id, err)
	}

	n.CreatedAt, n.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()

	return n, nil
}

func scanVisit(row scanner) (nurserybus.Visit, error) {
	var (
		v            nurserybus.Visit
		id, nursery  string
		day, created int64
	)

	if err := row.Scan(&id, &nursery, &v.Nursery, &day, &created); err != nil {
		return nurserybus.Visit{}, err
	}

	var err error
	if v.ID, err = types.ParseID(id); err != nil {
		return nurserybus.Visit{}, fmt.Errorf("a nursery visit has an unreadable id %q: %w", id, err)
	}

	if v.NurseryID, err = types.ParseID(nursery); err != nil {
		return nurserybus.Visit{}, fmt.Errorf("nursery visit %s has an unreadable nursery %q: %w", id, nursery, err)
	}

	v.Day, v.CreatedAt = time.UnixMilli(day).UTC(), time.UnixMilli(created).UTC()

	return v, nil
}

func scanLine(row scanner) (nurserybus.Line, error) {
	var (
		l                nurserybus.Line
		id, visit        string
		species, inbox   sql.NullString
		created, updated int64
	)

	err := row.Scan(&id, &visit, &species, &l.NameOnTag, &l.PotSize, &l.PriceCents, &l.Count, &l.Note, &inbox, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nurserybus.Line{}, err
		}

		return nurserybus.Line{}, fmt.Errorf("reading a line of nursery stock: %w", err)
	}

	for _, ref := range []struct {
		raw string
		ok  bool
		dst *types.ID
	}{{id, true, &l.ID}, {visit, true, &l.VisitID}, {species.String, species.Valid, &l.SpeciesID}, {inbox.String, inbox.Valid, &l.InboxID}} {
		if !ref.ok {
			continue
		}

		if *ref.dst, err = types.ParseID(ref.raw); err != nil {
			return nurserybus.Line{}, fmt.Errorf("line %s of nursery stock has an unreadable id: %w", id, err)
		}
	}

	l.CreatedAt, l.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()

	return l, nil
}

func orNull(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}
