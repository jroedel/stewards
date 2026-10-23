// Package rsvpbus is the rules about saying "I'll be there" to a
// stewardship day: a name to show, and an email, a phone or both to be
// reached by.
//
// # The name is public, and that is the point
//
// The day's page lists who is coming, by the name each person chose to show
// -- the stewards asked for it because people come when their friends are
// (2026-10-21). It is the one place a volunteer's name appears to strangers,
// which design.md allows because they put it there themselves ("Individual
// names appear only where a person chose to add theirs"). So the form says it
// will be shown, the name is whatever they typed and nothing more, and a
// steward can take one off the page.
//
// Because it is a stranger's words on a public page, it is held to a
// display name's shape: short, one line, and no web address, which is what
// a name field on a public page attracts first.
//
// # Contact, like the list's
//
// The same as subscriberbus, per day: a person is one row for a day,
// whichever halves they gave, and signing up again with a half the day does
// not have adds it. Signing up again with the same halves changes the name
// and sends nothing, so a person can correct a typo in it.
//
// A confirmation is a message a stranger can cause, so it is capped by
// noticebus like the list's welcome. The page after the form says the same
// whatever happened, as the list's does.
package rsvpbus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
)

// CancelsKept is how long a cancellation is remembered: long enough for the
// stewards' once-a-day summary to have said it, with weeks to spare for a
// server that was down, and no longer, since it is a person's name.
const CancelsKept = 30 * 24 * time.Hour

// MaxName is the longest name shown, in characters: a first name and a
// surname, or "Ana and the kids", and not a sentence.
const MaxName = 40

// RSVP is one person coming to one day.
type RSVP struct {
	ID      types.ID
	DayID   types.ID
	Name    string
	Contact types.Contact
	Lang    types.Lang

	// Cancel is the token in the link that takes them off the day.
	Cancel string

	CreatedAt time.Time
}

// Cancelled is a sign-up its person took off the day with their own link:
// the name and when, without the contact, which nothing needs any more.
type Cancelled struct {
	ID        types.ID
	DayID     types.ID
	Name      string
	CreatedAt time.Time
	At        time.Time
}

// Changes is who signed up and who cancelled between two times, for the
// stewards' summary. Joined is the sign-ups still on their day; one made
// and cancelled in the same span is in Cancelled alone.
type Changes struct {
	Joined    []RSVP
	Cancelled []Cancelled
}

// Zero reports whether nothing changed.
func (c Changes) Zero() bool { return len(c.Joined) == 0 && len(c.Cancelled) == 0 }

// Outcome is what came of a sign-up, for the app to log.
type Outcome string

const (
	// Added: someone new is coming, or a new way to reach them was given;
	// a confirmation is to go to it.
	Added Outcome = "added"

	// AlreadyOn: everything given was on the day already. The name may
	// have changed; nothing is sent.
	AlreadyOn Outcome = "already_on"

	// Capped: a cap was reached; nothing is added or sent.
	Capped Outcome = "capped"
)

// Request is the answer to a sign-up.
type Request struct {
	Outcome Outcome
	RSVP    RSVP

	// New is what the confirmation goes to: the halves not on the day
	// before.
	New types.Contact

	// Joined is a name new to the day, rather than a phone or an email
	// added to someone already on it. It is what the stewards who asked
	// are told about: the same person giving a second way to reach them is
	// not one more person coming.
	Joined bool
}

// Invalid is a sign-up refused for what was typed. Field is "name"; Kind is
// which rule, so the page can say it in the reader's language: "blank",
// "long", "link" or "letters".
type Invalid struct{ Field, Kind, Problem string }

func (i Invalid) Error() string { return i.Field + ": " + i.Problem }

var (
	// ErrNotFound is a day or a sign-up that does not exist.
	ErrNotFound = errors.New("that is not on the calendar")

	// ErrOver is a day that has ended: nobody can say they will come.
	ErrOver = errors.New("that day is over")
)

