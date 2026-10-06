// Package inboxdb stores what is known about each photo in the inbox. The
// pictures are kept by a photofs directory of the inbox's own; a row here is
// what makes one appear on the stewards' inbox.
//
// place_id and from_user_id reference their tables with no ON DELETE action,
// as photos do: a place with an inbox photo cannot be deleted until the photo
// is sorted away from it, which placedb already reports as ErrInUse.
//
// What a sorted photo became -- species_id, photo_id -- and who sorted it are
// plain columns, without references. They are a record, not a constraint: a
// steward who later removes a wrong photo from a plant, or a plant that was
// added twice, must not be refused because the inbox remembers it.
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
	"github.com/jroedel/stewards/business/domain/photo/photobus"
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
		"outcome", "species_id", "photo_id", "sorted_by", "sorted_at",
		"nursery_line_id", "pruned_at", "kind", "site",
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

	// Later columns, for sorting. Each arrives as an ALTER beside the CREATE
	// above, so a fresh database and one from before them end the same; and
	// nothing that mentions one may sit in the CREATE block (CLAUDE.md).
	for _, c := range []struct{ name, decl string }{
		// inboxbus.Outcome; '' until it is sorted.
		{"outcome", "TEXT NOT NULL DEFAULT ''"},
		{"species_id", "TEXT"},
		{"photo_id", "TEXT"},
		{"sorted_by", "TEXT"},
		{"sorted_at", "INTEGER"},

		// For nursery stock: the line it became, and when its pictures
		// were removed, which they are some months later.
		{"nursery_line_id", "TEXT"},
		{"pruned_at", "INTEGER"},

		// photobus.Kind it was sorted as, for a plant's photo or a
		// planting; '' otherwise, and for one sorted before it was kept.
		{"kind", "TEXT NOT NULL DEFAULT ''"},

		// Where off the property a batch was taken, if the steward said:
		// a nursery's name, or a park's. '' on the property.
		{"site", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := sqldb.AddColumn(ctx, db, "inbox", c.name, c.decl); err != nil {
			return err
		}
	}

	return nil
}

const columns = `id, from_user_id, at, place_id, note, taken_at, lat, lon, status, format, sha256,
large_width, large_height, small_width, small_height, created_at, updated_at,
outcome, species_id, photo_id, sorted_by, sorted_at, nursery_line_id, pruned_at, kind, site`

// open is the statuses a photo can still be sorted from, as SQL.
const open = `status IN ('` + string(inboxbus.New) + `', '` + string(inboxbus.Unsure) + `')`

// Create inserts a photo.
func (s *Store) Create(ctx context.Context, it inboxbus.Item) error {
	var taken, lat, lon any
	if !it.TakenAt.IsZero() {
		taken = it.TakenAt.UnixMilli()
	}

	if it.Located {
		lat, lon = it.Where.Lat, it.Where.Lon
	}

	var sorted, pruned any
	if !it.SortedAt.IsZero() {
		sorted = it.SortedAt.UnixMilli()
	}

	if !it.PrunedAt.IsZero() {
		pruned = it.PrunedAt.UnixMilli()
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO inbox (`+columns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID.String(), orNull(it.FromID), string(it.At), orNull(it.PlaceID), it.Note,
		taken, lat, lon, string(it.Status), it.Format, it.SHA256,
		it.Large.Width, it.Large.Height, it.Small.Width, it.Small.Height,
		it.CreatedAt.UnixMilli(), it.UpdatedAt.UnixMilli(),
		string(it.Outcome), orNull(it.SpeciesID), orNull(it.PhotoID), orNull(it.SortedBy), sorted,
		orNull(it.LineID), pruned, string(it.Kind), it.Site)

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

// Claim marks a photo sorted or discarded, only if it is still new or unsure:
// one statement, so of two at once exactly one changes a row.
func (s *Store) Claim(ctx context.Context, id types.ID, c inboxbus.Claim) error {
	res, err := s.db.ExecContext(ctx, `UPDATE inbox SET
    status = ?, outcome = ?, species_id = ?, kind = ?, sorted_by = ?, sorted_at = ?, updated_at = ?
WHERE id = ? AND `+open,
		string(c.Status), string(c.Outcome), orNull(c.SpeciesID), string(c.Kind), orNull(c.By), c.At.UnixMilli(), c.At.UnixMilli(),
		id.String())
	if err != nil {
		return fmt.Errorf("claiming an inbox photo: %w", err)
	}

	return s.changed(ctx, res, id)
}

// Unclaim puts a claimed photo back.
func (s *Store) Unclaim(ctx context.Context, id types.ID, was inboxbus.Status, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE inbox SET
    status = ?, outcome = '', species_id = NULL, kind = '', photo_id = NULL, sorted_by = NULL, sorted_at = NULL, updated_at = ?
WHERE id = ?`, string(was), at.UnixMilli(), id.String())
	if err != nil {
		return fmt.Errorf("putting an inbox photo back: %w", err)
	}

	return nil
}

// SetPhoto records the photo a sorted one became.
func (s *Store) SetPhoto(ctx context.Context, id, photoID types.ID, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE inbox SET photo_id = ?, updated_at = ? WHERE id = ?`,
		orNull(photoID), at.UnixMilli(), id.String()); err != nil {
		return fmt.Errorf("recording what an inbox photo became: %w", err)
	}

	return nil
}

// SetLine records the line of nursery stock a sorted one became.
func (s *Store) SetLine(ctx context.Context, id, lineID types.ID, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE inbox SET nursery_line_id = ?, updated_at = ? WHERE id = ?`,
		orNull(lineID), at.UnixMilli(), id.String()); err != nil {
		return fmt.Errorf("recording the nursery stock an inbox photo became: %w", err)
	}

	return nil
}

