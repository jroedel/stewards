package inboxbus_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/nursery/stores/nurserydb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type garden struct {
	inbox    *inboxbus.Business
	places   *placebus.Business
	photos   *photobus.Business
	listings *listingbus.Business
	nursery  *nurserybus.Business
	dir      string
	clock    *time.Time

	steward   userbus.User
	inflow    placebus.Place
	penstemon speciesbus.Species
}

func setup(t *testing.T) *garden {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return speciesdb.Init(t.Context(), db) },
		func() error { return listingdb.Init(t.Context(), db) },
		func() error { return photodb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) },
		func() error { return nurserydb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) }, // at every startup
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, inboxdb.Expected); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "inbox")

	files, err := photofs.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	photoFiles, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }

	g := &garden{
		places:   placebus.NewBusiness(placedb.NewStore(db), nil),
		photos:   photobus.NewBusiness(photodb.NewStore(db), photoFiles, now),
		listings: listingbus.NewBusiness(listingdb.NewStore(db), now),
		dir:      dir,
		clock:    &clock,
	}
	g.nursery = nurserybus.NewBusiness(nurserydb.NewStore(db), now)
	g.inbox = inboxbus.NewBusiness(inboxdb.NewStore(db), files, inboxbus.Deps{Photos: g.photos, Listings: g.listings, Stock: g.nursery}, now)

	species := speciesbus.NewBusiness(speciesdb.NewStore(db), nil)
	if g.penstemon, err = species.Create(t.Context(), speciesbus.Fields{Slug: "brazos-penstemon", Common: types.Text{EN: "Brazos penstemon"}}); err != nil {
		t.Fatal(err)
	}

	email, err := types.ParseEmail("steward@example.org")
	if err != nil {
		t.Fatal(err)
	}

	users := userbus.NewBusiness(slog.New(slog.DiscardHandler), userdb.NewStore(db), nil)
	if g.steward, err = users.Create(t.Context(), email, ""); err != nil {
		t.Fatal(err)
	}

	if g.inflow, err = g.places.Create(t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}}); err != nil {
		t.Fatal(err)
	}

	return g
}

// photo is an invented picture, never a real one (CLAUDE.md). shade makes
// each one different, so that two are not the same photo sent twice.
func photo(t *testing.T, shade uint8) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := range 480 {
		for x := range 640 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), shade, 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// dated puts an EXIF date with no zone into a JPEG, as a camera that never
// heard of OffsetTimeOriginal writes it: a little-endian TIFF whose IFD0
// points at an Exif directory holding DateTimeOriginal.
func dated(jpg []byte, taken string) []byte {
	le := binary.LittleEndian
	var t []byte
	entry := func(tag, kind uint16, count, value uint32) {
		t = le.AppendUint16(t, tag)
		t = le.AppendUint16(t, kind)
		t = le.AppendUint32(t, count)
		t = le.AppendUint32(t, value)
	}

	const exifAt = 8 + 2 + 12 + 4
	const dateAt = exifAt + 2 + 12 + 4

	t = append(t, "II*\x00"...)
	t = le.AppendUint32(t, 8)
	t = le.AppendUint16(t, 1)
	entry(0x8769, 4, 1, exifAt)
	t = le.AppendUint32(t, 0)
	t = le.AppendUint16(t, 1)
	entry(0x9003, 2, 20, dateAt)
	t = le.AppendUint32(t, 0)
	t = append(t, taken+"\x00"...)

	seg := append([]byte("Exif\x00\x00"), t...)
	app1 := binary.BigEndian.AppendUint16([]byte{0xFF, 0xE1}, uint16(len(seg)+2))

	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, seg...)

	return append(out, jpg[2:]...)
}

func (g *garden) property() inboxbus.Fields {
	return inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Property}
}

func (g *garden) files(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(g.dir)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}

