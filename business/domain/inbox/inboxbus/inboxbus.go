// Package inboxbus is the rules about the photo inbox: photos sent now and
// sorted later.
//
// A steward takes photos in the garden for different reasons -- to find out
// later what a plant is, because a flower is out that is not often seen,
// because something was just planted, or at a nursery, to note what is in
// stock this week for a bed about to be planted. Each of those ends somewhere
// different: a species photo, a listing at a place, a line of a nursery's
// stock. But in the moment, with ten photos chosen at once and dirty hands,
// the only question worth asking is where they were taken, and even that is
// one toggle for the whole batch. Everything else is worked out when the
// photos are sorted, on a screen or in a steward's Claude session, days later.
//
// So the inbox keeps a photo with almost nothing said about it: who sent it,
// whether it was taken on the property or at a nursery, optionally the place
// and a note, and what the camera knew -- when, and where if the phone kept
// its position, which phones often do not. The photo itself is kept as it
// arrived, privately, with a large and a small copy for the stewards' screens,
// made the way photobus makes them.
//
// An inbox photo is never shown to a volunteer, checked or not: it has not
// been looked at yet, and a photo of the garden is before long a photo of
// somebody in it.
//
// Sorting is one rule, Sort, whether a steward does it on the phone or their
// Claude does it through the API: a photo becomes a plant's photo, or a plant
// just planted at a place (a listing and a photo), or is set aside as not
// sure yet, or is discarded. What it becomes arrives as photobus and
// listingbus make anything: a plant's photo unchecked, for a steward to
// compare with the plant, as every photo from an import is. The inbox keeps
// its row afterwards, with what it became, so that the same photo sent again
// is recognised rather than sorted twice.
package inboxbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/imaging"
)

// MaxBytes is the largest photo accepted, the same as photobus's: the inbox is
// where a photo goes on its way to becoming one of those.
const MaxBytes = photobus.MaxBytes

// MaxNote is the longest note, in characters. A line, not a report: the note
// is what jogs the memory at sort time ("the one by the gate"), and anything
// longer belongs on the place.
const MaxNote = 300

// At is where a batch of photos was taken.
type At string

const (
	// Property is the garden itself, and the default: most photos are.
	Property At = "property"

	// Nursery is a nursery's sales yard, where a photo is of a plant for
	// sale or its tag, and never of a place here.
	Nursery At = "nursery"
)

// Label is the stewards' name for it.
func (a At) Label() string {
	if a == Nursery {
		return "At a nursery"
	}

	return "On the property"
}

// Status is how far a photo has got.
type Status string

const (
	// New is a photo nobody has sorted yet.
	New Status = "new"

	// Unsure is a photo looked at and set aside: not sure yet what it is,
	// with the question in its note. Still in the inbox, and the stewards'
	// "unknown" until somebody knows.
	Unsure Status = "unsure"

	// Sorted is a photo that became something: its Outcome says what.
	Sorted Status = "sorted"

	// Discarded is a photo thrown away. Its files are gone; the row stays,
	// so the same photo sent again is recognised.
	Discarded Status = "discarded"
)

// Outcome is what a photo is sorted into.
type Outcome string

const (
	// AsPhoto is a photo of a plant, added to the plant's photos: to find
	// out later what one is, or a flower not often seen.
	AsPhoto Outcome = "photo"

	// AsPlanted is a plant just planted at a place: it is listed there to
	// protect, off the To plant list if it was on it, and the photo is
	// added to its photos as a young plant there.
	AsPlanted Outcome = "planted"

	// AsUnsure sets a photo aside, with a question. Not a final outcome:
	// the photo stays in the inbox, to be sorted again.
	AsUnsure Outcome = "unsure"

	// AsDiscard throws the photo away.
	AsDiscard Outcome = "discard"
)

// Position is where the camera said it was. Never shown as fact: under oak
// and cedar a phone's GPS drifts by 5 to 15 metres (design.md §3), and the
// place a steward chose is the answer to "where".
type Position = imaging.Position

