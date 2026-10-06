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
	"crypto/sha256"
	"encoding/hex"
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

// Kind is which of the six pictures of a plant this is.
//
// There were five, with seed heads filed under winter. Fruit came sixth on
// 2026-10-06, as "fruit or seed": berries, pods, winged seeds and seed
// heads, whenever the plant carries them. It is what tells nandina by its
// red berries and tree of heaven by its papery winged seeds, and what a
// planter asks about birds; winter is now how the plant looks in winter.
// Photos filed as winter before then stay so until a steward re-files them.
type Kind string

const (
	Young  Kind = "young"
	Leaf   Kind = "leaf"
	Flower Kind = "flower"
	Fruit  Kind = "fruit"
	Mature Kind = "mature"
	Winter Kind = "winter"
)

// Kinds is every kind, in the order a plant grows through them and a form
// offers them.
var Kinds = []Kind{Young, Leaf, Flower, Fruit, Mature, Winter}

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
	case Fruit:
		return "Fruit or seed"
	case Mature:
		return "Mature plant, full size"
	case Winter:
		return "In winter"
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

	// Full is the photo at the size it was kept -- up to 4096 pixels on its
	// long side since the send screen shrinks in the phone, and whatever it
	// came at before -- with what its camera wrote about it taken out
	// (imaging.Stripped). For seeing the leaves on a whole plant, which
	// 1600 pixels do not show. Made the first time it is asked for, and kept.
	Full Size = "full"
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

	// Elsewhere is one of ours taken off the property: in a park, at a
	// nursery, in a friend's garden. TakenWhere names it if anybody said;
	// it may be empty, and the card then says only that it was not taken
	// here. Kept apart from the name because the name is optional where a
	// photo is sent, and a photo from a park with no name given must still
	// never be shown as if it were of this garden.
	Elsewhere  bool
	TakenWhere string

	Kind Kind

	// The month it was taken. Either may be zero: a borrowed photo often
	// says the year and not the month, or neither.
	TakenYear, TakenMonth int

	// TakenAt is when it was taken, as the camera said, or as a steward said
	// to the day; zero for not known. When it is known, TakenYear and
	// TakenMonth are its, in the garden's time. The flowering record's
	// first and last days are read from it.
	TakenAt time.Time

	// InFlower is a steward's word that the plant is in flower in the photo,
	// whatever its kind: a mature plant in bloom is in flower as much as a
	// close-up of one. A flower photo always is. The flowering record counts
	// these.
	InFlower bool

	// InFruit is the same for fruit or seed on the plant: a fruit photo
	// always is. The record's fruiting days are read from these.
	InFruit bool

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

	// SHA256 is the original's digest, hex, by which the same photo sent
	// twice is recognised. Empty for none recorded.
	SHA256 string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Fields is what a steward says about a photo.
type Fields struct {
	Kind                  Kind
	PlaceID               types.ID
	Elsewhere             bool
	TakenWhere            string
	TakenYear, TakenMonth int
	TakenAt               time.Time
	InFlower, InFruit     bool
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

	// ErrDuplicate is returned by a Storer's Create when the species already
	// has a photo with the same content.
	ErrDuplicate = errors.New("the species already has this photo")
)

// Duplicate is Add's answer to a photo the species already has: the one it
// has. The screen says so; an import treats it as done.
type Duplicate struct {
	Photo Photo
}

func (d Duplicate) Error() string {
	return "that photo is already kept for this plant, as photo " + d.Photo.ID.String()
}

// Storer is what the rules need from the database.
type Storer interface {
	Create(ctx context.Context, p Photo) error
	Update(ctx context.Context, p Photo) error
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (Photo, error)
	ForSpecies(ctx context.Context, speciesID types.ID) ([]Photo, error)
	All(ctx context.Context) ([]Photo, error)
	BySHA256(ctx context.Context, speciesID types.ID, sum string) (Photo, error)
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
}