func TestAPhotoArrivesWithWhatTheCameraKnew(t *testing.T) {
	g := setup(t)

	f := g.property()
	f.PlaceID, f.Note = g.inflow.ID, "  the one by the outlet  "

	it, err := g.inbox.Add(t.Context(), f, dated(photo(t, 1), "2026:10:03 16:45:09"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := g.inbox.ByID(t.Context(), it.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 4:45 pm in Austin in October is daylight time, five hours behind.
	if want := time.Date(2026, 10, 3, 21, 45, 9, 0, time.UTC); !got.TakenAt.Equal(want) {
		t.Errorf("taken at %v, want %v: a camera with no zone is on the garden's clock", got.TakenAt, want)
	}

	switch {
	case got.At != inboxbus.Property, got.PlaceID != g.inflow.ID, got.FromID != g.steward.ID:
		t.Errorf("kept as %+v", got)
	case got.Note != "the one by the outlet":
		t.Errorf("note %q", got.Note)
	case got.Status != inboxbus.New, got.Located:
		t.Errorf("status %q, located %v", got.Status, got.Located)
	case got.Large.Width != 640 || got.Small.Width != 640:
		t.Errorf("a photo smaller than both sizes is kept at its own: %+v %+v", got.Large, got.Small)
	}

	want := []string{it.ID.String() + "-large.jpg", it.ID.String() + "-original.jpg", it.ID.String() + "-small.jpg"}
	if names := g.files(t); !slices.Equal(names, want) {
		t.Errorf("files %v, want %v", names, want)
	}

	if n, err := g.inbox.Count(t.Context()); err != nil || n != 1 {
		t.Errorf("count %d, %v", n, err)
	}
}

// The signal drops after six of ten, and the steward sends all ten again: the
// six are recognised, not kept twice.
func TestTheSamePhotoSentAgainIsTheOneAlreadyThere(t *testing.T) {
	g := setup(t)
	data := photo(t, 2)

	first, err := g.inbox.Add(t.Context(), g.property(), data)
	if err != nil {
		t.Fatal(err)
	}

	_, err = g.inbox.Add(t.Context(), g.property(), data)

	dup, ok := errors.AsType[inboxbus.Duplicate](err)
	if !ok || dup.Item.ID != first.ID {
		t.Fatalf("sent again: %v, want the first", err)
	}

	if n := len(g.files(t)); n != 3 {
		t.Errorf("%d files, want the first photo's three", n)
	}
}

// At a nursery there is no place here it could be of. A place chosen before
// the toggle was flipped is dropped, not refused.
func TestANurseryPhotoHasNoPlace(t *testing.T) {
	g := setup(t)

	f := inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Nursery, PlaceID: g.inflow.ID}

	it, err := g.inbox.Add(t.Context(), f, photo(t, 3))
	if err != nil {
		t.Fatal(err)
	}

	if got, _ := g.inbox.ByID(t.Context(), it.ID); got.At != inboxbus.Nursery || !got.PlaceID.Zero() {
		t.Errorf("kept as %+v", got)
	}
}

func TestWhatCannotBeKeptLeavesNothingBehind(t *testing.T) {
	g := setup(t)

	unknown := g.property()
	unknown.PlaceID = types.NewID()

	for name, tc := range map[string]struct {
		f     inboxbus.Fields
		data  []byte
		field string
	}{
		"nowhere":         {inboxbus.Fields{FromID: g.steward.ID}, photo(t, 4), "at"},
		"nobody":          {inboxbus.Fields{At: inboxbus.Property}, photo(t, 4), "from"},
		"a long note":     {inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Property, Note: strings.Repeat("x", inboxbus.MaxNote+1)}, photo(t, 4), "note"},
		"not a photo":     {g.property(), []byte("a shopping list"), "photo"},
		"a place no more": {unknown, photo(t, 4), "place"},
	} {
		_, err := g.inbox.Add(t.Context(), tc.f, tc.data)

		if invalid, ok := errors.AsType[inboxbus.Invalid](err); !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	if names := g.files(t); len(names) != 0 {
		t.Errorf("files left behind: %v", names)
	}

	if n, _ := g.inbox.Count(t.Context()); n != 0 {
		t.Errorf("%d rows kept", n)
	}
}

// The newest photo first, by when it was taken; one whose camera said nothing
// goes by when it arrived.
func TestTheInboxIsNewestFirst(t *testing.T) {
	g := setup(t)

	add := func(data []byte) types.ID {
		t.Helper()

		it, err := g.inbox.Add(t.Context(), g.property(), data)
		if err != nil {
			t.Fatal(err)
		}

		return it.ID
	}

	morning := add(dated(photo(t, 5), "2026:10:04 09:00:00"))
	evening := add(dated(photo(t, 6), "2026:10:04 18:00:00"))

	// Arrives last, on the clock at 9 am UTC on the 5th: newest of all.
	*g.clock = g.clock.Add(time.Hour)
	undated := add(photo(t, 7))

	all, err := g.inbox.Waiting(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var order []types.ID
	for _, it := range all {
		order = append(order, it.ID)
	}

	if want := []types.ID{undated, evening, morning}; !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
}

func TestOnlyTheThreeSizesCanBeOpened(t *testing.T) {
	g := setup(t)

	it, err := g.inbox.Add(t.Context(), g.property(), photo(t, 8))
	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []photobus.Size{photobus.Large, photobus.Small, photobus.Full} {
		_, f, err := g.inbox.Open(t.Context(), it.ID, size)
		if err != nil {
			t.Errorf("%s: %v", size, err)

			continue
		}
		f.Close()
	}

	if _, _, err := g.inbox.Open(t.Context(), it.ID, "original"); !errors.Is(err, inboxbus.ErrNotFound) {
		t.Errorf("the original: %v, want not found", err)
	}
}

// A place with a photo waiting in the inbox is not deleted from under it, and
// the steward is told where the photo is.
func TestAPlaceWithAnInboxPhotoIsKept(t *testing.T) {
	g := setup(t)

	f := g.property()
	f.PlaceID = g.inflow.ID

	if _, err := g.inbox.Add(t.Context(), f, photo(t, 9)); err != nil {
		t.Fatal(err)
	}

	err := g.places.Delete(t.Context(), g.inflow.ID)
	if invalid, ok := errors.AsType[placebus.Invalid](err); !ok || !strings.Contains(invalid.Problem, "inbox") {
		t.Errorf("deleting the place: %v", err)
	}
}

// ------------------------------------------------------------------ sorting

func (g *garden) sent(t *testing.T, f inboxbus.Fields, shade uint8) inboxbus.Item {
	t.Helper()

	it, err := g.inbox.Add(t.Context(), f, dated(photo(t, shade), "2026:04:18 09:30:00"))
	if err != nil {
		t.Fatal(err)
	}

	return it
}

func (g *garden) atInflow() inboxbus.Fields {
	f := g.property()
	f.PlaceID = g.inflow.ID

	return f
}

// A flower not often seen: the photo becomes the plant's, unchecked, with the
// place it was sent with and the camera's month, and leaves the inbox.
func TestAPhotoBecomesThePlantsUnchecked(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.atInflow(), 20)

	res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: photobus.Flower})
	if err != nil {
		t.Fatal(err)
	}

	p, err := g.photos.ByID(t.Context(), res.Photo.ID)
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case p.SpeciesID != g.penstemon.ID, p.Kind != photobus.Flower, p.PlaceID != g.inflow.ID, p.Source != photobus.Ours:
		t.Errorf("the plant's photo is %+v", p)
	case p.Checked:
		t.Error("a sorted photo arrived checked: a steward checks it on the plant's photos screen")
	case p.TakenYear != 2026 || p.TakenMonth != 4:
		t.Errorf("taken %d/%d, want the camera's April 2026", p.TakenMonth, p.TakenYear)
	}

	got, _ := g.inbox.ByID(t.Context(), it.ID)
	if got.Status != inboxbus.Sorted || got.Outcome != inboxbus.AsPhoto || got.PhotoID != p.ID || got.SortedBy != g.steward.ID || got.SortedAt.IsZero() {
		t.Errorf("the inbox remembers %+v", got)
	}

	if names := g.files(t); len(names) != 0 {
		t.Errorf("the inbox still keeps %v", names)
	}

	if _, _, err := g.inbox.Open(t.Context(), it.ID, photobus.Small); !errors.Is(err, inboxbus.ErrNotFound) {
		t.Errorf("a sorted photo's picture: %v", err)
	}

	if n, _ := g.inbox.Count(t.Context()); n != 0 {
		t.Errorf("%d still to sort", n)
	}
}