// Item is one photo in the inbox.
type Item struct {
	ID types.ID

	// FromID is the steward who sent it.
	FromID types.ID

	At At

	// PlaceID is where on the property it was taken, if the steward said;
	// zero for not said, and always zero at a nursery.
	PlaceID types.ID
	Note    string

	// TakenAt is when the camera says it was taken; zero when it did not
	// say the day. Where is where it says, if Located.
	TakenAt time.Time
	Where   Position
	Located bool

	Status Status

	// What it was sorted into, by whom and when, once it was. SpeciesID
	// and PhotoID are the plant and the photo it became; zero for none.
	Outcome   Outcome
	SpeciesID types.ID
	PhotoID   types.ID
	SortedBy  types.ID
	SortedAt  time.Time

	// Format is the original's, "jpeg" or "png", which names its file.
	Format       string
	Large, Small photobus.Dimensions

	// SHA256 is the original's digest, hex: the same photo sent twice is
	// recognised rather than kept twice.
	SHA256 string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// When is the moment to sort a photo by: when it was taken, or when it
// arrived if the camera did not say.
func (it Item) When() time.Time {
	if it.TakenAt.IsZero() {
		return it.CreatedAt
	}

	return it.TakenAt
}

// Fields is what is said about a batch of photos when it is sent. Every photo
// in the batch gets the same.
type Fields struct {
	FromID  types.ID
	At      At
	PlaceID types.ID
	Note    string
}

// Invalid is a batch that could not be kept as given, shaped like
// photobus.Invalid.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

var (
	// ErrNotFound is returned for a photo that is not in the inbox.
	ErrNotFound = errors.New("there is no such photo in the inbox")

	// ErrUnknown is returned by a Storer when the place or the steward a
	// photo names does not exist.
	ErrUnknown = errors.New("the place or the steward is not known")

	// ErrDuplicate is returned by a Storer's Create when the inbox already
	// has a photo with the same content.
	ErrDuplicate = errors.New("the inbox already has this photo")

	// ErrTaken is returned by a Storer's Claim when the photo has already
	// been sorted or discarded: somebody else got there first.
	ErrTaken = errors.New("the photo has already been sorted")
)

// AlreadySorted is Sort's answer for a photo that has been sorted already,
// differently from what was asked: the photo as it was sorted. The same sort
// asked twice is not this but the first answer again, as an import's is.
type AlreadySorted struct {
	Item Item
}

func (e AlreadySorted) Error() string {
	if e.Item.Status == Discarded {
		return "that photo was discarded"
	}

	return "that photo was already sorted, as " + string(e.Item.Outcome)
}

// Duplicate is Add's answer to a photo the inbox already has: the one it has.
// Not a failure to the person sending -- ten photos chosen again because the
// signal dropped halfway are nine already safe -- but worth saying.
type Duplicate struct {
	Item Item
}

func (d Duplicate) Error() string {
	return "that photo is already in the inbox, as " + d.Item.ID.String()
}

// Claim is what a Storer's Claim writes: the photo's end, if it is still
// new or unsure.
type Claim struct {
	Status    Status
	Outcome   Outcome
	SpeciesID types.ID
	By        types.ID
	At        time.Time
}

// Storer is what the rules need from the database.
type Storer interface {
	Create(ctx context.Context, it Item) error
	ByID(ctx context.Context, id types.ID) (Item, error)
	BySHA256(ctx context.Context, sum string) (Item, error)
	WithStatus(ctx context.Context, s Status) ([]Item, error)
	Count(ctx context.Context, s Status) (int, error)

	// Claim marks a photo sorted or discarded, in one statement that
	// matches only a photo still new or unsure, so two stewards sorting the
	// same photo at once cannot both succeed: the second gets ErrTaken.
	Claim(ctx context.Context, id types.ID, c Claim) error

	// Unclaim puts a claimed photo back as it was, when what it was being
	// sorted into could not be made.
	Unclaim(ctx context.Context, id types.ID, was Status, at time.Time) error

	// SetPhoto records the photo a sorted one became.
	SetPhoto(ctx context.Context, id, photoID types.ID, at time.Time) error

	// SetUnsure sets a photo still new or unsure aside, with its note;
	// ErrTaken if it has been sorted meanwhile.
	SetUnsure(ctx context.Context, id types.ID, note string, at time.Time) error
}

// Photos is what sorting needs from the photo rules.
type Photos interface {
	Add(ctx context.Context, speciesID types.ID, f photobus.Fields, data []byte) (photobus.Photo, error)
}

// Listings is what it needs from the listing rules.
type Listings interface {
	ForPlace(ctx context.Context, placeID types.ID) ([]listingbus.Listing, error)
	Set(ctx context.Context, placeID, speciesID types.ID, f listingbus.Fields) (listingbus.Listing, error)
}

// Business holds the rules.
type Business struct {
	store    Storer
	files    photobus.Files
	photos   Photos
	listings Listings
	now      func() time.Time
}

// NewBusiness constructs one. files is a directory of the inbox's own,
// separate from the species photos', so that an inbox photo can never be
// served by the route that serves those. now may be nil, for the real clock.
func NewBusiness(store Storer, files photobus.Files, photos Photos, listings Listings, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, files: files, photos: photos, listings: listings, now: now}
}

