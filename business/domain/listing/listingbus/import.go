package listingbus

import (
	"context"
	"fmt"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
)

// Import is how a program lists a plant at a place: creating the listing if
// there is none, changing it if what was sent differs, and doing nothing if
// it does not. The API's PUT /api/v1/places/<slug>/plants/<species> is this,
// so a batch can be sent twice and the second time is a list of "unchanged".
//
// An import may say pull. It could not at first: a listing has no "checked"
// box for a steward to tick after looking, as a photo has, and is on the
// place card the moment it is saved, so pull -- the dangerous direction, the
// native pulled that the Phase 1 test is about -- was left to a steward on
// the place's Plants screen. That held until the API found tree of heaven
// on the property and could only list it careful with "pull it" in the
// note, which is pull by another name with less said. The stewards decided
// (2026-10-06) that the API marks pulls too. What keeps it honest is the
// same as for every other listing: the steward's own key, the log line each
// one writes, and the rules below, which refuse to pull what is being
// planted.
//
// The outcomes are speciesbus's words, so a batch reads the same for plants
// and their places.

// Outcome is what an import did.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
	Removed   Outcome = "removed"
)

// Imported is a listing after an import, and what the import did to it.
type Imported struct {
	Listing Listing
	Outcome Outcome
}

// Import lists the species at the place, or changes how it is listed. See
// the comment above.
func (b *Business) Import(ctx context.Context, placeID, speciesID types.ID, f Fields) (Imported, error) {
	f.Note = f.Note.Trimmed()

	existing, found, err := b.listed(ctx, placeID, speciesID)
	if err != nil {
		return Imported{}, err
	}

	if found {
		translationbus.Complete([]*types.Text{&f.Note}, []types.Text{existing.Note})
	}

	if found && existing.Action == f.Action && existing.Planned == f.Planned && existing.Note == f.Note {
		return Imported{Listing: existing, Outcome: Unchanged}, nil
	}

	l, err := b.Set(ctx, placeID, speciesID, f)
	if err != nil {
		return Imported{}, err
	}

	if found {
		return Imported{Listing: l, Outcome: Updated}, nil
	}

	return Imported{Listing: l, Outcome: Created}, nil
}

// listed is the species' listing at the place, if it has one. Read from the
// place's list rather than by the pair: a place lists tens of plants, and the
// store has no other reason to look one up alone.
func (b *Business) listed(ctx context.Context, placeID, speciesID types.ID) (Listing, bool, error) {
	all, err := b.ForPlace(ctx, placeID)
	if err != nil {
		return Listing{}, false, fmt.Errorf("reading what is listed at the place: %w", err)
	}

	for _, l := range all {
		if l.SpeciesID == speciesID {
			return l, true, nil
		}
	}

	return Listing{}, false, nil
}

// Unlist takes the species off the place's list, for a program: Removed when
// it was listed there, Unchanged when it was not, so that a batch sent twice
// reads the second time as nothing to do.
func (b *Business) Unlist(ctx context.Context, placeID, speciesID types.ID) (Outcome, error) {
	_, found, err := b.listed(ctx, placeID, speciesID)
	switch {
	case err != nil:
		return "", err
	case !found:
		return Unchanged, nil
	}

	if err := b.Remove(ctx, placeID, speciesID); err != nil {
		return "", fmt.Errorf("taking the plant off the place: %w", err)
	}

	return Removed, nil
}