// Just planted: it is protected there, off the To plant list, and the photo
// is its young plant at that place.
func TestAPlantingIsListedAndPhotographed(t *testing.T) {
	g := setup(t)

	// It was on the place's To plant list.
	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.penstemon.ID, listingbus.Fields{Action: listingbus.Protect, Planned: true}); err != nil {
		t.Fatal(err)
	}

	it := g.sent(t, g.property(), 21)

	res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID, PlaceID: g.inflow.ID})
	if err != nil {
		t.Fatal(err)
	}

	if res.Listing.Action != listingbus.Protect || res.Listing.Planned {
		t.Errorf("listed as %+v, want protected and planted", res.Listing)
	}

	if res.Photo.Kind != photobus.Young || res.Photo.PlaceID != g.inflow.ID {
		t.Errorf("the photo is %+v, want a young plant at the inflow", res.Photo)
	}
}

// A plant listed careful stays careful, with its note, when it is planted.
func TestAPlantingKeepsHowItWasListed(t *testing.T) {
	g := setup(t)

	note := types.Text{EN: "gloves: it seeds everywhere"}
	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.penstemon.ID, listingbus.Fields{Action: listingbus.Careful, Note: note}); err != nil {
		t.Fatal(err)
	}

	it := g.sent(t, g.atInflow(), 22)

	res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID})
	if err != nil {
		t.Fatal(err)
	}

	if res.Listing.Action != listingbus.Careful || res.Listing.Note != note {
		t.Errorf("listed as %+v", res.Listing)
	}
}