// Storer is what the rules need from storage.
type Storer interface {
	// ByContact is every sign-up for day holding either half of c.
	ByContact(ctx context.Context, day types.ID, c types.Contact) ([]RSVP, error)

	// Add stores r, and reports false when a half of it was on the day
	// already.
	Add(ctx context.Context, r RSVP) (bool, error)

	// Extend fills in the halves of c the row has empty and sets its
	// name, as one statement, and reports false when a half was taken.
	Extend(ctx context.Context, id types.ID, name string, c types.Contact) (bool, error)

	// Rename sets a row's name.
	Rename(ctx context.Context, id types.ID, name string) error

	ByID(ctx context.Context, id types.ID) (RSVP, error)
	ByCancel(ctx context.Context, token string) (RSVP, error)

	// ForDay is every sign-up for day, earliest first.
	ForDay(ctx context.Context, day types.ID) ([]RSVP, error)

	// Counts is how many are coming to each of days.
	Counts(ctx context.Context, days []types.ID) (map[types.ID]int, error)

	// Delete removes a row by ID, reporting whether there was one.
	Delete(ctx context.Context, id types.ID) (bool, error)

	// Cancel removes r's row and keeps it as a Cancelled at at, as one
	// transaction, reporting whether there was a row.
	Cancel(ctx context.Context, r RSVP, at time.Time) (bool, error)

	// Joined is every row made in [from, to), and Cancels every
	// cancellation in it, each earliest first.
	Joined(ctx context.Context, from, to time.Time) ([]RSVP, error)
	Cancels(ctx context.Context, from, to time.Time) ([]Cancelled, error)

	// PruneCancels forgets cancellations made before before.
	PruneCancels(ctx context.Context, before time.Time) error

	// Stopped takes a number that replied STOP off every day.
	Stopped(ctx context.Context, p types.Phone) error
}

// Days is what the rules need from the work-day rules: whether the day is
// there, and whether it is over.
type Days interface {
	ByID(ctx context.Context, id types.ID) (workdaybus.Day, error)
}

// Claimer is the caps on messages a stranger can cause; noticebus keeps them.
type Claimer interface {
	Claim(ctx context.Context, c types.Contact) (bool, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store  Storer
	days   Days
	claims Claimer
	now    func() time.Time
}

// NewBusiness constructs one; nil now means the wall clock.
func NewBusiness(store Storer, days Days, claims Claimer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, days: days, claims: claims, now: now}
}

// Name checks and tidies a display name: inner runs of space become one,
// and the ends are trimmed. Exported so the form can check it before the
// contact, and say both problems at once.
func Name(s string) (string, error) {
	name := strings.Join(strings.Fields(s), " ")

	switch {
	case name == "":
		return "", Invalid{Field: "name", Kind: "blank", Problem: "write the name you'd like shown, such as your first name"}
	case utf8.RuneCountInString(name) > MaxName:
		return "", Invalid{Field: "name", Kind: "long", Problem: fmt.Sprintf("keep the name to %d letters or fewer", MaxName)}
	}

	lower := strings.ToLower(name)
	for _, link := range []string{"://", "www.", ".com", ".org", ".net", "@"} {
		if strings.Contains(lower, link) {
			return "", Invalid{Field: "name", Kind: "link", Problem: "write a name, not an address or a link"}
		}
	}

	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsMark(r) && !unicode.IsSpace(r) && !strings.ContainsRune("'’-.&,()", r) && !unicode.IsDigit(r) {
			return "", Invalid{Field: "name", Kind: "letters", Problem: "use letters in the name; a few marks like - and ' are fine"}
		}
	}

	return name, nil
}

// Request signs c up for day under name, in lang. The order is subscriberbus's:
// what is there already is answered before anything is claimed, and the cap
// is claimed before a row is written.
func (b *Business) Request(ctx context.Context, day types.ID, name string, c types.Contact, lang types.Lang) (Request, error) {
	name, err := Name(name)
	if err != nil {
		return Request{}, err
	}

	if c.Zero() {
		return Request{}, types.ContactInvalid{Err: types.ErrNoContact}
	}

	if _, err := b.Upcoming(ctx, day); err != nil {
		return Request{}, err
	}

	rows, err := b.store.ByContact(ctx, day, c)
	if err != nil {
		return Request{}, fmt.Errorf("looking up the sign-up: %w", err)
	}

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
		row := rows[0]

		if row.Name != name {
			if err := b.store.Rename(ctx, row.ID, name); err != nil {
				return Request{}, fmt.Errorf("changing the name: %w", err)
			}

			row.Name = name
		}

		return Request{Outcome: AlreadyOn, RSVP: row}, nil
	}

	ok, err := b.claims.Claim(ctx, fresh)
	if err != nil {
		return Request{}, fmt.Errorf("counting the messages sent: %w", err)
	}

	if !ok {
		return Request{Outcome: Capped}, nil
	}

	if len(rows) == 1 && fits(rows[0].Contact, fresh) {
		row := rows[0]

		extended, err := b.store.Extend(ctx, row.ID, name, fresh)
		if err != nil {
			return Request{}, fmt.Errorf("adding to a sign-up: %w", err)
		}

		if !extended {
			return Request{Outcome: AlreadyOn, RSVP: row}, nil
		}

		if row, err = b.store.ByID(ctx, row.ID); err != nil {
			return Request{}, fmt.Errorf("reading the sign-up back: %w", err)
		}

		return Request{Outcome: Added, RSVP: row, New: fresh}, nil
	}

	row := RSVP{
		ID: types.NewID(), DayID: day, Name: name, Contact: fresh, Lang: lang,
		Cancel: newToken(), CreatedAt: b.now(),
	}

	added, err := b.store.Add(ctx, row)
	if err != nil {
		return Request{}, fmt.Errorf("signing up: %w", err)
	}

	if !added {
		return Request{Outcome: AlreadyOn}, nil
	}

	return Request{Outcome: Added, RSVP: row, New: fresh, Joined: true}, nil
}

