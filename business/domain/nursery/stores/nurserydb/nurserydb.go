// Package nurserydb stores nursery stock: visits, and the lines of what each
// nursery had.
//
// A visit is unique by its nursery, ignoring case, and its day, so that two
// tag photos sorted at once from the same morning at the same nursery land on
// one visit: found or made by one INSERT … ON CONFLICT DO NOTHING and a read,
// never a read and then a write (CLAUDE.md).
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
	"nursery_visits": {"id", "nursery", "nursery_key", "day", "created_at"},
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

	return nil
}

// Visit finds the visit for a nursery on a day, or makes it.
func (s *Store) Visit(ctx context.Context, v nurserybus.Visit) (nurserybus.Visit, error) {
	key := strings.ToLower(v.Nursery)

	if _, err := s.db.ExecContext(ctx, `INSERT INTO nursery_visits (id, nursery, nursery_key, day, created_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		v.ID.String(), v.Nursery, key, v.Day.UnixMilli(), v.CreatedAt.UnixMilli()); err != nil {
		return nurserybus.Visit{}, fmt.Errorf("recording a nursery visit: %w", err)
	}

	got, err := scanVisit(s.db.QueryRowContext(ctx, `SELECT id, nursery, day, created_at FROM nursery_visits WHERE nursery_key = ? AND day = ?`,
		key, v.Day.UnixMilli()))
	if err != nil {
		return nurserybus.Visit{}, fmt.Errorf("reading a nursery visit: %w", err)
	}

	return got, nil
}

// Visits is every visit, the most recent first.
func (s *Store) Visits(ctx context.Context) ([]nurserybus.Visit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, nursery, day, created_at FROM nursery_visits ORDER BY day DESC, created_at DESC, id`)
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

func scanVisit(row scanner) (nurserybus.Visit, error) {
	var (
		v            nurserybus.Visit
		id           string
		day, created int64
	)

	if err := row.Scan(&id, &v.Nursery, &day, &created); err != nil {
		return nurserybus.Visit{}, err
	}

	var err error
	if v.ID, err = types.ParseID(id); err != nil {
		return nurserybus.Visit{}, fmt.Errorf("a nursery visit has an unreadable id %q: %w", id, err)
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
