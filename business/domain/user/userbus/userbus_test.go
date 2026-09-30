package userbus_test

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// clock is a time a test moves by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// Against the real store rather than a fake: the claims that matter here are
// SQL statements, and a fake would be a second implementation of them to get
// wrong.
func setup(t *testing.T) (*userbus.Business, *clock) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := sqldb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := userdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	c := &clock{t: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	return userbus.NewBusiness(log, userdb.NewStore(db), c.now), c
}

func email(t *testing.T, s string) types.Email {
	t.Helper()

	e, err := types.ParseEmail(s)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func TestALinkSignsInOnceAndTheSessionAuthenticates(t *testing.T) {
	b, _ := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, "A Steward"); err != nil {
		t.Fatal(err)
	}

	req, err := b.RequestSignIn(t.Context(), addr)
	if err != nil || !req.Sendable() {
		t.Fatalf("RequestSignIn = %+v, %v; want a link", req, err)
	}

	u, cookie, err := b.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}

	if u.Email != addr {
		t.Errorf("signed in as %v", u.Email)
	}

	if _, _, err := b.SignIn(t.Context(), req.Secret); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a link used twice: %v, want ErrDenied", err)
	}

	got, err := b.Authenticate(t.Context(), cookie)
	if err != nil || got.ID != u.ID {
		t.Fatalf("Authenticate = %v, %v", got.ID, err)
	}

	if err := b.SignOut(t.Context(), cookie); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Authenticate(t.Context(), cookie); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a session after sign-out: %v, want ErrDenied", err)
	}
}

// The page after "send me a link" must read the same whether or not the
// address is a steward's, so nothing here may be an error.
func TestAnAddressWithNoAccountIsTreatedLikeOneThatHasIt(t *testing.T) {
	b, _ := setup(t)

	req, err := b.RequestSignIn(t.Context(), email(t, "nobody@example.org"))
	if err != nil {
		t.Errorf("RequestSignIn for a stranger: %v; want no error", err)
	}

	if req.Sendable() {
		t.Error("a link was minted for an address with no account")
	}
}

func TestALinkExpires(t *testing.T) {
	b, c := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := b.RequestSignIn(t.Context(), addr)
	c.advance(userbus.LinkLife)

	if _, _, err := b.SignIn(t.Context(), req.Secret); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a link at the end of its life: %v, want ErrDenied", err)
	}
}

func TestASessionEndsAfterNinetyDaysHoweverOftenItIsUsed(t *testing.T) {
	b, c := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := b.RequestSignIn(t.Context(), addr)
	_, cookie, err := b.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	start := c.t

	// Used every week for twelve weeks. Nothing extends it.
	for range 12 {
		if _, err := b.Authenticate(t.Context(), cookie); err != nil {
			t.Fatalf("a session in use failed at %v: %v", c.t, err)
		}

		c.advance(7 * 24 * time.Hour)
	}

	c.t = start.Add(userbus.SessionLife - time.Millisecond)
	if _, err := b.Authenticate(t.Context(), cookie); err != nil {
		t.Fatalf("a session failed a millisecond before its end: %v", err)
	}

	c.t = start.Add(userbus.SessionLife)

	if _, err := b.Authenticate(t.Context(), cookie); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a session at the end of its life: %v, want ErrDenied", err)
	}
}

// A stranger typing a steward's address again and again sends at most
// MaxLiveLinks messages until one expires.
func TestAStrangerCannotFloodAStewardsInbox(t *testing.T) {
	b, c := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	sent := 0
	for range userbus.MaxLiveLinks + 5 {
		req, err := b.RequestSignIn(t.Context(), addr)
		if err != nil {
			t.Fatalf("RequestSignIn: %v; the refusal must not be an error", err)
		}

		if req.Sendable() {
			sent++
		}
	}

	if sent != userbus.MaxLiveLinks {
		t.Errorf("%d links minted, want %d", sent, userbus.MaxLiveLinks)
	}

	c.advance(userbus.LinkLife)

	if req, _ := b.RequestSignIn(t.Context(), addr); !req.Sendable() {
		t.Error("once the links expired, no new one could be had")
	}
}

func TestEveryBadCredentialIsTheSameRefusal(t *testing.T) {
	b, _ := setup(t)

	for _, presented := range []string{
		"",
		"no-dot",
		"not-an-id.ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		types.NewID().String() + ".short",
		types.NewID().String() + ".ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	} {
		if _, _, err := b.SignIn(t.Context(), presented); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("SignIn(%q) = %v, want ErrDenied", presented, err)
		}

		if _, err := b.Authenticate(t.Context(), presented); !errors.Is(err, userbus.ErrDenied) {
			t.Errorf("Authenticate(%q) = %v, want ErrDenied", presented, err)
		}
	}
}

