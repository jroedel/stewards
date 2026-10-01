package photobus_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type garden struct {
	photos  *photobus.Business
	places  *placebus.Business
	species *speciesbus.Business
	dir     string
	clock   *time.Time

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
		func() error { return photodb.Init(t.Context(), db) },
		func() error { return photodb.Init(t.Context(), db) }, // at every startup
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, photodb.Expected); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "photo-files")

	files, err := photofs.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	g := &garden{
		photos:  photobus.NewBusiness(photodb.NewStore(db), files, func() time.Time { return clock }),
		places:  placebus.NewBusiness(placedb.NewStore(db), nil),
		species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		dir:     dir,
		clock:   &clock,
	}

	if g.inflow, err = g.places.Create(t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}}); err != nil {
		t.Fatal(err)
	}

	if g.penstemon, err = g.species.Create(t.Context(), speciesbus.Fields{Slug: "brazos-penstemon", Common: types.Text{EN: "Brazos penstemon"}}); err != nil {
		t.Fatal(err)
	}

	return g
}

// photo is an invented picture, never a real one (CLAUDE.md).
func photo(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func ours(k photobus.Kind) photobus.Fields {
	return photobus.Fields{Kind: k, Source: photobus.Ours, Checked: true}
}

func borrowed(k photobus.Kind) photobus.Fields {
	return photobus.Fields{
		Kind: k, Source: photobus.Borrowed, Checked: true,
		Credit: "A. Botanist", License: "CC BY-SA 4.0",
		SourceURL: "https://commons.wikimedia.org/wiki/File:Invented_example.jpg",
	}
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

func TestAPhotoIsKeptInTwoSizesWithItsOriginal(t *testing.T) {
	g := setup(t)

	f := ours(photobus.Leaf)
	f.PlaceID, f.TakenYear, f.TakenMonth = g.inflow.ID, 2027, 4

	original := photo(t, 2400, 1800)

	p, err := g.photos.Add(t.Context(), g.penstemon.ID, f, original)
	if err != nil {
		t.Fatal(err)
	}

	if p.Large != (photobus.Dimensions{Width: 1600, Height: 1200}) || p.Small != (photobus.Dimensions{Width: 800, Height: 600}) {
		t.Errorf("sizes %+v and %+v", p.Large, p.Small)
	}

	if got := g.files(t); len(got) != 3 {
		t.Errorf("files kept: %v, want the large, the small and the original", got)
	}

	kept, err := os.ReadFile(filepath.Join(g.dir, p.ID.String()+"-original.jpg"))
	if err != nil || !bytes.Equal(kept, original) {
		t.Error("the original is not kept as it was sent")
	}

	back, err := g.photos.ByID(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}

	if back.PlaceID != g.inflow.ID || back.TakenYear != 2027 || back.TakenMonth != 4 || back.Kind != photobus.Leaf || !back.Checked || back.Format != "jpeg" {
		t.Errorf("read back %+v", back)
	}

	_, file, err := g.photos.Open(t.Context(), p.ID, photobus.Small)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	if b, _ := io.ReadAll(file); len(b) == 0 || !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) {
		t.Error("the small picture is not a JPEG")
	}

	if _, _, err := g.photos.Open(t.Context(), p.ID, "original"); !errors.Is(err, photobus.ErrNotFound) {
		t.Errorf("the original can be opened: %v", err)
	}
}

func TestABorrowedPhotoNeedsItsCredit(t *testing.T) {
	g := setup(t)

	for name, tc := range map[string]struct {
		change func(*photobus.Fields)
		field  string
	}{
		"no address":        {func(f *photobus.Fields) { f.SourceURL = "" }, "source_url"},
		"not an address":    {func(f *photobus.Fields) { f.SourceURL = "commons.wikimedia.org/x" }, "source_url"},
		"a script":          {func(f *photobus.Fields) { f.SourceURL = "javascript:alert(1)" }, "source_url"},
		"no author":         {func(f *photobus.Fields) { f.Credit = "  " }, "credit"},
		"no licence":        {func(f *photobus.Fields) { f.License = "" }, "license"},
		"no kind":           {func(f *photobus.Fields) { f.Kind = "" }, "kind"},
		"no source":         {func(f *photobus.Fields) { f.Source = "" }, "source"},
		"month 13":          {func(f *photobus.Fields) { f.TakenMonth = 13 }, "taken_month"},
		"a two-figure year": {func(f *photobus.Fields) { f.TakenYear = 27 }, "taken_year"},
		"next decade":       {func(f *photobus.Fields) { f.TakenYear = 2036 }, "taken_year"},
	} {
		f := borrowed(photobus.Flower)
		tc.change(&f)

		// Not a photo at all: the fields are refused before it is looked at.
		_, err := g.photos.Add(t.Context(), g.penstemon.ID, f, []byte("not looked at"))

		if invalid, ok := errors.AsType[photobus.Invalid](err); !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	if got := g.files(t); len(got) != 0 {
		t.Errorf("a refused photo left files: %v", got)
	}
}

func TestWhatIsNotAPhotoLeavesNothingBehind(t *testing.T) {
	g := setup(t)

	_, err := g.photos.Add(t.Context(), g.penstemon.ID, ours(photobus.Leaf), []byte("a text file renamed .jpg"))
	if invalid, ok := errors.AsType[photobus.Invalid](err); !ok || invalid.Field != "photo" || !strings.Contains(invalid.Problem, "JPEG or PNG") {
		t.Errorf("not a photo: %v", err)
	}

	// A place that is not there: the photo was good, the row was refused,
	// and the files written for it are taken away again.
	f := ours(photobus.Leaf)
	f.PlaceID = types.NewID()

	_, err = g.photos.Add(t.Context(), g.penstemon.ID, f, photo(t, 600, 400))
	if invalid, ok := errors.AsType[photobus.Invalid](err); !ok || invalid.Field != "place" {
		t.Errorf("an unknown place: %v", err)
	}

	if got := g.files(t); len(got) != 0 {
		t.Errorf("files left behind: %v", got)
	}
}

func TestChangingAPhotoKeepsItsPictures(t *testing.T) {
	g := setup(t)

	p, err := g.photos.Add(t.Context(), g.penstemon.ID, borrowed(photobus.Flower), photo(t, 600, 400))
	if err != nil {
		t.Fatal(err)
	}

	// Switched to ours: the address and licence mean nothing now and go.
	f := ours(photobus.Mature)
	f.SourceURL, f.License = "https://example.org/left-over", "CC0"
	f.Checked = false

	*g.clock = g.clock.Add(time.Hour)

	changed, err := g.photos.Update(t.Context(), p.ID, f)
	if err != nil {
		t.Fatal(err)
	}

	back, _ := g.photos.ByID(t.Context(), p.ID)
	if back.Kind != photobus.Mature || back.Source != photobus.Ours || back.SourceURL != "" || back.License != "" || back.Checked {
		t.Errorf("after the change: %+v", back)
	}

	if back.Large != p.Large || !back.CreatedAt.Equal(p.CreatedAt) || !back.UpdatedAt.Equal(changed.UpdatedAt) || back.UpdatedAt.Equal(p.UpdatedAt) {
		t.Error("the change touched the pictures or the dates wrongly")
	}

	if _, err := g.photos.Update(t.Context(), types.NewID(), f); !errors.Is(err, photobus.ErrNotFound) {
		t.Errorf("changing a photo that is not there: %v", err)
	}
}

func TestRemovingAPhotoTakesItsFiles(t *testing.T) {
	g := setup(t)

	p, err := g.photos.Add(t.Context(), g.penstemon.ID, ours(photobus.Young), photo(t, 600, 400))
	if err != nil {
		t.Fatal(err)
	}

	if err := g.photos.Delete(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}

	if got := g.files(t); len(got) != 0 {
		t.Errorf("files left: %v", got)
	}

	if err := g.photos.Delete(t.Context(), p.ID); !errors.Is(err, photobus.ErrNotFound) {
		t.Errorf("removing it twice: %v", err)
	}
}

// A species or a place a photo names cannot be removed from under it.
func TestAPhotographedPlantOrPlaceStays(t *testing.T) {
	g := setup(t)

	f := ours(photobus.Leaf)
	f.PlaceID = g.inflow.ID

	p, err := g.photos.Add(t.Context(), g.penstemon.ID, f, photo(t, 600, 400))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := errors.AsType[speciesbus.Invalid](g.species.Delete(t.Context(), g.penstemon.ID)); !ok {
		t.Error("a photographed plant could be removed")
	}

	if _, ok := errors.AsType[placebus.Invalid](g.places.Delete(t.Context(), g.inflow.ID)); !ok {
		t.Error("a place a photo was taken in could be removed")
	}

	if err := g.photos.Delete(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}

	if err := g.places.Delete(t.Context(), g.inflow.ID); err != nil {
		t.Errorf("the place, once its photo is gone: %v", err)
	}

	if err := g.species.Delete(t.Context(), g.penstemon.ID); err != nil {
		t.Errorf("the plant, once its photo is gone: %v", err)
	}
}

// The photo a volunteer sees for a kind: checked, ours before borrowed, the
// more recent first.
func TestTheBestPhotoIsOursAndRecentAndChecked(t *testing.T) {
	at := func(year, month int) func(*photobus.Photo) {
		return func(p *photobus.Photo) { p.TakenYear, p.TakenMonth = year, month }
	}

	mk := func(id string, s photobus.Source, checked bool, opts ...func(*photobus.Photo)) photobus.Photo {
		p := photobus.Photo{Kind: photobus.Leaf, Source: s, Checked: checked}
		p.ID, _ = types.ParseID(strings.Repeat(id, 32))
		for _, o := range opts {
			o(&p)
		}

		return p
	}

	borrowedNew := mk("a", photobus.Borrowed, true, at(2027, 6))
	oursOld := mk("b", photobus.Ours, true, at(2026, 4))
	oursNew := mk("c", photobus.Ours, true, at(2027, 4))
	oursNewest := mk("d", photobus.Ours, false, at(2027, 9))
	flower := mk("e", photobus.Ours, true, at(2028, 1), func(p *photobus.Photo) { p.Kind = photobus.Flower })

	all := []photobus.Photo{borrowedNew, oursOld, oursNew, oursNewest, flower}

	if best, ok := photobus.Best(all, photobus.Leaf); !ok || best.ID != oursNew.ID {
		t.Errorf("best leaf %v, want our newest checked one", best.ID)
	}

	if _, ok := photobus.Best([]photobus.Photo{oursNewest}, photobus.Leaf); ok {
		t.Error("an unchecked photo was chosen")
	}

	if _, ok := photobus.Best(all, photobus.Winter); ok {
		t.Error("a photo was found for a kind that has none")
	}
}
