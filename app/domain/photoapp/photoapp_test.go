package photoapp_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/photo/stores/photodb"
	"github.com/jroedel/stewards/business/domain/photo/stores/photofs"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/species/stores/speciesdb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type site struct {
	t       *testing.T
	h       http.Handler
	species *speciesbus.Business
	places  *placebus.Business
	photos  *photobus.Business
	cookie  *http.Cookie

	penstemon string // its id
}

// Through the muxer, signed in as a steward, so the upload goes through the
// same fork in the middleware it does in production.
func serve(t *testing.T) *site {
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
		func() error { return workdaydb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	s := &site{
		t:       t,
		species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		places:  placebus.NewBusiness(placedb.NewStore(db), nil),
		photos:  photobus.NewBusiness(photodb.NewStore(db), files, nil),
	}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: s.places, Species: s.species, Photos: s.photos, Users: users,
		Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil),
		BaseURL:  "https://stewards.example.invalid", Mail: &mail.Recorder{},
	}); err != nil {
		t.Fatal(err)
	}

	addr, _ := types.ParseEmail("steward@example.org")
	if _, err := users.Create(t.Context(), addr, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := users.RequestSignIn(t.Context(), addr)
	_, value, err := users.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	s.cookie = &http.Cookie{Name: mid.SessionCookie, Value: value}

	sp, err := s.species.Create(t.Context(), speciesbus.Fields{
		Slug: "brazos-penstemon", Common: types.Text{EN: "Brazos penstemon"}, Status: speciesbus.StatusNative,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.penstemon = sp.ID.String()

	return s
}

func (s *site) send(r *http.Request, signedIn bool) *httptest.ResponseRecorder {
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if signedIn {
		r.AddCookie(s.cookie)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func (s *site) get(path string, signedIn bool) *httptest.ResponseRecorder {
	return s.send(httptest.NewRequest(http.MethodGet, path, nil), signedIn)
}

func (s *site) post(path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return s.send(r, true)
}

// upload posts the add form as a browser would: multipart, the photo as a
// file part.
func (s *site) upload(path string, fields url.Values, photo []byte, signedIn bool) *httptest.ResponseRecorder {
	s.t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}

	if photo != nil {
		part, err := mw.CreateFormFile("photo", "IMG_0001.JPG")
		if err != nil {
			s.t.Fatal(err)
		}
		_, _ = part.Write(photo)
	}

	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	return s.send(r, signedIn)
}

func (s *site) photosPath() string { return "/steward/species/" + s.penstemon + "/photos" }

// noisy is an invented photo whose JPEG is well over 64 KB, the limit every
// other route keeps: noise does not compress.
func noisy(t *testing.T) []byte {
	t.Helper()

	return noise(t, 1)
}

// noise is noisy with another seed, for a second photo that is not the
// same photo again.
func noise(t *testing.T, seed uint64) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	rng := rand.New(rand.NewPCG(seed, 2))

	for i := range img.Pix {
		img.Pix[i] = uint8(rng.UintN(256))
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}

	if buf.Len() < 64<<10 {
		t.Fatalf("the fixture is only %d bytes", buf.Len())
	}

	return buf.Bytes()
}

func leaf() url.Values {
	return url.Values{"kind": {"leaf"}, "source": {"ours"}, "taken_month": {"4"}, "taken_year": {"2027"}}
}

var photoID = regexp.MustCompile(`/photos/([0-9a-z]+)/small\.jpg`)

// The whole life of a photo: added unchecked, invisible to a volunteer,
// checked, then on the card and served to anybody.
func TestAStewardAddsAPhotoAndChecksIt(t *testing.T) {
	s := serve(t)

	w := s.upload(s.photosPath(), leaf(), noisy(t), true)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != s.photosPath()+"?done=added#leaf" {
		t.Fatalf("upload: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	list := s.get(s.photosPath()+"?done=added", true).Body.String()
	for _, want := range []string{"Photo added.", "Not checked: volunteers don't see it", "Our photo · April 2027", "6 of the 6 kinds have no checked photo yet."} {
		if !strings.Contains(list, want) {
			t.Errorf("the photos screen does not show %q", want)
		}
	}

	m := photoID.FindStringSubmatch(list)
	if m == nil {
		t.Fatal("no picture on the photos screen")
	}
	id := m[1]

	// Each photo says its ID, as the API names it in a conversation about
	// the photos: on the steward's screens, and never on a volunteer's.
	shown := `Photo <code class="photo-id">` + id[:8] + `</code>`
	if !strings.Contains(list, shown) {
		t.Errorf("the photos screen does not show %s", shown)
	}
	if edit := s.get("/steward/photos/"+id+"/edit", true).Body.String(); !strings.Contains(edit, shown) {
		t.Errorf("the photo's edit screen does not show %s", shown)
	}
	if card := s.get("/plants/brazos-penstemon?view=weeding", true).Body.String(); strings.Contains(card, "photo-id") {
		t.Error("the plant's card shows a photo's ID")
	}

	// Unchecked: a steward sees it, nobody else can find it at all.
	if w := s.get("/photos/"+id+"/small.jpg", true); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("a steward's view of an unchecked photo: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}

	for _, file := range []string{"small.jpg", "large.jpg", "full.jpg", "original.jpg", "original"} {
		if w := s.get("/photos/"+id+"/"+file, false); w.Code != http.StatusNotFound {
			t.Errorf("signed out, %s of an unchecked photo: %d", file, w.Code)
		}
	}

	card := s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	if strings.Contains(card, id) || !strings.Contains(card, "No leaf photo yet.") {
		t.Error("an unchecked photo is on the card")
	}

	// Its page of its own is a steward's too. Anybody else is sent back
	// to the card, with nothing to say there is a photo they cannot see.
	zoom := "/plants/brazos-penstemon/photos/" + id + "?view=weeding"
	if w := s.get(zoom, false); w.Code != http.StatusFound || w.Header().Get("Location") != "/plants/brazos-penstemon?view=weeding" {
		t.Errorf("signed out, the page of an unchecked photo: %d %q", w.Code, w.Header().Get("Location"))
	}

	if w := s.get(zoom, true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Not checked: volunteers don't see it") || !strings.Contains(w.Body.String(), `href="/steward/photos/`+id+`/edit"`) {
		t.Errorf("a steward's page of an unchecked photo: %d\n%s", w.Code, w.Body.String())
	}

	if edit := s.get("/steward/photos/"+id+"/edit", true).Body.String(); !strings.Contains(edit, `href="/plants/brazos-penstemon/photos/`+id+`"`) {
		t.Error("the edit screen does not zoom in")
	}

	// Checked.
	f := leaf()
	f.Set("checked", "yes")

	if w := s.post("/steward/photos/"+id, f); w.Code != http.StatusSeeOther {
		t.Fatalf("checking it: %d\n%s", w.Code, w.Body.String())
	}

	w = s.get("/photos/"+id+"/large.jpg", false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("signed out, the checked photo: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}

	if img, err := jpeg.Decode(w.Body); err != nil || img.Bounds().Dx() != 900 {
		t.Errorf("the large picture: %v, %v", err, img)
	}

	// And at full size, under the same rule: the original's pixels, and
	// as a JPEG, so not under the name a PNG's would have.
	w = s.get("/photos/"+id+"/full.jpg", false)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("signed out, the checked photo at full size: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}

	if img, err := jpeg.Decode(w.Body); err != nil || img.Bounds().Dx() != 900 {
		t.Errorf("the full picture: %v, %v", err, img)
	}

	if w := s.get("/photos/"+id+"/full.png", true); w.Code != http.StatusNotFound {
		t.Errorf("a JPEG's full picture as full.png: %d", w.Code)
	}

	card = s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	for _, want := range []string{
		`src="/photos/` + id + `/small.jpg"`,
		`href="/plants/brazos-penstemon/photos/` + id + `?view=weeding"`,
		`alt="Brazos penstemon: leaf"`,
		"Our photo · April 2027",
		"No young-plant photo yet.",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("the weeding view does not show %q", want)
		}
	}

	if strings.Contains(card, "No leaf photo yet.") {
		t.Error("the card still says there is no leaf photo")
	}

	// Tapped, it opens on a page of its own: the large picture under the
	// full one, credited as on the card, and back to the view it came from.
	w = s.get(zoom, false)
	page := w.Body.String()
	for _, want := range []string{
		`src="/photos/` + id + `/large.jpg"`,
		`class="zoom-full" src="/photos/` + id + `/full.jpg"`,
		`alt="Brazos penstemon: leaf"`,
		"Our photo · April 2027",
		"Pinch to zoom in.",
		`href="/photos/` + id + `/full.jpg"`,
		`href="/plants/brazos-penstemon?view=weeding"`,
	} {
		if w.Code != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("the photo's page: %d, without %q", w.Code, want)
		}
	}

	if strings.Contains(page, "/steward/") {
		t.Error("the photo's page shows a volunteer a steward's link")
	}

	for _, path := range []string{"/plants/brazos-penstemon/photos/not-an-id", "/plants/brazos-penstemon/photos/" + strings.Repeat("0", 32)} {
		if w := s.get(path, false); w.Code != http.StatusFound || w.Header().Get("Location") != "/plants/brazos-penstemon" {
			t.Errorf("%s: %d %q", path, w.Code, w.Header().Get("Location"))
		}
	}

	if w := s.get("/plants/no-such-plant/photos/"+id, false); w.Code != http.StatusNotFound {
		t.Errorf("a photo of a plant that is not there: %d", w.Code)
	}

	// The planting view shows flowers and the grown plant, not leaves.
	if planting := s.get("/plants/brazos-penstemon", false).Body.String(); strings.Contains(planting, id) {
		t.Error("the leaf is on the planting view")
	}
}

func TestABorrowedPhotoWithoutItsLicenceIsRefused(t *testing.T) {
	s := serve(t)

	f := url.Values{
		"kind": {"flower"}, "source": {"borrowed"}, "credit": {"A. Botanist"},
		"source_url": {"https://commons.wikimedia.org/wiki/File:Invented.jpg"},
	}

	w := s.upload(s.photosPath(), f, noisy(t), true)
	body := w.Body.String()

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no licence: %d", w.Code)
	}

	for _, want := range []string{
		"A borrowed photo needs its licence, such as CC BY-SA 4.0.",
		"choose the photo again",
		`value="A. Botanist"`,
		`value="https://commons.wikimedia.org/wiki/File:Invented.jpg"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not show %q", want)
		}
	}

	if photos, _ := s.photos.ForSpecies(t.Context(), mustID(t, s.penstemon)); len(photos) != 0 {
		t.Error("a refused photo was kept")
	}

	// With it, the card credits it as the licence asks.
	f.Set("license", "CC BY-SA 4.0")
	f.Set("checked", "yes")

	if w := s.upload(s.photosPath(), f, noisy(t), true); w.Code != http.StatusSeeOther {
		t.Fatalf("with the licence: %d", w.Code)
	}

	card := s.get("/plants/brazos-penstemon", false).Body.String()
	if !strings.Contains(card, `Photo: A. Botanist, CC BY-SA 4.0 · <a href="https://commons.wikimedia.org/wiki/File:Invented.jpg"`) {
		t.Error("the borrowed photo is not credited on the card")
	}
}

func TestWhatIsNotAPhotoIsRefusedWithAReason(t *testing.T) {
	s := serve(t)

	for name, data := range map[string][]byte{
		"a text file": []byte("this is a shopping list, not a photo"),
		"nothing":     nil,
	} {
		w := s.upload(s.photosPath(), leaf(), data, true)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, w.Code)
		}

		if body := w.Body.String(); !strings.Contains(body, "JPEG or PNG") && !strings.Contains(body, "Choose a photo to send.") {
			t.Errorf("%s: no reason given", name)
		}
	}
}

// The upload route is the one that takes a file, and the only one.
func TestOnlyTheUploadRouteTakesAFile(t *testing.T) {
	s := serve(t)

	// A form post to it, not multipart, is refused before anything reads it.
	if w := s.post(s.photosPath(), leaf()); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a plain form to the upload route: %d", w.Code)
	}

	// A file to any other route is refused by the 64 KB limit or the form
	// check, whichever comes first.
	if w := s.upload("/steward/species", url.Values{"slug": {"x"}}, noisy(t), true); w.Code == http.StatusSeeOther || w.Code == http.StatusOK {
		t.Errorf("a file posted to the add-a-plant form: %d", w.Code)
	}

	// Signed out, the upload goes nowhere: a bare 403, since a redirected
	// POST would lose its photo anyway (mid.Require).
	w := s.upload(s.photosPath(), leaf(), noisy(t), false)
	if w.Code != http.StatusForbidden {
		t.Errorf("signed out: %d %s", w.Code, w.Header().Get("Location"))
	}

	if photos, _ := s.photos.ForSpecies(t.Context(), mustID(t, s.penstemon)); len(photos) != 0 {
		t.Error("a signed-out upload was kept")
	}
}

func TestRemovingAPhotoNeedsTheBoxTicked(t *testing.T) {
	s := serve(t)

	f := leaf()
	f.Set("checked", "yes")
	s.upload(s.photosPath(), f, noisy(t), true)

	id := photoID.FindStringSubmatch(s.get(s.photosPath(), true).Body.String())[1]

	if w := s.post("/steward/photos/"+id+"/delete", url.Values{}); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), "Tick the box to confirm") {
		t.Errorf("without the box: %d", w.Code)
	}

	if w := s.post("/steward/photos/"+id+"/delete", url.Values{"confirm": {"yes"}}); w.Code != http.StatusSeeOther ||
		w.Header().Get("Location") != s.photosPath()+"?done=removed" {
		t.Fatalf("with the box: %d", w.Code)
	}

	if w := s.get("/photos/"+id+"/small.jpg", true); w.Code != http.StatusNotFound {
		t.Errorf("the removed photo is still served: %d", w.Code)
	}

	if w := s.get("/steward/photos/"+id+"/edit", true); w.Code != http.StatusNotFound {
		t.Errorf("the removed photo's edit page: %d", w.Code)
	}
}

// A planned plant's flower goes beside it on the place card.
func TestThePlaceCardShowsAPlannedPlantsFlower(t *testing.T) {
	s := serve(t)

	if w := s.post("/steward/places", url.Values{"slug": {"rain-garden"}, "name_en": {"Rain garden"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("adding the place: %d", w.Code)
	}

	garden, _ := s.places.BySlug(t.Context(), "rain-garden")
	s.post("/steward/places/"+garden.ID.String()+"/plants", url.Values{"species": {s.penstemon}, "action": {"protect"}, "planned": {"yes"}})

	f := url.Values{"kind": {"flower"}, "source": {"ours"}, "place": {garden.ID.String()}, "checked": {"yes"}}
	if w := s.upload(s.photosPath(), f, noisy(t), true); w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", w.Code)
	}

	id := photoID.FindStringSubmatch(s.get(s.photosPath(), true).Body.String())[1]

	card := s.get("/places/rain-garden", false).Body.String()
	if !strings.Contains(card, `class="row-thumb" src="/photos/`+id+`/small.jpg"`) {
		t.Error("the planned plant's flower is not on the place card")
	}

	// And the plant card says where ours was taken.
	if plant := s.get("/plants/brazos-penstemon", false).Body.String(); !strings.Contains(plant, "Our photo · Rain garden") {
		t.Error("the plant card does not say where the photo was taken")
	}

	// The place is now where a photo was taken, so it stays.
	if w := s.post("/steward/places/"+garden.ID.String()+"/delete", url.Values{"confirm": {"yes"}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("removing a place a photo names: %d", w.Code)
	}
}

func mustID(t *testing.T, s string) types.ID {
	t.Helper()

	id, err := types.ParseID(s)
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// The photos screen is reached from the plant's own edit form.
func TestThePlantsEditFormLeadsToItsPhotos(t *testing.T) {
	s := serve(t)

	if body := s.get("/steward/species/"+s.penstemon+"/edit", true).Body.String(); !strings.Contains(body, `href="`+s.photosPath()+`"`) {
		t.Error("the edit form does not link to the photos")
	}
}

func TestTheSamePhotoTwiceIsSaidSo(t *testing.T) {
	s := serve(t)
	data := noisy(t)

	s.upload(s.photosPath(), leaf(), data, true)

	w := s.upload(s.photosPath(), leaf(), data, true)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "That photo is already here, under Leaf close-up.") {
		t.Errorf("the same photo twice: %d", w.Code)
	}
}

// A photo of ours from a park says so on the card, by name when a steward
// gave one, and one taken here is shown before it however much newer the
// park's is: a volunteer looking at the card is looking at this garden.
func TestAPhotoTakenOffThePropertySaysWhere(t *testing.T) {
	s := serve(t)

	park := leaf()
	park.Set("place", "elsewhere")
	park.Set("taken_where", "  Pedernales Falls   State Park ")
	park.Set("checked", "yes")

	if w := s.upload(s.photosPath(), park, noise(t, 1), true); w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d\n%s", w.Code, w.Body.String())
	}

	list := s.get(s.photosPath(), true).Body.String()
	if !strings.Contains(list, "Our photo · Pedernales Falls State Park · April 2027") {
		t.Error("the photos screen does not say where it was taken")
	}

	id := photoID.FindStringSubmatch(list)[1]

	edit := s.get("/steward/photos/"+id+"/edit", true).Body.String()
	for _, want := range []string{
		`<option value="elsewhere" selected>Somewhere else, off the property</option>`,
		`name="taken_where" type="text" value="Pedernales Falls State Park"`,
		`<option value="">Not said</option>`,
	} {
		if !strings.Contains(edit, want) {
			t.Errorf("the edit screen does not show %q", want)
		}
	}

	card := s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	if !strings.Contains(card, "Our photo · Pedernales Falls State Park · April 2027") {
		t.Error("the card does not say where the photo was taken")
	}

	// Without a name it still says it was not taken here.
	park.Set("taken_where", "")
	if w := s.post("/steward/photos/"+id, park); w.Code != http.StatusSeeOther {
		t.Fatalf("saving it without a name: %d\n%s", w.Code, w.Body.String())
	}

	if card := s.get("/plants/brazos-penstemon?view=weeding", false).Body.String(); !strings.Contains(card, "Our photo · not taken here · April 2027") {
		t.Error("the card does not say an unnamed park photo was not taken here")
	}

	// A name left in the hidden box when "Not said" is chosen again does
	// not send the photo back off the property.
	here := leaf()
	here.Set("taken_where", "Pedernales Falls State Park")
	here.Set("checked", "yes")

	if w := s.post("/steward/photos/"+id, here); w.Code != http.StatusSeeOther {
		t.Fatalf("saving it as here: %d", w.Code)
	}

	if p, _ := s.photos.ByID(t.Context(), mustID(t, id)); p.Elsewhere || p.TakenWhere != "" {
		t.Errorf("chosen as not said, the photo is %+v", p)
	}

	// Off the property again, then a photo taken here a year earlier: the
	// card shows ours from here.
	park.Set("taken_where", "Pedernales Falls State Park")
	s.post("/steward/photos/"+id, park)

	older := leaf()
	older.Set("taken_year", "2026")
	older.Set("checked", "yes")

	if w := s.upload(s.photosPath(), older, noise(t, 2), true); w.Code != http.StatusSeeOther {
		t.Fatalf("the second upload: %d\n%s", w.Code, w.Body.String())
	}

	card = s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	if !strings.Contains(card, "Our photo · April 2026") || strings.Contains(card, "Pedernales") || strings.Contains(card, id) {
		t.Error("the card does not show the photo taken here first")
	}

	// A name too long to fit is refused, and said where it was typed.
	park.Set("taken_where", strings.Repeat("a", 101))
	if w := s.post("/steward/photos/"+id, park); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "longer than 100 characters") {
		t.Errorf("a long name: %d", w.Code)
	}
}

// The day it was taken and whether it is in flower are said on the edit
// screen, for the flowering record, and shown there again.
func TestAPhotosDayAndFlowerAreSaidOnItsScreen(t *testing.T) {
	s := serve(t)

	if w := s.upload(s.photosPath(), leaf(), noisy(t), true); w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", w.Code)
	}
	id := photoID.FindStringSubmatch(s.get(s.photosPath(), true).Body.String())[1]

	f := leaf()
	f.Set("taken_on", "2026-09-20")
	f.Set("in_flower", "yes")
	if w := s.post("/steward/photos/"+id, f); w.Code != http.StatusSeeOther {
		t.Fatalf("saving: %d\n%s", w.Code, w.Body.String())
	}

	edit := s.get("/steward/photos/"+id+"/edit", true).Body.String()
	if !strings.Contains(edit, `name="taken_on" type="date" value="2026-09-20"`) || !strings.Contains(edit, `name="in_flower" value="yes" checked`) {
		t.Error("the edit screen does not show the day and the flower it was given")
	}

	// And the plant's flowering record has it, first and last, not checked.
	list := s.get(s.photosPath(), true).Body.String()
	if !strings.Contains(list, "Flowering and fruiting") || strings.Count(list, `/edit">20 Sep</a>`) != 3 || !strings.Contains(list, "not checked") {
		t.Error("the flowering record does not show the photo's day")
	}

	// The month and year follow the day, whatever the boxes said.
	if !strings.Contains(edit, `<option value="9" selected>September</option>`) || !strings.Contains(edit, `value="2026"`) {
		t.Error("the month and year do not follow the day")
	}

	f.Set("taken_on", "20 September")
	if w := s.post("/steward/photos/"+id, f); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Choose the day") {
		t.Errorf("a day that is not one: %d", w.Code)
	}
}
