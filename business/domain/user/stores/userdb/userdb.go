// Package userdb stores stewards' accounts, sign-in links and sessions in
// SQLite.
//
// Times are Unix milliseconds in INTEGER columns, NULL for the zero time. No
// CHECK constraints, as in placedb: a CHECK removed later stays on every
// database that already exists.
//
// # The three claims
//
// CreateToken, UseToken and ClaimBootstrap are each one statement that only
// matches while the claim is open, and each reports whether it was the one
// that matched. That is userbus.Storer's contract and the reason none of them
// reads the row first.
package userdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Store is the SQLite implementation of userbus.Storer.
type Store struct {
	db *sql.DB
}

// NewStore constructs one.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var _ userbus.Storer = (*Store)(nil)

// Expected is what CheckSchema verifies at startup and on every /healthz.
var Expected = sqldb.Expected{
	"users":         {"id", "email", "name", "enabled", "created_at", "updated_at"},
	"signin_tokens": {"id", "user_id", "hash", "created_at", "expires_at", "used_at"},
	"sessions":      {"id", "user_id", "hash", "created_at", "expires_at"},
	"bootstrap":     {"id", "claimed_at"},
}

// Init creates the tables. Idempotent, and run at every startup.
func Init(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id          TEXT    PRIMARY KEY,

    -- Case-sensitive UNIQUE, which is right: types.Email folds case before
    -- an address reaches this layer, so any address has one spelling.
    email       TEXT    NOT NULL UNIQUE,
    name        TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS signin_tokens (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash        BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS signin_tokens_user ON signin_tokens (user_id, expires_at);

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT    PRIMARY KEY,
    user_id     TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    hash        BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS sessions_user ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at ON sessions (expires_at);

-- At most one row, ever, with id 1: claiming the bootstrap is an insert that
-- conflicts on the primary key the second time, which is one statement with
-- no race. mass-intentions pins the id with a CHECK; here the store is the
-- only writer and always writes 1, and there is no CHECK to be stuck with.
CREATE TABLE IF NOT EXISTS bootstrap (
    id          INTEGER PRIMARY KEY,
    claimed_at  INTEGER NOT NULL
) STRICT;
`

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("creating the account tables: %w", err)
	}

	return nil
}

// ------------------------------------------------------------------ users

const userColumns = `id, email, name, enabled, created_at, updated_at`

// CreateUser inserts an account. The UNIQUE on email is the check for a
// taken address; see userbus.Business.Create.
func (s *Store) CreateUser(ctx context.Context, u userbus.User) error {
	const q = `INSERT INTO users (` + userColumns + `) VALUES (?, ?, ?, ?, ?, ?)`

	_, err := s.db.ExecContext(ctx, q,
		u.ID.String(), u.Email.String(), u.Name, boolOf(u.Enabled), ms(u.CreatedAt), ms(u.UpdatedAt))

	switch {
	case sqldb.IsUniqueViolation(err):
		return userbus.ErrEmailTaken
	case err != nil:
		return fmt.Errorf("inserting the steward: %w", err)
	}

	return nil
}

// UserByID finds an account by identifier.
func (s *Store) UserByID(ctx context.Context, id types.ID) (userbus.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id.String()))
}

// UserByEmail finds an account by address.
func (s *Store) UserByEmail(ctx context.Context, email types.Email) (userbus.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email.String()))
}

// Users is every account, oldest first.
func (s *Store) Users(ctx context.Context) ([]userbus.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("reading the stewards: %w", err)
	}
	defer rows.Close()

	var out []userbus.User

	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the stewards: %w", err)
	}

	return out, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanUser(row scanner) (userbus.User, error) {
	var (
		u                 userbus.User
		id, email         string
		enabled           int64
		created, modified int64
	)

	err := row.Scan(&id, &email, &u.Name, &enabled, &created, &modified)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.User{}, userbus.ErrNotFound
	case err != nil:
		return userbus.User{}, fmt.Errorf("reading a steward: %w", err)
	}

	if u.ID, err = types.ParseID(id); err != nil {
		return userbus.User{}, fmt.Errorf("a stored steward has a bad identifier: %w", err)
	}

	if u.Email, err = types.ParseEmail(email); err != nil {
		return userbus.User{}, fmt.Errorf("a stored steward has a bad address: %w", err)
	}

	u.Enabled = enabled != 0
	u.CreatedAt = timeOf(created)
	u.UpdatedAt = timeOf(modified)

	return u, nil
}

// ------------------------------------------------------------------ links

// CreateToken records a link unless the account already has limit live ones.
//
// The count and the insert are one statement, INSERT … SELECT … WHERE, so two
// requests at once cannot both see two live links and both make a third.
// "Live" is unused and unexpired as of the new link's creation time.
func (s *Store) CreateToken(ctx context.Context, t userbus.Token, limit int) (bool, error) {
	const q = `
INSERT INTO signin_tokens (id, user_id, hash, created_at, expires_at)
SELECT ?, ?, ?, ?, ?
WHERE (SELECT count(*) FROM signin_tokens
       WHERE user_id = ? AND used_at IS NULL AND expires_at > ?) < ?`

	res, err := s.db.ExecContext(ctx, q,
		t.ID.String(), t.UserID.String(), t.Hash, ms(t.CreatedAt), ms(t.ExpiresAt),
		t.UserID.String(), ms(t.CreatedAt), limit)
	if err != nil {
		return false, fmt.Errorf("inserting the sign-in link: %w", err)
	}

	return affected(res)
}

// TokenByID finds a link by identifier.
func (s *Store) TokenByID(ctx context.Context, id types.ID) (userbus.Token, error) {
	const q = `SELECT id, user_id, hash, created_at, expires_at, used_at FROM signin_tokens WHERE id = ?`

	var (
		t                userbus.Token
		rawID, rawUser   string
		created, expires int64
		used             sql.NullInt64
	)

	err := s.db.QueryRowContext(ctx, q, id.String()).Scan(&rawID, &rawUser, &t.Hash, &created, &expires, &used)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.Token{}, userbus.ErrNotFound
	case err != nil:
		return userbus.Token{}, fmt.Errorf("reading the sign-in link: %w", err)
	}

	if t.ID, err = types.ParseID(rawID); err != nil {
		return userbus.Token{}, fmt.Errorf("a stored sign-in link has a bad identifier: %w", err)
	}

	if t.UserID, err = types.ParseID(rawUser); err != nil {
		return userbus.Token{}, fmt.Errorf("a stored sign-in link names a bad account: %w", err)
	}

	t.CreatedAt = timeOf(created)
	t.ExpiresAt = timeOf(expires)
	t.UsedAt = timeOfNull(used)

	return t, nil
}

// UseToken spends a link, reporting whether this call was the one that spent
// it. `used_at IS NULL` is what makes it a claim rather than a write.
func (s *Store) UseToken(ctx context.Context, id types.ID, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE signin_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL`, ms(at), id.String())
	if err != nil {
		return false, fmt.Errorf("spending the sign-in link: %w", err)
	}

	return affected(res)
}