// What cannot be sorted leaves the photo where it was, files and all, for
// another try.
func TestASortThatCannotBeMadeLeavesThePhotoInTheInbox(t *testing.T) {
	g := setup(t)

	if _, err := g.listings.Set(t.Context(), g.inflow.ID, g.penstemon.ID, listingbus.Fields{Action: listingbus.Pull}); err != nil {
		t.Fatal(err)
	}

	it := g.sent(t, g.atInflow(), 23)
	nursery := g.sent(t, inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Nursery}, 24)

	for name, tc := range map[string]struct {
		id    types.ID
		s     inboxbus.Sorting
		field string
	}{
		"no plant":           {it.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, Kind: photobus.Leaf}, "species"},
		"no kind":            {it.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID}, "kind"},
		"no outcome":         {it.ID, inboxbus.Sorting{SpeciesID: g.penstemon.ID}, "outcome"},
		"a plant not here":   {it.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: types.NewID(), Kind: photobus.Leaf}, "place"},
		"listed to pull":     {it.ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID}, "species"},
		"planted at nursery": {nursery.ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID, PlaceID: g.inflow.ID}, "outcome"},
		"planted nowhere":    {g.sent(t, g.property(), 25).ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID}, "place"},
		"a long question":    {it.ID, inboxbus.Sorting{Outcome: inboxbus.AsUnsure, Note: strings.Repeat("?", inboxbus.MaxNote+1)}, "note"},
	} {
		_, err := g.inbox.Sort(t.Context(), tc.id, g.steward.ID, tc.s)

		if invalid, ok := errors.AsType[inboxbus.Invalid](err); !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	if got, _ := g.inbox.ByID(t.Context(), it.ID); got.Status != inboxbus.New || got.Outcome != "" || !got.SpeciesID.Zero() {
		t.Errorf("after the refusals the photo is %+v", got)
	}

	if n := len(g.files(t)); n != 9 {
		t.Errorf("%d inbox files, want all three photos' three", n)
	}
}

// Not sure yet: set aside with the question, out of the count, and still
// sortable once somebody knows.
func TestAPhotoSetAsideCanBeSortedLater(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.atInflow(), 26)

	if _, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsUnsure, Note: "  frostweed or wingstem?  "}); err != nil {
		t.Fatal(err)
	}

	aside, _ := g.inbox.SetAside(t.Context())
	waiting, _ := g.inbox.Waiting(t.Context())

	if len(aside) != 1 || aside[0].Note != "frostweed or wingstem?" || len(waiting) != 0 {
		t.Errorf("aside %+v, waiting %d", aside, len(waiting))
	}

	if n, _ := g.inbox.Count(t.Context()); n != 0 {
		t.Errorf("a photo set aside is counted as to sort: %d", n)
	}

	if _, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: photobus.Leaf}); err != nil {
		t.Errorf("sorting it once known: %v", err)
	}
}

