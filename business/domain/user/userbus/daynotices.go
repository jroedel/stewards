package userbus

import (
	"context"
	"fmt"

	"github.com/jroedel/stewards/business/types"
)

// SignUps is what a steward is told about people saying they'll come to a
// stewardship day, or taking their name off one. Each steward chooses it for
// themselves, on the stewards screen: there is no "admin" account here
// (every account is a steward), and the stewards decided on 2026-10-22 that
// the one who wants to know is the one who says so.
//
// The zero value is off, so an account made before the choice existed, and
// every account made since, is told nothing until its steward asks.
type SignUps string

const (
	// SignUpsOff: nothing; the Days screen still counts who is coming.
	SignUpsOff SignUps = ""

	// SignUpsEach: a note for each sign-up and each cancellation, as it
	// happens.
	SignUpsEach SignUps = "each"

	// SignUpsDaily: one summary in the morning of the day before's sign-ups
	// and cancellations, and none on a morning with nothing to say.
	SignUpsDaily SignUps = "daily"
)

// ParseSignUps is the choice a form sent, refusing anything else: the value
// reaches the database and, from there, decides who is written to.
func ParseSignUps(s string) (SignUps, error) {
	switch v := SignUps(s); v {
	case SignUpsOff, SignUpsEach, SignUpsDaily:
		return v, nil
	}

	return SignUpsOff, Invalid{Field: "signups", Problem: "choose one of the options shown"}
}

// DayNotices is a steward's choice about the stewardship days, and the
// language the notes are written in.
//
// Evening is apart from SignUps (decided 2026-10-22): the list the evening
// before each day, of everyone coming, is for planning the morning, and a
// steward may want it without hearing of each sign-up, or both.
//
// Lang is the language of the screen they chose on. An account has no
// language of its own -- the screens follow whoever is reading -- and a note
// sent from the background has nobody reading, so the choice carries the
// best guess there is: the language the steward was reading when they made
// it.
type DayNotices struct {
	SignUps SignUps
	Evening bool
	Lang    types.Lang
}

// SetDayNotices records what steward id wants to be told.
//
// One statement that sets these columns and no others, rather than a read of
// the account and an UpdateUser of all of it: another steward turning this
// account off at the same moment must not be undone by it.
func (b *Business) SetDayNotices(ctx context.Context, id types.ID, n DayNotices) error {
	if _, err := ParseSignUps(string(n.SignUps)); err != nil {
		return err
	}

	if _, err := types.ParseLang(string(n.Lang)); err != nil {
		n.Lang = types.English
	}

	if err := b.store.SetDayNotices(ctx, id, n, b.now()); err != nil {
		return fmt.Errorf("saving what the steward is told: %w", err)
	}

	return nil
}

// TellEvening is every steward who can sign in and asked for the list the
// evening before each day.
func (b *Business) TellEvening(ctx context.Context) ([]User, error) {
	return b.tell(ctx, func(n DayNotices) bool { return n.Evening })
}

// TellSignUps is every steward who can sign in and chose s: who a note
// about a sign-up goes to. A steward who has been turned off is told
// nothing, whatever they chose before.
func (b *Business) TellSignUps(ctx context.Context, s SignUps) ([]User, error) {
	return b.tell(ctx, func(n DayNotices) bool { return n.SignUps == s })
}

// tell is every steward who can sign in and whose choice is wants.
func (b *Business) tell(ctx context.Context, wants func(DayNotices) bool) ([]User, error) {
	all, err := b.store.Users(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the stewards: %w", err)
	}

	var out []User

	for _, u := range all {
		if u.Enabled && wants(u.DayNotices) {
			out = append(out, u)
		}
	}

	return out, nil
}
