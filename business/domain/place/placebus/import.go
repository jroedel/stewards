package placebus

import (
	"context"
	"errors"
)

// Import is how a program adds a place or changes one: a steward's Claude,
// through the API, writing down a bed the steward has just dug, or the
// conditions they described standing in it. It creates the place at f.Slug if
// there is none, changes it if what was sent differs, and does nothing if it
// does not, so a batch sent twice reads the second time as "unchanged".
//
// The API could not touch places at first (CLAUDE.md, section 6): they came
// in through the steward's own screens. The stewards widened that on
// 2026-10-06. Nothing here asks for a check first, as a photo or a plant's ID
// does, because a place's name and notes are what a steward said, not a
// claim about the world that a source has to back; a wrong one is corrected
// by sending it again.
//
// Where a place is on the map is not here, for the reason SetSpot gives: it
// is its own change. Neither is removing one, which stays a person's.

// Outcome is what an import did, in speciesbus's words.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
)

// Imported is a place after an import, and what the import did to it.
type Imported struct {
	Place   Place
	Outcome Outcome
}

// Import creates or changes the place at f.Slug. See the comment above.
func (b *Business) Import(ctx context.Context, f Fields) (Imported, error) {
	f = tidy(f)

	existing, err := b.store.BySlug(ctx, f.Slug)

	switch {
	case errors.Is(err, ErrNotFound):
		p, err := b.Create(ctx, f)
		if err != nil {
			return Imported{}, err
		}

		return Imported{Place: p, Outcome: Created}, nil
	case err != nil:
		return Imported{}, err
	}

	if FieldsOf(existing) == f {
		return Imported{Place: existing, Outcome: Unchanged}, nil
	}

	p, err := b.Update(ctx, existing.ID, f)
	if err != nil {
		return Imported{}, err
	}

	return Imported{Place: p, Outcome: Updated}, nil
}

// FieldsOf is what a steward would send to get p, but for where it is on the
// map.
func FieldsOf(p Place) Fields {
	return Fields{
		Slug: p.Slug, Name: p.Name, ParentID: p.ParentID,
		Purpose: p.Purpose, Conditions: p.Conditions, PhotoPoint: p.PhotoPoint,
		TrailAnchor: p.TrailAnchor, Sort: p.Sort,
	}
}
