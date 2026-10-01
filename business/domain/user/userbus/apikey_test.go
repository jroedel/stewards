package userbus_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/user/userbus"
)

func TestAnAPIKeyActsAsItsSteward(t *testing.T) {
	b, clock := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	k, key, err := b.CreateAPIKey(t.Context(), u.ID, "  laptop  ")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(key, userbus.APIKeyPrefix) || k.Name != "laptop" {
		t.Errorf("key %q, name %q", key[:4], k.Name)
	}

	got, err := b.AuthenticateAPIKey(t.Context(), key)
	if err != nil || got.ID != u.ID {
		t.Fatalf("the key does not authenticate: %v", err)
	}

	// The first use is recorded; uses within the hour write nothing more.
	keys, _ := b.APIKeys(t.Context(), u.ID)
	if len(keys) != 1 || !keys[0].LastUsedAt.Equal(clock.t) {
		t.Fatalf("last used %v, want %v", keys[0].LastUsedAt, clock.t)
	}

	first := clock.t
	clock.advance(30 * time.Minute)
	_, _ = b.AuthenticateAPIKey(t.Context(), key)

	if keys, _ := b.APIKeys(t.Context(), u.ID); !keys[0].LastUsedAt.Equal(first) {
		t.Error("a use within the hour was recorded")
	}

	clock.advance(31 * time.Minute)
	_, _ = b.AuthenticateAPIKey(t.Context(), key)

	if keys, _ := b.APIKeys(t.Context(), u.ID); !keys[0].LastUsedAt.Equal(clock.t) {
		t.Error("a use after the hour was not recorded")
	}
}

func TestABadKeyIsTheSameRefusalAsAnyOther(t *testing.T) {
	b, _ := setup(t)
	u, session := signedIn(t, b, "steward@example.org")

	_, key, err := b.CreateAPIKey(t.Context(), u.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}

	id, _, _ := strings.Cut(strings.TrimPrefix(key, userbus.APIKeyPrefix), ".")

	for name, presented := range map[string]string{
		"empty":                  "",
		"no prefix":              strings.TrimPrefix(key, userbus.APIKeyPrefix),
		"a session cookie":       session,
		"a session with prefix":  userbus.APIKeyPrefix + session,
		"the id alone":           userbus.APIKeyPrefix + id,
		"the wrong secret":       userbus.APIKeyPrefix + id + ".AAAAAAAAAAAAAAAAAAAAAAAAAA",
		"a key one letter short": key[:len(key)-1],
	} {
		if _, err := b.AuthenticateAPIKey(t.Context(), presented); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s: %v, want ErrDenied", name, err)
		}
	}

	// And the other way: a key is not a session.
	if _, err := b.Authenticate(t.Context(), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a key accepted as a session: %v", err)
	}
}

func TestAKeyEndsOnItsOwnOrWhenRevoked(t *testing.T) {
	b, clock := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")
	other, _ := signedIn(t, b, "other@example.org")

	k, key, _ := b.CreateAPIKey(t.Context(), u.ID, "laptop")

	// Only its owner can revoke it.
	if err := b.RevokeAPIKey(t.Context(), other.ID, k.ID); !errors.Is(err, userbus.ErrNotFound) {
		t.Errorf("another steward revoked it: %v", err)
	}

	if err := b.RevokeAPIKey(t.Context(), u.ID, k.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a revoked key: %v", err)
	}

	_, key, _ = b.CreateAPIKey(t.Context(), u.ID, "desktop")
	clock.advance(userbus.APIKeyLife)

	if _, err := b.AuthenticateAPIKey(t.Context(), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a key ninety days old: %v", err)
	}

	if keys, _ := b.APIKeys(t.Context(), u.ID); len(keys) != 0 {
		t.Errorf("an expired key is still listed: %d", len(keys))
	}
}

func TestAKeyNeedsANameAndThereIsALimit(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	if _, _, err := b.CreateAPIKey(t.Context(), u.ID, "  "); !isInvalid(err, "name") {
		t.Errorf("no name: %v", err)
	}

	for i := range userbus.MaxAPIKeys {
		if _, _, err := b.CreateAPIKey(t.Context(), u.ID, "key "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := b.CreateAPIKey(t.Context(), u.ID, "one too many"); !isInvalid(err, "name") {
		t.Errorf("past the limit: %v", err)
	}
}

func TestTurningAStewardOffDeletesTheirKeys(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")
	admin, _ := signedIn(t, b, "admin@example.org")

	_, key, _ := b.CreateAPIKey(t.Context(), u.ID, "laptop")

	if _, err := b.SetEnabled(t.Context(), admin.ID, u.ID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a turned-off steward's key: %v", err)
	}

	// Gone, not merely refused: turning the account back on does not
	// revive a key that may be in a script somewhere.
	if _, err := b.SetEnabled(t.Context(), admin.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), key); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("the key came back with the account: %v", err)
	}
}

func isInvalid(err error, field string) bool {
	invalid, ok := errors.AsType[userbus.Invalid](err)

	return ok && invalid.Field == field
}
