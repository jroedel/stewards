// Package subscriberbus is the list of people who want to hear about
// stewardship days: an email address, a phone number or both, and the
// language they signed up in.
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
// The welcome is a message a stranger can make this app send, so it is
// capped, by noticebus.Business.Claim: a few to one address or number in a
// day, so that signing up, leaving and signing up again cannot flood an
// inbox or a phone, and a ceiling across everybody per hour, so that a bot
// cannot use the form to burn the relay's reputation or the Twilio bill. A
// capped request adds nobody and sends nothing, and looks to the person
// exactly like one that did; only the log says otherwise.
//
// # Email, phone, or both
//
// A person is one row, whichever halves they gave. Signing up again with a
// half the list does not have yet adds it -- to their row when the row has
// that half empty, or as a row of its own when the half they already gave
// belongs to somebody else's -- and welcomes only the new half. A half
// already on the list is never moved or changed by a stranger's form.
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

// Subscriber is one person on the list.
type Subscriber struct {
	ID      types.ID
	Contact types.Contact
	Lang    types.Lang

	// CreatedAt is when they signed up.
	CreatedAt time.Time

	// Unsubscribe is the token in every link that takes them off the list.
	Unsubscribe string
}

// Outcome is what came of a request to sign up, for the app to log; the
// person is shown the same page whichever it was.
type Outcome string

const (
	// Added: something new is on the list -- a person, or a half of one --
	// and a welcome is to be sent to it.
	Added Outcome = "added"

	// AlreadyOn: everything given was on the list already, and nothing is
	// sent.
	AlreadyOn Outcome = "already_on"

	// Capped: a cap was reached; nobody is added and nothing is sent.
	Capped Outcome = "capped"
)

// Request is the answer to a sign-up.
type Request struct {
	Outcome Outcome

	// Subscriber is the row the request ended at, when there is one: the
	// welcome needs its unsubscribe token, and the log its ID.
	Subscriber Subscriber

	// New is what the welcome goes to: the halves of the request that were
	// not on the list before it.
	New types.Contact
}

// ErrNotFound is returned when there is no such subscriber.
var ErrNotFound = errors.New("that person is not on the list")

// Storer is what the rules need from storage.
type Storer interface {
	// ByContact is every row holding either half of c.
	ByContact(ctx context.Context, c types.Contact) ([]Subscriber, error)

	// Add puts s on the list, as one statement, and reports false when a
	// half of it was on the list already.
	Add(ctx context.Context, s Subscriber) (bool, error)

	// Extend fills in the halves of c the row has empty, as one statement,
	// and reports false when nothing was filled in.
	Extend(ctx context.Context, id types.ID, c types.Contact) (bool, error)

	ByID(ctx context.Context, id types.ID) (Subscriber, error)

	// Unsubscribe deletes the row with this token, and reports whether
	// there was one.
	Unsubscribe(ctx context.Context, token string) (bool, error)

	Remove(ctx context.Context, id types.ID) error

	// Stopped takes a number that replied STOP off the list.
	Stopped(ctx context.Context, p types.Phone) error

	// All is everybody on the list, earliest first.
	All(ctx context.Context) ([]Subscriber, error)
}

// Claimer is the caps on messages a stranger can cause; noticebus keeps them.
type Claimer interface {
	Claim(ctx context.Context, c types.Contact) (bool, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store  Storer
	claims Claimer
	now    func() time.Time
}

// NewBusiness constructs one; nil now means the wall clock.
func NewBusiness(store Storer, claims Claimer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, claims: claims, now: now}
}

// Request puts c on the list, in lang.
//
// The order is the point. What is on the list already is answered before
// anything is claimed, so a stranger retyping an address costs nobody a
// message. The cap is claimed before the row is written, so a flood of new
// addresses stops adding rows at the same moment it stops sending.
func (b *Business) Request(ctx context.Context, c types.Contact, lang types.Lang) (Request, error) {
	if c.Zero() {
		return Request{}, types.ContactInvalid{Err: types.ErrNoContact}
	}

	rows, err := b.store.ByContact(ctx, c)
	if err != nil {
		return Request{}, fmt.Errorf("looking up the contact: %w", err)
	}

	// What is new: the halves no row holds.
	fresh := c
	for _, row := range rows {
		if row.Contact.Email == c.Email {
			fresh.Email = types.Email{}
		}

		if row.Contact.Phone == c.Phone {
			fresh.Phone = types.Phone{}
		}
	}

	if fresh.Zero() {
		return Request{Outcome: AlreadyOn, Subscriber: rows[0]}, nil
	}

	ok, err := b.claims.Claim(ctx, fresh)
	if err != nil {
		return Request{}, fmt.Errorf("counting the messages sent: %w", err)
	}

	if !ok {
		return Request{Outcome: Capped}, nil
	}

	// One row already holds the other half, and has room for the new one:
	// it is the same person, giving the list a second way to reach them.
	if len(rows) == 1 && fits(rows[0].Contact, fresh) {
		row := rows[0]

		extended, err := b.store.Extend(ctx, row.ID, fresh)
		if err != nil {
			return Request{}, fmt.Errorf("adding to a subscriber: %w", err)
		}

		// Taken by another request between the lookup and here: nothing to
		// send. The claim is not given back; it is one of a few.
		if !extended {
			return Request{Outcome: AlreadyOn, Subscriber: row}, nil
		}

		if row, err = b.store.ByID(ctx, row.ID); err != nil {
			return Request{}, fmt.Errorf("reading the subscriber back: %w", err)
		}

		return Request{Outcome: Added, Subscriber: row, New: fresh}, nil
	}

	row := Subscriber{ID: types.NewID(), Contact: fresh, Lang: lang, CreatedAt: b.now(), Unsubscribe: newToken()}

	added, err := b.store.Add(ctx, row)
	if err != nil {
		return Request{}, fmt.Errorf("adding to the list: %w", err)
	}

	if !added {
		return Request{Outcome: AlreadyOn}, nil
	}

	return Request{Outcome: Added, Subscriber: row, New: fresh}, nil
}

// fits reports whether row has each half of fresh empty.
func fits(row, fresh types.Contact) bool {
	return (fresh.Email.Zero() || row.Email.Zero()) && (fresh.Phone.Zero() || row.Phone.Zero())
}

// Unsubscribe takes whoever holds token off the list, email and phone both.
// A token that matches nobody is not an error: they are off the list either
// way, which is all the person asking wants to know.
func (b *Business) Unsubscribe(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	if _, err := b.store.Unsubscribe(ctx, token); err != nil {
		return fmt.Errorf("taking a person off the list: %w", err)
	}

	return nil
}

// Remove is a steward taking somebody off the list, when they asked in
// person or by reply.
func (b *Business) Remove(ctx context.Context, id types.ID) error {
	return b.store.Remove(ctx, id)
}

// Stopped takes a number that replied STOP off the list, keeping the email
// of a person who gave both: STOP is about texts.
func (b *Business) Stopped(ctx context.Context, p types.Phone) error {
	return b.store.Stopped(ctx, p)
}

// All is everybody on the list, earliest first.
func (b *Business) All(ctx context.Context) ([]Subscriber, error) {
	return b.store.All(ctx)
}

// newToken is 32 random bytes in hex: an unsubscribe token nobody can guess.
// crypto/rand.Read never fails; see types.NewID.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}