// Check is the batch's fields checked on their own, before any photo is read:
// a batch of ten with a note too long is refused once, not ten times.
func (b *Business) Check(f Fields) (Fields, error) {
	f.Note = strings.TrimSpace(f.Note)

	// A place at a nursery is a contradiction a form can make -- a place
	// chosen, then the toggle flipped -- and the toggle is the later and
	// the likelier word. Dropped rather than refused, for a steward
	// standing in a sales yard.
	if f.At == Nursery {
		f.PlaceID = types.ID{}
	}

	switch {
	case f.At != Property && f.At != Nursery:
		return f, Invalid{Field: "at", Problem: "say whether these were taken on the property or at a nursery"}
	case f.FromID.Zero():
		return f, Invalid{Field: "from", Problem: "sign in again, and then send the photos"}
	case utf8.RuneCountInString(f.Note) > MaxNote:
		return f, Invalid{Field: "note", Problem: fmt.Sprintf("the note is longer than %d characters. Keep it to a line; the rest can be said when the photos are sorted", MaxNote)}
	}

	return f, nil
}

// Add keeps one photo of a batch: the original as it came, its large and small
// pictures, and the row that says what is known of it.
func (b *Business) Add(ctx context.Context, f Fields, data []byte) (Item, error) {
	f, err := b.Check(f)
	if err != nil {
		return Item{}, err
	}

	digest := sha256.Sum256(data)
	sum := hex.EncodeToString(digest[:])

	// Before decoding, which is the expensive part; the unique index makes
	// it a rule rather than a courtesy.
	switch existing, err := b.store.BySHA256(ctx, sum); {
	case err == nil:
		return Item{}, Duplicate{Item: existing}
	case !errors.Is(err, ErrNotFound):
		return Item{}, err
	}

	prepared, err := imaging.Prepare(ctx, data)

	switch {
	case errors.Is(err, imaging.ErrNotAPhoto), errors.Is(err, imaging.ErrTooSmall), errors.Is(err, imaging.ErrTooLarge):
		return Item{}, Invalid{Field: "photo", Problem: err.Error()}
	case err != nil:
		return Item{}, fmt.Errorf("preparing a photo for the inbox: %w", err)
	}

	now := b.now().UTC().Truncate(time.Millisecond)

	it := Item{
		ID: types.NewID(), FromID: f.FromID, At: f.At, PlaceID: f.PlaceID, Note: f.Note,
		Where: prepared.Where, Located: prepared.Located,
		Status: New, Format: prepared.Format, SHA256: sum,
		Large:     photobus.Dimensions{Width: prepared.Large.Width, Height: prepared.Large.Height},
		Small:     photobus.Dimensions{Width: prepared.Small.Width, Height: prepared.Small.Height},
		CreatedAt: now, UpdatedAt: now,
	}

	// A camera that wrote no zone wrote the garden's wall clock, or the
	// nursery's, which is the same one for every nursery anybody here
	// drives to.
	if at, ok := prepared.Taken.Time(types.Garden); ok {
		it.TakenAt = at.UTC().Truncate(time.Millisecond)
	}

	// The pictures first and the row second, as photobus does: a row must
	// never name a file that is not there.
	written, err := b.put(it, map[string][]byte{
		photobus.Name(it.ID, photobus.Large): prepared.Large.JPEG,
		photobus.Name(it.ID, photobus.Small): prepared.Small.JPEG,
		originalName(it):                     data,
	})
	if err != nil {
		b.remove(written)

		return Item{}, err
	}

	if err := b.store.Create(ctx, it); err != nil {
		b.remove(written)

		if errors.Is(err, ErrDuplicate) {
			if existing, err := b.store.BySHA256(ctx, sum); err == nil {
				return Item{}, Duplicate{Item: existing}
			}
		}

		if errors.Is(err, ErrUnknown) {
			return Item{}, Invalid{Field: "place", Problem: "that place is not in the list any more. Open the page again and choose from the list, or leave it empty"}
		}

		return Item{}, err
	}

	return it, nil
}

