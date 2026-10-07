// Package userbus is the garden stewards' accounts, and how somebody proves
// they hold one.
//
// Taken from mass-intentions and cut down to what this app needs. There is one
// kind of account: a steward, who may edit places and, later, species and the
// day's job. A volunteer never signs in. The Phase 1 test is somebody with a
// phone and a tray of plants, and a sign-in screen between them and the rain
// garden is a screen they would give up at, so everything a volunteer reads is
// public and only changing it asks who you are.
//
// Sign-in is by emailed link. There is no password, which removes password
// storage, reset, reuse and strength from this app entirely: the mailbox is
// the factor, and it is one the steward already looks after.
//
// # The bootstrap
//
// A link cannot be sent until mail works, and an account cannot be added until
// somebody is signed in. [Business.Bootstrap] breaks that circle: a one-time
// secret from the server's config that produces a session without sending
// anything, and creates the first account if there is none. It works once,
// recorded in the database, so neither a restart nor a new secret in the
// config revives it.
//
// Backup codes, which mass-intentions has for when mail fails, are left out.
// There a priest locked out on the morning of a Mass is a real cost; here a
// steward who cannot sign in waits for the mail to be fixed, and the plants do
// not notice.
//
// # What this package refuses to tell anybody
//
// Every failed attempt returns [ErrDenied] and nothing else -- not "no such
// account", not "that link expired". The app layer cannot leak which half was
// wrong, because it is never told. [Business.RequestSignIn] succeeds for an
// address with no account, so that the page after it reads the same either
// way; a form that answers differently is a list of who the stewards are.
package userbus

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

const (
	// LinkLife is how long an emailed link works: long enough to walk to
	// another device and open the mail, short enough that a link left in an
	// inbox is not a standing key. Exported so a test asserts the boundary
	// without writing the number a second time.
	LinkLife = 15 * time.Minute

	// SessionLife is ninety days, absolute, with no renewal on use.
	//
	// Long because a steward signs in on the phone they carry in the garden,
	// and a sign-in every week is a sign-in in the sun with dirty hands. Not
	// sliding, because a session that renews whenever it is used never ends
	// for whoever is using it -- which is exactly wrong when that is not the
	// steward.
	SessionLife = 90 * 24 * time.Hour

	// MaxLiveLinks is how many unexpired, unused links one account may have.
	//
	// This is the throttle, and it is per account rather than per address of
	// origin. Every request arrives from Apache's proxy on the loopback, so the
	// app cannot see an IP address it could trust. What it can bound is the
	// harm: a stranger typing a steward's address over and over sends that
	// steward at most this many messages every quarter of an hour, and
	// then nothing until one expires. The page they see does not change, so
	// the limit tells them nothing either.
	MaxLiveLinks = 3

	maxName = 60
)

var (
	// ErrDenied is every failed attempt to prove who somebody is: an
	// unknown address, a disabled account, a link that expired or was
	// already used, a wrong secret, a bootstrap secret already spent. One
	// error, so the difference cannot leak.
	ErrDenied = errors.New("that did not work")

	// ErrNotFound is a lookup a signed-in caller made by identifier, rather
	// than a credential a stranger presented.
	ErrNotFound = errors.New("there is no such steward")

	// ErrEmailTaken is returned to a steward adding another, where the
	// collision is information they are entitled to.
	ErrEmailTaken = errors.New("a steward already has that address")
)

// Invalid is a change a steward asked for that cannot be made as given. Field
// names the input to fix; Problem says what is wrong, in a sentence the page
// can show beside it. The same shape as placebus.Invalid, so an app handles
// both the same way.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

