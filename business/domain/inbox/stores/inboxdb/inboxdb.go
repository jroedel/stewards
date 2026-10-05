// Package inboxdb stores what is known about each photo in the inbox. The
// pictures are kept by a photofs directory of the inbox's own; a row here is
// what makes one appear on the stewards' inbox.
//
// place_id and from_user_id reference their tables with no ON DELETE action,
// as photos do: a place with an inbox photo cannot be deleted until the photo
// is sorted away from it, which placedb already reports as ErrInUse.
//
// taken_at and the position are nullable rather than zero for unknown. A
// photo taken at midnight UTC on 1 January 1970 is not a photo anybody here
// took, but 0, 0 is a real point and a reader should not have to know which
// zero means what.
package inboxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of inboxbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ inboxbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"inbox": {
		"id", "from_user_id", "at", "place_id", "note",
		"taken_at", "lat", "lon", "status", "format", "sha256",
		"large_width", "large_height", "small_width", "small_height",
		"created_at", "updated_at",
	},
}

// Init creates the table. Idempotent, run at every startup, after places and
// stewards, which it references.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS inbox (
    id            TEXT    PRIMARY KEY,
    from_user_id  TEXT    REFERENCES users (id),

    -- inboxbus.At: 'property' or 'nursery'.
    at            TEXT    NOT NULL,

    -- Where on the property, if the steward said. NULL when not said.
    place_id      TEXT    REFERENCES places (id),
    note          TEXT    NOT NULL DEFAULT '',

    -- When the camera says it was taken, Unix milliseconds; NULL when it
    -- did not say the day. And where, if the phone kept its position.
    taken_at      INTEGER,
    lat           REAL,
    lon           REAL,

    -- inboxbus.Status.
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

CREATE INDEX IF NOT EXISTS inbox_status ON inbox (status);
CREATE INDEX IF NOT EXISTS inbox_place ON inbox (place_id);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the inbox table: %w", err)
	}

	return nil
}

const columns = `id, from_user_id, at, place_id, note, taken_at, lat, lon, status, format, sha256,
large_width, large_height, small_width, small_height, created_at, updated_at`

// Create inserts a photo.
func (s *Store) Create(ctx context.Context, it inboxbus.Item) error {
	var taken, lat, lon any
	if !it.TakenAt.IsZero() {
		taken = it.TakenAt.UnixMilli()
	}

	if it.Located {
		lat, lon = it.Where.Lat, it.Where.Lon
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO inbox (`+columns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID.String(), orNull(it.FromID), string(it.At), orNull(it.PlaceID), it.Note,
		taken, lat, lon, string(it.Status), it.Format, it.SHA256,
		it.Large.Width, it.Large.Height, it.Small.Width, it.Small.Height,
		it.CreatedAt.UnixMilli(), it.UpdatedAt.UnixMilli())

	switch {
	case sqldb.IsForeignKeyViolation(err):
		return inboxbus.ErrUnknown
	case sqldb.IsUniqueViolation(err):
		// The id is random, so the constraint that can collide is the
		// content's.
		return inboxbus.ErrDuplicate
	case err != nil:
		return fmt.Errorf("inserting an inbox photo: %w", err)
	}

	return nil
}

// ByID is one photo, or inboxbus.ErrNotFound.
func (s *Store) ByID(ctx context.Context, id types.ID) (inboxbus.Item, error) {
	return s.one(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM inbox WHERE id = ?`, id.String()))
}

// BySHA256 is the photo with this content, or inboxbus.ErrNotFound.
func (s *Store) BySHA256(ctx context.Context, sum string) (inboxbus.Item, error) {
	return s.one(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM inbox WHERE sha256 = ?`, sum))
}

// WithStatus is every photo with a status, oldest first; inboxbus orders them.
func (s *Store) WithStatus(ctx context.Context, status inboxbus.Status) ([]inboxbus.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM inbox WHERE status = ? ORDER BY created_at, id`, string(status))
	if err != nil {
		return nil, fmt.Errorf("listing the inbox: %w", err)
	}
	defer rows.Close()

	var all []inboxbus.Item
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, it)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the inbox: %w", err)
	}

	return all, nil
}

// Count is how many photos have a status.
func (s *Store) Count(ctx context.Context, status inboxbus.Status) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM inbox WHERE status = ?`, string(status)).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting the inbox: %w", err)
	}

	return n, nil
}

func (s *Store) one(row *sql.Row) (inboxbus.Item, error) {
	it, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return inboxbus.Item{}, inboxbus.ErrNotFound
	}

	return it, err
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (inboxbus.Item, error) {
	var (
		it               inboxbus.Item
		id, at, status   string
		from, place      sql.NullString
		taken            sql.NullInt64
		lat, lon         sql.NullFloat64
		created, updated int64
	)

	err := row.Scan(&id, &from, &at, &place, &it.Note, &taken, &lat, &lon, &status, &it.Format, &it.SHA256,
		&it.Large.Width, &it.Large.Height, &it.Small.Width, &it.Small.Height, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inboxbus.Item{}, err
		}

		return inboxbus.Item{}, fmt.Errorf("reading an inbox photo: %w", err)
	}

	it.At, it.Status = inboxbus.At(at), inboxbus.Status(status)

	if it.ID, err = types.ParseID(id); err != nil {
		return inboxbus.Item{}, fmt.Errorf("an inbox photo has an unreadable id %q: %w", id, err)
	}

	for _, ref := range []struct {
		col sql.NullString
		dst *types.ID
	}{{from, &it.FromID}, {place, &it.PlaceID}} {
		if !ref.col.Valid {
			continue
		}

		if *ref.dst, err = types.ParseID(ref.col.String); err != nil {
			return inboxbus.Item{}, fmt.Errorf("inbox photo %s has an unreadable reference: %w", id, err)
		}
	}

	if taken.Valid {
		it.TakenAt = time.UnixMilli(taken.Int64).UTC()
	}

	if lat.Valid && lon.Valid {
		it.Where, it.Located = inboxbus.Position{Lat: lat.Float64, Lon: lon.Float64}, true
	}

	it.CreatedAt = time.UnixMilli(created).UTC()
	it.UpdatedAt = time.UnixMilli(updated).UTC()

	return it, nil
}

// orNull is NULL for an id not given, so the reference has nothing to check.
func orNull(id types.ID) any {
	if id.Zero() {
		return nil
	}

	return id.String()
}
