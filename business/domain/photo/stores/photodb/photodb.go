// Package photodb stores what is known about each photo. The pictures
// themselves are photofs's; a row here is what makes one visible.
//
// species_id and place_id reference their tables with no ON DELETE action,
// as listings do: a species or a place with a photo cannot be deleted until
// the photo is, and speciesdb and placedb turn the refusal into ErrInUse.
// Cascading would have been one clause shorter and would delete rows whose
// files nothing would then remove.
//
// species_id may be NULL. Every photo today is of a species, and photobus
// says so; a place's photo point and a volunteer's "What is this?" are photos
// of no species yet, and NOT NULL is a constraint SQLite cannot drop later
// without rebuilding the table (CLAUDE.md).
package photodb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of photobus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ photobus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"photos": {
		"id", "species_id", "place_id", "kind", "taken_year", "taken_month",
		"source", "credit", "source_url", "license", "checked", "format",
		"large_width", "large_height", "small_width", "small_height",
		"created_at", "updated_at", "sha256",
	},
}

// Init creates the table. Idempotent, run at every startup, after places and
// species.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS photos (
    id            TEXT    PRIMARY KEY,
    species_id    TEXT    REFERENCES species (id),

    -- Where it was taken, for one of ours. NULL when not said.
    place_id      TEXT    REFERENCES places (id),

    -- One of photobus.Kinds.
    kind          TEXT    NOT NULL DEFAULT '',

    -- The month it was taken; 0 for not known. Not a timestamp, so not
    -- milliseconds: "April 2027" is the whole of what anyone knows.
    taken_year    INTEGER NOT NULL DEFAULT 0,
    taken_month   INTEGER NOT NULL DEFAULT 0,

    source        TEXT    NOT NULL,
    credit        TEXT    NOT NULL DEFAULT '',
    source_url    TEXT    NOT NULL DEFAULT '',
    license       TEXT    NOT NULL DEFAULT '',
    checked       INTEGER NOT NULL DEFAULT 0,

    -- The original's format, which names its file.
    format        TEXT    NOT NULL,
    large_width   INTEGER NOT NULL,
    large_height  INTEGER NOT NULL,
    small_width   INTEGER NOT NULL,
    small_height  INTEGER NOT NULL,

    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS photos_species ON photos (species_id);
CREATE INDEX IF NOT EXISTS photos_place ON photos (place_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the photos table: %w", err)
	}

	// Later columns. Each arrives as an ALTER beside the CREATE above, so a
	// fresh database and one from before the column end the same; and
	// nothing that mentions one may sit in the CREATE block, because on a
	// database from before it the CREATE is skipped and the column is not
	// there yet (CLAUDE.md, and mass-intentions' four failed deploys).

	// sha256 is the original's digest, hex: the same photo sent twice --
	// a batch re-run, a double tap on Upload -- is recognised rather than
	// kept twice. '' for a photo added before it, which no index entry
	// covers.
	if err := sqldb.AddColumn(ctx, db, "photos", "sha256", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	// After the column, in its own statement, for the reason above. UNIQUE,
	// so that two uploads of one photo at the same moment cannot both be
	// kept: the second insert is refused, and a claim is one statement.
	const index = `CREATE UNIQUE INDEX IF NOT EXISTS photos_species_sha256 ON photos (species_id, sha256) WHERE sha256 != ''`
	if _, err := db.ExecContext(ctx, index); err != nil {
		return fmt.Errorf("indexing photos by content: %w", err)
	}

	return nil
}

const columns = `id, species_id, place_id, kind, taken_year, taken_month,
source, credit, source_url, license, checked, format,
large_width, large_height, small_width, small_height, created_at, updated_at, sha256`

