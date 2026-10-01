// Package photobus is the rules about photos: what kind each one is, where it
// came from, whether it has been checked, and what is kept of it.
//
// A species photo is one of five kinds (design.md §5): young plant, leaf
// close-up, flower close-up, mature plant at full size, and winter or seed
// head. The kind is what lets each view of a species card pick its picture --
// a weeder is shown the seedling and the leaf, a planter the flower and the
// grown plant -- and a kind with no photo yet is the stewards' to-do list.
//
// Every photo says where it came from. Ours carries the place it was taken
// and the month, "our Winecup, rim of the rain garden, April 2027", which is
// the photo record the plan wants growing for free. A borrowed one needs the
// page it came from, its author and its licence, because the licence is the
// permission and the credit is its condition.
//
// And a photo is shown to volunteers only once a steward has checked that it
// is the species it claims to be. Two of the skinny bed guide's "frostweed"
// photos were wingstem; an unchecked photo on a card teaches a volunteer the
// wrong plant with the app's authority behind it.
//
// The table has room for a photo that is not of a species -- a place's photo
// point, a volunteer's "What is this?" -- which is why the species is a column
// that may be empty rather than one that must not. Today every photo is a
// species photo, and Add says so.
package photobus

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/imaging"
)

// MaxBytes is the largest upload accepted: a phone camera's JPEG is 2 to 8 MB
// and a 50-megapixel one can pass 15, so this is room for any of them without
// being room for a video.
const MaxBytes = 25 << 20

// Kind is which of the five pictures of a plant this is.
type Kind string

const (
	Young  Kind = "young"
	Leaf   Kind = "leaf"
	Flower Kind = "flower"
	Mature Kind = "mature"
	Winter Kind = "winter"
)

// Kinds is every kind, in the order a plant grows through them and a form
// offers them.
var Kinds = []Kind{Young, Leaf, Flower, Mature, Winter}

// Label is the kind's name on the stewards' screens. Volunteer copy is the
// app's, in both languages.
func (k Kind) Label() string {
	switch k {
	case Young:
		return "Young plant"
	case Leaf:
		return "Leaf close-up"
	case Flower:
		return "Flower close-up"
	case Mature:
		return "Mature plant, full size"
	case Winter:
		return "Winter or seed head"
	}

	return "Not set"
}

// Source is whose photo it is.
type Source string

const (
	// Ours: taken here, by a steward or a volunteer.
	Ours Source = "ours"

	// Borrowed: from Wikimedia Commons, iNaturalist or the like, under an
	// open licence.
	Borrowed Source = "borrowed"
)

// Size is which of the kept pictures to read.
type Size string

const (
	Large Size = "large"
	Small Size = "small"
)

// Dimensions is a picture's size in pixels, for the page to reserve the space
// before it loads.
type Dimensions struct {
	Width, Height int
}