// StockTakenBefore is every nursery stock photo taken before the time, or
// sent before it when the camera did not say, whose pictures are still kept.
func (s *Store) StockTakenBefore(ctx context.Context, before time.Time) ([]inboxbus.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM inbox
WHERE status = ? AND outcome = ? AND pruned_at IS NULL AND coalesce(taken_at, created_at) < ?
ORDER BY created_at, id`, string(inboxbus.Sorted), string(inboxbus.AsStock), before.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("listing old nursery stock photos: %w", err)
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

	return all, rows.Err()
}

// SetPruned records that a photo's pictures are gone.
func (s *Store) SetPruned(ctx context.Context, id types.ID, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE inbox SET pruned_at = ?, updated_at = ? WHERE id = ?`,
		at.UnixMilli(), at.UnixMilli(), id.String()); err != nil {
		return fmt.Errorf("recording an inbox photo's pictures as removed: %w", err)
	}

	return nil
}

// SetUnsure sets a photo aside with its note, only if it is still new or
// unsure.
func (s *Store) SetUnsure(ctx context.Context, id types.ID, note string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE inbox SET status = ?, note = ?, updated_at = ? WHERE id = ? AND `+open,
		string(inboxbus.Unsure), note, at.UnixMilli(), id.String())
	if err != nil {
		return fmt.Errorf("setting an inbox photo aside: %w", err)
	}

	return s.changed(ctx, res, id)
}

// changed is nil when the update touched the row, and otherwise says why it
// did not: there is no such photo, or it is no longer open.
func (s *Store) changed(ctx context.Context, res sql.Result, id types.ID) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 1 {
		return nil
	}

	if _, err := s.ByID(ctx, id); err != nil {
		return err
	}

	return inboxbus.ErrTaken
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
		taken, sorted    sql.NullInt64
		pruned           sql.NullInt64
		line             sql.NullString
		lat, lon         sql.NullFloat64
		created, updated int64
		outcome, kind    string
		species, photo   sql.NullString
		by               sql.NullString
	)

	err := row.Scan(&id, &from, &at, &place, &it.Note, &taken, &lat, &lon, &status, &it.Format, &it.SHA256,
		&it.Large.Width, &it.Large.Height, &it.Small.Width, &it.Small.Height, &created, &updated,
		&outcome, &species, &photo, &by, &sorted, &line, &pruned, &kind, &it.Site)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inboxbus.Item{}, err
		}

		return inboxbus.Item{}, fmt.Errorf("reading an inbox photo: %w", err)
	}

	it.At, it.Status, it.Outcome, it.Kind = inboxbus.At(at), inboxbus.Status(status), inboxbus.Outcome(outcome), photobus.Kind(kind)

	if it.ID, err = types.ParseID(id); err != nil {
		return inboxbus.Item{}, fmt.Errorf("an inbox photo has an unreadable id %q: %w", id, err)
	}

	for _, ref := range []struct {
		col sql.NullString
		dst *types.ID
	}{{from, &it.FromID}, {place, &it.PlaceID}, {species, &it.SpeciesID}, {photo, &it.PhotoID}, {by, &it.SortedBy}, {line, &it.LineID}} {
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

	if sorted.Valid {
		it.SortedAt = time.UnixMilli(sorted.Int64).UTC()
	}

	if pruned.Valid {
		it.PrunedAt = time.UnixMilli(pruned.Int64).UTC()
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
