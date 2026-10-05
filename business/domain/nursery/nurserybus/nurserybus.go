// Package nurserybus is the rules about nursery stock: what a nursery had on
// its tables on the day a steward walked round it with a phone.
//
// It is for planning a bed about to be planted. The nurseries' stock changes
// every week, so what matters is the latest visit to each; the earlier ones
// are kept, because "they had Turk's cap in April" is cheap to keep and
// useful to know, but they are history rather than a catalogue.
//
// A visit is a nursery on a day. A line is one plant on its tables, as the tag
// gives it: the name on the tag, and -- when a steward or their Claude has
// matched it -- the plant here it is, by which the bed's plan can ask whether
// it is native and will take the light. Pot size and price are written as
// the tag writes them, because nurseries mix inches, gallons and "#1".
//
// Nothing here is shown to volunteers. A plant for sale is not a plant in the
// garden, and nothing on a stock list is an identification.
package nurserybus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// Visit is a nursery on a day.
type Visit struct {
	ID      types.ID
	Nursery string

	// Day is the garden's midnight at the start of the day it was visited.
	Day time.Time

	CreatedAt time.Time
}

// Line is one plant a nursery had.
type Line struct {
	ID      types.ID
	VisitID types.ID

	// SpeciesID is the plant here it is, once matched; zero for not yet.
	SpeciesID types.ID

	NameOnTag string
	PotSize   string

	// PriceCents is the price in cents; 0 for not noted.
	PriceCents int

	// Count is how many were on the table; 0 for not counted.
	Count int
	Note  string

	// InboxID is the inbox photo of it, or of its tag; zero for none.
	InboxID types.ID

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is what is said about a line.
type Fields struct {
	SpeciesID  types.ID
	NameOnTag  string
	PotSize    string
	PriceCents int
	Count      int
	Note       string
}

// Stock is a visit with its lines.
type Stock struct {
	Visit Visit
	Lines []Line
}

// Invalid is stock that could not be kept as given, shaped like the other
// domains'.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

// ErrNotFound is returned for a line or a visit that does not exist.
var ErrNotFound = errors.New("there is no such nursery stock")

// Storer is what the rules need from the database.
type Storer interface {
	// Visit finds the visit for a nursery on a day, by its name ignoring
	// case, or makes it: one statement that cannot make two.
	Visit(ctx context.Context, v Visit) (Visit, error)
	CreateLine(ctx context.Context, l Line) error
	UpdateLine(ctx context.Context, l Line) error
	Line(ctx context.Context, id types.ID) (Line, error)
	Visits(ctx context.Context) ([]Visit, error)
	Lines(ctx context.Context) ([]Line, error)
}

// Business holds the rules.
type Business struct {
	store Storer
	now   func() time.Time
}

// NewBusiness constructs one. now may be nil, for the real clock.
func NewBusiness(store Storer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, now: now}
}

const (
	maxName = 100
	maxPot  = 40
	maxNote = 300
)

// Add records a line of a nursery's stock on the day it was seen, making the
// visit if it is the first line of that day.
func (b *Business) Add(ctx context.Context, nursery string, seen time.Time, inboxID types.ID, f Fields) (Line, error) {
	nursery = strings.Join(strings.Fields(nursery), " ")

	switch {
	case nursery == "":
		return Line{}, Invalid{Field: "nursery", Problem: "say which nursery it was"}
	case utf8.RuneCountInString(nursery) > maxName:
		return Line{}, Invalid{Field: "nursery", Problem: fmt.Sprintf("the nursery's name is longer than %d characters", maxName)}
	}

	f, err := check(f)
	if err != nil {
		return Line{}, err
	}

	now := b.now().UTC().Truncate(time.Millisecond)

	v, err := b.store.Visit(ctx, Visit{ID: types.NewID(), Nursery: nursery, Day: DayOf(seen), CreatedAt: now})
	if err != nil {
		return Line{}, err
	}

	l := Line{ID: types.NewID(), VisitID: v.ID, InboxID: inboxID, CreatedAt: now, UpdatedAt: now}
	l.apply(f)

	if err := b.store.CreateLine(ctx, l); err != nil {
		return Line{}, err
	}

	return l, nil
}

// Update corrects a line: a tag read wrongly, a plant matched, a price.
func (b *Business) Update(ctx context.Context, id types.ID, f Fields) (Line, error) {
	l, err := b.store.Line(ctx, id)
	if err != nil {
		return Line{}, err
	}

	if f, err = check(f); err != nil {
		return Line{}, err
	}

	l.apply(f)
	l.UpdatedAt = b.now().UTC().Truncate(time.Millisecond)

	if err := b.store.UpdateLine(ctx, l); err != nil {
		return Line{}, err
	}

	return l, nil
}