// Discarded: the files go, and the same photo sent again is recognised
// rather than coming back.
func TestADiscardedPhotoStaysGone(t *testing.T) {
	g := setup(t)
	data := photo(t, 27)

	it, err := g.inbox.Add(t.Context(), g.property(), data)
	if err != nil {
		t.Fatal(err)
	}

	// Looked at full size first, so that there is a full picture to go too.
	_, f, err := g.inbox.Open(t.Context(), it.ID, photobus.Full)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	if n := len(g.files(t)); n != 4 {
		t.Fatalf("%d files before", n)
	}

	if _, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsDiscard}); err != nil {
		t.Fatal(err)
	}

	if names := g.files(t); len(names) != 0 {
		t.Errorf("files left: %v", names)
	}

	if _, err := g.inbox.Add(t.Context(), g.property(), data); err == nil {
		t.Error("a discarded photo came back when it was sent again")
	}
}

// The same sort sent twice is the first answer again; a different one is
// refused with what the photo became.
func TestASortedPhotoIsSortedOnce(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.atInflow(), 28)
	as := inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: photobus.Leaf}

	if _, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, as); err != nil {
		t.Fatal(err)
	}

	if res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, as); err != nil || !res.Unchanged {
		t.Errorf("the same sort again: %+v, %v", res, err)
	}

	_, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsDiscard})
	if already, ok := errors.AsType[inboxbus.AlreadySorted](err); !ok || already.Item.Outcome != inboxbus.AsPhoto {
		t.Errorf("discarding a sorted photo: %v", err)
	}
}

// Sorted as a leaf, then as a flower: the second is somebody's different
// answer, and is told what the first one was rather than that nothing
// changed. Only the same sort again is "unchanged". One after the other,
// as two sorts at once often run on a busy machine.
func TestASortAsAnotherKindIsToldTheFirst(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.atInflow(), 30)
	leaf := inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: photobus.Leaf}

	if _, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, leaf); err != nil {
		t.Fatal(err)
	}

	flower := leaf
	flower.Kind = photobus.Flower

	_, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, flower)
	if already, ok := errors.AsType[inboxbus.AlreadySorted](err); !ok || already.Item.Kind != photobus.Leaf {
		t.Errorf("sorted again as a flower: %v", err)
	}

	if res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, leaf); err != nil || !res.Unchanged || res.Item.Kind != photobus.Leaf {
		t.Errorf("sorted again as a leaf: %+v, %v", res, err)
	}

	if photos, _ := g.photos.ForSpecies(t.Context(), g.penstemon.ID); len(photos) != 1 || photos[0].Kind != photobus.Leaf {
		t.Errorf("photos made: %+v", photos)
	}
}

// Two stewards sorting the same photo at once: one makes something, the
// other is told it is done.
func TestTwoSortsAtOnceMakeOneThing(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.atInflow(), 29)

	var (
		wg   sync.WaitGroup
		errs [2]error
	)

	for i, k := range []photobus.Kind{photobus.Leaf, photobus.Flower} {
		wg.Go(func() {
			_, errs[i] = g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: k})
		})
	}

	wg.Wait()

	won := 0
	for _, err := range errs {
		_, lost := errors.AsType[inboxbus.AlreadySorted](err)

		switch {
		case err == nil:
			won++
		case !lost:
			t.Errorf("unexpected: %v", err)
		}
	}

	photos, _ := g.photos.ForSpecies(t.Context(), g.penstemon.ID)
	if won != 1 || len(photos) != 1 {
		t.Errorf("%d sorts succeeded and %d photos were made, want one of each", won, len(photos))
	}
}

// ------------------------------------------------------------------ nursery stock