// NewBusiness constructs one. now may be nil, for the real clock.
func NewBusiness(store Storer, files Files, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, files: files, now: now}
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

	digest := sha256.Sum256(data)
	sum := hex.EncodeToString(digest[:])

	// Before decoding, which is the expensive part. The index below makes
	// it a rule rather than a courtesy: two at once cannot both be kept.
	switch existing, err := b.store.BySHA256(ctx, speciesID, sum); {
	case err == nil:
		return Photo{}, Duplicate{Photo: existing}
	case !errors.Is(err, ErrNotFound):
		return Photo{}, err
	}

	prepared, err := b.prepared(ctx, data)
	if err != nil {
		return Photo{}, err
	}

	if f.TakenYear == 0 && f.TakenMonth == 0 && f.TakenAt.IsZero() {
		f.TakenYear, f.TakenMonth = prepared.Taken.Year, prepared.Taken.Month

		if at, ok := prepared.Taken.Time(types.Garden); ok {
			f.TakenAt = at
			f = tidy(f)
		}
	}

	now := b.now().UTC().Truncate(time.Millisecond)
	p := Photo{ID: types.NewID(), SpeciesID: speciesID, Format: prepared.Format, SHA256: sum, CreatedAt: now, UpdatedAt: now}
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

		if errors.Is(err, ErrDuplicate) {
			if existing, err := b.store.BySHA256(ctx, speciesID, sum); err == nil {
				return Photo{}, Duplicate{Photo: existing}
			}
		}

		if errors.Is(err, ErrUnknown) {
			return Photo{}, Invalid{Field: "place", Problem: "the plant or the place is not here any more. Open the page again and choose from the list"}
		}

		return Photo{}, err
	}

	return p, nil
}

// Import is Add for a program: a script, or a steward's Claude, through the
// API. Two differences, both for the same reason as speciesbus.Import. It
// cannot mark a photo checked: that is a steward's word that it shows this
// plant, given after looking, and the wingstem photos are why. And sending a
// photo the plant already has is not a mistake but a batch sent twice, so it
// answers with the photo already kept and duplicate true.
func (b *Business) Import(ctx context.Context, speciesID types.ID, f Fields, data []byte) (Photo, bool, error) {
	if f.Checked {
		return Photo{}, false, Invalid{Field: "checked", Problem: "a photo is checked by a steward on the plant's photos screen, after comparing it with the plant, not by an import. Leave checked out"}
	}

	p, err := b.Add(ctx, speciesID, f, data)
	if dup, ok := errors.AsType[Duplicate](err); ok {
		return dup.Photo, true, nil
	}

	return p, false, err
}