// User is a steward's account.
type User struct {
	ID    types.ID
	Email types.Email

	// Name is how the other stewards know them, on the screen that lists
	// accounts. It is never shown to a volunteer: the app speaks as "the
	// garden stewards" (CLAUDE.md).
	Name string

	// Enabled is positive rather than a Disabled flag, so that the zero User
	// -- what comes back beside every error here -- cannot sign in.
	Enabled bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Token is an emailed sign-in link. Only the hash is kept: mail is archived
// forever, and a leaked database must not be a way in.
type Token struct {
	ID        types.ID
	UserID    types.ID
	Hash      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    time.Time // zero means unused
}

// Session is a signed-in browser.
type Session struct {
	ID        types.ID
	UserID    types.ID
	Hash      []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Storer is what the rules need from storage.
//
// Three methods report a bool for "somebody else got there first", and they
// are the point of the interface. CreateToken, UseToken and ClaimBootstrap are
// each one statement whose WHERE only matches when the claim is still open
// (CLAUDE.md, "A single-use claim is one statement"). A read followed by a
// write would let two requests holding one link both sign in.
type Storer interface {
	CreateUser(ctx context.Context, u User) error
	UpdateUser(ctx context.Context, u User) error
	UserByID(ctx context.Context, id types.ID) (User, error)
	UserByEmail(ctx context.Context, email types.Email) (User, error)
	Users(ctx context.Context) ([]User, error)

	// CreateToken records a link unless the account already has limit live
	// ones, and reports whether it did.
	CreateToken(ctx context.Context, t Token, limit int) (bool, error)
	TokenByID(ctx context.Context, id types.ID) (Token, error)
	UseToken(ctx context.Context, id types.ID, at time.Time) (bool, error)

	CreateSession(ctx context.Context, s Session) error
	SessionByID(ctx context.Context, id types.ID) (Session, error)
	DeleteSession(ctx context.Context, id types.ID) error
	DeleteUserSessions(ctx context.Context, userID types.ID) error

	ClaimBootstrap(ctx context.Context, at time.Time) (bool, error)
	BootstrapSpent(ctx context.Context) (bool, error)

	// CreateAPIKey records a key unless the steward already has limit live
	// ones, in one statement, as CreateToken does.
	CreateAPIKey(ctx context.Context, k APIKey, limit int) (bool, error)
	APIKeyByID(ctx context.Context, id types.ID) (APIKey, error)
	APIKeys(ctx context.Context, userID types.ID, now time.Time) ([]APIKey, error)
	DeleteAPIKey(ctx context.Context, userID, id types.ID) error
	DeleteUserAPIKeys(ctx context.Context, userID types.ID) error

	// ReplaceAPIKey records a key given to a program through OAuth in place
	// of any the steward gave the same program before, unless that leaves
	// them with more than limit; and then it changes nothing.
	ReplaceAPIKey(ctx context.Context, k APIKey, limit int) (bool, error)

	// CreateGrant records an OAuth code unless the steward already has limit
	// unspent ones; UseGrant spends one. Both claims, as for links.
	CreateGrant(ctx context.Context, g Grant, limit int) (bool, error)
	GrantByID(ctx context.Context, id types.ID) (Grant, error)
	UseGrant(ctx context.Context, id types.ID, at time.Time) (bool, error)

	// TouchAPIKey records a use, only where the last one is before
	// notAfter, so most uses write nothing.
	TouchAPIKey(ctx context.Context, id types.ID, at, notAfter time.Time) error

	PruneExpired(ctx context.Context, before time.Time) error
}

// Business applies the rules and then asks the store.
type Business struct {
	log   *slog.Logger
	store Storer
	now   func() time.Time
}

// NewBusiness constructs one. now is injectable so a test can say what time it
// is; nil means the wall clock.
func NewBusiness(log *slog.Logger, store Storer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{log: log, store: store, now: now}
}

// Create adds a steward. Only a signed-in steward reaches this: there is no
// signing yourself up, because an account here can change what every
// volunteer is told to pull.
func (b *Business) Create(ctx context.Context, email types.Email, name string) (User, error) {
	name = strings.TrimSpace(name)

	switch {
	case email.Zero():
		return User{}, Invalid{Field: "email", Problem: "give their email address; the sign-in link goes there"}
	case utf8.RuneCountInString(name) > maxName:
		return User{}, Invalid{Field: "name", Problem: fmt.Sprintf("keep the name under %d characters", maxName)}
	}

	now := b.now()
	u := User{
		ID:        types.NewID(),
		Email:     email,
		Name:      name,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// The insert is the check. A read first would be a second way to get
	// the same answer and a race between them.
	if err := b.store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return User{}, ErrEmailTaken
		}

		return User{}, fmt.Errorf("saving the steward: %w", err)
	}

	return u, nil
}

// SetEnabled lets a steward sign in again, or stops them.
//
// Disabling rather than deleting, because an account is a record that
// somebody could edit the garden: when there is a history of who changed a
// place, it will point at accounts, and a deleted one would leave it pointing
// at nothing.
//
// Two refusals, both about never locking the garden out of its own app. A
// steward cannot disable themselves -- the realistic way that happens is a
// tap on the wrong row -- and the last enabled steward cannot be disabled by
// anybody, which with the first rule means it cannot happen at all. With the
// bootstrap already spent, the only way back from either would be the
// database on the server.
//
// Disabling also ends every session the account has, and deletes its API
// keys. Authenticate would refuse them anyway on their next request; deleting
// them means a phone left signed in is signed out in fact and not only in
// effect, and a key in a script is gone rather than waiting to be re-enabled.
func (b *Business) SetEnabled(ctx context.Context, actor, id types.ID, enabled bool) (User, error) {
	u, err := b.store.UserByID(ctx, id)
	if err != nil {
		return User{}, err
	}

	if u.Enabled == enabled {
		return u, nil
	}

	if !enabled {
		if actor == id {
			return User{}, Invalid{Field: "steward", Problem: "you cannot turn off your own account. Ask another steward to do it"}
		}

		all, err := b.store.Users(ctx)
		if err != nil {
			return User{}, fmt.Errorf("reading the stewards: %w", err)
		}

		others := 0
		for _, o := range all {
			if o.Enabled && o.ID != id {
				others++
			}
		}

		if others == 0 {
			return User{}, Invalid{Field: "steward", Problem: "that is the last steward who can sign in. Add another first"}
		}
	}

	u.Enabled = enabled
	u.UpdatedAt = b.now()

	if err := b.store.UpdateUser(ctx, u); err != nil {
		return User{}, fmt.Errorf("saving the steward: %w", err)
	}

	if !enabled {
		if err := b.store.DeleteUserSessions(ctx, id); err != nil {
			// The account is off, and Authenticate checks that on every
			// request, so a leftover session is refused regardless. Logged
			// rather than returned: the change the steward asked for did
			// happen.
			b.log.Error("a disabled steward's sessions could not be removed", "user_id", id.String(), "error", err)
		}

		if err := b.store.DeleteUserAPIKeys(ctx, id); err != nil {
			b.log.Error("a disabled steward's API keys could not be removed", "user_id", id.String(), "error", err)
		}
	}

	b.log.Info("steward access changed", "user_id", id.String(), "by", actor.String(), "enabled", enabled)

	return u, nil
}

// ByID is one account.
func (b *Business) ByID(ctx context.Context, id types.ID) (User, error) {
	return b.store.UserByID(ctx, id)
}

// All is every account, oldest first.
func (b *Business) All(ctx context.Context) ([]User, error) {
	return b.store.Users(ctx)
}

// SignInRequest is the result of asking for a link. Secret is empty when
// there is nothing to send, and the caller renders the same page either way --
// which is why this is a struct with an empty field rather than an error an
// app might be tempted to explain.
type SignInRequest struct {
	User   User
	Secret string
}

// Sendable reports whether there is a link to mail.
func (r SignInRequest) Sendable() bool { return r.Secret != "" }

// RequestSignIn mints a link for an address, if a steward holds it and has
// fewer than MaxLiveLinks outstanding. No error for any of the ways that
// fails; the log records each, since somebody trying addresses is worth
// seeing and the log is the one place that is safe to say so.
func (b *Business) RequestSignIn(ctx context.Context, email types.Email) (SignInRequest, error) {
	u, err := b.store.UserByEmail(ctx, email)

	switch {
	case errors.Is(err, ErrNotFound):
		b.log.Info("sign-in requested for an address with no account", "email_domain", email.Domain())

		return SignInRequest{}, nil
	case err != nil:
		return SignInRequest{}, fmt.Errorf("reading the stewards: %w", err)
	case !u.Enabled:
		b.log.Info("sign-in requested for a disabled account", "user_id", u.ID.String())

		return SignInRequest{}, nil
	}

	now := b.now()
	cred := mintCredential()

	made, err := b.store.CreateToken(ctx, Token{
		ID:        cred.id,
		UserID:    u.ID,
		Hash:      cred.hash,
		CreatedAt: now,
		ExpiresAt: now.Add(LinkLife),
	}, MaxLiveLinks)

	switch {
	case err != nil:
		return SignInRequest{}, fmt.Errorf("saving the sign-in link: %w", err)
	case !made:
		b.log.Warn("sign-in link refused: the account already has as many as it may", "user_id", u.ID.String())

		return SignInRequest{}, nil
	}

	return SignInRequest{User: u, Secret: cred.String()}, nil
}

// SignIn redeems a link and returns the account and a session credential.
//
// presented is what came back from the button on the page the link opens,
// never from opening the link: mail scanners and link previews fetch every URL
// in a message, and a link spent on GET is spent by software before the
// steward taps it. The app layer holds that line; this only redeems.
func (b *Business) SignIn(ctx context.Context, presented string) (User, string, error) {
	id, secret, err := splitCredential(presented)
	if err != nil {
		return User{}, "", ErrDenied
	}

	t, err := b.store.TokenByID(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, "", ErrDenied
	case err != nil:
		return User{}, "", fmt.Errorf("reading the sign-in link: %w", err)
	}

	// The secret before anything else about the row, so a guessed
	// identifier cannot learn whether its link expired or was used.
	if !verifySecret(t.Hash, secret) {
		return User{}, "", ErrDenied
	}

	now := b.now()

	switch {
	case !t.UsedAt.IsZero():
		b.log.Warn("a sign-in link was presented twice", "token_id", t.ID.String(), "user_id", t.UserID.String())

		return User{}, "", ErrDenied
	case !now.Before(t.ExpiresAt):
		return User{}, "", ErrDenied
	}

	// The claim. Another request with the same link may have spent it since
	// the read above, and only the store can settle that.
	claimed, err := b.store.UseToken(ctx, t.ID, now)
	switch {
	case err != nil:
		return User{}, "", fmt.Errorf("spending the sign-in link: %w", err)
	case !claimed:
		return User{}, "", ErrDenied
	}

	return b.start(ctx, t.UserID)
}

// Bootstrap trades the one-time secret from the config for a session,
// creating the account for email if there is none.
//
// The claim is recorded before the account is looked at, so a secret
// presented for a disabled account is still spent -- otherwise it could be
// retried against another address.
func (b *Business) Bootstrap(ctx context.Context, configured, presented string, email types.Email) (User, string, error) {
	// The one direct comparison in this package, since the configured value
	// is the secret itself. An empty one is refused by name, because
	// ConstantTimeCompare would say an empty presented secret matched it --
	// and "no secret configured" must mean the way in is shut.
	switch {
	case configured == "", email.Zero():
		return User{}, "", ErrDenied
	case subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) != 1:
		b.log.Warn("the bootstrap secret was presented incorrectly")

		return User{}, "", ErrDenied
	}

	claimed, err := b.store.ClaimBootstrap(ctx, b.now())
	switch {
	case err != nil:
		return User{}, "", fmt.Errorf("recording the bootstrap: %w", err)
	case !claimed:
		b.log.Warn("the bootstrap secret was presented after it had been spent")

		return User{}, "", ErrDenied
	}

	u, err := b.store.UserByEmail(ctx, email)

	switch {
	case errors.Is(err, ErrNotFound):
		if u, err = b.Create(ctx, email, ""); err != nil {
			return User{}, "", err
		}

		b.log.Warn("bootstrap created the first steward", "user_id", u.ID.String())
	case err != nil:
		return User{}, "", fmt.Errorf("reading the stewards: %w", err)
	case !u.Enabled:
		return User{}, "", ErrDenied
	default:
		b.log.Warn("bootstrap signed in an existing steward", "user_id", u.ID.String())
	}

	return b.start(ctx, u.ID)
}

