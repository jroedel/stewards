// Package speciesbus is the rules about species: one record per plant, read
// two ways (phase-1-plan.md, "Species cards: one record, two jobs").
//
// For planting, a species is its flower colour, bloom months, mature size and
// light: what it will look like here in a few years. For weeding it is its
// young-plant photo, its look-alikes and what to do with it in this place.
// The record holds what both views share and what planting needs. What to do
// with it is not here, on purpose: pull or protect belongs to a species *and*
// a place (poison ivy is native, pulled along the paths and maybe kept in the
// woods), so it lives on the link between them, which is the next layer.
//
// # Where an ID's authority comes from
//
// From its sources, never from a person (CLAUDE.md, design.md principle 9).
// So a species is "confirmed" only with a scientific name and at least one
// source cited -- a nursery tag, the Wildflower Center's page -- and the rule
// is here rather than in a form, so that nothing can mark an ID checked
// without saying what it was checked against. Until then the cards say "not
// yet confirmed", which is the honest default.
package speciesbus

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

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
)

// Status is what kind of plant this is to the garden, from the plan's list.
// The zero Status is "not set yet", which the cards show as such.
type Status string

const (
	StatusNative   Status = "native"
	StatusCultivar Status = "cultivar" // a native-derived cultivar or hybrid
	StatusAdapted  Status = "adapted"
	StatusEdible   Status = "edible" // edible or herb
	StatusInvasive Status = "invasive"
)

// Statuses is every status, in the order a form offers them.
var Statuses = []Status{StatusNative, StatusCultivar, StatusAdapted, StatusEdible, StatusInvasive}

// Label is the status in words, for a steward's form. The volunteer card will
// carry its own, bilingual wording.
func (s Status) Label() string {
	switch s {
	case StatusNative:
		return "Native"
	case StatusCultivar:
		return "Native cultivar or hybrid"
	case StatusAdapted:
		return "Adapted"
	case StatusEdible:
		return "Edible or herb"
	case StatusInvasive:
		return "Invasive"
	}

	return "Not set"
}

// Light is the light a plant takes, as a set: most take a range.
type Light uint8

const (
	FullSun Light = 1 << iota
	PartShade
	Shade

	allLight = FullSun | PartShade | Shade
)

// Water is the water a plant takes, as a set. The rain garden's three bands
// are, roughly, these three.
type Water uint8

const (
	Dry Water = 1 << iota
	Moist
	Wet

	allWater = Dry | Moist | Wet
)

// Size is a mature dimension in inches, from Min to Max. Zero for both is
// "not recorded". Inches because nursery lists mix them with feet ("18–24\"",
// "4–7'") and one unit is the only way to compare and sort them.
type Size struct{ Min, Max int }

// maxInches is twelve metres: taller than anything planted here, and short
// enough to catch a typo in feet where inches were meant the other way round.
const maxInches = 480

// String is the size for a card: inches up to three feet, which is how a
// nursery list writes the small plants ("18–24 in"), and whole feet above
// that when both ends are whole feet ("4–7 ft"). Anything else stays in
// inches rather than mixing units in one range.
func (s Size) String() string {
	n, feet := s.Amount()

	switch {
	case n == "":
		return ""
	case feet:
		return n + " ft"
	}

	return n + " in"
}

// Amount is the size's number, "18–24" or "4–7", and whether it is in feet
// rather than inches, by String's rule: for a card that writes the unit in
// the reader's language. "" for a size not recorded.
func (s Size) Amount() (n string, feet bool) {
	if s.Max == 0 {
		return "", false
	}

	in := func(v int) int { return v }
	if s.Max > 36 && s.Min%12 == 0 && s.Max%12 == 0 {
		in, feet = func(v int) int { return v / 12 }, true
	}

	if s.Min == s.Max {
		return strconv.Itoa(in(s.Max)), feet
	}

	return fmt.Sprintf("%d–%d", in(s.Min), in(s.Max)), feet
}

// Source is what an ID was checked against.
type Source struct {
	Label string // "Lady Bird Johnson Wildflower Center"
	URL   string // optional; a nursery tag has none
}

