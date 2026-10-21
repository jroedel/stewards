// Package noticedb stores the record the caps on messages count.
//
// One row per message a stranger's form caused, kept a day, keyed by the
// address it went to: an email address or a phone number in E.164, in one
// column, because the cap is the same rule for both. It holds the address
// rather than a subscriber's ID so that a cap can be claimed before there is
// any row to point at -- see noticebus.Business.Claim.
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
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the notice_claims table: %w", err)
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
