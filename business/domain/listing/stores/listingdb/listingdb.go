// Package listingdb stores which species are listed at which places.
//
// One row per place and species, which is the primary key, so listing a
// species again is the same statement as listing it the first time: an
// INSERT … ON CONFLICT DO UPDATE, keeping when it was first listed.
//
// # The references are the rule
//
// place_id and species_id reference their tables with no ON DELETE action.
// So a listing cannot name a place or a species that is not there, and --
// the half that matters -- neither can be deleted while it is listed: the
// DELETE itself fails, which placedb and speciesdb turn into their ErrInUse.
// A count before the delete would leave a moment for a listing to arrive in
// between; the reference leaves none.
package listingdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of listingbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ listingbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"listings": {"place_id", "species_id", "action", "planned", "note_en", "note_es", "created_at", "updated_at"},
}

// Init creates the table. Idempotent, run at every startup, and after both
// places and species, which it references.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS listings (
    place_id    TEXT    NOT NULL REFERENCES places (id),
    species_id  TEXT    NOT NULL REFERENCES species (id),

    -- One of listingbus.Actions.
    action      TEXT    NOT NULL,
    planned     INTEGER NOT NULL DEFAULT 0,
    note_en     TEXT    NOT NULL DEFAULT '',
    note_es     TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,

    PRIMARY KEY (place_id, species_id)
) STRICT;

-- The species card asks the other way round: where is this one listed?
CREATE INDEX IF NOT EXISTS listings_species ON listings (species_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the listings table: %w", err)
	}

	return nil
}

// Set inserts the listing, or replaces everything but when it was first made.
func (s *Store) Set(ctx context.Context, l listingbus.Listing) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO listings (place_id, species_id, action, planned, note_en, note_es, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (place_id, species_id) DO UPDATE SET
    action = excluded.action, planned = excluded.planned,
    note_en = excluded.note_en, note_es = excluded.note_es,
    updated_at = excluded.updated_at`,
		l.PlaceID.String(), l.SpeciesID.String(), string(l.Action), boolOf(l.Planned),
		l.Note.EN, l.Note.ES, l.CreatedAt.UnixMilli(), l.UpdatedAt.UnixMilli())

	switch {
	case sqldb.IsForeignKeyViolation(err):
		return listingbus.ErrUnknown
	case err != nil:
		return fmt.Errorf("saving the listing: %w", err)
	}

	return nil
}

// Remove deletes a listing.
func (s *Store) Remove(ctx context.Context, placeID, speciesID types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM listings WHERE place_id = ? AND species_id = ?`,
		placeID.String(), speciesID.String())
	if err != nil {
		return fmt.Errorf("removing the listing: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 0 {
		return listingbus.ErrNotFound
	}

	return nil
}

// ForPlace is every listing at a place.
func (s *Store) ForPlace(ctx context.Context, placeID types.ID) ([]listingbus.Listing, error) {
	return s.list(ctx, `WHERE place_id = ?`, placeID.String())
}

// ForSpecies is every listing of a species.
func (s *Store) ForSpecies(ctx context.Context, speciesID types.ID) ([]listingbus.Listing, error) {
	return s.list(ctx, `WHERE species_id = ?`, speciesID.String())
}

// All is every listing.
func (s *Store) All(ctx context.Context) ([]listingbus.Listing, error) {
	return s.list(ctx, ``)
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]listingbus.Listing, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT place_id, species_id, action, planned, note_en, note_es, created_at, updated_at
FROM listings `+where+` ORDER BY created_at, species_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading listings: %w", err)
	}
	defer rows.Close()

	var out []listingbus.Listing

	for rows.Next() {
		var (
			l                listingbus.Listing
			place, species   string
			action           string
			planned          int64
			created, updated int64
		)

		if err := rows.Scan(&place, &species, &action, &planned, &l.Note.EN, &l.Note.ES, &created, &updated); err != nil {
			return nil, fmt.Errorf("reading a listing: %w", err)
		}

		if l.PlaceID, err = types.ParseID(place); err != nil {
			return nil, fmt.Errorf("a stored listing names a bad place: %w", err)
		}

		if l.SpeciesID, err = types.ParseID(species); err != nil {
			return nil, fmt.Errorf("a stored listing names a bad species: %w", err)
		}

		l.Action = listingbus.Action(action)
		l.Planned = planned != 0
		l.CreatedAt = time.UnixMilli(created).UTC()
		l.UpdatedAt = time.UnixMilli(updated).UTC()

		out = append(out, l)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading listings: %w", err)
	}

	return out, nil
}

func boolOf(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
