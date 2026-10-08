// Package listingbus is the rules about a species listed at a place: what to
// do with it there, and whether there is planting still to do for it.
//
// This is the layer the plan insists on. Pull or protect is not a property of
// a species -- poison ivy is native, pulled along the paths and maybe kept
// deep in the woods -- so it lives here, on the pair, and a place card's
// Protect and Pull panels are read from these rows. So is its "To plant"
// list, and the species card's "Where it grows here".
//
// A listing is keyed by its place and its species, so a species is listed at
// most once in a place; saving it again changes it. The references are the
// database's to enforce: a listing cannot name a place or a species that does
// not exist, and neither can be deleted while a listing names it.
package listingbus

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

// Action is what a volunteer does with this plant in this place.
type Action string

const (
	// Protect: leave it. Everything planned here is protected.
	Protect Action = "protect"

	// Pull: take it out, root and all.
	Pull Action = "pull"

	// Careful: it stays or goes as the note says, but touch it with gloves
	// on -- poison ivy, nettles, cactus. The plan's "careful (wear PPE)".
	Careful Action = "careful"
)

// Actions is every action, in the order a form offers them.
var Actions = []Action{Protect, Pull, Careful}

// Listing is one species at one place.
type Listing struct {
	PlaceID   types.ID
	SpeciesID types.ID
	Action    Action

	// Planned says there is planting still to do for the species here: none
	// of it is in the ground yet, or more is going in beside what is growing
	// (the note says how many). It puts the plant on the card's "To plant"
	// list, and a steward unticks it once the planting is done; the plant
	// stays protected, and on the flowering calendar, either way. A plant
	// growing here and not to plant -- put in last season, or come up on its
	// own -- is protected and not planned. A planned plant is always
	// protected; see Set.
	//
	// The name is older than this meaning, when the list was "Planned
	// here"; it is kept because the API's field and the column are both
	// "planned", and a batch script written for one should not break.
	Planned bool

	// Note is what to know about it here: "pull before it seeds", "two at
	// the back, against the wall".
	Note types.Text

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is what a steward sets on a listing.
type Fields struct {
	Action  Action
	Planned bool
	Note    types.Text
}

// Invalid is a listing a steward could not save as given, shaped like
// placebus.Invalid.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

var (
	// ErrNotFound is returned when there is no such listing.
	ErrNotFound = errors.New("that plant is not listed there")

	// ErrUnknown is returned by a Storer when the place or the species a
	// listing names does not exist -- removed by another steward a moment
	// ago, or an identifier made by hand.
	ErrUnknown = errors.New("that place or that plant no longer exists")
)

// Storer is what the rules need from storage.
type Storer interface {
	// Set inserts or replaces the listing for its place and species, as one
	// statement.
	Set(ctx context.Context, l Listing) error
	Remove(ctx context.Context, placeID, speciesID types.ID) error
	ForPlace(ctx context.Context, placeID types.ID) ([]Listing, error)
	ForSpecies(ctx context.Context, speciesID types.ID) ([]Listing, error)
	All(ctx context.Context) ([]Listing, error)

	// Noted is every listing with a note, with its place's and its plant's
	// names, for a reader who meets the note away from the place's card.
	Noted(ctx context.Context) ([]Noted, error)
}

// Noted is a listing with the names of its place and its plant, as stored:
// each in whichever language it was written.
type Noted struct {
	Listing
	Place string
	Plant string
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	tr    translationbus.Memory
	now   func() time.Time
}

// NewBusiness constructs one. tr is where a note finds its other language;
// nil is translationbus.None, for a test not about translation. nil now means
// the wall clock.
func NewBusiness(store Storer, tr translationbus.Memory, now func() time.Time) *Business {
	if tr == nil {
		tr = translationbus.None{}
	}

	if now == nil {
		now = time.Now
	}

	return &Business{store: store, tr: tr, now: now}
}

const maxNote = 500

// Set lists a species at a place, or changes how it is listed.
//
// One refusal is about meaning rather than shape: a plant to plant cannot be
// pulled. A card that told a volunteer to plant something and pull it at once
// is a contradiction, and to pull what the stewards are planting is exactly
// the mistake the Phase 1 test is about.
func (b *Business) Set(ctx context.Context, placeID, speciesID types.ID, f Fields) (Listing, error) {
	f.Note = f.Note.Trimmed()

	switch {
	case f.Action == "":
		return Listing{}, Invalid{Field: "action", Problem: "choose what to do with it here: protect, pull or careful"}
	case !validAction(f.Action):
		return Listing{}, Invalid{Field: "action", Problem: "choose protect, pull or careful"}
	case f.Planned && f.Action == Pull:
		return Listing{}, Invalid{Field: "action", Problem: "a plant still to plant here is not one to pull. Untick To plant, or choose protect or careful"}
	case utf8.RuneCountInString(f.Note.EN) > maxNote || utf8.RuneCountInString(f.Note.ES) > maxNote:
		return Listing{}, Invalid{Field: "note", Problem: fmt.Sprintf("keep the note under %d characters", maxNote)}
	}

	// The note as stored, for Keep to tell words the steward wrote from
	// words left as they were shown. A place's list is a few dozen rows.
	listed, err := b.store.ForPlace(ctx, placeID)
	if err != nil {
		return Listing{}, err
	}

	var stored types.Text
	if i := slices.IndexFunc(listed, func(l Listing) bool { return l.SpeciesID == speciesID }); i >= 0 {
		stored = listed[i].Note
	}

	note, err := b.tr.Keep(ctx, f.Note, stored)
	if err != nil {
		return Listing{}, err
	}

	now := b.now()
	l := Listing{
		PlaceID: placeID, SpeciesID: speciesID,
		Action: f.Action, Planned: f.Planned, Note: note,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := b.store.Set(ctx, l); err != nil {
		if errors.Is(err, ErrUnknown) {
			return Listing{}, Invalid{Field: "species", Problem: "that place or that plant no longer exists. Open the page again"}
		}

		return Listing{}, fmt.Errorf("listing the plant: %w", err)
	}

	return b.fill(l), nil
}

// Remove takes a species off a place's list.
func (b *Business) Remove(ctx context.Context, placeID, speciesID types.ID) error {
	return b.store.Remove(ctx, placeID, speciesID)
}

// ForPlace is everything listed at a place.
func (b *Business) ForPlace(ctx context.Context, placeID types.ID) ([]Listing, error) {
	return b.filled(b.store.ForPlace(ctx, placeID))
}

// ForSpecies is every place a species is listed at.
func (b *Business) ForSpecies(ctx context.Context, speciesID types.ID) ([]Listing, error) {
	return b.filled(b.store.ForSpecies(ctx, speciesID))
}

// MoveTranslations puts the Spanish that listings stored beside their
// English notes, before the translation memory, into the memory, and leaves
// each note stored in English alone. It reports how many listings it
// changed; see placebus's for when it runs.
//
// The listing is written back through the store's Set, which keeps when it
// was first made; its UpdatedAt is left as it was, since nothing a person
// sees has changed.
func (b *Business) MoveTranslations(ctx context.Context) (int, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return 0, err
	}

	n := 0

	for _, l := range all {
		changed, err := translationbus.MoveAll(ctx, b.tr, &l.Note)
		if err != nil {
			return n, err
		}

		if !changed {
			continue
		}

		if err := b.store.Set(ctx, l); err != nil {
			return n, fmt.Errorf("saving a listing's note: %w", err)
		}

		n++
	}

	return n, nil
}

// Originals is every listing's note as stored, with where it is read, for
// the translation memory to find what is waiting for a translation.
func (b *Business) Originals(ctx context.Context) ([]translationbus.Source, error) {
	noted, err := b.store.Noted(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]translationbus.Source, 0, len(noted))
	for _, n := range noted {
		out = append(out, translationbus.Source{
			Text:  n.Note,
			Where: fmt.Sprintf("%s: a note on %s, listed there as %s", n.Place, n.Plant, n.Action),
		})
	}

	return out, nil
}

func (b *Business) fill(l Listing) Listing {
	l.Note = b.tr.Fill(l.Note)

	return l
}

func (b *Business) filled(all []Listing, err error) ([]Listing, error) {
	if err != nil {
		return nil, err
	}

	for i := range all {
		all[i] = b.fill(all[i])
	}

	return all, nil
}

func validAction(a Action) bool { return slices.Contains(Actions, a) }

// Label is the action as a steward's form names it.
func (a Action) Label() string {
	switch a {
	case Protect:
		return "Protect: leave it"
	case Pull:
		return "Pull: take it out"
	case Careful:
		return "Careful: wear gloves"
	}

	return "Not set"
}
