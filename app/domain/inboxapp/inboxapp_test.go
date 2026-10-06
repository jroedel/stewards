package inboxapp_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/domain/inboxapp"
	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
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
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type site struct {
	t        *testing.T
	h        http.Handler
	inbox    *inboxbus.Business
	nursery  *nurserybus.Business
	photos   *photobus.Business
	listings *listingbus.Business
	cookie   *http.Cookie

	inflow    placebus.Place
	penstemon speciesbus.Species
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
		func() error { return nurserydb.Init(t.Context(), db) },
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

	species := speciesbus.NewBusiness(speciesdb.NewStore(db), nil)

	s := &site{
		t:        t,
		photos:   photobus.NewBusiness(photodb.NewStore(db), photoFiles, nil),
		listings: listingbus.NewBusiness(listingdb.NewStore(db), nil),
	}
	s.nursery = nurserybus.NewBusiness(nurserydb.NewStore(db), nil)
	s.inbox = inboxbus.NewBusiness(inboxdb.NewStore(db), inboxFiles, inboxbus.Deps{Photos: s.photos, Listings: s.listings, Stock: s.nursery}, nil)

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:   places,
		Species:  species,
		Photos:   s.photos,
		Users:    users,
		Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		Listings: s.listings,
		Inbox:    s.inbox,
		Nursery:  s.nursery,
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

	if s.penstemon, err = species.Create(t.Context(), speciesbus.Fields{
		Slug: "brazos-penstemon", Common: types.Text{EN: "Brazos penstemon"}, Scientific: "Penstemon tenuis", Status: speciesbus.StatusNative,
	}); err != nil {
		t.Fatal(err)
	}

	return s
}

