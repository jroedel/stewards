package userbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/user/userbus"
)

// The pair from RFC 7636, appendix B, so the PKCE here is checked against
// somebody else's arithmetic rather than its own.
const (
	verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	claudeAI = "https://claude.ai/oauth/test-client-metadata"
	callback = "https://claude.ai/api/mcp/auth_callback"
)

func TestAGrantBecomesAKeyOnceAndOnlyForItsProgram(t *testing.T) {
	b, clock := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	code, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude (claude.ai)", callback, challenge)
	if err != nil {
		t.Fatal(err)
	}

	// Another program, another redirect, or the wrong verifier: refused,
	// and none of them spends the code.
	for name, try := range map[string][3]string{
		"another program":  {"https://claude.ai/oauth/other", callback, verifier},
		"another redirect": {claudeAI, "https://claude.ai/elsewhere", verifier},
		"a wrong verifier": {claudeAI, callback, verifier[:len(verifier)-1] + "A"},
		"a short verifier": {claudeAI, callback, "abc"},
	} {
		if _, _, err := b.RedeemGrant(t.Context(), code, try[0], try[1], try[2]); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}

	clock.advance(time.Minute)

	k, key, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier)
	if err != nil {
		t.Fatal(err)
	}

	if k.Name != "Claude (claude.ai)" || k.Client != claudeAI || !k.ExpiresAt.Equal(clock.t.Add(userbus.APIKeyLife)) {
		t.Errorf("the key is %+v", k)
	}

	if got, err := b.AuthenticateAPIKey(t.Context(), key); err != nil || got.ID != u.ID {
		t.Fatalf("the key does not act as its steward: %v", err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a code traded twice: %v", err)
	}
}

func TestAGrantExpires(t *testing.T) {
	b, clock := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	code, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude", callback, challenge)
	if err != nil {
		t.Fatal(err)
	}

	clock.advance(userbus.GrantLife)

	if _, _, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("an expired code: %v", err)
	}
}

func TestAGrantNeedsAnS256Challenge(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	for _, c := range []string{"", "plain-text-challenge", verifier + "x"} {
		_, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude", callback, c)
		if invalid, ok := errors.AsType[userbus.Invalid](err); !ok || invalid.Field != "code_challenge" {
			t.Errorf("challenge %q: %v", c, err)
		}
	}
}

// Connecting the same program again replaces its key rather than adding one,
// and leaves the steward's own keys alone.
func TestConnectingAgainReplacesTheProgramsKey(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	if _, _, err := b.CreateAPIKey(t.Context(), u.ID, "laptop"); err != nil {
		t.Fatal(err)
	}

	connect := func() string {
		t.Helper()

		code, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude", callback, challenge)
		if err != nil {
			t.Fatal(err)
		}

		_, key, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier)
		if err != nil {
			t.Fatal(err)
		}

		return key
	}

	first := connect()
	second := connect()

	if _, err := b.AuthenticateAPIKey(t.Context(), first); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("the first connection's key still works: %v", err)
	}

	if _, err := b.AuthenticateAPIKey(t.Context(), second); err != nil {
		t.Errorf("the second connection's key: %v", err)
	}

	keys, _ := b.APIKeys(t.Context(), u.ID)
	if len(keys) != 2 {
		t.Errorf("%d keys, want the laptop's and Claude's", len(keys))
	}
}

// At the limit, with none of the keys the program's, the trade is refused
// and nothing is lost; with one of them the program's, it is replaced.
func TestAProgramsKeyKeepsTheLimit(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	for range userbus.MaxAPIKeys {
		if _, _, err := b.CreateAPIKey(t.Context(), u.ID, "a computer"); err != nil {
			t.Fatal(err)
		}
	}

	code, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude", callback, challenge)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a sixth key: %v", err)
	}

	if keys, _ := b.APIKeys(t.Context(), u.ID); len(keys) != userbus.MaxAPIKeys {
		t.Errorf("%d keys after a refusal", len(keys))
	}
}

func TestADisabledStewardsGrantIsRefused(t *testing.T) {
	b, _ := setup(t)
	u, _ := signedIn(t, b, "steward@example.org")

	code, err := b.GrantAccess(t.Context(), u.ID, claudeAI, "Claude", callback, challenge)
	if err != nil {
		t.Fatal(err)
	}

	other, _ := signedIn(t, b, "other@example.org")

	if _, err := b.SetEnabled(t.Context(), other.ID, u.ID, false); err != nil {
		t.Fatal(err)
	}

	if _, _, err := b.RedeemGrant(t.Context(), code, claudeAI, callback, verifier); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled steward's code: %v", err)
	}
}
