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
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
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

	// All is every day, in no particular order.
	All(ctx context.Context) ([]Day, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	tr    translationbus.Memory
	now   func() time.Time
}

// NewBusiness constructs one. tr is where a day's words find their other
// language; nil is translationbus.None, for a test not about translation. nil
// now means the wall clock.
func NewBusiness(store Storer, tr translationbus.Memory, now func() time.Time) *Business {
	if tr == nil {
		tr = translationbus.None{}
	}

	if now == nil {
		now = time.Now
	}

	return &Business{store: store, tr: tr, now: now}
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

	if err := translationbus.KeepAll(ctx, b.tr, f.texts(), nil); err != nil {
		return Day{}, err
	}

	d := Day{
		ID: types.NewID(), Starts: f.Starts, Ends: f.Ends, Title: f.Title, Details: f.Details,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := b.store.Create(ctx, d); err != nil {
		return Day{}, fmt.Errorf("adding the day: %w", err)
	}

	return b.fill(d), nil
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

	if err := translationbus.KeepAll(ctx, b.tr, f.texts(), d.words()); err != nil {
		return Day{}, err
	}

	d.Starts, d.Ends, d.Title, d.Details = f.Starts, f.Ends, f.Title, f.Details
	d.UpdatedAt = b.now()

	if err := b.store.Update(ctx, d); err != nil {
		return Day{}, fmt.Errorf("saving the day: %w", err)
	}

	return b.fill(d), nil
}

// Delete takes a day off the calendar.
func (b *Business) Delete(ctx context.Context, id types.ID) error {
	return b.store.Delete(ctx, id)
}

// ByID is one day.
func (b *Business) ByID(ctx context.Context, id types.ID) (Day, error) {
	d, err := b.store.ByID(ctx, id)
	if err != nil {
		return Day{}, err
	}

	return b.fill(d), nil
}

// Upcoming is every day not yet over, soonest first. A day under way is
// still upcoming, so that someone who scans the sign at ten on a work
// morning learns the stewards are out there now.
func (b *Business) Upcoming(ctx context.Context) ([]Day, error) {
	return b.filled(b.store.EndingAfter(ctx, b.now()))
}

// Recent is the latest days that are over, most recent first, for the
// stewards' screen: enough to correct one just past, without the list
// growing for ever.
func (b *Business) Recent(ctx context.Context, limit int) ([]Day, error) {
	return b.filled(b.store.EndedBy(ctx, b.now(), limit))
}

// MoveTranslations puts the Spanish that days stored beside their English,
// before the translation memory, into the memory, and leaves each day
// storing its English alone. It reports how many days it changed; see
// placebus's for when it runs.
func (b *Business) MoveTranslations(ctx context.Context) (int, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return 0, err
	}

	n := 0

	for _, d := range all {
		changed, err := translationbus.MoveAll(ctx, b.tr, d.texts()...)
		if err != nil {
			return n, err
		}

		if !changed {
			continue
		}

		if err := b.store.Update(ctx, d); err != nil {
			return n, fmt.Errorf("saving a day: %w", err)
		}

		n++
	}

	return n, nil
}

// Originals is every day's words as stored, with where each is read, for
// the translation memory to find what is waiting for a translation. Days
// that are over included: the stewards' screen still lists the recent ones.
func (b *Business) Originals(ctx context.Context) ([]translationbus.Source, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(all, func(x, y Day) int { return x.Starts.Compare(y.Starts) })

	var out []translationbus.Source

	for _, d := range all {
		day := "the work day on " + d.Starts.In(types.Garden).Format("Monday 2 January 2006")

		for _, s := range []translationbus.Source{
			{Text: d.Title, Where: day + ": its title, on the home page"},
			{Text: d.Details, Where: day + ": its details"},
		} {
			if s.Text != (types.Text{}) {
				out = append(out, s)
			}
		}
	}

	return out, nil
}

func (b *Business) fill(d Day) Day {
	translationbus.FillAll(b.tr, d.texts()...)

	return d
}

func (b *Business) filled(all []Day, err error) ([]Day, error) {
	if err != nil {
		return nil, err
	}

	for i := range all {
		all[i] = b.fill(all[i])
	}

	return all, nil
}

// texts are a day's words, and words a copy of them, in the same order as
// Fields.texts -- the order translationbus.KeepAll pairs them in.
func (d *Day) texts() []*types.Text { return []*types.Text{&d.Title, &d.Details} }

func (d Day) words() []types.Text { return []types.Text{d.Title, d.Details} }

func (f *Fields) texts() []*types.Text { return []*types.Text{&f.Title, &f.Details} }

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
	case !f.Title.Written():
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