// Photo is one photo and what is known about it.
type Photo struct {
	ID        types.ID
	SpeciesID types.ID

	// PlaceID is where it was taken, for one of ours; zero when not said.
	PlaceID types.ID

	Kind Kind

	// The month it was taken. Either may be zero: a borrowed photo often
	// says the year and not the month, or neither.
	TakenYear, TakenMonth int

	Source Source

	// Credit is the author, for a borrowed photo; for ours it may be empty,
	// and the card then credits the garden stewards.
	Credit    string
	SourceURL string
	License   string

	// Checked is a steward's word that the photo shows this species.
	// Unchecked, it is not shown to volunteers or served to them.
	Checked bool

	// Format is the original's, "jpeg" or "png". The original is kept,
	// privately: it is the only copy at full size, and it holds the GPS
	// position the plan wants kept for Phase 2. It is never served.
	Format string

	Large, Small Dimensions

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is what a steward says about a photo.
type Fields struct {
	Kind                  Kind
	PlaceID               types.ID
	TakenYear, TakenMonth int
	Source                Source
	Credit                string
	SourceURL             string
	License               string
	Checked               bool
}

// Invalid is a photo a steward could not save as given, shaped like
// placebus.Invalid.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

var (
	// ErrNotFound is returned for a photo that does not exist.
	ErrNotFound = errors.New("there is no such photo")

	// ErrUnknown is returned by a Storer when the species or the place a
	// photo names does not exist.
	ErrUnknown = errors.New("the species or place is not known")
)

// Storer is what the rules need from the database.
type Storer interface {
	Create(ctx context.Context, p Photo) error
	Update(ctx context.Context, p Photo) error
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (Photo, error)
	ForSpecies(ctx context.Context, speciesID types.ID) ([]Photo, error)
}

// File is a kept picture opened for reading. *os.File is one.
type File interface {
	io.ReadSeekCloser
	Stat() (fs.FileInfo, error)
}

// Files is where the pictures themselves are kept: a directory, beside the
// database and never under the web root.
type Files interface {
	Put(name string, data []byte) error
	Open(name string) (File, error)
	Remove(name string) error
}

// Business holds the rules.
type Business struct {
	store Storer
	files Files
	now   func() time.Time

	// prepare is held while a photo is decoded and scaled. A 12-megapixel
	// photo is about 50 MB of pixels while it is worked on, and a shared
	// host's memory is not ours to spend twice over because two stewards
	// pressed Upload in the same second; the second waits a second.
	prepare chan struct{}
}

// NewBusiness constructs one. now may be nil, for the real clock.
func NewBusiness(store Storer, files Files, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, files: files, now: now, prepare: make(chan struct{}, 1)}
}

// Add keeps a new photo of a species: the large and small pictures made from
// it, the original, and the row that says what it is.
//
// The fields are checked before the photo is decoded, so a form missing its
// licence costs nothing. When the steward did not say the month, the
// camera's date is used, which is right for our own photos and harmless for
// a borrowed one (they rarely carry a date at all).
func (b *Business) Add(ctx context.Context, speciesID types.ID, f Fields, data []byte) (Photo, error) {
	f = tidy(f)

	if err := b.check(f); err != nil {
		return Photo{}, err
	}

	if speciesID.Zero() {
		return Photo{}, Invalid{Field: "species", Problem: "a photo needs the plant it shows"}
	}

	prepared, err := b.prepared(ctx, data)
	if err != nil {
		return Photo{}, err
	}

	if f.TakenYear == 0 && f.TakenMonth == 0 {
		f.TakenYear, f.TakenMonth = prepared.Taken.Year, prepared.Taken.Month
	}

	now := b.now().UTC().Truncate(time.Millisecond)
	p := Photo{ID: types.NewID(), SpeciesID: speciesID, Format: prepared.Format, CreatedAt: now, UpdatedAt: now}
	p.apply(f)
	p.Large = Dimensions{prepared.Large.Width, prepared.Large.Height}
	p.Small = Dimensions{prepared.Small.Width, prepared.Small.Height}

	// The pictures first and the row second: a row is what makes a photo
	// visible, so it must never name a file that is not there. A failure
	// after the files are written takes them away again.
	written, err := b.put(p, map[string][]byte{
		Name(p.ID, Large): prepared.Large.JPEG,
		Name(p.ID, Small): prepared.Small.JPEG,
		originalName(p):   data,
	})
	if err != nil {
		b.remove(written)

		return Photo{}, err
	}

	if err := b.store.Create(ctx, p); err != nil {
		b.remove(written)

		if errors.Is(err, ErrUnknown) {
			return Photo{}, Invalid{Field: "place", Problem: "the plant or the place is not here any more. Open the page again and choose from the list"}
		}

		return Photo{}, err
	}

	return p, nil
}

