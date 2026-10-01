// Package subscriberbus is the list of people who want to hear about
// stewardship days: an email address, the language they signed up in, and
// whether they have confirmed it.
//
// Anyone can sign up from the home page, which is where the QR code on the
// trail's signs leads, so the list is built to be safe for strangers to type
// into.
//
// # Double opt-in
//
// An address is on the list only once its owner has opened the email sent to
// it and pressed the button on the page it leads to. Until then it is pending,
// and a pending address is never written to except to send it that one
// confirmation. Without this, the form is a way to sign up somebody else --
// the stewards would be emailing people who never asked, which is the one
// thing that gets a parish's mail marked as spam.
//
// # The caps
//
// The confirmation email is the only mail a stranger can make this app send,
// so it is the one that is capped: PerAddress to one address in a day, so
// the form cannot be used to flood somebody's inbox, and PerHour across every
// address, so it cannot be used to burn the relay's reputation. A capped
// request looks exactly like one that sent, to the person and to anybody
// probing; only the log says otherwise.
//
// Every request arrives from Apache on the loopback, so there is no client
// address to cap by (see userbus.MaxLiveLinks for the same reasoning).
//
// # What the page after the form says
//
// The same thing for an address that is new, pending, already confirmed or
// capped: "check your email". A form that answered differently would tell
// anybody who is on the list.
//
// # Leaving
//
// Every subscriber has an unsubscribe token from the moment they sign up, and
// using it deletes the row. Deleting rather than marking, because once
// somebody has asked not to be emailed there is no reason for the stewards to
// keep their address at all.
package subscriberbus

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jroedel/stewards/business/types"
)

const (
	// ConfirmLife is how long a confirmation link works. A week, because
	// someone who scanned a sign on a Sunday walk may not open the mail
	// until the next weekend; and the link only adds them to a list they
	// asked to join.
	ConfirmLife = 7 * 24 * time.Hour

	// PerAddress is how many confirmation emails one address may be sent in
	// a day.
	PerAddress = 3

	// PerHour is how many confirmation emails the whole app may send in an
	// hour. Far above what a sign on a trail produces, and far below what
	// would trouble the relay.
	PerHour = 30
)

// Subscriber is one person on the list, or waiting to be.
type Subscriber struct {
	ID    types.ID
	Email types.Email
	Lang  types.Lang

	CreatedAt time.Time

	// ConfirmedAt is zero while the address is pending.
	ConfirmedAt time.Time

	// Unsubscribe is the token in every link that takes them off the list.
	Unsubscribe string
}

// Confirmed says whether they are on the list.
func (s Subscriber) Confirmed() bool { return !s.ConfirmedAt.IsZero() }

// Outcome is what came of a request to sign up, for the app to log; the
// person is shown the same page whichever it was.
type Outcome string

const (
	// Sent: a confirmation is to be emailed.
	Sent Outcome = "sent"

	// AlreadyOn: the address is confirmed already, and nothing is sent.
	AlreadyOn Outcome = "already_on"

	// Capped: one of the caps was reached, and nothing is sent.
	Capped Outcome = "capped"
)

// Request is the answer to a sign-up.
type Request struct {
	Outcome Outcome

	// SubscriberID is the row, for the log; it is zero when Capped.
	SubscriberID types.ID

	// Confirm is the credential for the confirmation link, when Outcome is
	// Sent. It is not kept anywhere but the email.
	Confirm string
}

// ErrDenied is every failed confirmation: unknown, used, expired or garbled.
// One error, so the page cannot say which, as userbus.ErrDenied.
var ErrDenied = errors.New("that link has expired or has been used")

// ErrNotFound is returned when there is no such subscriber.
var ErrNotFound = errors.New("that address is not on the list")

// Storer is what the rules need from storage.
type Storer interface {
	ByEmail(ctx context.Context, email types.Email) (Subscriber, error)

	// ClaimMail records one confirmation email to email at now, as one
	// statement, and only if fewer than perAddress went to it since
	// addressSince and fewer than perHour went to anybody since hourSince.
	// It reports whether the mail may be sent.
	ClaimMail(ctx context.Context, email types.Email, now, addressSince time.Time, perAddress int, hourSince time.Time, perHour int) (bool, error)

	// SetPending adds s as pending with the given confirmation, or gives an
	// existing pending row the new confirmation in place of the old. It
	// reports the row's ID and false when the address is confirmed already,
	// and then changes nothing.
	SetPending(ctx context.Context, s Subscriber, confirmHash []byte, expires time.Time) (types.ID, bool, error)

	// Confirm marks the pending row id confirmed at now, as one statement,
	// if confirmHash is its confirmation and it has not expired, and
	// clears the confirmation so the link works once. It returns the row,
	// and false when nothing matched.
	Confirm(ctx context.Context, id types.ID, confirmHash []byte, now time.Time) (Subscriber, bool, error)

	// Unsubscribe deletes the row with this token, and reports whether
	// there was one.
	Unsubscribe(ctx context.Context, token string) (bool, error)

	Remove(ctx context.Context, id types.ID) error
	Confirmed(ctx context.Context) ([]Subscriber, error)
	PendingCount(ctx context.Context, now time.Time) (int, error)

	// Prune deletes pending rows expired by now, and records of mail sent
	// before mailsBefore.
	Prune(ctx context.Context, now, mailsBefore time.Time) error
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	now   func() time.Time
}

