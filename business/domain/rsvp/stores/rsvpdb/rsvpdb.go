// Package rsvpdb stores who is coming to each stewardship day.
//
// One row per person per day, the shape of subscriberdb's: an email and a
// phone, each nullable and each UNIQUE within the day, with a CHECK for at
// least one. The day is a foreign key that cascades, so a day taken off the
// calendar takes its sign-ups with it -- after the people on them have been
// told, which is the rules' order to keep, not the database's.
//
// It follows workdaydb in main's prepare, since it references workdays.
//
// # Who cancelled
//
// A cancellation by the person's own link moves the name to rsvp_cancels, in
// the same transaction as the delete, for the stewards' once-a-day summary:
// a row that is gone cannot say it was there. Only the name and the two
// times are kept -- not the email or the phone, which nothing needs once the
// person is off the day -- and only for rsvpbus.CancelsKept. A steward's
// Remove and a STOP are not cancellations and are not kept.
package rsvpdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of rsvpbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ rsvpbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"rsvps":        {"id", "workday_id", "name", "email", "phone", "lang", "cancel", "created_at"},
	"rsvp_cancels": {"id", "workday_id", "name", "created_at", "cancelled_at"},
}

// Init creates the table. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS rsvps (
    id          TEXT    PRIMARY KEY,
    workday_id  TEXT    NOT NULL REFERENCES workdays (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    email       TEXT,
    phone       TEXT,
    lang        TEXT    NOT NULL,
    cancel      TEXT    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    UNIQUE (workday_id, email),
    UNIQUE (workday_id, phone),
    CHECK (email IS NOT NULL OR phone IS NOT NULL)
) STRICT;

CREATE INDEX IF NOT EXISTS rsvps_phone ON rsvps (phone);

