// Package placedb stores places in SQLite.
//
// Times are Unix milliseconds in INTEGER columns, per CLAUDE.md. Each half of
// a types.Text is its own column rather than JSON in one, so that a query can
// ask which places still have no Spanish name -- the list a translator works
// from -- without parsing anything.
package placedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of placebus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ placebus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz: the
// columns this binary reads.
var Expected = sqldb.Expected{
	"places": {
		"id", "slug", "name_en", "name_es", "parent_id",
		"purpose_en", "purpose_es", "conditions_en", "conditions_es",
		"photo_point_en", "photo_point_es", "trail_anchor", "sort",
		"created_at", "updated_at",
	},
}

// Init creates the table. Idempotent, and run at every startup.
//
// No CHECK constraints, deliberately: the rules are placebus's, and a CHECK
// removed from this block later would stay on every database that already
// exists, because SQLite has no ALTER that drops one.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS places (
    id              TEXT    PRIMARY KEY,

    -- UNIQUE, and the insert is the check: see placebus.ErrSlugTaken.
    slug            TEXT    NOT NULL UNIQUE,

    name_en         TEXT    NOT NULL,
    name_es         TEXT    NOT NULL DEFAULT '',

    -- NULL for a place that stands on its own. A reference, so a parent
    -- cannot be deleted from under its bands even by a query that skips
    -- placebus.
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

CREATE INDEX IF NOT EXISTS places_parent ON places (parent_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the places table: %w", err)
	}

	return nil
}

const columns = `id, slug, name_en, name_es, parent_id, purpose_en, purpose_es,
conditions_en, conditions_es, photo_point_en, photo_point_es, trail_anchor, sort,
created_at, updated_at`

// Create inserts a place.
func (s *Store) Create(ctx context.Context, p placebus.Place) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO places (`+columns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID.String(), p.Slug, p.Name.EN, p.Name.ES, parentOf(p),
		p.Purpose.EN, p.Purpose.ES, p.Conditions.EN, p.Conditions.ES,
		p.PhotoPoint.EN, p.PhotoPoint.ES, p.TrailAnchor, p.Sort,
		p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli())

	switch {
	case err == nil:
		return nil
	case sqldb.IsUniqueViolation(err):
		return placebus.ErrSlugTaken
	default:
		return fmt.Errorf("inserting the place: %w", err)
	}
}

// Update writes every field placebus lets change. Not the slug, not the
// id and not created_at, which is the storage half of placebus.Update's rule.
func (s *Store) Update(ctx context.Context, p placebus.Place) error {
	res, err := s.db.ExecContext(ctx, `UPDATE places SET
    name_en = ?, name_es = ?, parent_id = ?,
    purpose_en = ?, purpose_es = ?, conditions_en = ?, conditions_es = ?,
    photo_point_en = ?, photo_point_es = ?, trail_anchor = ?, sort = ?,
    updated_at = ?
WHERE id = ?`,
		p.Name.EN, p.Name.ES, parentOf(p),
		p.Purpose.EN, p.Purpose.ES, p.Conditions.EN, p.Conditions.ES,
		p.PhotoPoint.EN, p.PhotoPoint.ES, p.TrailAnchor, p.Sort,
		p.UpdatedAt.UnixMilli(), p.ID.String())
	if err != nil {
		return fmt.Errorf("updating the place: %w", err)
	}

	return oneRow(res)
}

// Delete removes a place.
func (s *Store) Delete(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM places WHERE id = ?`, id.String())

	switch {
	case sqldb.IsForeignKeyViolation(err):
		// A listing, or a band, still names it; see listingdb.
		return placebus.ErrInUse
	case err != nil:
		return fmt.Errorf("deleting the place: %w", err)
	}

	return oneRow(res)
}

// ByID is one place, or placebus.ErrNotFound.
func (s *Store) ByID(ctx context.Context, id types.ID) (placebus.Place, error) {
	return s.one(ctx, `WHERE id = ?`, id.String())
}

// BySlug is one place, or placebus.ErrNotFound.
func (s *Store) BySlug(ctx context.Context, slug string) (placebus.Place, error) {
	return s.one(ctx, `WHERE slug = ?`, slug)
}

// All is every place, in no particular order; placebus sorts.
func (s *Store) All(ctx context.Context) ([]placebus.Place, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM places`)
	if err != nil {
		return nil, fmt.Errorf("listing places: %w", err)
	}
	defer rows.Close()

	var all []placebus.Place
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing places: %w", err)
	}

	return all, nil
}

func (s *Store) one(ctx context.Context, where string, arg any) (placebus.Place, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM places `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return placebus.Place{}, placebus.ErrNotFound
	}

	return p, err
}

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (placebus.Place, error) {
	var (
		p                placebus.Place
		id               string
		parent           sql.NullString
		created, updated int64
	)

	err := row.Scan(&id, &p.Slug, &p.Name.EN, &p.Name.ES, &parent,
		&p.Purpose.EN, &p.Purpose.ES, &p.Conditions.EN, &p.Conditions.ES,
		&p.PhotoPoint.EN, &p.PhotoPoint.ES, &p.TrailAnchor, &p.Sort,
		&created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return placebus.Place{}, err
		}

		return placebus.Place{}, fmt.Errorf("reading a place: %w", err)
	}

	// An id that does not parse is a row this binary did not write, and
	// carrying on with it would put a place on a page that no link could
	// ever reach again.
	if p.ID, err = types.ParseID(id); err != nil {
		return placebus.Place{}, fmt.Errorf("the place %q has an unreadable id: %w", p.Slug, err)
	}

	if parent.Valid {
		if p.ParentID, err = types.ParseID(parent.String); err != nil {
			return placebus.Place{}, fmt.Errorf("the place %q has an unreadable parent id: %w", p.Slug, err)
		}
	}

	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()

	return p, nil
}

// parentOf is NULL for a place that stands on its own, so the foreign key
// has nothing to check, rather than an empty string it would fail to find.
func parentOf(p placebus.Place) any {
	if p.TopLevel() {
		return nil
	}

	return p.ParentID.String()
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 0 {
		return placebus.ErrNotFound
	}

	return nil
}