func fits(row, fresh types.Contact) bool {
	return (fresh.Email.Zero() || row.Email.Zero()) && (fresh.Phone.Zero() || row.Phone.Zero())
}

// Upcoming is the day, if it is there and not over: a sign-up for a day
// under way is fine -- someone scanning the sign at ten may still join --
// and one for a day that has ended is not.
func (b *Business) Upcoming(ctx context.Context, day types.ID) (workdaybus.Day, error) {
	d, err := b.days.ByID(ctx, day)

	switch {
	case errors.Is(err, workdaybus.ErrNotFound):
		return workdaybus.Day{}, ErrNotFound
	case err != nil:
		return workdaybus.Day{}, fmt.Errorf("reading the day: %w", err)
	case !b.now().Before(d.Ends):
		return d, ErrOver
	}

	return d, nil
}

// Coming is the names shown for day, earliest first.
func (b *Business) Coming(ctx context.Context, day types.ID) ([]string, error) {
	rows, err := b.store.ForDay(ctx, day)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}

	return names, nil
}

// ForDay is every sign-up for day, with contacts: for the stewards, and for
// writing to the people coming.
func (b *Business) ForDay(ctx context.Context, day types.ID) ([]RSVP, error) {
	return b.store.ForDay(ctx, day)
}

// Counts is how many are coming to each of days; a day nobody has signed up
// for is absent.
func (b *Business) Counts(ctx context.Context, days []types.ID) (map[types.ID]int, error) {
	if len(days) == 0 {
		return map[types.ID]int{}, nil
	}

	return b.store.Counts(ctx, days)
}

// ByCancel is the sign-up a cancel link is for.
func (b *Business) ByCancel(ctx context.Context, token string) (RSVP, error) {
	if token == "" {
		return RSVP{}, ErrNotFound
	}

	return b.store.ByCancel(ctx, token)
}

// Cancel takes whoever holds token off their day, and gives back the
// sign-up it took off, for the stewards who asked to be told. A token that
// matches nobody is not an error -- they are off it either way -- and gives
// back the zero RSVP, as does the second of two presses: only the press
// whose delete removed the row has someone to tell the stewards about.
func (b *Business) Cancel(ctx context.Context, token string) (RSVP, error) {
	r, err := b.ByCancel(ctx, token)

	switch {
	case errors.Is(err, ErrNotFound):
		return RSVP{}, nil
	case err != nil:
		return RSVP{}, err
	}

	off, err := b.store.Cancel(ctx, r, b.now())
	if err != nil {
		return RSVP{}, fmt.Errorf("cancelling: %w", err)
	}

	if !off {
		return RSVP{}, nil
	}

	return r, nil
}

// Changes is who signed up and who cancelled in [from, to).
func (b *Business) Changes(ctx context.Context, from, to time.Time) (Changes, error) {
	joined, err := b.store.Joined(ctx, from, to)
	if err != nil {
		return Changes{}, err
	}

	cancelled, err := b.store.Cancels(ctx, from, to)
	if err != nil {
		return Changes{}, err
	}

	return Changes{Joined: joined, Cancelled: cancelled}, nil
}

// Prune forgets cancellations older than CancelsKept.
func (b *Business) Prune(ctx context.Context) error {
	return b.store.PruneCancels(ctx, b.now().Add(-CancelsKept))
}

// Remove is a steward taking a sign-up off a day: a name that should not be
// on a public page, or someone who said in person they cannot come.
func (b *Business) Remove(ctx context.Context, id types.ID) error {
	ok, err := b.store.Delete(ctx, id)

	switch {
	case err != nil:
		return fmt.Errorf("removing a sign-up: %w", err)
	case !ok:
		return ErrNotFound
	}

	return nil
}

// Stopped takes a number that replied STOP off every day, keeping the
// sign-up of a person who also gave an email.
func (b *Business) Stopped(ctx context.Context, p types.Phone) error {
	return b.store.Stopped(ctx, p)
}

// newToken is 32 random bytes in hex, as subscriberbus's.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}