// A right identifier with a wrong secret: the identifier is safe to log, so
// holding it must be worth nothing.
func TestAnIdentifierWithoutItsSecretIsWorthNothing(t *testing.T) {
	b, _ := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := b.RequestSignIn(t.Context(), addr)
	id, _, _ := strings.Cut(req.Secret, ".")

	if _, _, err := b.SignIn(t.Context(), id+".ABCDEFGHIJKLMNOPQRSTUVWXYZ"); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a guessed secret: %v, want ErrDenied", err)
	}

	// And the guess did not spend it.
	if _, _, err := b.SignIn(t.Context(), req.Secret); err != nil {
		t.Errorf("the real link after a wrong guess: %v", err)
	}
}

func TestTheBootstrapWorksOnceAndMakesTheFirstSteward(t *testing.T) {
	b, _ := setup(t)
	const secret = "a-bootstrap-secret-long-enough"
	addr := email(t, "steward@example.org")

	if _, _, err := b.Bootstrap(t.Context(), secret, "wrong", addr); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a wrong secret: %v, want ErrDenied", err)
	}

	if spent, _ := b.BootstrapSpent(t.Context()); spent {
		t.Fatal("a wrong secret spent the bootstrap")
	}

	u, cookie, err := b.Bootstrap(t.Context(), secret, secret, addr)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if u.Email != addr || !u.Enabled {
		t.Errorf("bootstrap made %+v", u)
	}

	if _, err := b.Authenticate(t.Context(), cookie); err != nil {
		t.Errorf("the bootstrap session: %v", err)
	}

	if _, _, err := b.Bootstrap(t.Context(), secret, secret, email(t, "other@example.org")); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a second bootstrap: %v, want ErrDenied", err)
	}
}

// No secret in the config means the door is shut, including to an empty
// secret presented against it.
func TestNoConfiguredSecretMeansNoBootstrap(t *testing.T) {
	b, _ := setup(t)

	if _, _, err := b.Bootstrap(t.Context(), "", "", email(t, "steward@example.org")); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("Bootstrap with nothing configured: %v, want ErrDenied", err)
	}
}

func TestAnAddressHoldsOneAccount(t *testing.T) {
	b, _ := setup(t)
	addr := email(t, "steward@example.org")

	if _, err := b.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Create(t.Context(), addr, ""); !errors.Is(err, userbus.ErrEmailTaken) {
		t.Errorf("a second account: %v, want ErrEmailTaken", err)
	}
}

func signedIn(t *testing.T, b *userbus.Business, addr string) (userbus.User, string) {
	t.Helper()

	u, err := b.Create(t.Context(), email(t, addr), "")
	if err != nil {
		t.Fatal(err)
	}

	req, _ := b.RequestSignIn(t.Context(), u.Email)
	_, cookie, err := b.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	return u, cookie
}

// Turning a steward off signs them out everywhere at once, and turning them
// back on lets them ask for a new link -- but not use the old session.
func TestADisabledStewardIsSignedOutAndCannotSignIn(t *testing.T) {
	b, _ := setup(t)
	me, _ := signedIn(t, b, "steward@example.org")
	them, cookie := signedIn(t, b, "other@example.org")

	if _, err := b.SetEnabled(t.Context(), me.ID, them.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	if _, err := b.Authenticate(t.Context(), cookie); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a disabled steward's session: %v, want ErrDenied", err)
	}

	if req, _ := b.RequestSignIn(t.Context(), them.Email); req.Sendable() {
		t.Error("a disabled steward was sent a link")
	}

	if _, err := b.SetEnabled(t.Context(), me.ID, them.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Authenticate(t.Context(), cookie); !errors.Is(err, userbus.ErrDenied) {
		t.Error("turning a steward back on revived the session that was ended")
	}

	if req, _ := b.RequestSignIn(t.Context(), them.Email); !req.Sendable() {
		t.Error("a steward turned back on cannot ask for a link")
	}
}

func TestNobodyCanLockTheGardenOut(t *testing.T) {
	b, _ := setup(t)
	me, cookie := signedIn(t, b, "steward@example.org")

	_, err := b.SetEnabled(t.Context(), me.ID, me.ID, false)
	if invalid, ok := errors.AsType[userbus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "your own account") {
		t.Errorf("turning off yourself: %v", err)
	}

	// The last one, turned off by somebody who is themselves already off --
	// the only way the rule above does not already cover it.
	other, _ := signedIn(t, b, "other@example.org")
	if _, err := b.SetEnabled(t.Context(), me.ID, other.ID, false); err != nil {
		t.Fatal(err)
	}

	_, err = b.SetEnabled(t.Context(), other.ID, me.ID, false)
	if invalid, ok := errors.AsType[userbus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "last steward") {
		t.Errorf("turning off the last steward: %v", err)
	}

	if _, err := b.Authenticate(t.Context(), cookie); err != nil {
		t.Errorf("the refused change still signed me out: %v", err)
	}
}
