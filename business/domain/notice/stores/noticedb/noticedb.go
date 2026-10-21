// Package noticedb stores the record the caps on messages count.
//
// One row per message a stranger's form caused, kept a day, keyed by the
// address it went to: an email address or a phone number in E.164, in one
// column, because the cap is the same rule for both. It holds the address
// rather than a subscriber's ID so that a cap can be claimed before there is
// any row to point at -- see noticebus.Business.Claim.
//
// notice_sends is the other table: one row per recipient and channel of a
// steward's message to many, claimed before it is sent, so that nobody gets
// the same one twice (noticebus, "Sending to many"). It is kept, unlike the
// claims, because it is also the record a steward reads of what went out.
//
// This replaced subscriber_mails, which counted emails only and which
// subscriberdb now drops; it was a day's worth of counts, so nothing was
// lost by starting this table empty.
package noticedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of noticebus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ noticebus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"notice_claims": {"address", "sent_at"},
	"notice_sends":  {"batch", "recipient", "channel", "status", "at"},
}

// Init creates the table. Idempotent, run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS notice_claims (
    address  TEXT    NOT NULL,
    sent_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS notice_claims_address ON notice_claims (address, sent_at);
CREATE INDEX IF NOT EXISTS notice_claims_sent ON notice_claims (sent_at);

CREATE TABLE IF NOT EXISTS notice_sends (
    batch      TEXT    NOT NULL,
    recipient  TEXT    NOT NULL,
    channel    TEXT    NOT NULL CHECK (channel IN ('email', 'text')),
    status     TEXT    NOT NULL CHECK (status IN ('sending', 'sent', 'failed', 'stopped')),
    at         INTEGER NOT NULL,
    PRIMARY KEY (batch, recipient, channel)
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the notice tables: %w", err)
	}

	return nil
}

// Claim is one INSERT … SELECT … WHERE per address, inside one transaction,
// so two requests at the same moment cannot both pass a count only one of
// them should, and a contact with an email and a phone claims both or
// neither: a cap reached on the phone sends no email either, which is what
// a person reading "you're on the list" and getting half of it would find
// stranger than getting none.
func (s *Store) Claim(ctx context.Context, addresses []string, now, since time.Time, perAddress int, hourSince time.Time, perHour int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("starting a claim: %w", err)
	}
	defer tx.Rollback()

	for _, a := range addresses {
		res, err := tx.ExecContext(ctx, `
INSERT INTO notice_claims (address, sent_at)
SELECT ?, ?
WHERE (SELECT count(*) FROM notice_claims WHERE address = ? AND sent_at > ?) < ?
  AND (SELECT count(*) FROM notice_claims WHERE sent_at > ?) < ?`,
			a, now.UnixMilli(),
			a, since.UnixMilli(), perAddress,
			hourSince.UnixMilli(), perHour)
		if err != nil {
			return false, fmt.Errorf("claiming a message: %w", err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("counting the rows changed: %w", err)
		}

		if n == 0 {
			return false, nil
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("finishing a claim: %w", err)
	}

	return true, nil
}

// Prune deletes claims made before before.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM notice_claims WHERE sent_at < ?`, before.UnixMilli()); err != nil {
		return fmt.Errorf("pruning the message record: %w", err)
	}

	return nil
}

// ClaimSend is the single-use claim on one send, as one statement.
func (s *Store) ClaimSend(ctx context.Context, batch, recipient, channel string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO notice_sends (batch, recipient, channel, status, at) VALUES (?, ?, ?, 'sending', ?)
ON CONFLICT DO NOTHING`, batch, recipient, channel, at.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("claiming a send: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n == 1, nil
}

// MarkSend sets what came of a send.
func (s *Store) MarkSend(ctx context.Context, batch, recipient, channel, status string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE notice_sends SET status = ? WHERE batch = ? AND recipient = ? AND channel = ?`,
		status, batch, recipient, channel); err != nil {
		return fmt.Errorf("recording a send: %w", err)
	}

	return nil
}

// Batches is every batch whose key starts with prefix, by substr rather
// than LIKE, so that nothing in a key is read as a pattern.
func (s *Store) Batches(ctx context.Context, prefix string) ([]noticebus.Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT batch, min(at),
       sum(status = 'sent'), sum(status = 'failed'), sum(status = 'stopped'), sum(status = 'sending')
FROM notice_sends
WHERE substr(batch, 1, length(?)) = ?
GROUP BY batch
ORDER BY min(at), batch`, prefix, prefix)
	if err != nil {
		return nil, fmt.Errorf("reading what was sent: %w", err)
	}
	defer rows.Close()

	var out []noticebus.Batch

	for rows.Next() {
		var (
			b       noticebus.Batch
			started int64
		)

		if err := rows.Scan(&b.Key, &started, &b.Sent, &b.Failed, &b.Stopped, &b.Sending); err != nil {
			return nil, fmt.Errorf("reading what was sent: %w", err)
		}

		b.Started = time.UnixMilli(started).UTC()
		out = append(out, b)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading what was sent: %w", err)
	}

	return out, nil
}