// ByID is one photo, or ErrNotFound.
func (b *Business) ByID(ctx context.Context, id types.ID) (Item, error) {
	return b.store.ByID(ctx, id)
}

// Waiting is every photo not yet sorted, the most recently taken first: the
// batch just sent is at the top, and the order inside it is the order the
// steward walked the garden in, backwards.
func (b *Business) Waiting(ctx context.Context) ([]Item, error) {
	all, err := b.store.WithStatus(ctx, New)
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(all, func(x, y Item) int {
		if c := y.When().Compare(x.When()); c != 0 {
			return c
		}

		return y.CreatedAt.Compare(x.CreatedAt)
	})

	return all, nil
}

// SetAside is every photo set aside as not sure yet, in the same order as
// Waiting.
func (b *Business) SetAside(ctx context.Context) ([]Item, error) {
	all, err := b.store.WithStatus(ctx, Unsure)
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(all, func(x, y Item) int {
		if c := y.When().Compare(x.When()); c != 0 {
			return c
		}

		return y.CreatedAt.Compare(x.CreatedAt)
	})

	return all, nil
}

// Count is how many photos are waiting to be sorted, for the stewards' front
// page: the only reminder the inbox gives. Not those set aside, which have
// been looked at and are waiting on somebody knowing, not on somebody looking.
func (b *Business) Count(ctx context.Context) (int, error) {
	return b.store.Count(ctx, New)
}

// Sorting is what a photo is sorted into.
type Sorting struct {
	Outcome Outcome

	// SpeciesID is the plant it shows, for a photo or a planting.
	SpeciesID types.ID

	// Kind is which of a plant's five pictures it is. For a planting it may
	// be left empty, for a young plant.
	Kind photobus.Kind

	// PlaceID is where it was taken or planted. Empty means the place the
	// photo was sent with, if any. Required for a planting.
	PlaceID types.ID

	// Note is the question, for a photo set aside; empty keeps the note it
	// came with.
	Note string
}

// Result is what a photo was sorted into.
type Result struct {
	Item Item

	// Photo is the plant's photo it became, for a photo or a planting.
	// Duplicate is true when the plant already had the photo, sent to it
	// some other way, and Photo is that one.
	Photo     photobus.Photo
	Duplicate bool

	// Listing is the plant's listing at the place, for a planting.
	Listing listingbus.Listing

	// Unchanged is true when the photo had already been sorted exactly so:
	// the same sort sent twice, which a program retrying is apt to do.
	Unchanged bool
}

