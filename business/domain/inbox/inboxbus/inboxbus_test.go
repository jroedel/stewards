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
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type garden struct {
	inbox  *inboxbus.Business
	places *placebus.Business
	dir    string
	clock  *time.Time

	steward userbus.User
	inflow  placebus.Place
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
		func() error { return userdb.Init(t.Context(), db) },
		func() error { return inboxdb.Init(t.Context(), db) },
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

	clock := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	g := &garden{
		inbox:  inboxbus.NewBusiness(inboxdb.NewStore(db), files, func() time.Time { return clock }),
		places: placebus.NewBusiness(placedb.NewStore(db), nil),
		dir:    dir,
		clock:  &clock,
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

func TestOnlyTheTwoSizesCanBeOpened(t *testing.T) {
	g := setup(t)

	it, err := g.inbox.Add(t.Context(), g.property(), photo(t, 8))
	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []photobus.Size{photobus.Large, photobus.Small} {
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