-- A sign-up its person cancelled, for the stewards' summary; see the package
-- comment. id is the sign-up's own.
CREATE TABLE IF NOT EXISTS rsvp_cancels (
    id            TEXT    PRIMARY KEY,
    workday_id    TEXT    NOT NULL REFERENCES workdays (id) ON DELETE CASCADE,
    name          TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    cancelled_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS rsvp_cancels_at ON rsvp_cancels (cancelled_at);

`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the rsvps table: %w", err)
	}

	return nil
}

// ByContact is every row for day holding either half of c.
func (s *Store) ByContact(ctx context.Context, day types.ID, c types.Contact) ([]rsvpbus.RSVP, error) {
	return s.list(ctx, `WHERE workday_id = ? AND (email = ? OR phone = ?) ORDER BY created_at, id`,
		day.String(), nullable(c.Email.String()), nullable(c.Phone.String()))
}

// Add inserts the row; a half already on the day is a UNIQUE conflict,
// reported as false.
func (s *Store) Add(ctx context.Context, r rsvpbus.RSVP) (bool, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO rsvps (id, workday_id, name, email, phone, lang, cancel, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID.String(), r.DayID.String(), r.Name,
		nullable(r.Contact.Email.String()), nullable(r.Contact.Phone.String()),
		string(r.Lang), r.Cancel, r.CreatedAt.UnixMilli())

	switch {
	case sqldb.IsUniqueViolation(err):
		return false, nil
	case sqldb.IsForeignKeyViolation(err):
		// The day was taken off the calendar between the check and here.
		return false, rsvpbus.ErrNotFound
	case err != nil:
		return false, fmt.Errorf("adding a sign-up: %w", err)
	}

	return true, nil
}

// Extend fills in the empty halves and sets the name, as one statement.
func (s *Store) Extend(ctx context.Context, id types.ID, name string, c types.Contact) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE rsvps SET name = ?, email = COALESCE(email, ?), phone = COALESCE(phone, ?) WHERE id = ?`,
		name, nullable(c.Email.String()), nullable(c.Phone.String()), id.String())

	switch {
	case sqldb.IsUniqueViolation(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("adding to a sign-up: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// Rename sets a row's name.
func (s *Store) Rename(ctx context.Context, id types.ID, name string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE rsvps SET name = ? WHERE id = ?`, name, id.String()); err != nil {
		return fmt.Errorf("renaming a sign-up: %w", err)
	}

	return nil
}

// ByID is one row.
func (s *Store) ByID(ctx context.Context, id types.ID) (rsvpbus.RSVP, error) {
	return s.one(ctx, `WHERE id = ?`, id.String())
}

// ByCancel is the row a cancel link is for.
func (s *Store) ByCancel(ctx context.Context, token string) (rsvpbus.RSVP, error) {
	return s.one(ctx, `WHERE cancel = ?`, token)
}

// ForDay is every row for day, earliest first.
func (s *Store) ForDay(ctx context.Context, day types.ID) ([]rsvpbus.RSVP, error) {
	return s.list(ctx, `WHERE workday_id = ? ORDER BY created_at, id`, day.String())
}

// Counts is how many rows each of days has.
func (s *Store) Counts(ctx context.Context, days []types.ID) (map[types.ID]int, error) {
	args := make([]any, len(days))
	for i, d := range days {
		args[i] = d.String()
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT workday_id, count(*) FROM rsvps
WHERE workday_id IN (?`+strings.Repeat(", ?", len(days)-1)+`)
GROUP BY workday_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("counting sign-ups: %w", err)
	}
	defer rows.Close()

	out := map[types.ID]int{}

	for rows.Next() {
		var (
			id string
			n  int
		)

		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("counting sign-ups: %w", err)
		}

		day, err := types.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("a stored sign-up has a bad day: %w", err)
		}

		out[day] = n
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting sign-ups: %w", err)
	}

	return out, nil
}

// Delete removes a row by ID.
func (s *Store) Delete(ctx context.Context, id types.ID) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rsvps WHERE id = ?`, id.String())
	if err != nil {
		return false, fmt.Errorf("removing a sign-up: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// Cancel removes r's row and keeps its name in rsvp_cancels, as one
// transaction, reporting whether there was a row: of two presses at once,
// one removes it and the other finds nothing to keep.
func (s *Store) Cancel(ctx context.Context, r rsvpbus.RSVP, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("starting a cancellation: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM rsvps WHERE id = ?`, r.ID.String())
	if err != nil {
		return false, fmt.Errorf("cancelling a sign-up: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	if n == 0 {
		return false, nil
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO rsvp_cancels (id, workday_id, name, created_at, cancelled_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING`,
		r.ID.String(), r.DayID.String(), r.Name, r.CreatedAt.UnixMilli(), at.UnixMilli()); err != nil {
		return false, fmt.Errorf("keeping who cancelled: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("cancelling a sign-up: %w", err)
	}

	return true, nil
}

// Joined is every row made in [from, to), earliest first.
func (s *Store) Joined(ctx context.Context, from, to time.Time) ([]rsvpbus.RSVP, error) {
	return s.list(ctx, `WHERE created_at >= ? AND created_at < ? ORDER BY created_at, id`, from.UnixMilli(), to.UnixMilli())
}

// Cancels is every cancellation in [from, to), earliest first.
func (s *Store) Cancels(ctx context.Context, from, to time.Time) ([]rsvpbus.Cancelled, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workday_id, name, created_at, cancelled_at FROM rsvp_cancels
WHERE cancelled_at >= ? AND cancelled_at < ?
ORDER BY cancelled_at, id`, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("reading who cancelled: %w", err)
	}
	defer rows.Close()

	var out []rsvpbus.Cancelled

	for rows.Next() {
		var (
			c                  rsvpbus.Cancelled
			id, day            string
			created, cancelled int64
		)

		if err := rows.Scan(&id, &day, &c.Name, &created, &cancelled); err != nil {
			return nil, fmt.Errorf("reading who cancelled: %w", err)
		}

		if c.ID, err = types.ParseID(id); err != nil {
			return nil, fmt.Errorf("a stored cancellation has a bad id: %w", err)
		}

		if c.DayID, err = types.ParseID(day); err != nil {
			return nil, fmt.Errorf("a stored cancellation has a bad day: %w", err)
		}

		c.CreatedAt = time.UnixMilli(created).UTC()
		c.At = time.UnixMilli(cancelled).UTC()

		out = append(out, c)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading who cancelled: %w", err)
	}

	return out, nil
}

// PruneCancels forgets cancellations made before before.
func (s *Store) PruneCancels(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rsvp_cancels WHERE cancelled_at < ?`, before.UnixMilli()); err != nil {
		return fmt.Errorf("forgetting old cancellations: %w", err)
	}

	return nil
}

// Stopped takes the number off every day, deleting the rows it was all of;
// in that order for the reason subscriberdb.Store.Stopped gives.
func (s *Store) Stopped(ctx context.Context, p types.Phone) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rsvps WHERE phone = ? AND email IS NULL`, p.String()); err != nil {
		return fmt.Errorf("removing a stopped number: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE rsvps SET phone = NULL WHERE phone = ?`, p.String()); err != nil {
		return fmt.Errorf("removing a stopped number: %w", err)
	}

	return nil
}

const columns = `id, workday_id, name, email, phone, lang, cancel, created_at`

func (s *Store) one(ctx context.Context, where string, args ...any) (rsvpbus.RSVP, error) {
	rows, err := s.list(ctx, where, args...)
	if err != nil {
		return rsvpbus.RSVP{}, err
	}

	if len(rows) == 0 {
		return rsvpbus.RSVP{}, rsvpbus.ErrNotFound
	}

	return rows[0], nil
}

func (s *Store) list(ctx context.Context, rest string, args ...any) ([]rsvpbus.RSVP, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM rsvps `+rest, args...)
	if err != nil {
		return nil, fmt.Errorf("reading sign-ups: %w", err)
	}
	defer rows.Close()

	var out []rsvpbus.RSVP

	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading sign-ups: %w", err)
	}

	return out, nil
}

func scan(row interface{ Scan(...any) error }) (rsvpbus.RSVP, error) {
	var (
		r            rsvpbus.RSVP
		id, day      string
		lang         string
		email, phone sql.NullString
		created      int64
	)

	if err := row.Scan(&id, &day, &r.Name, &email, &phone, &lang, &r.Cancel, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, rsvpbus.ErrNotFound
		}

		return r, fmt.Errorf("reading a sign-up: %w", err)
	}

	var err error
	if r.ID, err = types.ParseID(id); err != nil {
		return r, fmt.Errorf("a stored sign-up has a bad id: %w", err)
	}

	if r.DayID, err = types.ParseID(day); err != nil {
		return r, fmt.Errorf("a stored sign-up has a bad day: %w", err)
	}

	if email.Valid {
		if r.Contact.Email, err = types.ParseEmail(email.String); err != nil {
			return r, fmt.Errorf("a stored sign-up has a bad address: %w", err)
		}
	}

	if phone.Valid {
		if r.Contact.Phone, err = types.ParsePhone(phone.String); err != nil {
			return r, fmt.Errorf("a stored sign-up has a bad number: %w", err)
		}
	}

	if r.Lang, err = types.ParseLang(lang); err != nil {
		r.Lang = types.English
	}

	r.CreatedAt = time.UnixMilli(created).UTC()

	return r, nil
}

// nullable is NULL for an empty half; see subscriberdb.
func nullable(s string) any {
	if s == "" {
		return nil
	}

	return s
}