// Line is one line, or ErrNotFound.
func (b *Business) Line(ctx context.Context, id types.ID) (Line, error) {
	return b.store.Line(ctx, id)
}

// All is every visit with its lines, the most recent visit first, and in a
// visit the lines in the order they were added: the order of the walk round
// the tables.
func (b *Business) All(ctx context.Context) ([]Stock, error) {
	visits, err := b.store.Visits(ctx)
	if err != nil {
		return nil, err
	}

	lines, err := b.store.Lines(ctx)
	if err != nil {
		return nil, err
	}

	at := map[types.ID]int{}
	out := make([]Stock, len(visits))

	for i, v := range visits {
		at[v.ID] = i
		out[i].Visit = v
	}

	for _, l := range lines {
		if i, ok := at[l.VisitID]; ok {
			out[i].Lines = append(out[i].Lines, l)
		}
	}

	return out, nil
}

// LastNursery is the nursery of the most recent visit on the day, if there was
// one: what the next tag photo from that day is most likely of.
func (b *Business) LastNursery(ctx context.Context, seen time.Time) (string, error) {
	visits, err := b.store.Visits(ctx)
	if err != nil {
		return "", err
	}

	day := DayOf(seen)
	for _, v := range visits {
		if v.Day.Equal(day) {
			return v.Nursery, nil
		}
	}

	return "", nil
}

// Nurseries is the name of every nursery visited, the most recently visited
// first, for a steward to choose from rather than type.
func (b *Business) Nurseries(ctx context.Context) ([]string, error) {
	visits, err := b.store.Visits(ctx)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	var names []string
	for _, v := range visits {
		if k := strings.ToLower(v.Nursery); !seen[k] {
			seen[k] = true
			names = append(names, v.Nursery)
		}
	}

	return names, nil
}

// DayOf is the garden's midnight at the start of the day t falls on.
func DayOf(t time.Time) time.Time {
	y, m, d := t.In(types.Garden).Date()

	return time.Date(y, m, d, 0, 0, 0, 0, types.Garden).UTC()
}

// ParsePrice reads a price as a person writes it -- "12", "$12.99", "12.5" --
// into cents. Empty is 0, not noted.
func ParsePrice(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return 0, nil
	}

	dollars, cents, hasCents := strings.Cut(s, ".")

	d, err := strconv.Atoi(dollars)
	if err != nil || d < 0 {
		return 0, errors.New("write the price as dollars and cents, such as 12.99")
	}

	c := 0
	if hasCents {
		switch len(cents) {
		case 1:
			cents += "0"
		case 2:
		default:
			return 0, errors.New("write the price as dollars and cents, such as 12.99")
		}

		if c, err = strconv.Atoi(cents); err != nil || c < 0 {
			return 0, errors.New("write the price as dollars and cents, such as 12.99")
		}
	}

	return d*100 + c, nil
}

// PriceWords is a price in cents as a person writes it, or "" for not noted.
func PriceWords(cents int) string {
	if cents <= 0 {
		return ""
	}

	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

func check(f Fields) (Fields, error) {
	f.NameOnTag = strings.Join(strings.Fields(f.NameOnTag), " ")
	f.PotSize = strings.TrimSpace(f.PotSize)
	f.Note = strings.TrimSpace(f.Note)

	switch {
	case f.NameOnTag == "" && f.SpeciesID.Zero():
		return f, Invalid{Field: "name_on_tag", Problem: "write the name on the tag, or choose the plant it is"}
	case utf8.RuneCountInString(f.NameOnTag) > maxName:
		return f, Invalid{Field: "name_on_tag", Problem: fmt.Sprintf("the name on the tag is longer than %d characters", maxName)}
	case utf8.RuneCountInString(f.PotSize) > maxPot:
		return f, Invalid{Field: "pot_size", Problem: fmt.Sprintf(`write the pot size as the tag does, such as "1 gal" or "4 in", in under %d characters`, maxPot)}
	case f.PriceCents < 0 || f.PriceCents > 1_000_000:
		return f, Invalid{Field: "price", Problem: "write the price as dollars and cents, such as 12.99"}
	case f.Count < 0 || f.Count > 10_000:
		return f, Invalid{Field: "count", Problem: "write how many there were as a number, or leave it empty"}
	case utf8.RuneCountInString(f.Note) > maxNote:
		return f, Invalid{Field: "note", Problem: fmt.Sprintf("keep the note under %d characters", maxNote)}
	}

	return f, nil
}

func (l *Line) apply(f Fields) {
	l.SpeciesID, l.NameOnTag, l.PotSize = f.SpeciesID, f.NameOnTag, f.PotSize
	l.PriceCents, l.Count, l.Note = f.PriceCents, f.Count, f.Note
}
