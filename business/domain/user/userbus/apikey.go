package userbus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// An API key is how a program acts as a steward: a script, or a steward's own
// Claude, adding plants and their photos through /api/v1 instead of the
// screens. It is a session without a browser -- the same identifier and
// hashed secret, the same refusal when the account is turned off -- with
// three differences that come from who holds it.
//
// It has a name, because a steward may have one on a laptop and one on a
// desktop and needs to know which to revoke. It is shown once, on the page
// that made it, and never again: only the hash is kept, as for a session.
// And it says when it was last used, to the hour, so a key nobody remembers
// using is visible as one.
//
// What a key cannot do is the import rules' business, not this package's:
// nothing sent with one is confirmed or checked (speciesbus.Import,
// photobus.Import). A key is a way to type faster, not a way to vouch.

const (
	// APIKeyLife is ninety days, absolute, as a session is. A key lives in a
	// shell profile or a password manager on somebody's laptop, which is
	// exactly the kind of place a credential is forgotten in; it ending on
	// its own is the backstop for the steward who forgets to revoke it.
	APIKeyLife = 90 * 24 * time.Hour

	// MaxAPIKeys is how many live keys one steward may have: a laptop, a
	// desktop, and room to make a new one before revoking the old.
	MaxAPIKeys = 5

	// APIKeyPrefix starts every key, so one pasted into the wrong place is
	// recognisable -- by a person, and by a secret scanner -- as this app's.
	APIKeyPrefix = "stw_"

	// touchEvery is how stale "last used" may be. Recording every use would
	// make every API read a write on a single-writer database; to the hour
	// is enough to answer "is this key still in use?".
	touchEvery = time.Hour

	maxKeyName = 60
)

// APIKey is one key, without its secret.
type APIKey struct {
	ID         types.ID
	UserID     types.ID
	Name       string
	Hash       []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time // zero means never
}

// CreateAPIKey makes a key for a steward and returns it with the secret, the
// only time the secret exists outside the request.
func (b *Business) CreateAPIKey(ctx context.Context, userID types.ID, name string) (APIKey, string, error) {
	name = strings.TrimSpace(name)

	switch {
	case name == "":
		return APIKey{}, "", Invalid{Field: "name", Problem: "give the key a name, such as the computer it is for, so you know which one to revoke"}
	case utf8.RuneCountInString(name) > maxKeyName:
		return APIKey{}, "", Invalid{Field: "name", Problem: fmt.Sprintf("the name is longer than %d characters. Shorten it", maxKeyName)}
	}

	now := b.now()
	cred := mintCredential()

	k := APIKey{
		ID: cred.id, UserID: userID, Name: name, Hash: cred.hash,
		CreatedAt: now, ExpiresAt: now.Add(APIKeyLife),
	}

	made, err := b.store.CreateAPIKey(ctx, k, MaxAPIKeys)

	switch {
	case err != nil:
		return APIKey{}, "", fmt.Errorf("saving the API key: %w", err)
	case !made:
		return APIKey{}, "", Invalid{Field: "name", Problem: fmt.Sprintf("you already have %d keys. Revoke one you no longer use first", MaxAPIKeys)}
	}

	b.log.Info("API key created", "user_id", userID.String(), "key_id", k.ID.String())

	return k, APIKeyPrefix + cred.String(), nil
}

// APIKeys is a steward's live keys, newest first.
func (b *Business) APIKeys(ctx context.Context, userID types.ID) ([]APIKey, error) {
	return b.store.APIKeys(ctx, userID, b.now())
}

// RevokeAPIKey deletes one of a steward's own keys. A key that is not theirs
// is ErrNotFound, the same as one that does not exist: the screen only ever
// offers a steward their own.
func (b *Business) RevokeAPIKey(ctx context.Context, userID, id types.ID) error {
	if err := b.store.DeleteAPIKey(ctx, userID, id); err != nil {
		return err
	}

	b.log.Info("API key revoked", "user_id", userID.String(), "key_id", id.String())

	return nil
}

// AuthenticateAPIKey turns a presented key into the steward it belongs to,
// with the same single answer for every failure as a session (ErrDenied).
func (b *Business) AuthenticateAPIKey(ctx context.Context, presented string) (User, error) {
	rest, ok := strings.CutPrefix(presented, APIKeyPrefix)
	if !ok {
		return User{}, ErrDenied
	}

	id, secret, err := splitCredential(rest)
	if err != nil {
		return User{}, ErrDenied
	}

	k, err := b.store.APIKeyByID(ctx, id)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the API key: %w", err)
	}

	now := b.now()

	if !verifySecret(k.Hash, secret) || !now.Before(k.ExpiresAt) {
		return User{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, k.UserID)

	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the steward: %w", err)
	case !u.Enabled:
		return User{}, ErrDenied
	}

	// One statement that only writes when the stamp is an hour old. A
	// failure is logged, not returned: the key is good, and the caller's
	// request should not fail over a bookkeeping column.
	if err := b.store.TouchAPIKey(ctx, k.ID, now, now.Add(-touchEvery)); err != nil {
		b.log.Error("an API key's last use could not be recorded", "key_id", k.ID.String(), "error", err)
	}

	return u, nil
}
