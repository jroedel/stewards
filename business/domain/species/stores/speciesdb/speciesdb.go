// Package speciesdb stores species, and the sources each ID was checked
// against, in SQLite.
//
// Two tables, because a species has any number of sources and each one is a
// row a later screen may want to ask about ("which IDs rest only on a nursery
// tag?"). A species is always written together with its sources, in one
// transaction, so a save can never leave an ID confirmed with its sources
// half-written.
//
// # One connection, so one query at a time
//
// sqldb.Open holds a single connection. A second query issued while a first
// one's rows are still open waits for that connection forever -- it does not
// fail, it hangs. So All reads every species, closes, and only then reads the
// sources; nothing here queries inside a rows loop.
package speciesdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of speciesbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ speciesbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"species": {
		"id", "slug", "common_en", "common_es", "scientific", "status", "confirmed",
		"flower_en", "flower_es", "swatches", "bloom",
		"height_min", "height_max", "width_min", "width_max", "light", "water",
		"note_en", "note_es", "created_at", "updated_at",
	},
	"species_sources": {"species_id", "position", "label", "url"},
}

// Init creates the tables. Idempotent, and run at every startup.
//
// No CHECK constraints, as in placedb: the rules are speciesbus's, and a CHECK
// removed later stays on every database that already has it.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS species (
    id          TEXT    PRIMARY KEY,

    -- UNIQUE, and the insert is the check: see speciesbus.ErrSlugTaken.
    slug        TEXT    NOT NULL UNIQUE,

    common_en   TEXT    NOT NULL,
    common_es   TEXT    NOT NULL DEFAULT '',
    scientific  TEXT    NOT NULL DEFAULT '',

    -- One of speciesbus.Statuses, or '' for not set.
    status      TEXT    NOT NULL DEFAULT '',
    confirmed   INTEGER NOT NULL DEFAULT 0,

    flower_en   TEXT    NOT NULL DEFAULT '',
    flower_es   TEXT    NOT NULL DEFAULT '',

    -- Space-separated #rrggbb codes. A column rather than a table: at most
    -- four, always read with the species, never asked about on their own.
    swatches    TEXT    NOT NULL DEFAULT '',

    -- A twelve-bit set, January in bit 0: types.Months.
    bloom       INTEGER NOT NULL DEFAULT 0,

    -- Inches; 0 for not recorded.
    height_min  INTEGER NOT NULL DEFAULT 0,
    height_max  INTEGER NOT NULL DEFAULT 0,
    width_min   INTEGER NOT NULL DEFAULT 0,
    width_max   INTEGER NOT NULL DEFAULT 0,

    -- Bit sets: speciesbus.Light and speciesbus.Water.
    light       INTEGER NOT NULL DEFAULT 0,
    water       INTEGER NOT NULL DEFAULT 0,

    note_en     TEXT    NOT NULL DEFAULT '',
    note_es     TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS species_sources (
    species_id  TEXT    NOT NULL REFERENCES species (id) ON DELETE CASCADE,

    -- The order the steward listed them in, from 0.
    position    INTEGER NOT NULL,
    label       TEXT    NOT NULL,
    url         TEXT    NOT NULL DEFAULT '',

    PRIMARY KEY (species_id, position)
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the species tables: %w", err)
	}

	return nil
}

// Create inserts a species and its sources together.
func (s *Store) Create(ctx context.Context, sp speciesbus.Species) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO species (`+columns+`) VALUES (`+marks+`)`, values(sp)...)

		switch {
		case sqldb.IsUniqueViolation(err):
			return speciesbus.ErrSlugTaken
		case err != nil:
			return fmt.Errorf("inserting the species: %w", err)
		}

		return writeSources(ctx, tx, sp)
	})
}

// Update writes every field speciesbus lets change, and replaces the sources.
// Not the slug, the id or created_at.
func (s *Store) Update(ctx context.Context, sp speciesbus.Species) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE species SET
    common_en = ?, common_es = ?, scientific = ?, status = ?, confirmed = ?,
    flower_en = ?, flower_es = ?, swatches = ?, bloom = ?,
    height_min = ?, height_max = ?, width_min = ?, width_max = ?, light = ?, water = ?,
    note_en = ?, note_es = ?, updated_at = ?
WHERE id = ?`,
			sp.Common.EN, sp.Common.ES, sp.Scientific, string(sp.Status), boolOf(sp.Confirmed),
			sp.FlowerColor.EN, sp.FlowerColor.ES, strings.Join(sp.Swatches, " "), int64(sp.Bloom),
			sp.Height.Min, sp.Height.Max, sp.Width.Min, sp.Width.Max, int64(sp.Light), int64(sp.Water),
			sp.Note.EN, sp.Note.ES, sp.UpdatedAt.UnixMilli(), sp.ID.String())
		if err != nil {
			return fmt.Errorf("updating the species: %w", err)
		}

		if err := oneRow(res); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM species_sources WHERE species_id = ?`, sp.ID.String()); err != nil {
			return fmt.Errorf("clearing the species' sources: %w", err)
		}

		return writeSources(ctx, tx, sp)
	})
}

// Delete removes a species; its sources go with it. A species still listed at
// a place, or with photos, is refused by the database, as speciesbus.ErrInUse.
func (s *Store) Delete(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM species WHERE id = ?`, id.String())

	switch {
	case sqldb.IsForeignKeyViolation(err):
		// Still listed at a place, or photographed; see listingdb and
		// photodb.
		return speciesbus.ErrInUse
	case err != nil:
		return fmt.Errorf("deleting the species: %w", err)
	}

	return oneRow(res)
}

