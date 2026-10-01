// Package workdaydb stores the stewardship days.
//
// One row per day, and nothing references it yet, so it can be initialised
// anywhere in main's order. The CHECK is the one rule the database can hold
// for itself -- a day ends after it starts -- and it is one that will never
// be removed, which matters: SQLite cannot drop a constraint without
// rebuilding the table.
package workdaydb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of workdaybus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ workdaybus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"workdays": {"id", "starts_at", "ends_at", "title_en", "title_es", "details_en", "details_es", "created_at", "updated_at"},
}

// Init creates the table. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS workdays (
    id          TEXT    PRIMARY KEY,
    starts_at   INTEGER NOT NULL,
    ends_at     INTEGER NOT NULL,
    title_en    TEXT    NOT NULL,
    title_es    TEXT    NOT NULL DEFAULT '',
    details_en  TEXT    NOT NULL DEFAULT '',
    details_es  TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,

    CHECK (ends_at > starts_at)
) STRICT;

-- The home page asks for the days not yet over, on every visit.
CREATE INDEX IF NOT EXISTS workdays_ends ON workdays (ends_at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the workdays table: %w", err)
	}

	return nil
}

// Create inserts a day.
func (s *Store) Create(ctx context.Context, d workdaybus.Day) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO workdays (id, starts_at, ends_at, title_en, title_es, details_en, details_es, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID.String(), d.Starts.UnixMilli(), d.Ends.UnixMilli(), d.Title.EN, d.Title.ES,
		d.Details.EN, d.Details.ES, d.CreatedAt.UnixMilli(), d.UpdatedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("inserting the day: %w", err)
	}

	return nil
}

// Update replaces everything but the ID and when it was made.
func (s *Store) Update(ctx context.Context, d workdaybus.Day) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE workdays SET starts_at = ?, ends_at = ?, title_en = ?, title_es = ?,
    details_en = ?, details_es = ?, updated_at = ?
WHERE id = ?`,
		d.Starts.UnixMilli(), d.Ends.UnixMilli(), d.Title.EN, d.Title.ES,
		d.Details.EN, d.Details.ES, d.UpdatedAt.UnixMilli(), d.ID.String())
	if err != nil {
		return fmt.Errorf("updating the day: %w", err)
	}

	return oneRow(res)
}

// Delete removes a day.
func (s *Store) Delete(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workdays WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("removing the day: %w", err)
	}

	return oneRow(res)
}

// ByID is one day.
func (s *Store) ByID(ctx context.Context, id types.ID) (workdaybus.Day, error) {
	days, err := s.list(ctx, `WHERE id = ?`, id.String())
	if err != nil {
		return workdaybus.Day{}, err
	}

	if len(days) == 0 {
		return workdaybus.Day{}, workdaybus.ErrNotFound
	}

	return days[0], nil
}

// EndingAfter is every day that ends after t, soonest first.
func (s *Store) EndingAfter(ctx context.Context, t time.Time) ([]workdaybus.Day, error) {
	return s.list(ctx, `WHERE ends_at > ? ORDER BY starts_at, id`, t.UnixMilli())
}

// EndedBy is the latest days over by t, most recent first.
func (s *Store) EndedBy(ctx context.Context, t time.Time, limit int) ([]workdaybus.Day, error) {
	return s.list(ctx, `WHERE ends_at <= ? ORDER BY starts_at DESC, id LIMIT ?`, t.UnixMilli(), limit)
}

func (s *Store) list(ctx context.Context, rest string, args ...any) ([]workdaybus.Day, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, starts_at, ends_at, title_en, title_es, details_en, details_es, created_at, updated_at
FROM workdays `+rest, args...)
	if err != nil {
		return nil, fmt.Errorf("reading days: %w", err)
	}
	defer rows.Close()

	var out []workdaybus.Day

	for rows.Next() {
		var (
			d                              workdaybus.Day
			id                             string
			starts, ends, created, updated int64
		)

		if err := rows.Scan(&id, &starts, &ends, &d.Title.EN, &d.Title.ES, &d.Details.EN, &d.Details.ES, &created, &updated); err != nil {
			return nil, fmt.Errorf("reading a day: %w", err)
		}

		if d.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a stored day has a bad id: %w", err)
		}

		d.Starts = time.UnixMilli(starts).UTC()
		d.Ends = time.UnixMilli(ends).UTC()
		d.CreatedAt = time.UnixMilli(created).UTC()
		d.UpdatedAt = time.UnixMilli(updated).UTC()

		out = append(out, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading days: %w", err)
	}

	return out, nil
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("counting the rows changed: %w", err)
	case n == 0:
		return workdaybus.ErrNotFound
	}

	return nil
}
