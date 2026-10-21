// Package subscriberdb stores the people who want to hear about stewardship
// days: an email address, a phone number, or both.
//
// # One row per person, either half unique
//
// A person who gave both is one row, so that one link takes them off the
// list entirely and a steward sees them once. Either half is UNIQUE on its
// own, so an address or a number can be on the list only once however it was
// paired; the CHECK keeps a row from holding neither.
//
// # The rebuild of 2026-10-21
//
// Until then the list was email only, and email was NOT NULL. SQLite has no
// ALTER that drops a NOT NULL, so Init rebuilds the table once -- a new table
// in the new shape, the rows copied, the old one dropped and the new one
// renamed, in one transaction -- when it finds a subscribers table with no
// phone column. That is the sqlite_master lookup CLAUDE.md asks for: on every
// startup after the first it finds the phone column and does nothing.
//
// The rebuild also lets go of what was left of the double opt-in the first
// version had (subscriberbus says why it was dropped): confirm_hash,
// confirm_expires_at and confirmed_at go, and so does any pending row, which
// was never on the list and whose link expired long ago. The welcome-mail
// record, subscriber_mails, is dropped too: the caps live in noticedb now,
// counting texts and emails alike, and it only ever held a day.
//
// A binary from before the rebuild, rolled back onto a rebuilt database,
// finds confirmed_at missing and reports unhealthy, which is what /healthz is
// for; going back past this release means restoring the backup the deploy
// took.
//
// # The unsubscribe token is kept as it is
//
// Every message the stewards send must carry it, long after the subscriber
// signed up, so it has to be readable when the message is written; and all it
// can do is take one person off the list, which is what its holder may do
// anyway. Hashing it would mean a new token in every message and every older
// link dead, which is the wrong way round for a link whose whole job is to
// work whenever it is clicked.
package subscriberdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of subscriberbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ subscriberbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"subscribers": {"id", "email", "phone", "lang", "unsubscribe", "created_at"},
}

// table is the shape of subscribers, written once for the CREATE and once
// for the rebuild's new table, so that the two cannot drift.
const table = ` (
    id           TEXT    PRIMARY KEY,
    email        TEXT    UNIQUE,
    phone        TEXT    UNIQUE,
    lang         TEXT    NOT NULL,
    unsubscribe  TEXT    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    CHECK (email IS NOT NULL OR phone IS NOT NULL)
) STRICT`

// Init creates the table, or rebuilds the email-only one. Idempotent, run at
// every startup.
func Init(ctx context.Context, db *sql.DB) error {
	if err := rebuild(ctx, db); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS subscribers`+table); err != nil {
		return fmt.Errorf("creating the subscribers table: %w", err)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS subscriber_mails`); err != nil {
		return fmt.Errorf("dropping the old welcome-mail record: %w", err)
	}

	return nil
}