// prepared decodes and scales one photo at a time, turning the reasons a
// photo cannot be used into something a steward can act on.
func (b *Business) prepared(ctx context.Context, data []byte) (imaging.Prepared, error) {
	select {
	case b.prepare <- struct{}{}:
	case <-ctx.Done():
		return imaging.Prepared{}, ctx.Err()
	}
	defer func() { <-b.prepare }()

	p, err := imaging.Prepare(data)

	switch {
	case errors.Is(err, imaging.ErrNotAPhoto), errors.Is(err, imaging.ErrTooSmall), errors.Is(err, imaging.ErrTooLarge):
		return p, Invalid{Field: "photo", Problem: err.Error()}
	case err != nil:
		return p, fmt.Errorf("preparing a photo: %w", err)
	}

	return p, nil
}

// Update changes what is said about a photo. The pictures do not change; a
// different photo is a new one.
func (b *Business) Update(ctx context.Context, id types.ID, f Fields) (Photo, error) {
	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Photo{}, err
	}

	f = tidy(f)
	if err := b.check(f); err != nil {
		return Photo{}, err
	}

	p.apply(f)
	p.UpdatedAt = b.now().UTC().Truncate(time.Millisecond)

	switch err := b.store.Update(ctx, p); {
	case errors.Is(err, ErrUnknown):
		return Photo{}, Invalid{Field: "place", Problem: "that place is not in the list any more. Choose another, or leave it empty"}
	case err != nil:
		return Photo{}, err
	}

	return p, nil
}

// Delete removes a photo: the row, so nothing shows it, and then its files.
//
// In that order, for the reason Add writes them in the other: a row naming
// missing files is a broken picture on a card, while files no row names are
// only disk. If the files cannot be removed the error says so, and the photo
// is already gone from every page.
func (b *Business) Delete(ctx context.Context, id types.ID) error {
	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return err
	}

	if err := b.store.Delete(ctx, id); err != nil {
		return err
	}

	var errs []error
	for _, name := range []string{Name(p.ID, Large), Name(p.ID, Small), originalName(p)} {
		if err := b.files.Remove(name); err != nil {
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("the photo is removed, but its files are not: %w", err)
	}

	return nil
}

// ByID is one photo, or ErrNotFound.
func (b *Business) ByID(ctx context.Context, id types.ID) (Photo, error) {
	return b.store.ByID(ctx, id)
}

// ForSpecies is every photo of a species, checked or not, by kind and then
// best first (see Better).
func (b *Business) ForSpecies(ctx context.Context, speciesID types.ID) ([]Photo, error) {
	all, err := b.store.ForSpecies(ctx, speciesID)
	if err != nil {
		return nil, err
	}

	slices.SortStableFunc(all, func(x, y Photo) int {
		if c := cmp.Compare(slices.Index(Kinds, x.Kind), slices.Index(Kinds, y.Kind)); c != 0 {
			return c
		}

		return Better(x, y)
	})

	return all, nil
}

// Open reads one of a photo's pictures. The caller decides who may see it:
// Checked is on the Photo it returns.
func (b *Business) Open(ctx context.Context, id types.ID, size Size) (Photo, File, error) {
	if size != Large && size != Small {
		return Photo{}, nil, ErrNotFound
	}

	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Photo{}, nil, err
	}

	f, err := b.files.Open(Name(p.ID, size))
	if err != nil {
		return Photo{}, nil, fmt.Errorf("opening the %s picture of photo %s: %w", size, p.ID, err)
	}

	return p, f, nil
}

// Best is the photo a volunteer is shown for a kind: checked, and the best of
// those by Better. ok is false when there is none, which a card shows as
// "No leaf photo yet".
func Best(photos []Photo, k Kind) (Photo, bool) {
	var best Photo
	found := false

	for _, p := range photos {
		if p.Kind != k || !p.Checked {
			continue
		}

		if !found || Better(p, best) < 0 {
			best, found = p, true
		}
	}

	return best, found
}