func (s *site) post(path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return s.send(r, true)
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

// batch posts the send form as a phone would without its script: multipart,
// every photo chosen a part called photo.
func (s *site) batch(fields url.Values, files ...file) *httptest.ResponseRecorder {
	s.t.Helper()

	return s.multipart(inboxapp.IndexPath, fields, files...)
}

// one sends a photo as the send screen's script does: one to a request, with
// the batch's fields every time.
func (s *site) one(fields url.Values, f file) *httptest.ResponseRecorder {
	s.t.Helper()

	return s.multipart(inboxapp.IndexPath+"/send", fields, f)
}

func (s *site) multipart(path string, fields url.Values, files ...file) *httptest.ResponseRecorder {
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

	r := httptest.NewRequest(http.MethodPost, path, &body)
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

// A batch sent by the script, a photo to a request: each answers whether it
// was kept, a photo sent twice is there already, and a photo that cannot be
// kept says why without stopping the ones after it. What the script counts,
// the inbox shows.
func TestPhotosSentOneAtATimeWaitInTheInbox(t *testing.T) {
	s := serve(t)

	fields := property()
	fields.Set("place", s.inflow.ID.String())
	fields.Set("note", "new by the outlet")

	first := noisy(t, 11)

	for _, tc := range []struct {
		f       file
		status  int
		outcome string
		field   string
	}{
		{file{"IMG_0011.JPG", first}, http.StatusCreated, "kept", ""},
		{file{"IMG_0011 copy.JPG", first}, http.StatusOK, "already", ""},
		{file{"Screenshot.txt", []byte("a shopping list")}, http.StatusUnprocessableEntity, "", "photo"},
		{file{"IMG_0012.JPG", noisy(t, 12)}, http.StatusCreated, "kept", ""},
	} {
		w := s.one(fields, tc.f)
		got := answer(t, w)

		if w.Code != tc.status || got.Outcome != tc.outcome || got.Error.Field != tc.field || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("%s: %d %+v, want %d %q %q", tc.f.name, w.Code, got, tc.status, tc.outcome, tc.field)
		}

		if tc.field != "" && got.Error.Problem == "" {
			t.Errorf("%s was refused without saying why", tc.f.name)
		}
	}

	list := s.get("/steward/inbox", true).Body.String()
	for _, want := range []string{"2 waiting to be sorted.", "Inflow band", "new by the outlet"} {
		if !strings.Contains(list, want) {
			t.Errorf("the inbox does not show %q", want)
		}
	}
}

// What is wrong with the batch rather than a photo is refused with its field,
// which is how the script knows to stop rather than go on to the next.
func TestAPhotoSentOneAtATimeNamesWhatIsWrongWithTheBatch(t *testing.T) {
	s := serve(t)

	for name, tc := range map[string]struct {
		fields url.Values
		files  []file
		field  string
	}{
		"nowhere":     {url.Values{}, []file{{"IMG.JPG", noisy(t, 13)}}, "at"},
		"a bad place": {url.Values{"at": {"property"}, "place": {"elsewhere"}}, []file{{"IMG.JPG", noisy(t, 13)}}, "place"},
		"no photo":    {property(), nil, "photo"},
		"two photos":  {property(), []file{{"A.JPG", noisy(t, 14)}, {"B.JPG", noisy(t, 15)}}, "photo"},
	} {
		w := s.multipart(inboxapp.IndexPath+"/send", tc.fields, tc.files...)
		if got := answer(t, w); w.Code != http.StatusUnprocessableEntity || got.Error.Field != tc.field || got.Error.Problem == "" {
			t.Errorf("%s: %d %+v, want 422 on %q", name, w.Code, got, tc.field)
		}
	}

	if n, _ := s.inbox.Count(t.Context()); n != 0 {
		t.Errorf("%d kept from photos that were refused", n)
	}

	// And nobody signed out sends anything.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("at", "property")
	part, _ := mw.CreateFormFile("photo", "IMG.JPG")
	_, _ = part.Write(noisy(t, 16))
	_ = mw.Close()

	r := httptest.NewRequest(http.MethodPost, inboxapp.IndexPath+"/send", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())

	if w := s.send(r, false); w.Code == http.StatusCreated || w.Code == http.StatusOK {
		t.Errorf("a photo was taken from somebody signed out: %d", w.Code)
	}

	if n, _ := s.inbox.Count(t.Context()); n != 0 {
		t.Error("a photo from somebody signed out is in the inbox")
	}
}

// The send screen loads its script as a module, the modules it imports are
// served beside it and nothing else is, and the header policy lets them run
// and send, and nothing inline.
func TestTheSendScreenLinksItsScript(t *testing.T) {
	s := serve(t)

	w := s.get("/steward/inbox/new", true)
	page := w.Body.String()

	if !strings.Contains(page, `<script type="module" src="/steward/inbox/static/send.mjs"></script>`) {
		t.Fatal("the send screen does not load its script as a module")
	}

	for _, want := range []string{`data-send="/steward/inbox/send"`, `data-done="/steward/inbox"`, `data-max="20"`, `id="sending"`, `id="sent"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the send screen has no %s for its script", want)
		}
	}

	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the header policy does not allow %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("the header policy allows inline script: %s", csp)
	}

	// Each module, and each import in it, is served: a module whose import
	// 404s fails as a whole, silently, and the page is back to one post.
	imports := regexp.MustCompile(`from "\./([a-z]+\.mjs)"`)
	for _, name := range []string{"send.mjs", "shrink.mjs", "jpeg.mjs"} {
		js := s.get("/steward/inbox/static/"+name, true)
		if js.Code != http.StatusOK || js.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("%s: %d %q", name, js.Code, js.Header().Get("Content-Type"))

			continue
		}

		if js.Header().Get("Cache-Control") != "private, no-cache" || js.Header().Get("ETag") == "" {
			t.Errorf("%s is not asked for again after a deploy: %q %q", name, js.Header().Get("Cache-Control"), js.Header().Get("ETag"))
		}

		for _, m := range imports.FindAllStringSubmatch(js.Body.String(), -1) {
			if w := s.get("/steward/inbox/static/"+m[1], true); w.Code != http.StatusOK {
				t.Errorf("%s imports %s, which is not served: %d", name, m[1], w.Code)
			}
		}

		// Unchanged since the phone last asked: 304, and nothing sent.
		r := httptest.NewRequest(http.MethodGet, "/steward/inbox/static/"+name, nil)
		r.Header.Set("If-None-Match", js.Header().Get("ETag"))
		if again := s.send(r, true); again.Code != http.StatusNotModified {
			t.Errorf("%s asked for again with its ETag: %d, want 304", name, again.Code)
		}
	}

	// The tests beside the modules, and anything else, are not served.
	for _, name := range []string{"jpeg_test.mjs", "send.js", "..%2Finboxapp.go"} {
		if w := s.get("/steward/inbox/static/"+name, true); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", name, w.Code)
		}
	}

	if w := s.get("/steward/inbox/static/send.mjs", false); w.Code == http.StatusOK {
		t.Error("the script was served to somebody signed out")
	}
}

// make prod-upload-check asks Apache whether it passes the sizes this app
// takes, and it can only ask about the numbers written into it: a limit
// raised here and not there would be checked at the old size.
func TestTheUploadCheckProbesTheseLimits(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		strconv.Itoa(inboxapp.MaxSendBytes) + " " + inboxapp.IndexPath + "/send ",
		strconv.Itoa(inboxapp.MaxBytes) + " " + inboxapp.IndexPath + " ",
	} {
		if !strings.Contains(string(script), want) {
			t.Errorf("deploy.sh's upload check does not probe %q", want)
		}
	}
}

type sendAnswer struct {
	Outcome string `json:"outcome"`
	Error   struct {
		Field   string `json:"field"`
		Problem string `json:"problem"`
	} `json:"error"`
}

func answer(t *testing.T, w *httptest.ResponseRecorder) sendAnswer {
	t.Helper()

	var a sendAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatalf("the answer is not JSON: %d %s", w.Code, w.Body.String())
	}

	return a
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

	for _, size := range []string{"small.jpg", "large.jpg", "full.jpg"} {
		path := "/steward/inbox/" + m[1] + "/" + size

		w := s.get(path, true)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("%s for a steward: %d %q %q", size, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}

		if w := s.get(path, false); w.Code == http.StatusOK {
			t.Errorf("%s was served to somebody signed out", size)
		}
	}

	for _, path := range []string{"/steward/inbox/" + m[1] + "/original.jpg", "/steward/inbox/" + m[1] + "/full.png", "/photos/" + m[1] + "/small.jpg"} {
		if w := s.get(path, true); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}

	// Closer, on a page of its own, from the sort screen.
	if sort := s.get("/steward/inbox/"+m[1], true).Body.String(); !strings.Contains(sort, `href="/steward/inbox/`+m[1]+`/zoom"`) {
		t.Error("the sort screen does not zoom in")
	}

	zoom := s.get("/steward/inbox/"+m[1]+"/zoom", true)
	for _, want := range []string{
		`src="/steward/inbox/` + m[1] + `/large.jpg"`,
		`class="zoom-full" src="/steward/inbox/` + m[1] + `/full.jpg"`,
		`href="/steward/inbox/` + m[1] + `"`,
	} {
		if zoom.Code != http.StatusOK || !strings.Contains(zoom.Body.String(), want) {
			t.Errorf("the zoomed page: %d, without %q", zoom.Code, want)
		}
	}

	if w := s.get("/steward/inbox/"+m[1]+"/zoom", false); w.Code == http.StatusOK {
		t.Error("the zoomed page was shown to somebody signed out")
	}

	if w := s.get("/steward/inbox", false); w.Code == http.StatusOK {
		t.Error("the inbox was shown to somebody signed out")
	}
}

// sent is the ids of the photos in the inbox, newest first, after sending
// these.
func (s *site) sent(fields url.Values, files ...file) []string {
	s.t.Helper()

	if w := s.batch(fields, files...); w.Code != http.StatusSeeOther {
		s.t.Fatalf("send: %d\n%s", w.Code, w.Body.String())
	}

	items, err := s.inbox.Waiting(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}

	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID.String())
	}

	return ids
}

// A batch sorted on the phone, one photo after another: each sort goes on to
// the next, and the last back to the inbox.
func TestABatchIsSortedOneAfterAnother(t *testing.T) {
	s := serve(t)

	fields := property()
	fields.Set("place", s.inflow.ID.String())
	ids := s.sent(fields, file{"IMG_0010.JPG", noisy(t, 10)}, file{"IMG_0011.JPG", noisy(t, 11)})

	first := s.get("/steward/inbox/"+ids[0], true).Body.String()
	for _, want := range []string{"A plant: add it to the plant's photos", "Just planted: list it at its place", "Not sure yet", "Discard", "Inflow band"} {
		if !strings.Contains(first, want) {
			t.Errorf("the sort screen does not offer %q", want)
		}
	}

	form := s.get("/steward/inbox/"+ids[0]+"?as=photo", true).Body.String()
	if !strings.Contains(form, "Brazos penstemon (Penstemon tenuis)") || !strings.Contains(form, "Flower close-up") {
		t.Error("the photo form does not list the plants and the kinds")
	}

	w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"photo"}, "species": {s.penstemon.ID.String()}, "kind": {"flower"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox/"+ids[1]+"?done=photo" {
		t.Fatalf("sorting the first: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	if next := s.get(w.Header().Get("Location"), true).Body.String(); !strings.Contains(next, "Added to the plant&#39;s photos") {
		t.Error("the next photo's screen does not say the last was added")
	}

	w = s.post("/steward/inbox/"+ids[1], url.Values{"as": {"planted"}, "species": {s.penstemon.ID.String()}, "kind": {"young"}, "place": {s.inflow.ID.String()}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox?done=planted" {
		t.Fatalf("sorting the last: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	photos, _ := s.photos.ForSpecies(t.Context(), s.penstemon.ID)
	listed, _ := s.listings.ForPlace(t.Context(), s.inflow.ID)

	if len(photos) != 2 || photos[0].Checked || photos[1].Checked {
		t.Errorf("the plant has %d photos, %+v; want two, unchecked", len(photos), photos)
	}

	if len(listed) != 1 || listed[0].Action != listingbus.Protect || listed[0].Planned {
		t.Errorf("listed at the inflow: %+v", listed)
	}

	// Sorted: the screen says what it became and where to see it, and the
	// inbox is empty.
	if done := s.get("/steward/inbox/"+ids[0], true).Body.String(); !strings.Contains(done, "has been sorted") || !strings.Contains(done, "/steward/species/"+s.penstemon.ID.String()+"/photos") {
		t.Error("a sorted photo's screen does not say what it became")
	}

	if list := s.get("/steward/inbox?done=planted", true).Body.String(); !strings.Contains(list, "Listed as planted there") || !strings.Contains(list, "Nothing waiting") {
		t.Error("the inbox after the last sort")
	}
}

// A photo taken at a nursery is never offered as planted here.
func TestANurseryPhotoIsNotOfferedAsPlanted(t *testing.T) {
	s := serve(t)
	ids := s.sent(url.Values{"at": {"nursery"}}, file{"IMG_0012.JPG", noisy(t, 12)})

	for _, path := range []string{"/steward/inbox/" + ids[0], "/steward/inbox/" + ids[0] + "?as=planted"} {
		body := s.get(path, true).Body.String()
		if strings.Contains(body, "Just planted") || strings.Contains(body, "Save the planting") {
			t.Errorf("%s offers a nursery photo as planted", path)
		}
	}
}

func TestADiscardAsksFirst(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0013.JPG", noisy(t, 13)})

	if w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"discard"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Tick the box") {
		t.Errorf("a discard without the box: %d", w.Code)
	}

	if n, _ := s.inbox.Count(t.Context()); n != 1 {
		t.Error("the photo was discarded without the box ticked")
	}

	if w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"discard"}, "confirm": {"yes"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox?done=discard" {
		t.Errorf("a discard with the box: %d %s", w.Code, w.Header().Get("Location"))
	}

	if w := s.get("/steward/inbox/"+ids[0]+"/small.jpg", true); w.Code != http.StatusNotFound {
		t.Errorf("a discarded photo's picture: %d", w.Code)
	}

	// A zoomed page left open on another tab goes to the sort screen,
	// which says what became of it.
	if w := s.get("/steward/inbox/"+ids[0]+"/zoom", true); w.Code != http.StatusFound || w.Header().Get("Location") != "/steward/inbox/"+ids[0] {
		t.Errorf("a discarded photo, zoomed: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// Set aside: it leaves the count, and waits under Not sure yet with its
// question.
func TestAPhotoSetAsideWaitsWithItsQuestion(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0014.JPG", noisy(t, 14)})

	if w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"unsure"}, "note": {"frostweed or wingstem?"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("setting it aside: %d\n%s", w.Code, w.Body.String())
	}

	list := s.get("/steward/inbox", true).Body.String()
	if !strings.Contains(list, "Not sure yet") || !strings.Contains(list, "frostweed or wingstem?") || !strings.Contains(list, "Nothing waiting") {
		t.Error("the inbox does not show the photo set aside")
	}

	if front := s.get("/steward", true).Body.String(); !strings.Contains(front, "nothing waiting") {
		t.Error("a photo set aside is still counted as to sort")
	}

	if body := s.get("/steward/inbox/"+ids[0], true).Body.String(); !strings.Contains(body, "Question: frostweed or wingstem?") {
		t.Error("the sort screen does not show the question")
	}
}

// A refusal from the rules is shown on the form, and the photo stays.
func TestASortTheRulesRefuseIsShown(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0015.JPG", noisy(t, 15)})

	w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"planted"}, "species": {s.penstemon.ID.String()}, "kind": {"young"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Choose the place it was planted.") {
		t.Errorf("a planting with no place: %d\n%s", w.Code, w.Body.String())
	}

	if n, _ := s.inbox.Count(t.Context()); n != 1 {
		t.Error("the refused photo left the inbox")
	}
}

// Sorted on one phone, then on another that still had the screen open.
func TestASecondSortIsToldTheFirstWon(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0016.JPG", noisy(t, 16)})

	if w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"photo"}, "species": {s.penstemon.ID.String()}, "kind": {"leaf"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("first: %d", w.Code)
	}

	w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"discard"}, "confirm": {"yes"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox?done=taken" {
		t.Errorf("second: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// A morning's tags at a nursery, sorted as stock: the second is offered the
// nursery the first was sorted to, and both are on the nursery stock page,
// with their photos.
func TestNurseryPhotosAreSortedIntoStock(t *testing.T) {
	s := serve(t)
	ids := s.sent(url.Values{"at": {"nursery"}}, file{"IMG_0020.JPG", noisy(t, 20)}, file{"IMG_0021.JPG", noisy(t, 21)})

	if body := s.get("/steward/inbox/"+ids[0], true).Body.String(); !strings.Contains(body, "Nursery stock: add it") {
		t.Fatal("a nursery photo is not offered as stock")
	}

	w := s.post("/steward/inbox/"+ids[0], url.Values{
		"as": {"stock"}, "nursery": {"Natural Gardener"}, "name_on_tag": {"Penstemon tenuis"},
		"species": {s.penstemon.ID.String()}, "pot_size": {"1 gal"}, "price": {"$12.99"}, "count": {"8"},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox/"+ids[1]+"?done=stock" {
		t.Fatalf("sorting the first tag: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	if form := s.get("/steward/inbox/"+ids[1]+"?as=stock", true).Body.String(); !strings.Contains(form, `value="Natural Gardener"`) {
		t.Error("the second tag is not offered the morning's nursery")
	}

	if w := s.post("/steward/inbox/"+ids[1], url.Values{"as": {"stock"}, "nursery": {"Natural Gardener"}, "name_on_tag": {"Malvaviscus arboreus"}, "price": {"twelve"}}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "dollars and cents") {
		t.Errorf("a price that is not one: %d", w.Code)
	}

	if w := s.post("/steward/inbox/"+ids[1], url.Values{"as": {"stock"}, "nursery": {"Natural Gardener"}, "name_on_tag": {"Malvaviscus arboreus"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("sorting the second tag: %d\n%s", w.Code, w.Body.String())
	}

	page := s.get("/steward/nursery", true).Body.String()
	for _, want := range []string{"Natural Gardener", "2 plants", "Brazos penstemon", "Tag: Penstemon tenuis", "1 gal · $12.99 · 8 there", "Malvaviscus arboreus", "Not matched to a plant yet", "/steward/inbox/" + ids[0] + "/small.jpg"} {
		if !strings.Contains(page, want) {
			t.Errorf("the nursery stock page does not show %q", want)
		}
	}

	// The tag photo is still served, as the line's.
	if w := s.get("/steward/inbox/"+ids[0]+"/small.jpg", true); w.Code != http.StatusOK {
		t.Errorf("the line's photo: %d", w.Code)
	}

	if front := s.get("/steward", true).Body.String(); !strings.Contains(front, "/steward/nursery") {
		t.Error("the stewards' front page does not link to the nursery stock")
	}
}

// A garden photo is never offered as stock.
func TestAGardenPhotoIsNotOfferedAsStock(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0022.JPG", noisy(t, 22)})

	for _, path := range []string{"/steward/inbox/" + ids[0], "/steward/inbox/" + ids[0] + "?as=stock"} {
		if body := s.get(path, true).Body.String(); strings.Contains(body, "Nursery stock") || strings.Contains(body, "Which nursery") {
			t.Errorf("%s offers a garden photo as stock", path)
		}
	}
}

// A tag read wrongly is corrected on the nursery stock page.
func TestALineOfStockIsCorrected(t *testing.T) {
	s := serve(t)
	ids := s.sent(url.Values{"at": {"nursery"}}, file{"IMG_0023.JPG", noisy(t, 23)})

	if w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"stock"}, "nursery": {"Natural Gardener"}, "name_on_tag": {"Penstemon tenius"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("sorting: %d", w.Code)
	}

	stock, _ := s.nursery.All(t.Context())
	line := stock[0].Lines[0].ID.String()

	if form := s.get("/steward/nursery/lines/"+line+"/edit", true).Body.String(); !strings.Contains(form, "Penstemon tenius") || !strings.Contains(form, "/steward/inbox/"+ids[0]+"/large.jpg") {
		t.Error("the correction form does not show the line and its photo")
	}

	w := s.post("/steward/nursery/lines/"+line, url.Values{"name_on_tag": {"Penstemon tenuis"}, "species": {s.penstemon.ID.String()}, "price": {"9.5"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/nursery?done=saved#line-"+line {
		t.Fatalf("correcting: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}

	if page := s.get("/steward/nursery?done=saved", true).Body.String(); !strings.Contains(page, "Saved.") || !strings.Contains(page, "$9.50") || !strings.Contains(page, "Brazos penstemon") {
		t.Error("the correction is not shown")
	}

	if w := s.post("/steward/nursery/lines/"+line, url.Values{"name_on_tag": {""}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a line with nothing to name it: %d", w.Code)
	}
}