// Sort sorts a photo, as by the steward by.
//
// A photo or a planting is claimed first, in one statement, and then made:
// the plant's photo added from the inbox's original, so the camera's date and
// position go with it, and for a planting the listing set. Claimed first so
// that two stewards sorting the same photo at once cannot both make
// something; put back if what it was to become cannot be made, so that a
// refusal leaves the photo in the inbox for another try. The inbox's copies
// are removed once the photo is the plant's, which keeps its own original.
func (b *Business) Sort(ctx context.Context, id, by types.ID, s Sorting) (Result, error) {
	it, err := b.store.ByID(ctx, id)
	if err != nil {
		return Result{}, err
	}

	s.Note = strings.TrimSpace(s.Note)

	if s.PlaceID.Zero() {
		s.PlaceID = it.PlaceID
	}

	// A nursery's plant is not growing at any place here.
	if it.At == Nursery {
		s.PlaceID = types.ID{}
	}

	if s.Outcome == AsPlanted && s.Kind == "" {
		s.Kind = photobus.Young
	}

	if it.Status == Sorted || it.Status == Discarded {
		if it.Outcome == s.Outcome && it.SpeciesID == s.SpeciesID && (s.Outcome == AsPhoto || s.Outcome == AsPlanted || s.Outcome == AsDiscard) {
			return Result{Item: it, Unchanged: true}, nil
		}

		return Result{}, AlreadySorted{Item: it}
	}

	if err := b.checkSorting(it, s); err != nil {
		return Result{}, err
	}

	now := b.now().UTC().Truncate(time.Millisecond)

	switch s.Outcome {
	case AsUnsure:
		note := s.Note
		if note == "" {
			note = it.Note
		}

		if err := b.store.SetUnsure(ctx, id, note, now); err != nil {
			return Result{}, b.taken(ctx, id, err)
		}

		it.Status, it.Note, it.UpdatedAt = Unsure, note, now

		return Result{Item: it}, nil

	case AsDiscard:
		if err := b.store.Claim(ctx, id, Claim{Status: Discarded, Outcome: AsDiscard, By: by, At: now}); err != nil {
			return Result{}, b.taken(ctx, id, err)
		}

		it.Status, it.Outcome, it.SortedBy, it.SortedAt, it.UpdatedAt = Discarded, AsDiscard, by, now, now

		if err := b.removeFiles(it); err != nil {
			return Result{Item: it}, fmt.Errorf("the photo is discarded, but its files are not: %w", err)
		}

		return Result{Item: it}, nil
	}

	// A planting is checked against what is listed before anything is
	// claimed: a plant listed to pull here is a contradiction a steward
	// settles on the place's screen, and nothing is made until they have.
	var listed *listingbus.Listing

	if s.Outcome == AsPlanted {
		if listed, err = b.listing(ctx, s.PlaceID, s.SpeciesID); err != nil {
			return Result{}, err
		}

		if listed != nil && listed.Action == listingbus.Pull {
			return Result{}, Invalid{Field: "species", Problem: "that plant is listed to pull at that place. If it was planted there on purpose, change it to protect on the place's Plants screen first, then sort this photo"}
		}
	}

	was := it.Status

	if err := b.store.Claim(ctx, id, Claim{Status: Sorted, Outcome: s.Outcome, SpeciesID: s.SpeciesID, By: by, At: now}); err != nil {
		return Result{}, b.taken(ctx, id, err)
	}

	// From the claim on, the bookkeeping is finished even if the request
	// that asked for it is gone: a phone that loses its signal halfway
	// must not leave a photo claimed with nothing made, or made with the
	// inbox not knowing.
	keep := context.WithoutCancel(ctx)

	res, err := b.make(ctx, it, s, listed)
	if err != nil {
		// Best effort: the error that matters is the one being returned.
		_ = b.store.Unclaim(keep, id, was, b.now().UTC().Truncate(time.Millisecond))

		return Result{}, err
	}

	if err := b.store.SetPhoto(keep, id, res.Photo.ID, now); err != nil {
		return Result{}, err
	}

	it.Status, it.Outcome, it.SpeciesID, it.PhotoID = Sorted, s.Outcome, s.SpeciesID, res.Photo.ID
	it.SortedBy, it.SortedAt, it.UpdatedAt = by, now, now
	res.Item = it

	// The plant's photo is made and holds its own original; the inbox's
	// copies are only disk now. A failure to remove them is not a failure
	// to sort.
	_ = b.removeFiles(it)

	return res, nil
}

// make adds the plant's photo and, for a planting, its listing.
func (b *Business) make(ctx context.Context, it Item, s Sorting, listed *listingbus.Listing) (Result, error) {
	data, err := b.original(it)
	if err != nil {
		return Result{}, err
	}

	var res Result

	res.Photo, err = b.photos.Add(ctx, s.SpeciesID, photobus.Fields{Kind: s.Kind, PlaceID: s.PlaceID, Source: photobus.Ours}, data)

	invalid, isInvalid := errors.AsType[photobus.Invalid](err)
	dup, isDup := errors.AsType[photobus.Duplicate](err)

	switch {
	case isDup:
		res.Photo, res.Duplicate = dup.Photo, true
	case isInvalid:
		return Result{}, Invalid{Field: invalid.Field, Problem: invalid.Problem}
	case err != nil:
		return Result{}, err
	}

	if s.Outcome != AsPlanted {
		return res, nil
	}

	// Kept as listed if it was -- careful stays careful, and its note stays
	// -- and taken off the To plant list, because it is in the ground now.
	f := listingbus.Fields{Action: listingbus.Protect}
	if listed != nil {
		f = listingbus.Fields{Action: listed.Action, Note: listed.Note}
	}

	res.Listing, err = b.listings.Set(ctx, s.PlaceID, s.SpeciesID, f)
	if invalid, ok := errors.AsType[listingbus.Invalid](err); ok {
		return Result{}, Invalid{Field: invalid.Field, Problem: invalid.Problem}
	}

	return res, err
}