func (g *garden) atNursery(t *testing.T, shade uint8) inboxbus.Item {
	t.Helper()

	return g.sent(t, inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Nursery}, shade)
}

// A tag photo becomes a line of that morning's stock, and keeps its picture
// as the line's.
func TestANurseryPhotoBecomesALineOfStock(t *testing.T) {
	g := setup(t)
	it := g.atNursery(t, 40)

	res, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{
		Outcome: inboxbus.AsStock, Nursery: "Natural Gardener",
		Stock: nurserybus.Fields{SpeciesID: g.penstemon.ID, NameOnTag: "Penstemon tenuis", PotSize: "1 gal", PriceCents: 1299},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.Line.NameOnTag != "Penstemon tenuis" || res.Line.InboxID != it.ID || res.Line.SpeciesID != g.penstemon.ID {
		t.Errorf("the line is %+v", res.Line)
	}

	stock, _ := g.nursery.All(t.Context())

	// The photo's camera date is 18 April 2026, and that is the visit's day.
	if len(stock) != 1 || !stock[0].Visit.Day.Equal(time.Date(2026, 4, 18, 0, 0, 0, 0, types.Garden)) {
		t.Errorf("stock %+v", stock)
	}

	got, _ := g.inbox.ByID(t.Context(), it.ID)
	if got.Status != inboxbus.Sorted || got.Outcome != inboxbus.AsStock || got.LineID != res.Line.ID || !got.HasPictures() {
		t.Errorf("the inbox remembers %+v", got)
	}

	if _, f, err := g.inbox.Open(t.Context(), it.ID, photobus.Small); err != nil {
		t.Errorf("the line's picture: %v", err)
	} else {
		f.Close()
	}

	if n, _ := g.inbox.Count(t.Context()); n != 0 {
		t.Errorf("%d still to sort", n)
	}
}

func TestOnlyANurseryPhotoIsStock(t *testing.T) {
	g := setup(t)
	it := g.sent(t, g.property(), 41)

	_, err := g.inbox.Sort(t.Context(), it.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsStock, Nursery: "Natural Gardener", Stock: nurserybus.Fields{NameOnTag: "Turk's cap"}})
	if invalid, ok := errors.AsType[inboxbus.Invalid](err); !ok || invalid.Field != "outcome" {
		t.Errorf("a garden photo as stock: %v", err)
	}

	// And a line the nursery rules refuse leaves the photo in the inbox.
	nursery := g.atNursery(t, 42)

	_, err = g.inbox.Sort(t.Context(), nursery.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsStock, Stock: nurserybus.Fields{NameOnTag: "Turk's cap"}})
	if invalid, ok := errors.AsType[inboxbus.Invalid](err); !ok || invalid.Field != "nursery" {
		t.Errorf("stock with no nursery: %v", err)
	}

	if got, _ := g.inbox.ByID(t.Context(), nursery.ID); got.Status != inboxbus.New {
		t.Errorf("after the refusal the photo is %+v", got)
	}
}