// ByID is one species with its sources, or speciesbus.ErrNotFound.
func (s *Store) ByID(ctx context.Context, id types.ID) (speciesbus.Species, error) {
	return s.one(ctx, `WHERE id = ?`, id.String())
}

// BySlug is one species with its sources, or speciesbus.ErrNotFound.
func (s *Store) BySlug(ctx context.Context, slug string) (speciesbus.Species, error) {
	return s.one(ctx, `WHERE slug = ?`, slug)
}

// All is every species with its sources, in no particular order;
// speciesbus sorts.
func (s *Store) All(ctx context.Context) ([]speciesbus.Species, error) {
	all, err := s.list(ctx, ``)
	if err != nil {
		return nil, err
	}

	// Only now, with the species' rows closed: see the package comment.
	sources, err := s.sources(ctx, ``)
	if err != nil {
		return nil, err
	}

	for i := range all {
		all[i].Sources = sources[all[i].ID]
	}

	return all, nil
}

// ------------------------------------------------------------------ helpers

const columns = `id, slug, common_en, common_es, scientific, status, confirmed,
    flower_en, flower_es, swatches, bloom,
    height_min, height_max, width_min, width_max, light, water,
    note_en, note_es, created_at, updated_at`

const marks = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

func values(sp speciesbus.Species) []any {
	return []any{
		sp.ID.String(), sp.Slug, sp.Common.EN, sp.Common.ES, sp.Scientific, string(sp.Status), boolOf(sp.Confirmed),
		sp.FlowerColor.EN, sp.FlowerColor.ES, strings.Join(sp.Swatches, " "), int64(sp.Bloom),
		sp.Height.Min, sp.Height.Max, sp.Width.Min, sp.Width.Max, int64(sp.Light), int64(sp.Water),
		sp.Note.EN, sp.Note.ES, sp.CreatedAt.UnixMilli(), sp.UpdatedAt.UnixMilli(),
	}
}

// write runs fn in a transaction, committing only if it succeeds.
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting to save the species: %w", err)
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("saving the species: %w", err)
	}

	return nil
}

func writeSources(ctx context.Context, tx *sql.Tx, sp speciesbus.Species) error {
	for i, src := range sp.Sources {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO species_sources (species_id, position, label, url) VALUES (?, ?, ?, ?)`,
			sp.ID.String(), i, src.Label, src.URL); err != nil {
			return fmt.Errorf("saving a source: %w", err)
		}
	}

	return nil
}

func (s *Store) one(ctx context.Context, where string, arg any) (speciesbus.Species, error) {
	found, err := s.list(ctx, where, arg)
	if err != nil {
		return speciesbus.Species{}, err
	}

	if len(found) == 0 {
		return speciesbus.Species{}, speciesbus.ErrNotFound
	}

	sp := found[0]

	sources, err := s.sources(ctx, `WHERE species_id = ?`, sp.ID.String())
	if err != nil {
		return speciesbus.Species{}, err
	}

	sp.Sources = sources[sp.ID]

	return sp, nil
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]speciesbus.Species, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM species `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("reading species: %w", err)
	}
	defer rows.Close()

	var out []speciesbus.Species

	for rows.Next() {
		var (
			sp                   speciesbus.Species
			id, status, swatches string
			confirmed, bloom     int64
			light, water         int64
			created, updated     int64
		)

		if err := rows.Scan(&id, &sp.Slug, &sp.Common.EN, &sp.Common.ES, &sp.Scientific, &status, &confirmed,
			&sp.FlowerColor.EN, &sp.FlowerColor.ES, &swatches, &bloom,
			&sp.Height.Min, &sp.Height.Max, &sp.Width.Min, &sp.Width.Max, &light, &water,
			&sp.Note.EN, &sp.Note.ES, &created, &updated); err != nil {
			return nil, fmt.Errorf("reading a species: %w", err)
		}

		if sp.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a stored species has a bad identifier: %w", err)
		}

		sp.Status = speciesbus.Status(status)
		sp.Confirmed = confirmed != 0
		sp.Swatches = strings.Fields(swatches)
		sp.Bloom = types.Months(bloom)
		sp.Light = speciesbus.Light(light)
		sp.Water = speciesbus.Water(water)
		sp.CreatedAt = time.UnixMilli(created).UTC()
		sp.UpdatedAt = time.UnixMilli(updated).UTC()

		out = append(out, sp)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading species: %w", err)
	}

	return out, nil
}

// sources is the sources matching where, by species, in their order.
func (s *Store) sources(ctx context.Context, where string, args ...any) (map[types.ID][]speciesbus.Source, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT species_id, label, url FROM species_sources `+where+` ORDER BY species_id, position`, args...)
	if err != nil {
		return nil, fmt.Errorf("reading sources: %w", err)
	}
	defer rows.Close()

	out := map[types.ID][]speciesbus.Source{}

	for rows.Next() {
		var (
			raw string
			src speciesbus.Source
		)

		if err := rows.Scan(&raw, &src.Label, &src.URL); err != nil {
			return nil, fmt.Errorf("reading a source: %w", err)
		}

		id, err := types.ParseID(raw)
		if err != nil {
			return nil, fmt.Errorf("a stored source names a bad species: %w", err)
		}

		out[id] = append(out[id], src)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading sources: %w", err)
	}

	return out, nil
}

func boolOf(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 0 {
		return speciesbus.ErrNotFound
	}

	return nil
}
