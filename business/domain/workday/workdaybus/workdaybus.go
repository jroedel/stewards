// Package workdaybus is the rules about stewardship days: a morning or an
// afternoon when the stewards are out working the ground and anyone may come
// and help.
//
// A day is when it starts, when it ends, and what the stewards plan to work
// on -- a short title for the list on the home page ("Planting the rain
// garden") and, if there is more to say, the details. It is what the QR code
// on the trail's signs leads to: someone who has never been here reads it
// standing on the path, so the title is what they decide on.
//
// Days are not attached to a place. A morning's work often covers several,
// and the place layers in phase-1-plan.md are about the ground, not about the
// calendar; when a day wants to say where to meet, the details say it. Tools
// to bring and skills asked for are a later iteration, and will be fields
// here rather than a second table, since they belong to one day each.
package workdaybus

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// Day is one stewardship day.
type Day struct {
	ID     types.ID
	Starts time.Time
	Ends   time.Time

	// Title is what will be worked on, in a few words.
	Title types.Text

	// Details is anything else a newcomer would want to know: where to
	// meet, what to wear, what the work is. It may be empty.
	Details types.Text

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is what a steward sets on a day.
type Fields struct {
	Starts  time.Time
	Ends    time.Time
	Title   types.Text
	Details types.Text
}

// Invalid is a day a steward could not save as given, shaped like
// placebus.Invalid: the field it is about and a sentence saying what to do.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

// ErrNotFound is returned when there is no such day.
var ErrNotFound = errors.New("that day is not on the calendar")

// Storer is what the rules need from storage.
type Storer interface {
	Create(ctx context.Context, d Day) error
	Update(ctx context.Context, d Day) error
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (Day, error)

	// EndingAfter is every day that ends after t, soonest first.
	EndingAfter(ctx context.Context, t time.Time) ([]Day, error)

	// EndedBy is the latest days that ended at or before t, most recent
	// first, at most limit of them.
	EndedBy(ctx context.Context, t time.Time, limit int) ([]Day, error)
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

const (
	maxTitle   = 80
	maxDetails = 1000

	// maxLength is the longest a day may run. A real work day is a morning
	// or an afternoon; twelve hours is room for a long one, and anything
	// longer is a slip on the form -- 8 pm for 8 am, or the wrong date --
	// that would otherwise show on the home page as a day running past
	// midnight.
	maxLength = 12 * time.Hour
)

// Create puts a day on the calendar.
//
// A day that has already ended is refused here and not on Update: adding one
// is always a mistake in the date, while editing one that is over is how a
// steward corrects what is on record.
func (b *Business) Create(ctx context.Context, f Fields) (Day, error) {
	f, err := check(f)
	if err != nil {
		return Day{}, err
	}

	now := b.now()
	if !f.Ends.After(now) {
		return Day{}, Invalid{Field: "date", Problem: "that day is already over. Check the date"}
	}

	d := Day{
		ID: types.NewID(), Starts: f.Starts, Ends: f.Ends, Title: f.Title, Details: f.Details,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := b.store.Create(ctx, d); err != nil {
		return Day{}, fmt.Errorf("adding the day: %w", err)
	}

	return d, nil
}

// Update changes a day.
func (b *Business) Update(ctx context.Context, id types.ID, f Fields) (Day, error) {
	f, err := check(f)
	if err != nil {
		return Day{}, err
	}

	d, err := b.store.ByID(ctx, id)
	if err != nil {
		return Day{}, err
	}

	d.Starts, d.Ends, d.Title, d.Details = f.Starts, f.Ends, f.Title, f.Details
	d.UpdatedAt = b.now()

	if err := b.store.Update(ctx, d); err != nil {
		return Day{}, fmt.Errorf("saving the day: %w", err)
	}

	return d, nil
}

// Delete takes a day off the calendar.
func (b *Business) Delete(ctx context.Context, id types.ID) error {
	return b.store.Delete(ctx, id)
}

// ByID is one day.
func (b *Business) ByID(ctx context.Context, id types.ID) (Day, error) {
	return b.store.ByID(ctx, id)
}

// Upcoming is every day not yet over, soonest first. A day under way is
// still upcoming, so that someone who scans the sign at ten on a work
// morning learns the stewards are out there now.
func (b *Business) Upcoming(ctx context.Context) ([]Day, error) {
	return b.store.EndingAfter(ctx, b.now())
}

// Recent is the latest days that are over, most recent first, for the
// stewards' screen: enough to correct one just past, without the list
// growing for ever.
func (b *Business) Recent(ctx context.Context, limit int) ([]Day, error) {
	return b.store.EndedBy(ctx, b.now(), limit)
}

// Now is the business's clock, for a screen that must say "today" or "now"
// by the same clock that decided what is upcoming.
func (b *Business) Now() time.Time { return b.now() }

func check(f Fields) (Fields, error) {
	f.Title = f.Title.Trimmed()
	f.Details = f.Details.Trimmed()

	switch {
	case f.Starts.IsZero():
		return f, Invalid{Field: "date", Problem: "choose the date and the time it starts"}
	case f.Ends.IsZero():
		return f, Invalid{Field: "ends", Problem: "choose the time it ends"}
	case !f.Ends.After(f.Starts):
		return f, Invalid{Field: "ends", Problem: "the end time is before the start. Check am and pm"}
	case f.Ends.Sub(f.Starts) > maxLength:
		return f, Invalid{Field: "ends", Problem: "that is longer than twelve hours. Check am and pm"}
	case f.Title.EN == "":
		return f, Invalid{Field: "title", Problem: "say in a few words what you will work on"}
	case utf8.RuneCountInString(f.Title.EN) > maxTitle || utf8.RuneCountInString(f.Title.ES) > maxTitle:
		return f, Invalid{Field: "title", Problem: fmt.Sprintf("keep the title under %d characters; the rest can go in the details", maxTitle)}
	case utf8.RuneCountInString(f.Details.EN) > maxDetails || utf8.RuneCountInString(f.Details.ES) > maxDetails:
		return f, Invalid{Field: "details", Problem: fmt.Sprintf("keep the details under %d characters", maxDetails)}
	}

	// To the millisecond, which is what the store keeps, so that a day
	// handed back from Create is the one a later read returns.
	f.Starts, f.Ends = f.Starts.Truncate(time.Millisecond), f.Ends.Truncate(time.Millisecond)

	return f, nil
}