// BootstrapSpent reports whether the secret has been used, so that the page
// can say so rather than collect a secret it will refuse. It decides nothing;
// Bootstrap's claim does.
func (b *Business) BootstrapSpent(ctx context.Context) (bool, error) {
	return b.store.BootstrapSpent(ctx)
}

// Authenticate turns a session cookie into the steward holding it.
//
// On every request that carries the cookie, so it is one indexed read and one
// comparison, and it never writes: a last-seen stamp on every read would make
// every page a write on a single-writer database.
func (b *Business) Authenticate(ctx context.Context, presented string) (User, error) {
	id, secret, err := splitCredential(presented)
	if err != nil {
		return User{}, ErrDenied
	}

	s, err := b.store.SessionByID(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the session: %w", err)
	}

	if !verifySecret(s.Hash, secret) || !b.now().Before(s.ExpiresAt) {
		return User{}, ErrDenied
	}

	u, err := b.store.UserByID(ctx, s.UserID)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, ErrDenied
	case err != nil:
		return User{}, fmt.Errorf("reading the steward: %w", err)
	case !u.Enabled:
		// Here as well as at sign-in, so disabling an account ends its
		// sessions on their next request rather than in ninety days.
		return User{}, ErrDenied
	}

	return u, nil
}

// SignOut ends one session. A malformed or unknown cookie is not an error:
// the caller wanted no session, and there is none.
func (b *Business) SignOut(ctx context.Context, presented string) error {
	id, _, err := splitCredential(presented)
	if err != nil {
		return nil
	}

	if err := b.store.DeleteSession(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("ending the session: %w", err)
	}

	return nil
}

