package photobus

import (
	"context"
	"fmt"
	"time"

	"github.com/jroedel/stewards/business/types"
)

// Amend is Update for a program: a steward's Claude, through the API,
// correcting what is said about a photo it added or sorted -- the kind it
// shows, the month it was taken, where.
//
// The same two rules as Import, for the same reason. It cannot mark a photo
// checked, which is a steward's word after looking. And a change to a
// checked photo takes the check away, for a steward to give again: what was
// checked is no longer what is said, and a photo filed as a flower that is
// really a leaf is shown on the card as one until somebody looks. A change
// that changes nothing leaves the check where it was, so sending the same
// correction twice is harmless.

// Outcome is what an amendment did.
type Outcome string

const (
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
)

// Amended is a photo after an amendment, and what the amendment did to it.
type Amended struct {
	Photo   Photo
	Outcome Outcome

	// Unchecked is true when the photo was checked and this change took
	// that away.
	Unchecked bool
}

// Amend changes what is said about the photo to f. See the comment above.
func (b *Business) Amend(ctx context.Context, id types.ID, f Fields) (Amended, error) {
	if f.Checked {
		return Amended{}, Invalid{Field: "checked", Problem: "a photo is checked by a steward on the plant's photos screen, after comparing it with the plant, not through the API. Leave checked out"}
	}

	existing, err := b.store.ByID(ctx, id)
	if err != nil {
		return Amended{}, err
	}

	f = keepMoment(f, existing)

	was := FieldsOf(existing)
	was.Checked = false
	if tidy(f) == tidy(was) {
		return Amended{Photo: existing, Outcome: Unchanged}, nil
	}

	p, err := b.Update(ctx, id, f)
	if err != nil {
		return Amended{}, fmt.Errorf("amending a photo: %w", err)
	}

	return Amended{Photo: p, Outcome: Updated, Unchecked: existing.Checked}, nil
}

// FieldsOf is what a steward would send to say what p says: the starting
// point for a change to some of it.
func FieldsOf(p Photo) Fields {
	return Fields{
		Kind: p.Kind, PlaceID: p.PlaceID, Elsewhere: p.Elsewhere, TakenWhere: p.TakenWhere,
		TakenYear: p.TakenYear, TakenMonth: p.TakenMonth, TakenAt: p.TakenAt, InFlower: p.InFlower, InFruit: p.InFruit,
		Source: p.Source, Credit: p.Credit, SourceURL: p.SourceURL, License: p.License,
		Checked: p.Checked,
	}
}

// DayLayout is how a day is written to and read from a person or a program:
// an HTML date input's value, and the API's taken_on.
const DayLayout = "2006-01-02"

// Day is the day s names, at noon in the garden's time: a day said by a
// person has no hour, and noon keeps it the same day in any zone a reader is
// apt to be in. The zero time for "".
func Day(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}

	d, err := time.ParseInLocation(DayLayout, s, types.Garden)
	if err != nil {
		return time.Time{}, Invalid{Field: "taken_on", Problem: "write the day as year, month and day, such as 2027-04-03"}
	}

	return d.Add(12 * time.Hour), nil
}

// DayOf is t's day in the garden's time, as Day reads it; "" for none.
func DayOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.In(types.Garden).Format(DayLayout)
}

// keepMoment keeps the camera's moment when what was sent is a bare day, as
// Day makes one, and the same day: a form or a program that can only say the
// day, sending back the day it was shown, has not changed when the photo was
// taken, and must not round the camera's 9:41 to noon. A moment sent -- the
// camera's, through a sort -- replaces a bare day as it should.
func keepMoment(f Fields, existing Photo) Fields {
	if f.TakenAt.IsZero() || DayOf(f.TakenAt) != DayOf(existing.TakenAt) {
		return f
	}

	if bare, _ := Day(DayOf(f.TakenAt)); bare.Equal(f.TakenAt) {
		f.TakenAt = existing.TakenAt
	}

	return f
}
