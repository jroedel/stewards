// Package listingbus is the rules about a species listed at a place: what to
// do with it there, and whether it is part of the planting.
//
// This is the layer the plan insists on. Pull or protect is not a property of
// a species -- poison ivy is native, pulled along the paths and maybe kept
// deep in the woods -- so it lives here, on the pair, and a place card's
// Protect and Pull panels are read from these rows. So is its "Planned here"
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

	// Planned says the species is part of the planting here: it goes on the
	// place's "Planned here" list and its bloom calendar. A planned plant is
	// always protected; see Set.
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

const maxNote = 500

// Set lists a species at a place, or changes how it is listed.
//
// One refusal is about meaning rather than shape: a planned plant cannot be
// pulled. Planned means it was put there on purpose, and a card that told a
// volunteer to pull what the stewards planted is exactly the mistake the
// Phase 1 test is about.
func (b *Business) Set(ctx context.Context, placeID, speciesID types.ID, f Fields) (Listing, error) {
	f.Note = f.Note.Trimmed()

	switch {
	case f.Action == "":
		return Listing{}, Invalid{Field: "action", Problem: "choose what to do with it here: protect, pull or careful"}
	case !validAction(f.Action):
		return Listing{}, Invalid{Field: "action", Problem: "choose protect, pull or careful"}
	case f.Planned && f.Action == Pull:
		return Listing{}, Invalid{Field: "action", Problem: "a plant that is planned here is not one to pull. Untick planned, or choose protect or careful"}
	case utf8.RuneCountInString(f.Note.EN) > maxNote || utf8.RuneCountInString(f.Note.ES) > maxNote:
		return Listing{}, Invalid{Field: "note", Problem: fmt.Sprintf("keep the note under %d characters", maxNote)}
	}

	now := b.now()
	l := Listing{
		PlaceID: placeID, SpeciesID: speciesID,
		Action: f.Action, Planned: f.Planned, Note: f.Note,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := b.store.Set(ctx, l); err != nil {
		if errors.Is(err, ErrUnknown) {
			return Listing{}, Invalid{Field: "species", Problem: "that place or that plant no longer exists. Open the page again"}
		}

		return Listing{}, fmt.Errorf("listing the plant: %w", err)
	}

	return l, nil
}

// Remove takes a species off a place's list.
func (b *Business) Remove(ctx context.Context, placeID, speciesID types.ID) error {
	return b.store.Remove(ctx, placeID, speciesID)
}

// ForPlace is everything listed at a place.
func (b *Business) ForPlace(ctx context.Context, placeID types.ID) ([]Listing, error) {
	return b.store.ForPlace(ctx, placeID)
}

// ForSpecies is every place a species is listed at.
func (b *Business) ForSpecies(ctx context.Context, speciesID types.ID) ([]Listing, error) {
	return b.store.ForSpecies(ctx, speciesID)
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
