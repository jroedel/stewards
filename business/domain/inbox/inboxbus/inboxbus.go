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
package inboxbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
)

// Duplicate is Add's answer to a photo the inbox already has: the one it has.
// Not a failure to the person sending -- ten photos chosen again because the
// signal dropped halfway are nine already safe -- but worth saying.
type Duplicate struct {
	Item Item
}

func (d Duplicate) Error() string {
	return "that photo is already in the inbox, as " + d.Item.ID.String()
}

// Storer is what the rules need from the database.
type Storer interface {
	Create(ctx context.Context, it Item) error
	ByID(ctx context.Context, id types.ID) (Item, error)
	BySHA256(ctx context.Context, sum string) (Item, error)
	WithStatus(ctx context.Context, s Status) ([]Item, error)
	Count(ctx context.Context, s Status) (int, error)
}

// Business holds the rules.
type Business struct {
	store Storer
	files photobus.Files
	now   func() time.Time
}

// NewBusiness constructs one. files is a directory of the inbox's own,
// separate from the species photos', so that an inbox photo can never be
// served by the route that serves those. now may be nil, for the real clock.
func NewBusiness(store Storer, files photobus.Files, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, files: files, now: now}
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

// Count is how many photos are waiting to be sorted, for the stewards' front
// page: the only reminder the inbox gives.
func (b *Business) Count(ctx context.Context) (int, error) {
	return b.store.Count(ctx, New)
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