// ------------------------------------------------------------------ sessions

// CreateSession records a signed-in browser.
func (s *Store) CreateSession(ctx context.Context, se userbus.Session) error {
	const q = `INSERT INTO sessions (id, user_id, hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`

	if _, err := s.db.ExecContext(ctx, q,
		se.ID.String(), se.UserID.String(), se.Hash, ms(se.CreatedAt), ms(se.ExpiresAt)); err != nil {
		return fmt.Errorf("inserting the session: %w", err)
	}

	return nil
}

// SessionByID finds a session by identifier.
func (s *Store) SessionByID(ctx context.Context, id types.ID) (userbus.Session, error) {
	const q = `SELECT id, user_id, hash, created_at, expires_at FROM sessions WHERE id = ?`

	var (
		se               userbus.Session
		rawID, rawUser   string
		created, expires int64
	)

	err := s.db.QueryRowContext(ctx, q, id.String()).Scan(&rawID, &rawUser, &se.Hash, &created, &expires)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return userbus.Session{}, userbus.ErrNotFound
	case err != nil:
		return userbus.Session{}, fmt.Errorf("reading the session: %w", err)
	}

	if se.ID, err = types.ParseID(rawID); err != nil {
		return userbus.Session{}, fmt.Errorf("a stored session has a bad identifier: %w", err)
	}

	if se.UserID, err = types.ParseID(rawUser); err != nil {
		return userbus.Session{}, fmt.Errorf("a stored session names a bad account: %w", err)
	}

	se.CreatedAt = timeOf(created)
	se.ExpiresAt = timeOf(expires)

	return se, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, id types.ID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id.String())
	if err != nil {
		return fmt.Errorf("deleting the session: %w", err)
	}

	switch ok, err := affected(res); {
	case err != nil:
		return err
	case !ok:
		return userbus.ErrNotFound
	}

	return nil
}

// ------------------------------------------------------------------ bootstrap

// ClaimBootstrap records that the secret is spent, reporting false if it
// already was.
func (s *Store) ClaimBootstrap(ctx context.Context, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO bootstrap (id, claimed_at) VALUES (1, ?) ON CONFLICT DO NOTHING`, ms(at))
	if err != nil {
		return false, fmt.Errorf("recording the bootstrap: %w", err)
	}

	return affected(res)
}

// BootstrapSpent reports whether ClaimBootstrap has ever succeeded.
func (s *Store) BootstrapSpent(ctx context.Context) (bool, error) {
	var n int

	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM bootstrap`).Scan(&n); err != nil {
		return false, fmt.Errorf("reading the bootstrap: %w", err)
	}

	return n > 0, nil
}

// ------------------------------------------------------------------ housekeeping

// PruneExpired deletes links and sessions that can no longer be used. A spent
// link is kept until it would have expired anyway, so "presented twice" is
// still something the log can say about it.
func (s *Store) PruneExpired(ctx context.Context, before time.Time) error {
	for _, q := range []string{
		`DELETE FROM signin_tokens WHERE expires_at <= ?`,
		`DELETE FROM sessions WHERE expires_at <= ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, ms(before)); err != nil {
			return fmt.Errorf("removing expired sign-ins: %w", err)
		}
	}

	return nil
}

// ------------------------------------------------------------------ helpers

func ms(t time.Time) int64 { return t.UnixMilli() }

func timeOf(v int64) time.Time { return time.UnixMilli(v).UTC() }

func timeOfNull(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}

	return timeOf(v.Int64)
}

func boolOf(b bool) int64 {
	if b {
		return 1
	}

	return 0
}

func affected(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("counting the rows changed: %w", err)
	}

	return n > 0, nil
}