// Prune deletes expired links and sessions. Housekeeping rather than a
// defence: a link is only minted for an account, so a stranger cannot fill
// the table.
func (b *Business) Prune(ctx context.Context) error {
	if err := b.store.PruneExpired(ctx, b.now()); err != nil {
		return fmt.Errorf("removing expired sign-ins: %w", err)
	}

	return nil
}

// start issues a session for an account that has just proved itself. The
// string returned is the cookie's value and exists for this request only.
func (b *Business) start(ctx context.Context, userID types.ID) (User, string, error) {
	u, err := b.store.UserByID(ctx, userID)
	switch {
	case errors.Is(err, ErrNotFound):
		// A good credential for an account that has gone. Louder than a
		// wrong secret, because it should not be possible.
		b.log.Error("a valid credential named an account that does not exist", "user_id", userID.String())

		return User{}, "", ErrDenied
	case err != nil:
		return User{}, "", fmt.Errorf("reading the steward: %w", err)
	case !u.Enabled:
		return User{}, "", ErrDenied
	}

	now := b.now()
	cred := mintCredential()

	if err := b.store.CreateSession(ctx, Session{
		ID:        cred.id,
		UserID:    u.ID,
		Hash:      cred.hash,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionLife),
	}); err != nil {
		return User{}, "", fmt.Errorf("saving the session: %w", err)
	}

	b.log.Info("signed in", "user_id", u.ID.String(), "session_id", cred.id.String())

	return u, cred.String(), nil
}