func (b *Business) checkSorting(it Item, s Sorting) error {
	switch s.Outcome {
	case AsPhoto, AsPlanted:
		switch {
		case s.SpeciesID.Zero():
			return Invalid{Field: "species", Problem: "choose the plant it shows. If it is not in the list, add the plant first"}
		case !slices.Contains(photobus.Kinds, s.Kind):
			return Invalid{Field: "kind", Problem: "choose which kind of photo this is: young plant, leaf, flower, full size, or winter"}
		case s.Outcome == AsPlanted && it.At == Nursery:
			return Invalid{Field: "outcome", Problem: "a photo taken at a nursery is not of a plant planted here. Add it as a photo of the plant instead"}
		case s.Outcome == AsPlanted && s.PlaceID.Zero():
			return Invalid{Field: "place", Problem: "choose the place it was planted"}
		}
	case AsUnsure:
		if utf8.RuneCountInString(s.Note) > MaxNote {
			return Invalid{Field: "note", Problem: fmt.Sprintf("the question is longer than %d characters. Keep it to a line", MaxNote)}
		}
	case AsDiscard:
	default:
		return Invalid{Field: "outcome", Problem: "choose what this photo is: a plant's photo, a plant just planted, not sure yet, or discard"}
	}

	return nil
}

// listing is the plant's listing at the place, or nil.
func (b *Business) listing(ctx context.Context, placeID, speciesID types.ID) (*listingbus.Listing, error) {
	all, err := b.listings.ForPlace(ctx, placeID)
	if err != nil {
		return nil, fmt.Errorf("reading what is listed at the place: %w", err)
	}

	for _, l := range all {
		if l.SpeciesID == speciesID {
			return &l, nil
		}
	}

	return nil, nil
}

// taken turns a refused claim into the photo as somebody else sorted it.
func (b *Business) taken(ctx context.Context, id types.ID, err error) error {
	if !errors.Is(err, ErrTaken) {
		return err
	}

	it, rerr := b.store.ByID(ctx, id)
	if rerr != nil {
		return rerr
	}

	return AlreadySorted{Item: it}
}

// original reads the photo as it was sent.
func (b *Business) original(it Item) ([]byte, error) {
	f, err := b.files.Open(originalName(it))
	if err != nil {
		return nil, fmt.Errorf("opening inbox photo %s: %w", it.ID, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading inbox photo %s: %w", it.ID, err)
	}

	return data, nil
}

func (b *Business) removeFiles(it Item) error {
	var errs []error
	for _, name := range []string{photobus.Name(it.ID, photobus.Large), photobus.Name(it.ID, photobus.Small), originalName(it)} {
		if err := b.files.Remove(name); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Open reads one of a photo's pictures. Only a steward may be shown one; the
// caller sees to that.
func (b *Business) Open(ctx context.Context, id types.ID, size photobus.Size) (Item, photobus.File, error) {
	if size != photobus.Large && size != photobus.Small {
		return Item{}, nil, ErrNotFound
	}

	it, err := b.store.ByID(ctx, id)
	if err != nil {
		return Item{}, nil, err
	}

	// Its pictures went when it became something, or was thrown away.
	if it.Status == Sorted || it.Status == Discarded {
		return Item{}, nil, ErrNotFound
	}

	f, err := b.files.Open(photobus.Name(it.ID, size))
	if err != nil {
		return Item{}, nil, fmt.Errorf("opening the %s picture of inbox photo %s: %w", size, it.ID, err)
	}

	return it, f, nil
}

func originalName(it Item) string {
	ext := "jpg"
	if it.Format == "png" {
		ext = "png"
	}

	return it.ID.String() + "-original." + ext
}

func (b *Business) put(it Item, files map[string][]byte) ([]string, error) {
	var written []string

	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := b.files.Put(name, files[name]); err != nil {
			return written, fmt.Errorf("keeping inbox photo %s: %w", it.ID, err)
		}

		written = append(written, name)
	}

	return written, nil
}

// remove is the cleanup after a failed Add, best effort: the error that
// mattered is the one already being returned.
func (b *Business) remove(names []string) {
	for _, name := range names {
		_ = b.files.Remove(name)
	}
}
