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

	img := image.NewRGBA(image.Rect(0, 0, 900, 600))
	rng := rand.New(rand.NewPCG(1, 2))

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
	for _, want := range []string{"Photo added.", "Not checked: volunteers don't see it", "Our photo · April 2027", "5 of the five kinds have no checked photo yet."} {
		if !strings.Contains(list, want) {
			t.Errorf("the photos screen does not show %q", want)
		}
	}

	m := photoID.FindStringSubmatch(list)
	if m == nil {
		t.Fatal("no picture on the photos screen")
	}
	id := m[1]

	// Unchecked: a steward sees it, nobody else can find it at all.
	if w := s.get("/photos/"+id+"/small.jpg", true); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("a steward's view of an unchecked photo: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}

	for _, file := range []string{"small.jpg", "large.jpg", "original.jpg", "original"} {
		if w := s.get("/photos/"+id+"/"+file, false); w.Code != http.StatusNotFound {
			t.Errorf("signed out, %s of an unchecked photo: %d", file, w.Code)
		}
	}

	card := s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	if strings.Contains(card, id) || !strings.Contains(card, "No leaf photo yet.") {
		t.Error("an unchecked photo is on the card")
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

	card = s.get("/plants/brazos-penstemon?view=weeding", false).Body.String()
	for _, want := range []string{
		`src="/photos/` + id + `/small.jpg"`,
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