// Species is one plant.
type Species struct {
	ID types.ID

	// Slug is the species' address, /plants/winecup. Fixed once made, like a
	// place's, because it gets printed on stakes.
	Slug string

	Common     types.Text // "Winecup" / "Copa de vino"
	Scientific string     // "Callirhoe involucrata"
	Status     Status
	Confirmed  bool

	// FlowerColor is the colour in words; Swatches are the same as #rrggbb,
	// for the bloom strip's dots. Data, not interface (design.md): a white
	// flower is #ffffff and gets an outline on the card.
	FlowerColor types.Text
	Swatches    []string
	Bloom       types.Months

	Height, Width Size
	Light         Light
	Water         Water

	// Note is everything else a planter needs: how to plant, what to watch.
	Note types.Text

	Sources []Source

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is everything a steward sets. Slug is read by Create only.
type Fields struct {
	Slug          string
	Common        types.Text
	Scientific    string
	Status        Status
	Confirmed     bool
	FlowerColor   types.Text
	Swatches      []string
	Bloom         types.Months
	Height, Width Size
	Light         Light
	Water         Water
	Note          types.Text
	Sources       []Source
}

// Invalid is a species a steward could not save as given, shaped like
// placebus.Invalid so an app shows both the same way.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

var (
	// ErrNotFound is returned when there is no such species.
	ErrNotFound = errors.New("there is no such species")

	// ErrSlugTaken is returned by a Storer when the address is in use; the
	// insert is the check.
	ErrSlugTaken = errors.New("another species already has that address")

	// ErrInUse is returned by a Storer's Delete when the species is still
	// listed at a place, or has photos, and the database refused the delete.
	ErrInUse = errors.New("the species is still listed somewhere")
)

// Storer is what the rules need from storage. A species is saved with its
// sources, as one write.
type Storer interface {
	Create(ctx context.Context, s Species) error
	Update(ctx context.Context, s Species) error
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (Species, error)
	BySlug(ctx context.Context, slug string) (Species, error)
	All(ctx context.Context) ([]Species, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	tr    translationbus.Memory
	now   func() time.Time
}

// NewBusiness constructs one. tr is where a plant's words find their other
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

// Create adds a species.
func (b *Business) Create(ctx context.Context, f Fields) (Species, error) {
	f = tidy(f)

	if f.Slug == "" {
		return Species{}, Invalid{Field: "slug", Problem: "choose the plant's address, such as winecup"}
	}

	if problem := types.SlugProblem(f.Slug); problem != "" {
		return Species{}, Invalid{Field: "slug", Problem: problem}
	}

	if err := check(f); err != nil {
		return Species{}, err
	}

	if err := translationbus.KeepAll(ctx, b.tr, f.texts(), nil); err != nil {
		return Species{}, err
	}

	now := b.now()
	s := Species{ID: types.NewID(), Slug: f.Slug, CreatedAt: now, UpdatedAt: now}
	s.apply(f)

	if err := b.store.Create(ctx, s); err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return Species{}, Invalid{Field: "slug", Problem: fmt.Sprintf("another plant already uses %q. Choose a different one", f.Slug)}
		}

		return Species{}, fmt.Errorf("adding the species %q: %w", f.Slug, err)
	}

	return b.fill(s), nil
}

// Update changes everything but the address.
func (b *Business) Update(ctx context.Context, id types.ID, f Fields) (Species, error) {
	f = tidy(f)

	s, err := b.store.ByID(ctx, id)
	if err != nil {
		return Species{}, err
	}

	if err := check(f); err != nil {
		return Species{}, err
	}

	if err := translationbus.KeepAll(ctx, b.tr, f.texts(), s.words()); err != nil {
		return Species{}, err
	}

	s.apply(f)
	s.UpdatedAt = b.now()

	if err := b.store.Update(ctx, s); err != nil {
		return Species{}, fmt.Errorf("saving the species %q: %w", s.Slug, err)
	}

	return b.fill(s), nil
}

// Delete removes a species that is not listed anywhere.
//
// Refused while it is listed, rather than taking its listings with it: a
// listing is a volunteer being told to protect or pull it somewhere, and
// that advice disappearing as a side effect of tidying the plant list is not
// something anybody would mean.
func (b *Business) Delete(ctx context.Context, id types.ID) error {
	sp, err := b.store.ByID(ctx, id)
	if err != nil {
		return err
	}

	if err := b.store.Delete(ctx, id); err != nil {
		if errors.Is(err, ErrInUse) {
			return Invalid{Field: "species", Problem: fmt.Sprintf("%s is still listed at a place or has photos. Take it off those lists and remove its photos first", sp.Common.In(types.English))}
		}

		return err
	}

	return nil
}

