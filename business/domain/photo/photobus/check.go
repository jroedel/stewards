package photobus

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/jroedel/stewards/business/types"
)

// The check queue: every photo no steward has checked yet, across every
// plant, so that a batch the API sorted can be looked over in one sitting
// rather than plant by plant from each one's photos screen.
//
// Checking is still a steward's word, given after comparing the photo with
// the plant (Import and Amend refuse it for that reason), and the queue does
// not change what the word means. What it changes is the walk to it: before
// it, checking one photo was five page loads and a long form, which in
// practice meant photos waited.

// Unchecked is every photo not yet checked, in the order the queue shows
// them.
//
// A plant's photos come together, so the steward compares a run of them with
// the same plant in mind, and in the order of kinds a card uses, the best of
// a kind first. The plants are in the order their newest unchecked photo
// arrived, newest first: what was sent this morning is what the steward
// remembers taking. Ties go by id, so the order is the same on every load
// and "next" always means the same photo.
func (b *Business) Unchecked(ctx context.Context) ([]Photo, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	var out []Photo
	newest := map[types.ID]time.Time{}

	for _, p := range all {
		if p.Checked {
			continue
		}

		out = append(out, p)
		if p.CreatedAt.After(newest[p.SpeciesID]) {
			newest[p.SpeciesID] = p.CreatedAt
		}
	}

	slices.SortFunc(out, func(x, y Photo) int {
		if c := newest[y.SpeciesID].Compare(newest[x.SpeciesID]); c != 0 {
			return c
		}

		if c := cmp.Compare(x.SpeciesID.String(), y.SpeciesID.String()); c != 0 {
			return c
		}

		if c := cmp.Compare(slices.Index(Kinds, x.Kind), slices.Index(Kinds, y.Kind)); c != 0 {
			return c
		}

		if c := Better(x, y); c != 0 {
			return c
		}

		return cmp.Compare(x.ID.String(), y.ID.String())
	})

	return out, nil
}

// SetChecked gives a photo a steward's check, or takes it away again: the
// queue's one-tap Yes, and its Undo.
//
// It goes through Update rather than writing the flag alone, so the queue is
// held to every rule the photo's own screen is, today's and any added later,
// and the photo's UpdatedAt says when it was checked. Asking for what is
// already so changes nothing, so a Yes sent twice from a phone that lost its
// signal is harmless.
func (b *Business) SetChecked(ctx context.Context, id types.ID, checked bool) (Photo, error) {
	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Photo{}, err
	}

	if p.Checked == checked {
		return p, nil
	}

	f := FieldsOf(p)
	f.Checked = checked

	return b.Update(ctx, id, f)
}