// Create inserts a photo.
func (s *Store) Create(ctx context.Context, p photobus.Photo) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO photos (`+columns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID.String(), orNull(p.SpeciesID), orNull(p.PlaceID), string(p.Kind), p.TakenYear, p.TakenMonth,
		string(p.Source), p.Credit, p.SourceURL, p.License, p.Checked, p.Format,
		p.Large.Width, p.Large.Height, p.Small.Width, p.Small.Height,
		p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli(), p.SHA256)

	switch {
	case sqldb.IsForeignKeyViolation(err):
		return photobus.ErrUnknown
	case sqldb.IsUniqueViolation(err):
		// The id is random, so the constraint that can collide is the
		// content index.
		return photobus.ErrDuplicate
	case err != nil:
		return fmt.Errorf("inserting the photo: %w", err)
	}

	return nil
}

// Update writes what a steward may change. Not the pictures' sizes or
// format, which belong to the files, and not the species: a photo of the
// wrong plant is removed, not moved.
func (s *Store) Update(ctx context.Context, p photobus.Photo) error {
	res, err := s.db.ExecContext(ctx, `UPDATE photos SET
    place_id = ?, kind = ?, taken_year = ?, taken_month = ?,
    source = ?, credit = ?, source_url = ?, license = ?, checked = ?,
    updated_at = ?
WHERE id = ?`,
		orNull(p.PlaceID), string(p.Kind), p.TakenYear, p.TakenMonth,
		string(p.Source), p.Credit, p.SourceURL, p.License, p.Checked,
		p.UpdatedAt.UnixMilli(), p.ID.String())

	switch {
	case sqldb.IsForeignKeyViolation(err):
		return photobus.ErrUnknown
	case err != nil:
		return fmt.Errorf("updating the photo: %w", err)
	}

	return oneRow(res)
}

// Delete removes a photo's row.
func (s *Store) Delete(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM photos WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("deleting the photo: %w", err)
	}

	return oneRow(res)
}

// ByID is one photo, or photobus.ErrNotFound.
func (s *Store) ByID(ctx context.Context, id types.ID) (photobus.Photo, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM photos WHERE id = ?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return photobus.Photo{}, photobus.ErrNotFound
	}

	return p, err
}

// BySHA256 is the photo of a species with this content, or
// photobus.ErrNotFound.
func (s *Store) BySHA256(ctx context.Context, speciesID types.ID, sum string) (photobus.Photo, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM photos WHERE species_id = ? AND sha256 = ? AND sha256 != ''`, speciesID.String(), sum))
	if errors.Is(err, sql.ErrNoRows) {
		return photobus.Photo{}, photobus.ErrNotFound
	}

	return p, err
}

// ForSpecies is every photo of a species, oldest first; photobus orders them.
func (s *Store) ForSpecies(ctx context.Context, speciesID types.ID) ([]photobus.Photo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM photos WHERE species_id = ? ORDER BY created_at, id`, speciesID.String())
	if err != nil {
		return nil, fmt.Errorf("listing a species' photos: %w", err)
	}
	defer rows.Close()

	var all []photobus.Photo
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, p)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing a species' photos: %w", err)
	}

	return all, nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (photobus.Photo, error) {
	var (
		p                photobus.Photo
		id               string
		species, place   sql.NullString
		kind, source     string
		created, updated int64
	)

	err := row.Scan(&id, &species, &place, &kind, &p.TakenYear, &p.TakenMonth,
		&source, &p.Credit, &p.SourceURL, &p.License, &p.Checked, &p.Format,
		&p.Large.Width, &p.Large.Height, &p.Small.Width, &p.Small.Height,
		&created, &updated, &p.SHA256)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return photobus.Photo{}, err
		}

		return photobus.Photo{}, fmt.Errorf("reading a photo: %w", err)
	}

	p.Kind, p.Source = photobus.Kind(kind), photobus.Source(source)

	if p.ID, err = types.ParseID(id); err != nil {
		return photobus.Photo{}, fmt.Errorf("a photo has an unreadable id %q: %w", id, err)
	}

	for _, ref := range []struct {
		col sql.NullString
		dst *types.ID
	}{{species, &p.SpeciesID}, {place, &p.PlaceID}} {
		if !ref.col.Valid {
			continue
		}

		if *ref.dst, err = types.ParseID(ref.col.String); err != nil {
			return photobus.Photo{}, fmt.Errorf("photo %s has an unreadable reference: %w", id, err)
		}
	}

	p.CreatedAt = time.UnixMilli(created).UTC()
	p.UpdatedAt = time.UnixMilli(updated).UTC()

	return p, nil
}

// orNull is NULL for an id not given, so the reference has nothing to check.
func orNull(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 0 {
		return photobus.ErrNotFound
	}

	return nil
}