// ByID is one species.
func (b *Business) ByID(ctx context.Context, id types.ID) (Species, error) {
	s, err := b.store.ByID(ctx, id)
	if err != nil {
		return Species{}, err
	}

	return b.fill(s), nil
}

// BySlug is the species at an address.
func (b *Business) BySlug(ctx context.Context, slug string) (Species, error) {
	s, err := b.store.BySlug(ctx, slug)
	if err != nil {
		return Species{}, err
	}

	return b.fill(s), nil
}

// All is every species, by English common name, ignoring case.
func (b *Business) All(ctx context.Context) ([]Species, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	for i := range all {
		all[i] = b.fill(all[i])
	}

	slices.SortStableFunc(all, func(x, y Species) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(x.Common.In(types.English)), strings.ToLower(y.Common.In(types.English))),
			strings.Compare(x.Slug, y.Slug),
		)
	})

	return all, nil
}

// MoveTranslations puts the Spanish that plants stored beside their English,
// before the translation memory, into the memory, and leaves each plant
// storing its English alone. It reports how many plants it changed; see
// placebus's for when it runs.
func (b *Business) MoveTranslations(ctx context.Context) (int, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return 0, err
	}

	n := 0

	for _, s := range all {
		changed, err := translationbus.MoveAll(ctx, b.tr, s.texts()...)
		if err != nil {
			return n, fmt.Errorf("the plant %q: %w", s.Slug, err)
		}

		if !changed {
			continue
		}

		if err := b.store.Update(ctx, s); err != nil {
			return n, fmt.Errorf("saving the plant %q: %w", s.Slug, err)
		}

		n++
	}

	return n, nil
}

// Originals is every plant's words as stored, with where each is read, for
// the translation memory to find what is waiting for a translation.
//
// The common name says which plant it names by its scientific name, because
// that is what a Spanish name is looked up by: a plant's Spanish name is an
// established one, from a source, or none.
func (b *Business) Originals(ctx context.Context) ([]translationbus.Source, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	var out []translationbus.Source

	for _, s := range all {
		label := s.Common.In(types.English)

		nameWhere := "the common name of a plant"
		if s.Scientific != "" {
			nameWhere = "the common name of " + s.Scientific
		}

		for _, src := range []translationbus.Source{
			{Text: s.Common, Where: nameWhere, Name: true},
			{Text: s.FlowerColor, Where: label + ": the colour of its flowers"},
			{Text: s.Note, Where: label + ": a note for whoever plants it"},
		} {
			if src.Text != (types.Text{}) {
				out = append(out, src)
			}
		}
	}

	return out, nil
}

// fill is s with its words in both languages, from the translation memory.
func (b *Business) fill(s Species) Species {
	translationbus.FillAll(b.tr, s.texts()...)

	return s
}

// texts are a plant's words, and words a copy of them, in the same order as
// Fields.texts -- the order translationbus.KeepAll pairs them in.
func (s *Species) texts() []*types.Text {
	return []*types.Text{&s.Common, &s.FlowerColor, &s.Note}
}

func (s Species) words() []types.Text {
	return []types.Text{s.Common, s.FlowerColor, s.Note}
}

func (f *Fields) texts() []*types.Text {
	return []*types.Text{&f.Common, &f.FlowerColor, &f.Note}
}

// ------------------------------------------------------------------ rules

const (
	maxName     = 60
	maxNote     = 2000
	maxSources  = 8
	maxSwatches = 4
	maxLabel    = 120
)

