package inboxapp_test

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

	"github.com/jroedel/stewards/app/domain/inboxapp"
	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/inbox/stores/inboxdb"
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
	t      *testing.T
	h      http.Handler
	inbox  *inboxbus.Business
	cookie *http.Cookie

	inflow placebus.Place
}

// Through the muxer, signed in as a steward, so a batch goes through the same
// fork in the middleware it does in production.
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
		func() error { return inboxdb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	photoFiles, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	inboxFiles, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files", "inbox"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	places := placebus.NewBusiness(placedb.NewStore(db), nil)

	s := &site{t: t, inbox: inboxbus.NewBusiness(inboxdb.NewStore(db), inboxFiles, nil)}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:   places,
		Species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		Photos:   photobus.NewBusiness(photodb.NewStore(db), photoFiles, nil),
		Users:    users,
		Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil),
		Inbox:    s.inbox,
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

	if s.inflow, err = places.Create(t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}}); err != nil {
		t.Fatal(err)
	}

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

type file struct {
	name string
	data []byte
}

// batch posts the send form as a phone would: multipart, every photo chosen a
// part called photo.
func (s *site) batch(fields url.Values, files ...file) *httptest.ResponseRecorder {
	s.t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}

	for _, f := range files {
		part, err := mw.CreateFormFile("photo", f.name)
		if err != nil {
			s.t.Fatal(err)
		}
		_, _ = part.Write(f.data)
	}

	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, inboxapp.IndexPath, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	return s.send(r, true)
}

// noisy is an invented photo, never a real one (CLAUDE.md), and well over the
// 64 KB every other route keeps: noise does not compress. seed makes each one
// different.
func noisy(t *testing.T, seed uint64) []byte {
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

	return buf.Bytes()
}

func property() url.Values { return url.Values{"at": {"property"}} }

var smallPicture = regexp.MustCompile(`/steward/inbox/([0-9a-z]+)/small\.jpg`)

// Ten photos chosen in the garden, sent, and not sorted: they wait in the
// inbox, and the stewards' front page says how many.
func TestABatchWaitsInTheInbox(t *testing.T) {
	s := serve(t)

	fields := property()
	fields.Set("place", s.inflow.ID.String())
	fields.Set("note", "new by the outlet")

	w := s.batch(fields, file{"IMG_0001.JPG", noisy(t, 1)}, file{"IMG_0002.JPG", noisy(t, 2)})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox?done=sent&n=2&d=0" {
		t.Fatalf("send: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	list := s.get(w.Header().Get("Location"), true).Body.String()
	for _, want := range []string{"2 photos are in the inbox.", "2 waiting to be sorted.", "Inflow band", "new by the outlet"} {
		if !strings.Contains(list, want) {
			t.Errorf("the inbox does not show %q", want)
		}
	}

	if n := len(smallPicture.FindAllString(list, -1)); n != 2 {
		t.Errorf("%d pictures on the inbox, want 2", n)
	}

	if front := s.get("/steward", true).Body.String(); !strings.Contains(front, "2 to sort") {
		t.Error("the stewards' front page does not count the inbox")
	}
}

// One photo that is not a photo, and one sent twice, and the rest are kept
// all the same; the steward is told which by the name the phone gave it.
func TestABatchKeepsWhatItCanAndNamesWhatItCannot(t *testing.T) {
	s := serve(t)
	again := noisy(t, 3)

	w := s.batch(property(),
		file{"IMG_0003.JPG", again},
		file{"Screenshot.txt", []byte("a shopping list")},
		file{"IMG_0003 copy.JPG", again},
		file{"IMG_0004.JPG", noisy(t, 4)},
	)

	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("send: %d\n%s", w.Code, body)
	}

	for _, want := range []string{"2 photos are in the inbox. 1 was there already.", "1 not kept:", "Screenshot.txt", "not a photo this site can read"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer does not say %q", want)
		}
	}

	if n, _ := s.inbox.Count(t.Context()); n != 2 {
		t.Errorf("%d kept, want 2", n)
	}
}

func TestABatchThatCannotBeSentSaysWhy(t *testing.T) {
	s := serve(t)

	var many []file
	for i := range inboxapp.MaxPhotos + 1 {
		many = append(many, file{"IMG.JPG", []byte{byte(i)}})
	}

	for name, tc := range map[string]struct {
		fields url.Values
		files  []file
		want   string
	}{
		"no photos":   {property(), nil, "Choose the photos to send."},
		"too many":    {property(), many, "Send up to 20 at a time."},
		"nowhere":     {url.Values{}, []file{{"IMG.JPG", noisy(t, 5)}}, "taken on the property or at a nursery"},
		"a bad place": {url.Values{"at": {"property"}, "place": {"elsewhere"}}, []file{{"IMG.JPG", noisy(t, 5)}}, "Choose the place from the list"},
	} {
		w := s.batch(tc.fields, tc.files...)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d, want 422 saying %q\n%s", name, w.Code, tc.want, w.Body.String())
		}
	}

	if n, _ := s.inbox.Count(t.Context()); n != 0 {
		t.Errorf("%d kept from batches that were refused", n)
	}
}

// At a nursery, a place chosen before the toggle was flipped is dropped.
func TestANurseryBatchIsMarkedSo(t *testing.T) {
	s := serve(t)

	w := s.batch(url.Values{"at": {"nursery"}, "place": {s.inflow.ID.String()}}, file{"IMG_0005.JPG", noisy(t, 6)})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("send: %d\n%s", w.Code, w.Body.String())
	}

	list := s.get("/steward/inbox", true).Body.String()
	if !strings.Contains(list, "At a nursery") || strings.Contains(list, "Inflow band") {
		t.Error("a nursery photo is not marked as one, or kept its place")
	}
}

// The send screen opens on the property every time.
func TestTheSendScreenStartsOnTheProperty(t *testing.T) {
	s := serve(t)

	body := s.get("/steward/inbox/new", true).Body.String()
	if !strings.Contains(body, `value="property" checked`) || strings.Contains(body, `value="nursery" checked`) {
		t.Error("the send screen does not start on the property")
	}

	if !strings.Contains(body, `multiple`) {
		t.Error("the send screen takes one photo at a time")
	}
}

// An inbox picture is a steward's alone, and kept nowhere.
func TestAnInboxPictureIsAStewardsAlone(t *testing.T) {
	s := serve(t)

	if w := s.batch(property(), file{"IMG_0006.JPG", noisy(t, 7)}); w.Code != http.StatusSeeOther {
		t.Fatalf("send: %d", w.Code)
	}

	m := smallPicture.FindStringSubmatch(s.get("/steward/inbox", true).Body.String())
	if m == nil {
		t.Fatal("no picture on the inbox")
	}

	for _, size := range []string{"small.jpg", "large.jpg"} {
		path := "/steward/inbox/" + m[1] + "/" + size

		w := s.get(path, true)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("%s for a steward: %d %q %q", size, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}

		if w := s.get(path, false); w.Code == http.StatusOK {
			t.Errorf("%s was served to somebody signed out", size)
		}
	}

	for _, path := range []string{"/steward/inbox/" + m[1] + "/original.jpg", "/photos/" + m[1] + "/small.jpg"} {
		if w := s.get(path, true); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}

	if w := s.get("/steward/inbox", false); w.Code == http.StatusOK {
		t.Error("the inbox was shown to somebody signed out")
	}
}