// NewBusiness constructs one; nil now means the wall clock.
func NewBusiness(store Storer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, now: now}
}

// Request asks to put email on the list, in lang.
//
// The order is the point. An address already confirmed is answered before
// anything is claimed, so a stranger retyping it costs nobody a mail. The
// cap is claimed before the row is written, so a flood of new addresses
// stops adding rows at the same moment it stops sending.
func (b *Business) Request(ctx context.Context, email types.Email, lang types.Lang) (Request, error) {
	now := b.now()

	s, err := b.store.ByEmail(ctx, email)

	switch {
	case err == nil && s.Confirmed():
		return Request{Outcome: AlreadyOn, SubscriberID: s.ID}, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return Request{}, fmt.Errorf("looking up the address: %w", err)
	}

	ok, err := b.store.ClaimMail(ctx, email, now, now.Add(-24*time.Hour), PerAddress, now.Add(-time.Hour), PerHour)
	if err != nil {
		return Request{}, fmt.Errorf("counting the mail sent: %w", err)
	}

	if !ok {
		return Request{Outcome: Capped}, nil
	}

	secret := newSecret()
	id, pending, err := b.store.SetPending(ctx, Subscriber{
		ID: types.NewID(), Email: email, Lang: lang, CreatedAt: now, Unsubscribe: newSecret(),
	}, hash(secret), now.Add(ConfirmLife))
	if err != nil {
		return Request{}, fmt.Errorf("saving the address: %w", err)
	}

	// Confirmed between the lookup and here, from another tab: nothing to
	// send. The claimed mail is not given back; it is one of three.
	if !pending {
		return Request{Outcome: AlreadyOn, SubscriberID: id}, nil
	}

	return Request{Outcome: Sent, SubscriberID: id, Confirm: id.String() + "." + secret}, nil
}

// Confirm puts the holder of a confirmation link on the list.
func (b *Business) Confirm(ctx context.Context, presented string) (Subscriber, error) {
	rawID, secret, found := strings.Cut(presented, ".")
	if !found || secret == "" {
		return Subscriber{}, ErrDenied
	}

	id, err := types.ParseID(rawID)
	if err != nil {
		return Subscriber{}, ErrDenied
	}

	s, ok, err := b.store.Confirm(ctx, id, hash(secret), b.now())

	switch {
	case err != nil:
		return Subscriber{}, fmt.Errorf("confirming the address: %w", err)
	case !ok:
		return Subscriber{}, ErrDenied
	}

	return s, nil
}

// Unsubscribe takes whoever holds token off the list. A token that matches
// nobody is not an error: they are off the list either way, which is all
// the person asking wants to know.
func (b *Business) Unsubscribe(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	if _, err := b.store.Unsubscribe(ctx, token); err != nil {
		return fmt.Errorf("taking the address off the list: %w", err)
	}

	return nil
}

// Remove is a steward taking somebody off the list, when they asked in
// person or by reply.
func (b *Business) Remove(ctx context.Context, id types.ID) error {
	return b.store.Remove(ctx, id)
}

// Confirmed is everybody on the list, earliest first.
func (b *Business) Confirmed(ctx context.Context) ([]Subscriber, error) {
	return b.store.Confirmed(ctx)
}

// PendingCount is how many have signed up and not yet confirmed.
func (b *Business) PendingCount(ctx context.Context) (int, error) {
	return b.store.PendingCount(ctx, b.now())
}

// Prune forgets pending sign-ups whose link has expired, and the record of
// confirmation mail older than the per-address cap looks back.
func (b *Business) Prune(ctx context.Context) error {
	now := b.now()

	return b.store.Prune(ctx, now, now.Add(-24*time.Hour))
}

// newSecret is 32 random bytes in hex. crypto/rand.Read never fails; see
// types.NewID.
func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// hash is what is kept of a confirmation secret, as userbus keeps a sign-in
// link's: a fast hash is right for 256 random bits, which nobody can guess
// their way through however quickly each guess is checked.
func hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))

	return sum[:]
}