func check(f Fields) error {
	bad := func(field, problem string) error { return Invalid{Field: field, Problem: problem} }

	switch {
	case !f.Common.Written():
		return bad("common", "give the plant's common name")
	case utf8.RuneCountInString(f.Common.EN) > maxName || utf8.RuneCountInString(f.Common.ES) > maxName:
		return bad("common", fmt.Sprintf("keep the common name under %d characters", maxName))
	case utf8.RuneCountInString(f.Scientific) > maxName*2:
		return bad("scientific", "that scientific name is longer than any there is. Check it")
	case f.Status != "" && !slices.Contains(Statuses, f.Status):
		return bad("status", "choose the status from the list")
	case !f.Bloom.Valid():
		return bad("bloom", "choose bloom months from the twelve")
	case f.Light&^allLight != 0:
		return bad("light", "choose the light from the list")
	case f.Water&^allWater != 0:
		return bad("water", "choose the water from the list")
	case utf8.RuneCountInString(f.Note.EN) > maxNote || utf8.RuneCountInString(f.Note.ES) > maxNote:
		return bad("note", fmt.Sprintf("keep the note under %d characters; the card is read on a phone", maxNote))
	case len(f.Swatches) > maxSwatches:
		return bad("swatches", fmt.Sprintf("give at most %d colours", maxSwatches))
	case len(f.Sources) > maxSources:
		return bad("sources", fmt.Sprintf("give at most %d sources", maxSources))
	}

	for _, sw := range f.Swatches {
		if !isSwatch(sw) {
			return bad("swatches", fmt.Sprintf("%q is not a colour code. Write it like #8e44ad", sw))
		}
	}

	for field, size := range map[string]Size{"height": f.Height, "width": f.Width} {
		switch {
		case size.Min < 0 || size.Max < 0:
			return bad(field, "a size cannot be below zero")
		case size.Max > maxInches:
			return bad(field, fmt.Sprintf("that is over %d feet. Sizes are in inches", maxInches/12))
		case size.Min > size.Max:
			return bad(field, "the smaller number goes first")
		}
	}

	for _, src := range f.Sources {
		switch {
		case src.Label == "":
			return bad("sources", "give each source a name, such as Lady Bird Johnson Wildflower Center")
		case utf8.RuneCountInString(src.Label) > maxLabel:
			return bad("sources", fmt.Sprintf("keep each source's name under %d characters", maxLabel))
		case src.URL != "" && !isWebAddress(src.URL):
			return bad("sources", fmt.Sprintf("%q is not a web address. Copy it from the browser, starting https://", src.URL))
		}
	}

	if f.Confirmed && (f.Scientific == "" || len(f.Sources) == 0) {
		return bad("confirmed", "an ID is confirmed by what it was checked against. Give the scientific name and at least one source first")
	}

	return nil
}

func isSwatch(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}

	for _, c := range s[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}

	return true
}

func isWebAddress(s string) bool {
	u, err := url.Parse(s)

	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// tidy trims what was typed, lower-cases colour codes, fills a size given as
// one number, and drops source rows left blank -- a form has spare rows, and
// an empty one is not a source.
func tidy(f Fields) Fields {
	f.Slug = strings.TrimSpace(f.Slug)
	f.Common = f.Common.Trimmed()
	f.Scientific = strings.Join(strings.Fields(f.Scientific), " ")
	f.FlowerColor = f.FlowerColor.Trimmed()
	f.Note = f.Note.Trimmed()

	swatches := f.Swatches[:0:0]
	for _, sw := range f.Swatches {
		if sw = strings.ToLower(strings.TrimSpace(sw)); sw != "" {
			if !strings.HasPrefix(sw, "#") {
				sw = "#" + sw
			}

			swatches = append(swatches, sw)
		}
	}
	f.Swatches = swatches

	for _, size := range []*Size{&f.Height, &f.Width} {
		switch {
		case size.Max == 0 && size.Min != 0:
			size.Max = size.Min
		case size.Min == 0 && size.Max != 0:
			size.Min = size.Max
		}
	}

	sources := f.Sources[:0:0]
	for _, src := range f.Sources {
		src.Label, src.URL = strings.TrimSpace(src.Label), strings.TrimSpace(src.URL)
		if src.Label != "" || src.URL != "" {
			sources = append(sources, src)
		}
	}
	f.Sources = sources

	return f
}

func (s *Species) apply(f Fields) {
	s.Common = f.Common
	s.Scientific = f.Scientific
	s.Status = f.Status
	s.Confirmed = f.Confirmed
	s.FlowerColor = f.FlowerColor
	s.Swatches = f.Swatches
	s.Bloom = f.Bloom
	s.Height = f.Height
	s.Width = f.Width
	s.Light = f.Light
	s.Water = f.Water
	s.Note = f.Note
	s.Sources = f.Sources
}
