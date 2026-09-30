// Package placebus is the rules about places: the named areas people stand in
// and work in.
//
// Places are the backbone of the app (phase-1-plan.md, "independent layers").
// Everything else -- watering, conditions, fire safety, wildlife, and in later
// phases plants, photos and drone frames -- is a separate layer that points at
// a place. None of them is a level in a hierarchy under it, so nothing here
// knows about them: a Rachio zone covers several places and a place can have
// several zones or none, and modelling that as a child would make one of those
// two facts impossible to write down.
//
// The one hierarchy there is, is places inside places, and only where it
// helps: the rain garden has three bands (inflow, middle, wall edge), and the
// switchbacks stay one place.
package placebus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// Place is one named area of the garden.
type Place struct {
	ID types.ID

	// Slug is the place's address: /places/rain-garden. It never changes once
	// the place exists; see Update.
	Slug string

	// Name is what people call it, in both languages.
	Name types.Text

	// ParentID is the place this one is part of, or the zero ID for a place
	// that stands on its own. A place is at most one level deep.
	ParentID types.ID

	// Purpose is what the place is for: "the backdrop of the gathering space".
	Purpose types.Text

	// Conditions are the notes a planter needs: sun, slope, soil, wet or dry.
	// Free text in Phase 1, per the plan's layer table. The day conditions
	// are measured rather than described, they become a layer of their own.
	Conditions types.Text

	// PhotoPoint says where to stand and which way to face for this place's
	// fixed photo, re-shot each season: "from the fire-pit bench, facing the
	// wall".
	PhotoPoint types.Text

	// TrailAnchor links a station space to its section of the public trail
	// page, as /trail/#<anchor>. Empty for a place that is not a station.
	TrailAnchor string

	// Sort orders places in a list, lowest first; ties go by English name.
	// The map is the primary way to choose a place, and this is its
	// accessible twin, so the order is chosen rather than alphabetical.
	Sort int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// TopLevel reports whether the place stands on its own.
func (p Place) TopLevel() bool { return p.ParentID.Zero() }

// Fields is everything a steward sets on a place. The slug is here for
// Create and ignored by Update.
type Fields struct {
	Slug        string
	Name        types.Text
	ParentID    types.ID
	Purpose     types.Text
	Conditions  types.Text
	PhotoPoint  types.Text
	TrailAnchor string
	Sort        int
}

// TrailAnchors are the anchors on the public trail page that this app may link
// to. They are burned into plywood as QR codes, so they are a contract
// (design.md, section 1): this list changes only when the boards do.
var TrailAnchors = []string{"reinisch", "pozzobon", "francis", "therese", "joseph"}

// Invalid is a place a steward could not save as given. Field names which
// input to fix; Problem says what is wrong with it, in a sentence the page can
// show beside that input.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

// ErrNotFound is returned when there is no such place.
var ErrNotFound = errors.New("there is no such place")

// ErrSlugTaken is returned by a Storer when another place already has the
// slug. The store's insert is the check -- a UNIQUE index -- so two stewards
// adding the same place at once cannot both succeed.
var ErrSlugTaken = errors.New("another place already has that address")

// Storer is what the rules need from storage.
type Storer interface {
	Create(ctx context.Context, p Place) error
	Update(ctx context.Context, p Place) error
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (Place, error)
	BySlug(ctx context.Context, slug string) (Place, error)
	All(ctx context.Context) ([]Place, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	now   func() time.Time
}

// NewBusiness constructs one. now is injectable so a test can say what time
// it is; nil means the wall clock.
func NewBusiness(store Storer, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, now: now}
}

// Create adds a place.
func (b *Business) Create(ctx context.Context, f Fields) (Place, error) {
	f = tidy(f)

	if err := checkSlug(f.Slug); err != nil {
		return Place{}, err
	}

	if err := b.check(ctx, types.ID{}, f); err != nil {
		return Place{}, err
	}

	now := b.now()
	p := Place{
		ID:        types.NewID(),
		Slug:      f.Slug,
		CreatedAt: now,
		UpdatedAt: now,
	}
	p.apply(f)

	if err := b.store.Create(ctx, p); err != nil {
		if errors.Is(err, ErrSlugTaken) {
			return Place{}, Invalid{Field: "slug", Problem: fmt.Sprintf("another place already uses %q. Choose a different one", f.Slug)}
		}

		return Place{}, fmt.Errorf("adding the place %q: %w", f.Slug, err)
	}

	return p, nil
}

// Update changes a place. Everything a steward sets can change except the
// slug.
//
// The slug is fixed because it is an address, and addresses get printed. The
// trail's QR anchors are the lesson already paid for here: once a link is on
// a board or a plant stake in the ground, changing the thing it points to
// breaks it for everybody holding one, and nobody is told. The name can
// change freely; the slug is chosen once, with that in mind.
func (b *Business) Update(ctx context.Context, id types.ID, f Fields) (Place, error) {
	f = tidy(f)

	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Place{}, err
	}

	if err := b.check(ctx, id, f); err != nil {
		return Place{}, err
	}

	p.apply(f)
	p.UpdatedAt = b.now()

	if err := b.store.Update(ctx, p); err != nil {
		return Place{}, fmt.Errorf("saving the place %q: %w", p.Slug, err)
	}

	return p, nil
}