// Better orders two photos of the same kind, the one to show first first: our
// own before a borrowed one, because "our Winecup, rim of the rain garden"
// beats any nursery photo (the plan); then the more recently taken; then the
// more recently added.
func Better(x, y Photo) int {
	if x.Source != y.Source {
		if x.Source == Ours {
			return -1
		}

		return 1
	}

	if c := cmp.Compare(y.TakenYear*12+y.TakenMonth, x.TakenYear*12+x.TakenMonth); c != 0 {
		return c
	}

	return y.CreatedAt.Compare(x.CreatedAt)
}

// Name is the file a picture is kept in. Public so the store and its tests
// agree on it without repeating the pattern.
func Name(id types.ID, size Size) string { return id.String() + "-" + string(size) + ".jpg" }

func originalName(p Photo) string {
	ext := "jpg"
	if p.Format == "png" {
		ext = "png"
	}

	return p.ID.String() + "-original." + ext
}

func (b *Business) put(p Photo, files map[string][]byte) ([]string, error) {
	var written []string

	// Sorted, so a failure part-way leaves the same files every time.
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := b.files.Put(name, files[name]); err != nil {
			return written, fmt.Errorf("keeping photo %s: %w", p.ID, err)
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

func (p *Photo) apply(f Fields) {
	p.Kind, p.PlaceID = f.Kind, f.PlaceID
	p.TakenYear, p.TakenMonth = f.TakenYear, f.TakenMonth
	p.Source, p.Credit, p.SourceURL, p.License = f.Source, f.Credit, f.SourceURL, f.License
	p.Checked = f.Checked
}

// tidy trims what was typed. An address and a licence on one of our own
// photos are dropped rather than refused: they mean nothing there, and a
// steward who switched the source back to ours should not have to empty two
// boxes first.
func tidy(f Fields) Fields {
	f.Credit = strings.TrimSpace(f.Credit)
	f.SourceURL = strings.TrimSpace(f.SourceURL)
	f.License = strings.TrimSpace(f.License)

	if f.Source == Ours {
		f.SourceURL, f.License = "", ""
	}

	return f
}

func (b *Business) check(f Fields) error {
	switch {
	case !slices.Contains(Kinds, f.Kind):
		return Invalid{Field: "kind", Problem: "choose which kind of photo this is: young plant, leaf, flower, full size, or winter"}
	case f.Source != Ours && f.Source != Borrowed:
		return Invalid{Field: "source", Problem: "say whether this is our own photo or a borrowed one"}
	case f.TakenMonth < 0 || f.TakenMonth > 12:
		return Invalid{Field: "taken_month", Problem: "choose the month from the list"}
	case f.TakenYear != 0 && (f.TakenYear < 1990 || f.TakenYear > b.now().Year()+1):
		return Invalid{Field: "taken_year", Problem: "the year needs to be four figures, such as 2027, or empty"}
	case utf8.RuneCountInString(f.Credit) > 200:
		return Invalid{Field: "credit", Problem: "the credit is longer than 200 characters. Shorten it"}
	case utf8.RuneCountInString(f.License) > 100:
		return Invalid{Field: "license", Problem: "the licence is longer than 100 characters. Write its short name, such as CC BY-SA 4.0"}
	case len(f.SourceURL) > 500:
		return Invalid{Field: "source_url", Problem: "the address is longer than 500 characters. Use the photo's own page rather than a search"}
	}

	if f.Source != Borrowed {
		return nil
	}

	// A borrowed photo is used on the terms of its licence, and those terms
	// are what the three fields record.
	switch u, err := url.Parse(f.SourceURL); {
	case f.SourceURL == "":
		return Invalid{Field: "source_url", Problem: "a borrowed photo needs the address of the page it came from, so it can be credited and checked"}
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return Invalid{Field: "source_url", Problem: "the address needs to start with https://, copied from the photo's page"}
	case f.Credit == "":
		return Invalid{Field: "credit", Problem: "a borrowed photo needs its author's name, as its licence asks"}
	case f.License == "":
		return Invalid{Field: "license", Problem: "a borrowed photo needs its licence, such as CC BY-SA 4.0"}
	}

	return nil
}
