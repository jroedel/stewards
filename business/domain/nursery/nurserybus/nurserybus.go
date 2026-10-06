// Package nurserybus is the rules about nursery stock: what a nursery had on
// its tables on the day a steward walked round it with a phone.
//
// It is for planning a bed about to be planted. The nurseries' stock changes
// every week, so what matters is the latest visit to each; the earlier ones
// are kept, because "they had Turk's cap in April" is cheap to keep and
// useful to know, but they are history rather than a catalogue.
//
// The nurseries themselves are a register: a name a steward chose once, with
// the address, website, phone and a note on what to know about the place
// ("natives are in the back greenhouse"). A nursery is added there, or by
// typing a new name when a tag photo is sorted, which must never wait on a
// trip to another screen. Either way the name is the register's from then on,
// so "Natural Gardener" and "the natural gardener" are one nursery with one
// history rather than two that each looked a week out of date.
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
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// Nursery is one nursery in the register.
type Nursery struct {
	ID      types.ID
	Name    string
	Address string
	Website string
	Phone   string
	Note    string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NurseryFields is what a steward says about a nursery.
type NurseryFields struct {
	Name, Address, Website, Phone, Note string
}

// Visit is a nursery on a day.
type Visit struct {
	ID        types.ID
	NurseryID types.ID

	// Nursery is its name, as the register has it now.
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

// ErrNotFound is returned for a nursery, a line or a visit that does not
// exist.
var ErrNotFound = errors.New("there is no such nursery stock")

// ErrDuplicate is a store's answer to a nursery named as another already is,
// ignoring case: the insert or update is the check.
var ErrDuplicate = errors.New("there is already a nursery by that name")

// ErrInUse is a store's answer to removing a nursery that has visits.
var ErrInUse = errors.New("that nursery has visits")

// Storer is what the rules need from the database.
type Storer interface {
	// EnsureNursery finds the nursery by its name ignoring case, or adds n:
	// one statement that cannot add two.
	EnsureNursery(ctx context.Context, n Nursery) (Nursery, error)
	CreateNursery(ctx context.Context, n Nursery) error
	UpdateNursery(ctx context.Context, n Nursery) error

	// DeleteNursery removes a nursery with no visits, in one statement:
	// ErrInUse if it has one, ErrNotFound if it is not there.
	DeleteNursery(ctx context.Context, id types.ID) error
	NurseryByID(ctx context.Context, id types.ID) (Nursery, error)

	// Register is every nursery, by name.
	Register(ctx context.Context) ([]Nursery, error)

	// Visit finds the visit for a nursery on a day or makes it: one
	// statement that cannot make two.
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
	maxName    = 100
	maxPot     = 40
	maxNote    = 300
	maxAddress = 200
	maxWebsite = 300
	maxPhone   = 40
)

// Add records a line of a nursery's stock on the day it was seen, making the
// visit if it is the first line of that day. nursery is a name: one in the
// register, however it is typed, or a new one, which is added to it.
func (b *Business) Add(ctx context.Context, nursery string, seen time.Time, inboxID types.ID, f Fields) (Line, error) {
	nursery = oneLine(nursery)

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

	n, err := b.store.EnsureNursery(ctx, Nursery{ID: types.NewID(), Name: nursery, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Line{}, err
	}

	v, err := b.store.Visit(ctx, Visit{ID: types.NewID(), NurseryID: n.ID, Nursery: n.Name, Day: DayOf(seen), CreatedAt: now})
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

// Nurseries is the name of every nursery in the register, for a steward to
// choose from rather than type: the most recently visited first, since the
// next tag photo is likeliest to be from there, then the rest by name.
func (b *Business) Nurseries(ctx context.Context) ([]string, error) {
	register, err := b.store.Register(ctx)
	if err != nil {
		return nil, err
	}

	last, err := b.LastVisits(ctx)
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(register, func(x, y Nursery) int {
		return last[y.ID].Compare(last[x.ID]) // zero, never visited, sorts last
	})

	names := make([]string, len(register))
	for i, n := range register {
		names[i] = n.Name
	}

	return names, nil
}

// LastVisits is the day of each nursery's most recent visit, by its id. A
// nursery never visited is not in it.
func (b *Business) LastVisits(ctx context.Context) (map[types.ID]time.Time, error) {
	visits, err := b.store.Visits(ctx)
	if err != nil {
		return nil, err
	}

	last := map[types.ID]time.Time{}
	for _, v := range visits {
		if v.Day.After(last[v.NurseryID]) {
			last[v.NurseryID] = v.Day
		}
	}

	return last, nil
}

// ------------------------------------------------------------------ the register

// Register is every nursery, by name.
func (b *Business) Register(ctx context.Context) ([]Nursery, error) {
	all, err := b.store.Register(ctx)
	if err != nil {
		return nil, err
	}

	slices.SortFunc(all, func(x, y Nursery) int { return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })

	return all, nil
}

// Nursery is one nursery, or ErrNotFound.
func (b *Business) Nursery(ctx context.Context, id types.ID) (Nursery, error) {
	return b.store.NurseryByID(ctx, id)
}

// CreateNursery adds a nursery to the register.
func (b *Business) CreateNursery(ctx context.Context, f NurseryFields) (Nursery, error) {
	f, err := checkNursery(f)
	if err != nil {
		return Nursery{}, err
	}

	now := b.now().UTC().Truncate(time.Millisecond)
	n := Nursery{ID: types.NewID(), CreatedAt: now, UpdatedAt: now}
	n.apply(f)

	if err := b.store.CreateNursery(ctx, n); err != nil {
		return Nursery{}, duplicate(err, f.Name)
	}

	return n, nil
}

// UpdateNursery changes what the register says about a nursery. A new name
// is the name of every visit to it from then on, past ones too: it is the
// same nursery, spelled right at last.
func (b *Business) UpdateNursery(ctx context.Context, id types.ID, f NurseryFields) (Nursery, error) {
	n, err := b.store.NurseryByID(ctx, id)
	if err != nil {
		return Nursery{}, err
	}

	if f, err = checkNursery(f); err != nil {
		return Nursery{}, err
	}

	n.apply(f)
	n.UpdatedAt = b.now().UTC().Truncate(time.Millisecond)

	if err := b.store.UpdateNursery(ctx, n); err != nil {
		return Nursery{}, duplicate(err, f.Name)
	}

	return n, nil
}

// DeleteNursery removes a nursery from the register: one added twice, or by
// mistake. Only while it has no visits. A nursery the stewards have walked
// round is part of what the stock says, and its history stays with it.
func (b *Business) DeleteNursery(ctx context.Context, id types.ID) error {
	return b.store.DeleteNursery(ctx, id)
}

func duplicate(err error, name string) error {
	if errors.Is(err, ErrDuplicate) {
		return Invalid{Field: "name", Problem: fmt.Sprintf("there is already a nursery called %s. Change that one instead, or give this one a name of its own", name)}
	}

	return err
}

func checkNursery(f NurseryFields) (NurseryFields, error) {
	f.Name, f.Address, f.Phone = oneLine(f.Name), oneLine(f.Address), oneLine(f.Phone)
	f.Website, f.Note = strings.TrimSpace(f.Website), strings.TrimSpace(f.Note)

	// A website as a person types it, "naturalgardener.com", is the https
	// address of it. Anything else must be a web address outright: it is a
	// link on a steward's screen, and a javascript: one would run there.
	if f.Website != "" && !strings.Contains(f.Website, "://") {
		f.Website = "https://" + f.Website
	}

	switch {
	case f.Name == "":
		return f, Invalid{Field: "name", Problem: "write the nursery's name"}
	case utf8.RuneCountInString(f.Name) > maxName:
		return f, Invalid{Field: "name", Problem: fmt.Sprintf("the name is longer than %d characters", maxName)}
	case utf8.RuneCountInString(f.Address) > maxAddress:
		return f, Invalid{Field: "address", Problem: fmt.Sprintf("the address is longer than %d characters", maxAddress)}
	case f.Website != "" && !webAddress(f.Website):
		return f, Invalid{Field: "website", Problem: "write the website's address, such as naturalgardener.com"}
	case utf8.RuneCountInString(f.Website) > maxWebsite:
		return f, Invalid{Field: "website", Problem: fmt.Sprintf("the website's address is longer than %d characters", maxWebsite)}
	case utf8.RuneCountInString(f.Phone) > maxPhone:
		return f, Invalid{Field: "phone", Problem: fmt.Sprintf("the phone number is longer than %d characters", maxPhone)}
	case utf8.RuneCountInString(f.Note) > maxNote:
		return f, Invalid{Field: "note", Problem: fmt.Sprintf("keep the note under %d characters", maxNote)}
	}

	return f, nil
}

func webAddress(s string) bool {
	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && strings.Contains(u.Host, ".") && !strings.ContainsAny(s, " \t")
}

// oneLine is s with its runs of spaces, tabs and line breaks made one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (n *Nursery) apply(f NurseryFields) {
	n.Name, n.Address, n.Website, n.Phone, n.Note = f.Name, f.Address, f.Website, f.Phone, f.Note
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
