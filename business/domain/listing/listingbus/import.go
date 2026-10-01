package listingbus

import (
	"context"
	"fmt"

	"github.com/jroedel/stewards/business/types"
)

// Import is how a program lists a plant at a place: creating the listing if
// there is none, changing it if what was sent differs, and doing nothing if
// it does not. The API's PUT /api/v1/places/<slug>/plants/<species> is this,
// so a batch can be sent twice and the second time is a list of "unchanged".
//
// What an import cannot do is say pull. A listing has no "checked" box for a
// steward to tick after looking, as a photo has: it is on the place card the
// moment it is saved. Protect is the safe direction to be wrong in -- a
// volunteer leaves a weed for a week -- and pull is the dangerous one, the
// native pulled that the Phase 1 test is about, from a program that found a
// page calling it a weed. So the API may protect a plant, or mark it
// careful, and only a steward on the place's Plants screen tells volunteers
// to pull one. An import may still change a listing a steward made pull into
// protect; that is the safe direction too.
//
// The outcomes are speciesbus's words, so a batch reads the same for plants
// and their places.

// Outcome is what an import did.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
)

// Imported is a listing after an import, and what the import did to it.
type Imported struct {
	Listing Listing
	Outcome Outcome
}

// Import lists the species at the place, or changes how it is listed. See
// the comment above.
func (b *Business) Import(ctx context.Context, placeID, speciesID types.ID, f Fields) (Imported, error) {
	if f.Action == Pull {
		return Imported{}, Invalid{Field: "action", Problem: "a plant is marked to pull by a steward on the place's Plants screen, after looking, not by an import. Send protect or careful"}
	}

	f.Note = f.Note.Trimmed()

	existing, found, err := b.listed(ctx, placeID, speciesID)
	if err != nil {
		return Imported{}, err
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
	all, err := b.store.ForPlace(ctx, placeID)
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
