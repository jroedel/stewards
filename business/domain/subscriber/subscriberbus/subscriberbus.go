// Package subscriberbus is the list of people who want to hear about
// stewardship days: an email address and the language they signed up in.
//
// Anyone can sign up from the home page, which is where the QR code on the
// trail's signs leads, so the list is built to be safe for strangers to type
// into.
//
// # One step, not two
//
// An address is on the list as soon as it is sent, and its owner is emailed
// a welcome that carries the link off the list. The first version asked
// them to confirm from an email first (double opt-in); the stewards decided
// against it, because to a person who has just typed their address and
// pressed the button, a second step asks them to consent twice, and nobody
// they had signed up with anywhere else had asked it of them.
//
// What that gives up, so that it is a known trade rather than a forgotten
// one: anybody can put anybody's address on the list, and a typo puts a
// stranger's on it. The welcome is the answer to both. It reaches the owner
// at once, it says what to do if they did not sign up, and its link takes
// the address off in one tap -- and a person whose welcome never came knows
// to check what they typed.
//
// # The caps
//
// The welcome is the only mail a stranger can make this app send, so it is
// the one that is capped: PerAddress to one address in a day, so that
// signing up, leaving and signing up again cannot flood an inbox, and
// PerHour across every address, so that a bot cannot use the form to burn
// the relay's reputation -- the relay that also carries the stewards'
// sign-in links. A capped request adds nobody and sends nothing, and looks
// to the person exactly like one that did; only the log says otherwise.
//
// Every request arrives from Apache on the loopback, so there is no client
// address to cap by (see userbus.MaxLiveLinks for the same reasoning).
//
// # What the page after the form says
//
// The same thing for an address that is new, already on the list, or
// capped. A form that answered differently would tell anybody who is on the
// list.
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
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/types"
)

const (
	// PerAddress is how many welcomes one address may be sent in a day.
	PerAddress = 3

	// PerHour is how many welcomes the whole app may send in an hour. Far
	// above what a sign on a trail produces, and far below what would
	// trouble the relay.
	PerHour = 30
)

// Subscriber is one person on the list.
type Subscriber struct {
	ID    types.ID
	Email types.Email
	Lang  types.Lang

	// CreatedAt is when they signed up.
	CreatedAt time.Time

	// Unsubscribe is the token in every link that takes them off the list.
	Unsubscribe string
}

// Outcome is what came of a request to sign up, for the app to log; the
// person is shown the same page whichever it was.
type Outcome string

const (
	// Added: the address is on the list now, and a welcome is to be sent.
	Added Outcome = "added"

	// AlreadyOn: the address was on the list already, and nothing is sent.
	AlreadyOn Outcome = "already_on"

	// Capped: one of the caps was reached; nobody is added and nothing is
	// sent.
	Capped Outcome = "capped"
)

// Request is the answer to a sign-up.
type Request struct {
	Outcome Outcome

	// Subscriber is the row, when Outcome is Added or AlreadyOn: the welcome
	// needs its address and its unsubscribe token, and the log its ID.
	Subscriber Subscriber
}

// ErrNotFound is returned when there is no such subscriber.
var ErrNotFound = errors.New("that address is not on the list")

// Storer is what the rules need from storage.
type Storer interface {
	ByEmail(ctx context.Context, email types.Email) (Subscriber, error)

	// ClaimMail records one welcome to email at now, as one statement, and
	// only if fewer than perAddress went to it since addressSince and fewer
	// than perHour went to anybody since hourSince. It reports whether the
	// mail may be sent.
	ClaimMail(ctx context.Context, email types.Email, now, addressSince time.Time, perAddress int, hourSince time.Time, perHour int) (bool, error)

	// Add puts s on the list, as one statement. It returns the row as it
	// now stands and true, or the existing row and false when the address
	// was on the list already.
	Add(ctx context.Context, s Subscriber) (Subscriber, bool, error)

	// Unsubscribe deletes the row with this token, and reports whether
	// there was one.
	Unsubscribe(ctx context.Context, token string) (bool, error)

	Remove(ctx context.Context, id types.ID) error

	// All is everybody on the list, earliest first.
	All(ctx context.Context) ([]Subscriber, error)

	// Prune deletes records of mail sent before mailsBefore, and anything
	// left of the double opt-in this package once had; see subscriberdb.
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

// Request puts email on the list, in lang.
//
// The order is the point. An address already on the list is answered before
// anything is claimed, so a stranger retyping it costs nobody a mail. The cap
// is claimed before the row is written, so a flood of new addresses stops
// adding rows at the same moment it stops sending.
func (b *Business) Request(ctx context.Context, email types.Email, lang types.Lang) (Request, error) {
	now := b.now()

	s, err := b.store.ByEmail(ctx, email)

	switch {
	case err == nil:
		return Request{Outcome: AlreadyOn, Subscriber: s}, nil
	case !errors.Is(err, ErrNotFound):
		return Request{}, fmt.Errorf("looking up the address: %w", err)
	}

	ok, err := b.store.ClaimMail(ctx, email, now, now.Add(-24*time.Hour), PerAddress, now.Add(-time.Hour), PerHour)
	if err != nil {
		return Request{}, fmt.Errorf("counting the mail sent: %w", err)
	}

	if !ok {
		return Request{Outcome: Capped}, nil
	}

	row, added, err := b.store.Add(ctx, Subscriber{
		ID: types.NewID(), Email: email, Lang: lang, CreatedAt: now, Unsubscribe: newToken(),
	})
	if err != nil {
		return Request{}, fmt.Errorf("adding the address: %w", err)
	}

	// Added between the lookup and here, from another tab: nothing to send.
	// The claimed mail is not given back; it is one of three.
	if !added {
		return Request{Outcome: AlreadyOn, Subscriber: row}, nil
	}

	return Request{Outcome: Added, Subscriber: row}, nil
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

// All is everybody on the list, earliest first.
func (b *Business) All(ctx context.Context) ([]Subscriber, error) {
	return b.store.All(ctx)
}

// Prune forgets the record of mail older than the per-address cap looks
// back.
func (b *Business) Prune(ctx context.Context) error {
	now := b.now()

	return b.store.Prune(ctx, now, now.Add(-24*time.Hour))
}

// newToken is 32 random bytes in hex: an unsubscribe token nobody can guess.
// crypto/rand.Read never fails; see types.NewID.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}
