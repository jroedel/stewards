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

	// Spot is where the place sits on the map, or nil for a place that is
	// not on it yet. Only a place that stands on its own has one: a band is
	// shown on its place's card, and a dot for each band of the rain garden
	// would be three dots on top of each other at the map's scale.
	Spot *Spot

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Spot is a point on the map, in the drawing's own units: X across from the
// west edge, Y down from the north edge, inside MapWidth by MapHeight.
//
// The map is a schematic over the public trail map's geometry, not a survey
// (design.md, section 1), so a spot is where a steward tapped on the drawing
// and not a coordinate on the ground. GPS is a different thing, kept for
// Phase 2 and never shown as fact; keeping the two apart is why this is not
// a latitude and longitude.
type Spot struct {
	X, Y int
}

// The map's size in its own units: the public trail map's viewBox, which
// the drawing is copied from. They change only if the drawing is redrawn,
// and every spot already set would move with it.
const (
	MapWidth  = 800
	MapHeight = 520
)

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

// ErrInUse is returned by a Storer's Delete when something still points at
// the place -- plants listed there, or a photo taken there -- and the
// database refused the delete.
var ErrInUse = errors.New("the place is still in use")

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

	// A place that has just gone inside another comes off the map, rather
	// than keep a spot that nothing draws and that would come back,
	// unchosen, the day it stands on its own again.
	if !p.TopLevel() {
		p.Spot = nil
	}

	if err := b.store.Update(ctx, p); err != nil {
		return Place{}, fmt.Errorf("saving the place %q: %w", p.Slug, err)
	}

	return p, nil
}

// SetSpot puts a place on the map, moves it, or takes it off with a nil
// spot.
//
// It is its own operation rather than another of the Fields, because it is
// its own form: a steward taps the map, and the tap is the whole of what
// they sent. Folding it into Update would have every Save of the names and
// notes carry a spot along too, and the one form that can lose a spot is the
// one that never meant to touch it.
func (b *Business) SetSpot(ctx context.Context, id types.ID, spot *Spot) (Place, error) {
	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Place{}, err
	}

	if spot != nil {
		if !p.TopLevel() {
			return Place{}, Invalid{Field: "spot", Problem: fmt.Sprintf("%s is inside another place, so it is shown on that place's card rather than on the map", p.Name.EN)}
		}

		if spot.X < 0 || spot.X > MapWidth || spot.Y < 0 || spot.Y > MapHeight {
			return Place{}, Invalid{Field: "spot", Problem: "that is off the edge of the map. Tap inside the drawing"}
		}

		s := *spot
		spot = &s
	}

	p.Spot = spot
	p.UpdatedAt = b.now()

	if err := b.store.Update(ctx, p); err != nil {
		return Place{}, fmt.Errorf("saving where %q is on the map: %w", p.Slug, err)
	}

	return p, nil
}

// Delete removes a place that nothing is part of and nothing is listed at.
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

	// Plants listed here, and photos that say they were taken here, are
	// the other things that keep a place: the database refuses the delete
	// while a listing or a photo names it.
	if err := b.store.Delete(ctx, id); err != nil {
		if errors.Is(err, ErrInUse) {
			return Invalid{Field: "place", Problem: fmt.Sprintf("%s still has plants listed, or photos taken there. Take the plants off its list, and clear the place from those photos, first", p.Name.EN)}
		}

		return err
	}

	return nil
}

// ByID is one place, by the identifier that never changes. The edit screens
// use it rather than the slug so that their addresses do not share a
// namespace with the places' own.
func (b *Business) ByID(ctx context.Context, id types.ID) (Place, error) {
	return b.store.ByID(ctx, id)
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

const maxName = 60

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

// checkSlug holds an address to types.SlugProblem's shape, with the place's
// own example in each sentence.
func checkSlug(s string) error {
	if s == "" {
		return Invalid{Field: "slug", Problem: "choose the place's address, such as rain-garden"}
	}

	if problem := types.SlugProblem(s); problem != "" {
		if strings.HasPrefix(problem, "use only") {
			problem += ", such as rain-garden"
		}

		return Invalid{Field: "slug", Problem: problem}
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