// rebuild brings an email-only subscribers table to the shape above. See the
// package comment.
func rebuild(ctx context.Context, db *sql.DB) error {
	var ddl string

	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'subscribers'`).Scan(&ddl)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // a fresh database: the CREATE makes it
	case err != nil:
		return fmt.Errorf("reading the subscribers table's shape: %w", err)
	case strings.Contains(ddl, "phone"):
		return nil // rebuilt already
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting the subscribers rebuild: %w", err)
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`CREATE TABLE subscribers_rebuilt` + table,
		`INSERT INTO subscribers_rebuilt (id, email, phone, lang, unsubscribe, created_at)
		 SELECT id, email, NULL, lang, unsubscribe, created_at FROM subscribers WHERE confirmed_at IS NOT NULL`,
		`DROP TABLE subscribers`,
		`ALTER TABLE subscribers_rebuilt RENAME TO subscribers`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuilding the subscribers table: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finishing the subscribers rebuild: %w", err)
	}

	return nil
}

// ByContact is every row holding either half of c: none, one, or -- when the
// address is one person's and the number another's -- two.
func (s *Store) ByContact(ctx context.Context, c types.Contact) ([]subscriberbus.Subscriber, error) {
	return s.list(ctx, `WHERE email = ? OR phone = ? ORDER BY created_at, id`, nullable(c.Email.String()), nullable(c.Phone.String()))
}

// Add inserts the row. A half already on the list, from another request at
// the same moment, is a UNIQUE conflict, reported as false.
func (s *Store) Add(ctx context.Context, sub subscriberbus.Subscriber) (bool, error) {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO subscribers (id, email, phone, lang, unsubscribe, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		sub.ID.String(), nullable(sub.Contact.Email.String()), nullable(sub.Contact.Phone.String()),
		string(sub.Lang), sub.Unsubscribe, sub.CreatedAt.UnixMilli())

	switch {
	case sqldb.IsUniqueViolation(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("adding to the list: %w", err)
	}

	return true, nil
}

// Extend fills in the halves of c that the row has empty, as one statement:
// COALESCE keeps a half the row already has. A half taken by another row in
// the meantime is a UNIQUE conflict, reported as false.
func (s *Store) Extend(ctx context.Context, id types.ID, c types.Contact) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE subscribers SET email = COALESCE(email, ?), phone = COALESCE(phone, ?) WHERE id = ?`,
		nullable(c.Email.String()), nullable(c.Phone.String()), id.String())

	switch {
	case sqldb.IsUniqueViolation(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("adding to a subscriber: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// ByID is one row.
func (s *Store) ByID(ctx context.Context, id types.ID) (subscriberbus.Subscriber, error) {
	rows, err := s.list(ctx, `WHERE id = ?`, id.String())
	if err != nil {
		return subscriberbus.Subscriber{}, err
	}

	if len(rows) == 0 {
		return subscriberbus.Subscriber{}, subscriberbus.ErrNotFound
	}

	return rows[0], nil
}

// Unsubscribe deletes the row with the token.
func (s *Store) Unsubscribe(ctx context.Context, token string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subscribers WHERE unsubscribe = ?`, token)
	if err != nil {
		return false, fmt.Errorf("unsubscribing: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// Remove deletes a row by ID.
func (s *Store) Remove(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subscribers WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("removing the subscriber: %w", err)
	}

	n, err := res.RowsAffected()

	switch {
	case err != nil:
		return fmt.Errorf("counting the rows changed: %w", err)
	case n == 0:
		return subscriberbus.ErrNotFound
	}

	return nil
}

// Stopped takes the number off its row, and the row off the list when the
// number was all it had: a row with no email and no phone is nobody the list
// can reach. Two statements, and in this order, because the CHECK refuses a
// row with neither half -- so the phone-only row goes first, and the UPDATE
// then finds only rows that keep their email.
func (s *Store) Stopped(ctx context.Context, p types.Phone) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM subscribers WHERE phone = ? AND email IS NULL`, p.String()); err != nil {
		return fmt.Errorf("removing a stopped number: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE subscribers SET phone = NULL WHERE phone = ?`, p.String()); err != nil {
		return fmt.Errorf("removing a stopped number: %w", err)
	}

	return nil
}

// All is every row on the list, earliest first.
func (s *Store) All(ctx context.Context) ([]subscriberbus.Subscriber, error) {
	return s.list(ctx, `ORDER BY created_at, id`)
}

const columns = `id, email, phone, lang, unsubscribe, created_at`

func (s *Store) list(ctx context.Context, rest string, args ...any) ([]subscriberbus.Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM subscribers `+rest, args...)
	if err != nil {
		return nil, fmt.Errorf("reading the list: %w", err)
	}
	defer rows.Close()

	var out []subscriberbus.Subscriber

	for rows.Next() {
		sub, err := scan(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, sub)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the list: %w", err)
	}

	return out, nil
}

func scan(row interface{ Scan(...any) error }) (subscriberbus.Subscriber, error) {
	var (
		sub          subscriberbus.Subscriber
		id, lang     string
		email, phone sql.NullString
		created      int64
	)

	if err := row.Scan(&id, &email, &phone, &lang, &sub.Unsubscribe, &created); err != nil {
		return sub, fmt.Errorf("reading a subscriber: %w", err)
	}

	var err error
	if sub.ID, err = types.ParseID(id); err != nil {
		return sub, fmt.Errorf("a stored subscriber has a bad id: %w", err)
	}

	if email.Valid {
		if sub.Contact.Email, err = types.ParseEmail(email.String); err != nil {
			return sub, fmt.Errorf("a stored subscriber has a bad address: %w", err)
		}
	}

	if phone.Valid {
		if sub.Contact.Phone, err = types.ParsePhone(phone.String); err != nil {
			return sub, fmt.Errorf("a stored subscriber has a bad number: %w", err)
		}
	}

	if sub.Lang, err = types.ParseLang(lang); err != nil {
		sub.Lang = types.English
	}

	sub.CreatedAt = time.UnixMilli(created).UTC()

	return sub, nil
}

// nullable is NULL for an empty half, so that UNIQUE -- which never counts
// two NULLs as equal -- lets any number of rows lack one.
func nullable(s string) any {
	if s == "" {
		return nil
	}

	return s
}
