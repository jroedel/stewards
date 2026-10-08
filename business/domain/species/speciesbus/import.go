package speciesbus

import (
	"context"
	"errors"
	"slices"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
)

// Import is how a program adds a plant, by its address: creating it if there
// is none, changing it if what was sent differs, and doing nothing if it does
// not. The API's PUT /api/v1/species/<slug> is this, so a batch can be sent
// twice and the second time is a list of "unchanged".
//
// What an import cannot do is confirm. Confirmed is a steward's word that the
// ID was checked against its sources, and a program -- a steward's own Claude
// included -- can find a page that says "frostweed" as easily as the skinny
// bed guide did. So an import asking for it is refused, and an import that
// changes a confirmed plant takes the confirmation away: what was checked is
// no longer what is there. A person confirms it again on the plant's screen.
// One that changes nothing leaves it confirmed, which is what makes sending a
// batch twice harmless.

// Outcome is what an import did.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
)

// Imported is a species after an import, and what the import did to it.
type Imported struct {
	Species Species
	Outcome Outcome

	// Unconfirmed is true when the plant was confirmed and this import's
	// change took that away.
	Unconfirmed bool
}

// Import creates or changes the species at f.Slug. See the comment above.
func (b *Business) Import(ctx context.Context, f Fields) (Imported, error) {
	if f.Confirmed {
		return Imported{}, Invalid{Field: "confirmed", Problem: "a plant is confirmed by a steward on its screen, after checking it against its sources, not by an import. Leave confirmed out"}
	}

	f = tidy(f)

	existing, err := b.BySlug(ctx, f.Slug)

	switch {
	case errors.Is(err, ErrNotFound):
		s, err := b.Create(ctx, f)
		if err != nil {
			return Imported{}, err
		}

		return Imported{Species: s, Outcome: Created}, nil
	case err != nil:
		return Imported{}, err
	}

	translationbus.Complete(f.texts(), existing.words())

	if same(fieldsOf(existing), f) {
		return Imported{Species: existing, Outcome: Unchanged}, nil
	}

	s, err := b.Update(ctx, existing.ID, f)
	if err != nil {
		return Imported{}, err
	}

	return Imported{Species: s, Outcome: Updated, Unconfirmed: existing.Confirmed}, nil
}

// fieldsOf is what a steward would have to send to get s, apart from the
// confirmation, which an import never sends.
func fieldsOf(s Species) Fields {
	return Fields{
		Slug: s.Slug, Common: s.Common, Scientific: s.Scientific, Status: s.Status,
		FlowerColor: s.FlowerColor, Swatches: s.Swatches, Bloom: s.Bloom,
		Height: s.Height, Width: s.Width, Light: s.Light, Water: s.Water,
		Note: s.Note, Sources: s.Sources,
	}
}

// same compares every field. Written out, because Fields holds slices and so
// cannot be compared with ==, and reflect.DeepEqual would call a nil list of
// sources different from an empty one.
func same(a, b Fields) bool {
	return a.Slug == b.Slug && a.Common == b.Common && a.Scientific == b.Scientific &&
		a.Status == b.Status && a.Confirmed == b.Confirmed &&
		a.FlowerColor == b.FlowerColor && slices.Equal(a.Swatches, b.Swatches) &&
		a.Bloom == b.Bloom && a.Height == b.Height && a.Width == b.Width &&
		a.Light == b.Light && a.Water == b.Water && a.Note == b.Note &&
		slices.Equal(a.Sources, b.Sources)
}