// prepared decodes and scales a photo -- one at a time, across the process,
// which imaging sees to -- turning the reasons a photo cannot be used into
// something a steward can act on.
func (b *Business) prepared(ctx context.Context, data []byte) (imaging.Prepared, error) {
	p, err := imaging.Prepare(ctx, data)

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

	f = tidy(keepMoment(f, p))
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
	for _, name := range []string{Name(p.ID, Large), Name(p.ID, Small), originalName(p), FullName(p.ID, p.Format)} {
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

// BySpecies is every photo there is, checked or not, grouped by the species
// it shows, in no particular order within a species: for a screen that
// shows one beside each plant (see Lead), in one query rather than one per
// plant.
func (b *Business) BySpecies(ctx context.Context) (map[types.ID][]Photo, error) {
	all, err := b.store.All(ctx)
	if err != nil {
		return nil, err
	}

	by := make(map[types.ID][]Photo)
	for _, p := range all {
		by[p.SpeciesID] = append(by[p.SpeciesID], p)
	}

	return by, nil
}

// Open reads one of a photo's pictures. The caller decides who may see it:
// Checked is on the Photo it returns. A Full picture is a JPEG or a PNG, as
// the photo's Format says; the others are always JPEGs.
func (b *Business) Open(ctx context.Context, id types.ID, size Size) (Photo, File, error) {
	if size != Large && size != Small && size != Full {
		return Photo{}, nil, ErrNotFound
	}

	p, err := b.store.ByID(ctx, id)
	if err != nil {
		return Photo{}, nil, err
	}

	if size == Full {
		f, err := OpenFull(ctx, b.files, p.ID, p.Format)
		if err != nil {
			return Photo{}, nil, err
		}

		return p, f, nil
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

// LeadOrder is the order of kinds Lead looks through: the flower says most
// about a plant at a glance, then the leaf, which is what it is told apart by
// out of bloom; the grown plant before the seedling, and winter last, since
// bare stems look much alike in a thumbnail.
var LeadOrder = []Kind{Flower, Leaf, Mature, Young, Winter}

// Lead is the photo a steward's list of plants shows beside one: the first
// kind in LeadOrder that has a photo, and of that kind a checked one if
// there is one, else the best of those still waiting to be checked.
//
// Unchecked photos count, unlike Best, because the list is behind sign-in
// and a steward may see every photo; a plant whose photos all came in
// through the API and are waiting to be looked at should still be easy to
// pick out. The volunteers' pages keep to Best.
func Lead(photos []Photo) (Photo, bool) {
	for _, k := range LeadOrder {
		if p, ok := Best(photos, k); ok {
			return p, true
		}

		var best Photo
		found := false

		for _, p := range photos {
			if p.Kind == k && (!found || Better(p, best) < 0) {
				best, found = p, true
			}
		}

		if found {
			return best, true
		}
	}

	return Photo{}, false
}

// Better orders two photos of the same kind, the one to show first first: our
// own taken here, because "our Winecup, rim of the rain garden" beats any
// nursery photo (the plan); then our own from elsewhere, which is still a
// plant we have seen; then a borrowed one; and among those the more recently
// taken, then the more recently added.
func Better(x, y Photo) int {
	if c := cmp.Compare(rank(x), rank(y)); c != 0 {
		return c
	}

	if c := cmp.Compare(y.TakenYear*12+y.TakenMonth, x.TakenYear*12+x.TakenMonth); c != 0 {
		return c
	}

	return y.CreatedAt.Compare(x.CreatedAt)
}

// rank is how near a photo is to the garden a volunteer is standing in.
func rank(p Photo) int {
	switch {
	case p.Source == Borrowed:
		return 2
	case p.Elsewhere:
		return 1
	}

	return 0
}

// Name is the file a picture is kept in. Public so the store and its tests
// agree on it without repeating the pattern.
func Name(id types.ID, size Size) string { return id.String() + "-" + string(size) + ".jpg" }

func originalName(p Photo) string { return p.ID.String() + "-original." + Ext(p.Format) }

// ServedName is the last part of the address a picture is served at:
// large.jpg, small.jpg, and full.jpg or full.png as the photo's format is.
// The large and small are always JPEGs, made that way; the full one is the
// original's own picture, so it is whatever the original was.
func ServedName(size Size, format string) string {
	if size == Full {
		return "full." + Ext(format)
	}

	return string(size) + ".jpg"
}

// ServedSize is the Size an address asks for, before the photo is read.
// full.jpg and full.png are both Full; the handler then compares the address
// with the photo's own ServedName, so that a PNG is never served as full.jpg
// with a JPEG's content type, which nosniff would leave unshown.
func ServedSize(name string) (Size, bool) {
	switch name {
	case "large.jpg":
		return Large, true
	case "small.jpg":
		return Small, true
	case "full.jpg", "full.png":
		return Full, true
	}

	return "", false
}

// ContentType is a served picture's, by its ServedName.
func ContentType(name string) string {
	if strings.HasSuffix(name, ".png") {
		return "image/png"
	}

	return "image/jpeg"
}

// FullName is the file the Full picture is kept in once it has been made.
func FullName(id types.ID, format string) string { return id.String() + "-full." + Ext(format) }

// Ext is the file extension for a format as imaging names it.
func Ext(format string) string {
	if format == "png" {
		return "png"
	}

	return "jpg"
}

// OpenFull opens a Full picture from files, making it from the original the
// first time. The inbox keeps its photos the same way in a directory of its
// own, which is why this takes the files rather than being a method.
//
// Made when first asked for rather than when the photo arrives, so that every
// photo kept before there was a full size has one too, and kept once made, so
// that a phone pinching into a leaf asks the disk for a range of a file rather
// than the process for a fresh copy of it in memory each time.
//
// An original that cannot be followed to its end is an error rather than
// ErrNotFound: Prepare decoded it when it arrived, so it is something to
// find out about, not a photo without a full size.
func OpenFull(ctx context.Context, files Files, id types.ID, format string) (File, error) {
	name := FullName(id, format)

	f, err := files.Open(name)
	if err == nil {
		return f, nil
	}

	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("opening the full picture of photo %s: %w", id, err)
	}

	// The original read in and the stripped copy beside it are the photo's
	// bytes twice over, so this waits its turn behind any decode.
	release, err := imaging.Turn(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Somebody else may have made it while this waited.
	if f, err := files.Open(name); err == nil {
		return f, nil
	}

	data, err := readAll(files, id.String()+"-original."+Ext(format))
	if err != nil {
		return nil, fmt.Errorf("reading the original of photo %s: %w", id, err)
	}

	full, err := imaging.Stripped(data, format)
	if err != nil {
		return nil, fmt.Errorf("making the full picture of photo %s: %w", id, err)
	}

	if err := files.Put(name, full); err != nil {
		return nil, err
	}

	f, err = files.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening the full picture of photo %s: %w", id, err)
	}

	return f, nil
}

func readAll(files Files, name string) ([]byte, error) {
	f, err := files.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(io.LimitReader(f, MaxBytes+1))
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
	p.Kind, p.PlaceID, p.Elsewhere, p.TakenWhere = f.Kind, f.PlaceID, f.Elsewhere, f.TakenWhere
	p.TakenYear, p.TakenMonth = f.TakenYear, f.TakenMonth
	p.TakenAt, p.InFlower, p.InFruit = f.TakenAt, f.InFlower, f.InFruit
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
	f.TakenWhere = strings.Join(strings.Fields(f.TakenWhere), " ")

	// Where a photo was taken off the property is a name, which makes it
	// off the property. Neither means anything for a borrowed photo, whose
	// page says where; and a place here is the answer to where for one
	// taken here, so a place chosen wins over a name left from before.
	if f.TakenWhere != "" {
		f.Elsewhere = true
	}

	if f.Source == Borrowed || !f.PlaceID.Zero() {
		f.Elsewhere, f.TakenWhere = false, ""
	}

	if f.Source == Ours {
		f.SourceURL, f.License = "", ""
	}

	// A day said is the month and year said too, in the garden's time,
	// which is the calendar a flowering date is read against. Kept to the
	// millisecond, as the store keeps it, so that a photo read back compares
	// equal to the one written.
	if !f.TakenAt.IsZero() {
		f.TakenAt = f.TakenAt.UTC().Truncate(time.Millisecond)
		local := f.TakenAt.In(types.Garden)
		f.TakenYear, f.TakenMonth = local.Year(), int(local.Month())
	}

	switch f.Kind {
	case Flower:
		f.InFlower = true
	case Fruit:
		f.InFruit = true
	}

	return f
}

func (b *Business) check(f Fields) error {
	switch {
	case !slices.Contains(Kinds, f.Kind):
		return Invalid{Field: "kind", Problem: "choose which kind of photo this is: young plant, leaf, flower, fruit or seed, full size, or winter"}
	case f.Source != Ours && f.Source != Borrowed:
		return Invalid{Field: "source", Problem: "say whether this is our own photo or a borrowed one"}
	case f.TakenMonth < 0 || f.TakenMonth > 12:
		return Invalid{Field: "taken_month", Problem: "choose the month from the list"}
	case f.TakenYear != 0 && (f.TakenYear < 1990 || f.TakenYear > b.now().Year()+1):
		return Invalid{Field: "taken_year", Problem: "the year needs to be four figures, such as 2027, or empty"}
	case f.TakenAt.After(b.now().Add(36 * time.Hour)):
		return Invalid{Field: "taken_on", Problem: "that day has not come yet. Check the date"}
	case utf8.RuneCountInString(f.Credit) > 200:
		return Invalid{Field: "credit", Problem: "the credit is longer than 200 characters. Shorten it"}
	case utf8.RuneCountInString(f.License) > 100:
		return Invalid{Field: "license", Problem: "the licence is longer than 100 characters. Write its short name, such as CC BY-SA 4.0"}
	case len(f.SourceURL) > 500:
		return Invalid{Field: "source_url", Problem: "the address is longer than 500 characters. Use the photo's own page rather than a search"}
	case utf8.RuneCountInString(f.TakenWhere) > 100:
		return Invalid{Field: "taken_where", Problem: "the name of where it was taken is longer than 100 characters. Shorten it"}
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