// Delete removes a place that nothing is part of.
//
// A place with smaller places inside it is refused rather than taking them
// with it: the rain garden's bands are places a volunteer has been told to
// plant in, and removing them as a side effect is not something anybody would
// mean to do. When photos and plants point at places, they will be a reason
// to refuse too.
func (b *Business) Delete(ctx context.Context, id types.ID) error {
	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return err
	}

	all, err := b.store.All(ctx)
	if err != nil {
		return err
	}

	if kids := children(all, id); len(kids) > 0 {
		return Invalid{Field: "place", Problem: fmt.Sprintf("%s has %d smaller places inside it. Move or remove those first", p.Name.EN, len(kids))}
	}

	return b.store.Delete(ctx, id)
}

// BySlug is the place at an address.
func (b *Business) BySlug(ctx context.Context, slug string) (Place, error) {
	return b.store.BySlug(ctx, slug)
}

// All is every place, in list order: by Sort, then by English name.
func (b *Business) All(ctx context.Context) ([]Place, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(all, byListOrder)

	return all, nil
}

// Children is the places inside one, in list order.
func (b *Business) Children(ctx context.Context, id types.ID) ([]Place, error) {
	all, err := b.All(ctx)
	if err != nil {
		return nil, err
	}

	return children(all, id), nil
}

// ------------------------------------------------------------------ rules

const (
	maxSlug = 48
	maxName = 60
)

// check is every rule except the slug's, which only Create checks. self is
// the place being updated, or the zero ID for a new one.
func (b *Business) check(ctx context.Context, self types.ID, f Fields) error {
	switch {
	case f.Name.EN == "":
		return Invalid{Field: "name", Problem: "give the place a name in English. Spanish is optional"}
	case utf8.RuneCountInString(f.Name.EN) > maxName || utf8.RuneCountInString(f.Name.ES) > maxName:
		return Invalid{Field: "name", Problem: fmt.Sprintf("keep the name under %d characters; it has to fit on a phone", maxName)}
	}

	if f.TrailAnchor != "" && !slices.Contains(TrailAnchors, f.TrailAnchor) {
		return Invalid{Field: "trail_anchor", Problem: fmt.Sprintf("%q is not a station on the trail page. Use one of: %s", f.TrailAnchor, strings.Join(TrailAnchors, ", "))}
	}

	if f.ParentID.Zero() {
		return nil
	}

	// One level deep, and no deeper. The plan's example is the whole case:
	// a bed and its bands. A band inside a band is a layout within a place,
	// which Phase 1 draws on the card rather than naming as places. Keeping
	// to one level also makes a loop impossible -- a place cannot end up
	// inside itself -- without walking a chain to prove it.
	if f.ParentID == self {
		return Invalid{Field: "parent", Problem: "a place cannot be inside itself"}
	}

	parent, err := b.store.ByID(ctx, f.ParentID)
	if errors.Is(err, ErrNotFound) {
		return Invalid{Field: "parent", Problem: "the place chosen to put this inside no longer exists"}
	}
	if err != nil {
		return err
	}

	if !parent.TopLevel() {
		return Invalid{Field: "parent", Problem: fmt.Sprintf("%s is itself inside another place. Places go only one level deep", parent.Name.EN)}
	}

	if self.Zero() {
		return nil
	}

	all, err := b.store.All(ctx)
	if err != nil {
		return err
	}

	if kids := children(all, self); len(kids) > 0 {
		return Invalid{Field: "parent", Problem: "this place has smaller places inside it, so it cannot go inside another. Places go only one level deep"}
	}

	return nil
}

// checkSlug holds an address to the shape a person can read aloud and type
// on a phone: lower case letters, digits and single hyphens.
func checkSlug(s string) error {
	bad := func(problem string) error { return Invalid{Field: "slug", Problem: problem} }

	switch {
	case s == "":
		return bad("choose the place's address, such as rain-garden")
	case len(s) > maxSlug:
		return bad(fmt.Sprintf("keep the address under %d characters", maxSlug))
	case strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") || strings.Contains(s, "--"):
		return bad("use single hyphens between words, and none at the ends")
	}

	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return bad("use only lower-case letters, digits and hyphens, such as rain-garden")
		}
	}

	return nil
}

func tidy(f Fields) Fields {
	f.Slug = strings.TrimSpace(f.Slug)
	f.TrailAnchor = strings.TrimSpace(f.TrailAnchor)
	f.Name = f.Name.Trimmed()
	f.Purpose = f.Purpose.Trimmed()
	f.Conditions = f.Conditions.Trimmed()
	f.PhotoPoint = f.PhotoPoint.Trimmed()

	return f
}

func (p *Place) apply(f Fields) {
	p.Name = f.Name
	p.ParentID = f.ParentID
	p.Purpose = f.Purpose
	p.Conditions = f.Conditions
	p.PhotoPoint = f.PhotoPoint
	p.TrailAnchor = f.TrailAnchor
	p.Sort = f.Sort
}

func children(all []Place, id types.ID) []Place {
	var kids []Place
	for _, p := range all {
		if p.ParentID == id {
			kids = append(kids, p)
		}
	}

	return kids
}

func byListOrder(a, b Place) int {
	return cmp.Or(cmp.Compare(a.Sort, b.Sort), strings.Compare(a.Name.EN, b.Name.EN))
}