// Three months on, a tag photo's pictures go; the line stays.
func TestOldStockPhotosArePruned(t *testing.T) {
	g := setup(t)

	// Taken 18 April; the clock is 5 October, so it is old already. One sent
	// today with no camera date is not.
	old := g.atNursery(t, 43)

	recent, err := g.inbox.Add(t.Context(), inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Nursery}, photo(t, 44))
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []types.ID{old.ID, recent.ID} {
		if _, err := g.inbox.Sort(t.Context(), id, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsStock, Nursery: "Natural Gardener", Stock: nurserybus.Fields{NameOnTag: "Turk's cap"}}); err != nil {
			t.Fatal(err)
		}
	}

	for range 2 { // and again, which finds nothing more to do
		if err := g.inbox.PruneStock(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if got, _ := g.inbox.ByID(t.Context(), old.ID); got.PrunedAt.IsZero() || got.HasPictures() {
		t.Errorf("the old photo: %+v", got)
	}

	if _, _, err := g.inbox.Open(t.Context(), old.ID, photobus.Small); !errors.Is(err, inboxbus.ErrNotFound) {
		t.Errorf("the old photo's picture: %v", err)
	}

	if got, _ := g.inbox.ByID(t.Context(), recent.ID); !got.HasPictures() {
		t.Error("the recent photo was pruned")
	}

	if n := len(g.files(t)); n != 3 {
		t.Errorf("%d files, want the recent photo's three", n)
	}

	stock, _ := g.nursery.All(t.Context())
	lines := 0
	for _, st := range stock {
		lines += len(st.Lines)
	}

	if lines != 2 {
		t.Errorf("%d lines, want both kept", lines)
	}
}

// ------------------------------------------------------------------ from elsewhere

// A batch from a park keeps the park's name and never a place here; one from
// the garden keeps no site. Either way round is a form changed halfway, and
// the choice of where is the word that counts.
func TestWhereABatchWasTakenIsKeptForWhereItWas(t *testing.T) {
	g := setup(t)

	for name, tc := range map[string]struct {
		in         inboxbus.Fields
		site       string
		placeKnown bool
	}{
		"a park":     {inboxbus.Fields{At: inboxbus.Elsewhere, Site: "  Pedernales   Falls State Park ", PlaceID: g.inflow.ID}, "Pedernales Falls State Park", false},
		"a nursery":  {inboxbus.Fields{At: inboxbus.Nursery, Site: "Natural Gardener", PlaceID: g.inflow.ID}, "Natural Gardener", false},
		"the garden": {inboxbus.Fields{At: inboxbus.Property, Site: "Pedernales Falls", PlaceID: g.inflow.ID}, "", true},
	} {
		tc.in.FromID = g.steward.ID

		got, err := g.inbox.Check(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if got.Site != tc.site || got.PlaceID.Zero() == tc.placeKnown {
			t.Errorf("%s: kept as %+v", name, got)
		}
	}

	it := g.sent(t, inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Elsewhere, Site: "Pedernales Falls State Park"}, 40)
	back, err := g.inbox.ByID(t.Context(), it.ID)
	if err != nil || back.At != inboxbus.Elsewhere || back.Site != "Pedernales Falls State Park" {
		t.Errorf("read back %+v, %v", back, err)
	}

	if _, err := g.inbox.Check(inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Elsewhere, Site: strings.Repeat("x", 101)}); !isInvalidField(err, "site") {
		t.Errorf("a long site: %v", err)
	}

	if _, err := g.inbox.Check(inboxbus.Fields{FromID: g.steward.ID, At: "the moon"}); !isInvalidField(err, "at") {
		t.Errorf("an unknown where: %v", err)
	}
}

// A photo from a park becomes its plant's photo, with no place here even if
// one is asked for; it is not a planting, and not stock.
func TestAPhotoFromElsewhereIsOnlyAPhoto(t *testing.T) {
	g := setup(t)
	park := inboxbus.Fields{FromID: g.steward.ID, At: inboxbus.Elsewhere, Site: "Pedernales Falls State Park"}

	planted := g.sent(t, park, 41)
	_, err := g.inbox.Sort(t.Context(), planted.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPlanted, SpeciesID: g.penstemon.ID, PlaceID: g.inflow.ID})
	if !isInvalidField(err, "outcome") {
		t.Errorf("a park photo as planted here: %v", err)
	}

	_, err = g.inbox.Sort(t.Context(), planted.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsStock, Nursery: "Natural Gardener", Stock: nurserybus.Fields{NameOnTag: "x"}})
	if !isInvalidField(err, "outcome") {
		t.Errorf("a park photo as stock: %v", err)
	}

	res, err := g.inbox.Sort(t.Context(), planted.ID, g.steward.ID, inboxbus.Sorting{Outcome: inboxbus.AsPhoto, SpeciesID: g.penstemon.ID, Kind: photobus.Flower, PlaceID: g.inflow.ID})
	if err != nil {
		t.Fatal(err)
	}

	if !res.Photo.PlaceID.Zero() || res.Photo.Source != photobus.Ours {
		t.Errorf("the plant's photo: %+v", res.Photo)
	}
}

func isInvalidField(err error, field string) bool {
	invalid, ok := errors.AsType[inboxbus.Invalid](err)

	return ok && invalid.Field == field
}
