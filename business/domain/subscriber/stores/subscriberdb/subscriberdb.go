// Package subscriberdb stores the people who want to hear about stewardship
// days, and a record of the welcome mail sent to them.
//
// # Two tables
//
// subscribers is one row per address, unique. subscriber_mails is one row per
// welcome sent, kept a day, and is what the caps count. It holds the address
// rather than a subscriber's ID so that the cap can be claimed before any
// subscriber row exists; see subscriberbus.Business.Request for why that
// order matters.
//
// # What is left of the double opt-in
//
// The first version of the list asked people to confirm from an email before
// they were on it, and kept a pending row with the confirmation's hash until
// they did. That was dropped (subscriberbus says why), and three things
// remain of it here, each on purpose:
//
//   - confirm_hash and confirm_expires_at stay in the CREATE, unwritten.
//     Every database made by that version has them and SQLite cannot drop a
//     column cheaply, so a fresh database gets them too, rather than two
//     shapes of the same table in the wild.
//   - confirmed_at is when the address went on the list; NULL is a pending
//     row from that version, and only such a row can be NULL. A pending
//     address that signs up again goes on the list then (Add), and one that
//     never does is deleted by Prune once its old link has expired.
//   - All reads only rows with confirmed_at set, so a pending row is never
//     on the list without its owner having asked a second time.
//
// # The unsubscribe token is kept as it is
//
// Every email the stewards send must carry it, long after the subscriber
// signed up, so it has to be readable when the mail is written; and all it
// can do is take one address off the list, which is what its holder may do
// anyway. Hashing it would mean a new token in every email and every older
// link dead, which is the wrong way round for a link whose whole job is to
// work whenever it is clicked.
package subscriberdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"subscribers":      {"id", "email", "lang", "unsubscribe", "confirmed_at", "created_at"},
	"subscriber_mails": {"email", "sent_at"},
}

// Init creates the tables. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS subscribers (
    id                  TEXT    PRIMARY KEY,
    email               TEXT    NOT NULL UNIQUE,
    lang                TEXT    NOT NULL,
    unsubscribe         TEXT    NOT NULL UNIQUE,

    -- Left from the double opt-in, and no longer written; see above.
    confirm_hash        BLOB,
    confirm_expires_at  INTEGER,

    -- When the address went on the list. NULL only on a pending row left
    -- from the double opt-in.
    confirmed_at        INTEGER,
    created_at          INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS subscriber_mails (
    email    TEXT    NOT NULL,
    sent_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS subscriber_mails_email ON subscriber_mails (email, sent_at);
CREATE INDEX IF NOT EXISTS subscriber_mails_sent ON subscriber_mails (sent_at);
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the subscriber tables: %w", err)
	}

	return nil
}

// ByEmail is the row for an address on the list. A pending row left from
// the double opt-in is not found, so that signing up again reaches Add,
// which puts it on the list.
func (s *Store) ByEmail(ctx context.Context, email types.Email) (subscriberbus.Subscriber, error) {
	return s.one(ctx, `WHERE email = ? AND confirmed_at IS NOT NULL`, email.String())
}

// ClaimMail is one INSERT … SELECT … WHERE, so two requests at the same
// moment cannot both pass a count that only one of them should.
func (s *Store) ClaimMail(ctx context.Context, email types.Email, now, addressSince time.Time, perAddress int, hourSince time.Time, perHour int) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO subscriber_mails (email, sent_at)
SELECT ?, ?
WHERE (SELECT count(*) FROM subscriber_mails WHERE email = ? AND sent_at > ?) < ?
  AND (SELECT count(*) FROM subscriber_mails WHERE sent_at > ?) < ?`,
		email.String(), now.UnixMilli(),
		email.String(), addressSince.UnixMilli(), perAddress,
		hourSince.UnixMilli(), perHour)
	if err != nil {
		return false, fmt.Errorf("claiming a confirmation mail: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// Add inserts the row, already on the list. On an existing address it
// changes nothing -- unless the row is pending from the double opt-in, which
// it puts on the list then, keeping its ID and unsubscribe token: they asked
// twice. The WHERE on the upsert is what leaves a row on the list alone, in
// the same statement.
func (s *Store) Add(ctx context.Context, sub subscriberbus.Subscriber) (subscriberbus.Subscriber, bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO subscribers (id, email, lang, unsubscribe, confirmed_at, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (email) DO UPDATE SET
    confirmed_at = excluded.confirmed_at,
    confirm_hash = NULL, confirm_expires_at = NULL,
    lang = excluded.lang
WHERE confirmed_at IS NULL`,
		sub.ID.String(), sub.Email.String(), string(sub.Lang), sub.Unsubscribe,
		sub.CreatedAt.UnixMilli(), sub.CreatedAt.UnixMilli())
	if err != nil {
		return subscriberbus.Subscriber{}, false, fmt.Errorf("adding the address: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return subscriberbus.Subscriber{}, false, fmt.Errorf("counting the rows changed: %w", err)
	}

	got, err := s.ByEmail(ctx, sub.Email)
	if err != nil {
		return subscriberbus.Subscriber{}, false, err
	}

	return got, n == 1, nil
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

// All is every row on the list, earliest first; a pending row left from the
// double opt-in is not on it.
func (s *Store) All(ctx context.Context) ([]subscriberbus.Subscriber, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM subscribers WHERE confirmed_at IS NOT NULL ORDER BY confirmed_at, id`)
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

// Prune deletes old mail records, and pending rows from the double opt-in
// whose link has expired.
func (s *Store) Prune(ctx context.Context, now, mailsBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM subscribers WHERE confirmed_at IS NULL AND confirm_expires_at <= ?`, now.UnixMilli()); err != nil {
		return fmt.Errorf("pruning expired sign-ups: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM subscriber_mails WHERE sent_at < ?`, mailsBefore.UnixMilli()); err != nil {
		return fmt.Errorf("pruning the mail record: %w", err)
	}

	return nil
}

const columns = `id, email, lang, unsubscribe, created_at`

func (s *Store) one(ctx context.Context, where string, args ...any) (subscriberbus.Subscriber, error) {
	sub, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM subscribers `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return subscriberbus.Subscriber{}, subscriberbus.ErrNotFound
	}

	return sub, err
}

func scan(row interface{ Scan(...any) error }) (subscriberbus.Subscriber, error) {
	var (
		sub             subscriberbus.Subscriber
		id, email, lang string
		created         int64
	)

	if err := row.Scan(&id, &email, &lang, &sub.Unsubscribe, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sub, err
		}

		return sub, fmt.Errorf("reading a subscriber: %w", err)
	}

	var err error
	if sub.ID, err = types.ParseID(id); err != nil {
		return sub, fmt.Errorf("a stored subscriber has a bad id: %w", err)
	}

	if sub.Email, err = types.ParseEmail(email); err != nil {
		return sub, fmt.Errorf("a stored subscriber has a bad address: %w", err)
	}

	if sub.Lang, err = types.ParseLang(lang); err != nil {
		sub.Lang = types.English
	}

	sub.CreatedAt = time.UnixMilli(created).UTC()

	return sub, nil
}
