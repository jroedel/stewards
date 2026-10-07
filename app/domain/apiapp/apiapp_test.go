package apiapp_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
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

	"github.com/jroedel/stewards/app/domain/apiapp"
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
	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
)

const base = "https://stewards.example.invalid"

type site struct {
	t        *testing.T
	h        http.Handler
	species  *speciesbus.Business
	places   *placebus.Business
	listings *listingbus.Business
	photos   *photobus.Business
	inbox    *inboxbus.Business
	nursery  *nurserybus.Business
	memory   *translationbus.Business
	cookie   *http.Cookie
	key      string
	steward  types.ID
}

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
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	inboxFiles, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files", "inbox"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)

	// The translation memory as main makes it, so that what the API reads
	// and writes is what the app does.
	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	workdays := workdaybus.NewBusiness(workdaydb.NewStore(db), memory, nil)
	s := &site{
		t:        t,
		species:  speciesbus.NewBusiness(speciesdb.NewStore(db), memory, nil),
		places:   placebus.NewBusiness(placedb.NewStore(db), memory, nil),
		listings: listingbus.NewBusiness(listingdb.NewStore(db), memory, nil),
		photos:   photobus.NewBusiness(photodb.NewStore(db), files, nil),
		memory:   memory,
	}
	memory.ReadFrom(s.places, s.species, s.listings, workdays)
	s.nursery = nurserybus.NewBusiness(nurserydb.NewStore(db), nil)
	s.inbox = inboxbus.NewBusiness(inboxdb.NewStore(db), inboxFiles, inboxbus.Deps{Photos: s.photos, Listings: s.listings, Stock: s.nursery}, nil)

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: s.places, Species: s.species, Users: users,
		Workdays: workdays,
		Photos:   s.photos,
		Listings: s.listings,
		Inbox:    s.inbox,
		Nursery:  s.nursery,

		Translations: memory,
		BaseURL:      base, Mail: &mail.Recorder{},
	}); err != nil {
		t.Fatal(err)
	}

	addr, _ := types.ParseEmail("steward@example.org")
	u, err := users.Create(t.Context(), addr, "")
	if err != nil {
		t.Fatal(err)
	}
	s.steward = u.ID

	req, _ := users.RequestSignIn(t.Context(), addr)
	_, value, err := users.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	s.cookie = &http.Cookie{Name: mid.SessionCookie, Value: value}
	s.key = s.makeKey("laptop")

	return s
}

var keyPattern = regexp.MustCompile(`<p class="key">(stw_[^<]+)</p>`)

// makeKey makes a key the way a steward does: on the screen, signed in.
func (s *site) makeKey(name string) string {
	s.t.Helper()

	r := httptest.NewRequest(http.MethodPost, "/steward/keys", strings.NewReader(url.Values{"name": {name}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(s.cookie)

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	m := keyPattern.FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || m == nil {
		s.t.Fatalf("making a key: %d\n%s", w.Code, w.Body.String())
	}

	if w.Header().Get("Cache-Control") != "no-store" {
		s.t.Error("the page showing a new key may be cached")
	}

	return m[1]
}

// api sends a request as a program does: no Sec-Fetch headers, no cookie,
// the key if one is given.
func (s *site) api(method, path, key string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	s.t.Helper()

	r := httptest.NewRequest(method, path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func (s *site) put(slug string, body any) *httptest.ResponseRecorder {
	s.t.Helper()

	b, _ := json.Marshal(body)

	return s.api(http.MethodPut, "/api/v1/species/"+slug, s.key, bytes.NewReader(b), "application/json")
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("not JSON (%d): %v\n%s", w.Code, err, w.Body.String())
	}

	return v
}

type problem struct {
	Error struct {
		Field   string `json:"field"`
		Problem string `json:"problem"`
	} `json:"error"`
}

func winecup() map[string]any {
	return map[string]any{
		"common":     map[string]string{"en": "Winecup"},
		"scientific": "Callirhoe involucrata",
		"status":     "native",
		"bloom":      []int{3, 4, 5, 6},
		"height":     map[string]int{"min": 6, "max": 12},
		"light":      []string{"full_sun"},
		"water":      []string{"dry"},
		"swatches":   []string{"#8e1b4b"},
		"sources":    []map[string]string{{"label": "Lady Bird Johnson Wildflower Center", "url": "https://www.wildflower.org/"}},
	}
}

// The index is public, and every endpoint it lists is mounted where it says.
func TestTheIndexListsWhatIsThere(t *testing.T) {
	s := serve(t)

	for _, path := range []string{"/api/v1", "/api/v1/"} {
		if w := s.api(http.MethodGet, path, "", nil, ""); w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}

	idx := decode[apiapp.Index](t, s.api(http.MethodGet, "/api/v1", "", nil, ""))

	if idx.BaseURL != base || !strings.Contains(idx.Authentication, base+"/steward/keys") || len(idx.Rules) == 0 {
		t.Errorf("the index's header: %+v", idx)
	}

	seen := map[string]bool{}
	for _, e := range idx.Endpoints {
		seen[e.Method+" "+e.Path] = true

		// Mounted: without a key, a route that needs one says so, and is
		// not the catch-all's "there is no". Sent the kind of body the
		// index says it takes, since the shape is checked before the key.
		var body io.Reader
		var kind string

		if e.Body != nil {
			switch e.Body.Encoding {
			case "json":
				body, kind = strings.NewReader("{}"), "application/json"
			case "multipart":
				var buf bytes.Buffer
				mw := multipart.NewWriter(&buf)
				_ = mw.Close()
				body, kind = &buf, mw.FormDataContentType()
			default:
				t.Errorf("%s %s: an encoding the index should not offer: %q", e.Method, e.Path, e.Body.Encoding)
			}
		}

		w := s.api(e.Method, strings.ReplaceAll(e.Path, "{slug}", "winecup"), "", body, kind)

		switch {
		case e.NeedsKey && w.Code != http.StatusUnauthorized:
			t.Errorf("%s %s without a key: %d", e.Method, e.Path, w.Code)
		case !e.NeedsKey && w.Code != http.StatusOK:
			t.Errorf("%s %s: %d", e.Method, e.Path, w.Code)
		}

		if e.Summary == "" || e.Returns == "" {
			t.Errorf("%s %s is not described", e.Method, e.Path)
		}

		if e.Body != nil {
			for _, f := range e.Body.Fields {
				if f.Description == "" || f.Type == "" {
					t.Errorf("%s %s: field %s is not described", e.Method, e.Path, f.Name)
				}
			}
		}
	}

	for _, want := range []string{
		"GET /api/v1", "GET /api/v1/places", "GET /api/v1/species", "GET /api/v1/species/{slug}",
		"PUT /api/v1/species/{slug}", apiapp.UploadPattern,
		"GET /api/v1/places/{slug}/plants", "PUT /api/v1/places/{slug}/plants/{species}",
		"GET /api/v1/inbox", "GET /api/v1/inbox/{id}/{file}", "POST /api/v1/inbox/{id}/sort",
		"GET /api/v1/nursery", "PUT /api/v1/nursery/lines/{id}", "GET /api/v1/nurseries",
	} {
		if !seen[want] {
			t.Errorf("the index does not list %s", want)
		}
	}

	// The vocabularies are the rules' own.
	body := s.api(http.MethodGet, "/api/v1", "", nil, "").Body.String()
	for _, word := range []string{`"full_sun"`, `"part_shade"`, `"invasive"`, `"winter"`, `"borrowed"`} {
		if !strings.Contains(body, word) {
			t.Errorf("the index does not offer %s", word)
		}
	}

	// Anything else under /api is a JSON answer pointing at the index.
	w := s.api(http.MethodGet, "/api/v1/nothing-here", s.key, nil, "")
	if p := decode[problem](t, w); w.Code != http.StatusNotFound || !strings.Contains(p.Error.Problem, "GET /api/v1 lists every endpoint") {
		t.Errorf("an unknown path: %d %q", w.Code, p.Error.Problem)
	}
}

// A key, and only a key: the session cookie is never an API credential.
func TestOnlyAKeyOpensTheAPI(t *testing.T) {
	s := serve(t)

	if w := s.api(http.MethodGet, "/api/v1/species", s.key, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("with the key: %d", w.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/species", nil)
	r.AddCookie(s.cookie)
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("with the session cookie: %d", w.Code)
	}

	w = s.api(http.MethodGet, "/api/v1/species", "stw_not.a-real-key-at-all-not-at-all", nil, "")
	if p := decode[problem](t, w); w.Code != http.StatusUnauthorized || !strings.Contains(p.Error.Problem, "expired or been revoked") {
		t.Errorf("a wrong key: %d %q", w.Code, p.Error.Problem)
	}

	// And a key is not a way into the screens.
	r = httptest.NewRequest(http.MethodGet, "/steward/species", nil)
	r.Header.Set("Authorization", "Bearer "+s.key)
	w = httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("the screens with a key: %d, want the sign-in redirect", w.Code)
	}
}

func TestAPlantIsAddedThenLeftAloneThenChanged(t *testing.T) {
	s := serve(t)

	type answer struct {
		Outcome             string             `json:"outcome"`
		ConfirmationCleared bool               `json:"confirmation_cleared"`
		Species             apiapp.SpeciesJSON `json:"species"`
	}

	w := s.put("winecup", winecup())
	got := decode[answer](t, w)

	if w.Code != http.StatusCreated || got.Outcome != "created" || got.Species.Confirmed {
		t.Fatalf("created: %d %+v", w.Code, got)
	}

	if got.Species.CardURL != base+"/plants/winecup" || len(got.Species.Bloom) != 4 || got.Species.Light[0] != "full_sun" || got.Species.Height.Max != 12 {
		t.Errorf("read back: %+v", got.Species)
	}

	if w := s.put("winecup", winecup()); w.Code != http.StatusOK || decode[answer](t, w).Outcome != "unchanged" {
		t.Errorf("sent again: %d %s", w.Code, w.Body.String())
	}

	// A steward confirms it on the screen; a change through the API takes
	// that away, and says so.
	sp, _ := s.species.BySlug(t.Context(), "winecup")
	f := speciesbus.Fields{Slug: sp.Slug, Common: sp.Common, Scientific: sp.Scientific, Status: sp.Status, Confirmed: true,
		Swatches: sp.Swatches, Bloom: sp.Bloom, Height: sp.Height, Light: sp.Light, Water: sp.Water, Sources: sp.Sources}
	if _, err := s.species.Update(t.Context(), sp.ID, f); err != nil {
		t.Fatal(err)
	}

	if a := decode[answer](t, s.put("winecup", winecup())); a.Outcome != "unchanged" || !a.Species.Confirmed {
		t.Errorf("the same again after confirming: %+v", a)
	}

	changed := winecup()
	changed["note"] = map[string]string{"en": "Let it sprawl over the wall."}

	if a := decode[answer](t, s.put("winecup", changed)); a.Outcome != "updated" || !a.ConfirmationCleared || a.Species.Confirmed {
		t.Errorf("a change to a confirmed plant: %+v", a)
	}

	// Read it back, with the list and alone.
	list := s.api(http.MethodGet, "/api/v1/species", s.key, nil, "").Body.String()
	if !strings.Contains(list, `"slug": "winecup"`) {
		t.Error("the list does not have it")
	}

	if w := s.api(http.MethodGet, "/api/v1/species/winecup", s.key, nil, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Let it sprawl") {
		t.Errorf("one plant: %d", w.Code)
	}

	if w := s.api(http.MethodGet, "/api/v1/species/nothing", s.key, nil, ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "PUT /api/v1/species/nothing adds it") {
		t.Errorf("a plant that is not there: %d", w.Code)
	}
}

func TestAPlantThatIsWrongIsRefusedWithTheField(t *testing.T) {
	s := serve(t)

	with := func(k string, v any) map[string]any { m := winecup(); m[k] = v; return m }

	for name, tc := range map[string]struct {
		slug  string
		body  any
		code  int
		field string
		says  string
	}{
		"asked to confirm":   {"winecup", with("confirmed", true), 422, "confirmed", "confirmed by a steward"},
		"a misspelt field":   {"winecup", with("scientifc", "x"), 400, "", "scientifc"},
		"a light not known":  {"winecup", with("light", []string{"sunny"}), 422, "light", "full_sun"},
		"month thirteen":     {"winecup", with("bloom", []int{13}), 422, "bloom", "1 to 12"},
		"a status not known": {"winecup", with("status", "weed"), 422, "status", "invasive"},
		"another slug":       {"winecup", with("slug", "copa-de-vino"), 422, "slug", "cannot change"},
		"a bad slug":         {"Wine_Cup", winecup(), 422, "slug", ""},
		"no common name":     {"winecup", with("common", map[string]string{}), 422, "common", ""},
	} {
		w := s.put(tc.slug, tc.body)
		p := decode[problem](t, w)

		if w.Code != tc.code || p.Error.Field != tc.field || !strings.Contains(p.Error.Problem, tc.says) {
			t.Errorf("%s: %d %+v", name, w.Code, p.Error)
		}
	}

	// A form post, or no type at all, is not the API's.
	form := strings.NewReader("common=Winecup")
	if w := s.api(http.MethodPut, "/api/v1/species/winecup", s.key, form, "application/x-www-form-urlencoded"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a form: %d", w.Code)
	}

	if _, err := s.species.BySlug(t.Context(), "winecup"); err == nil {
		t.Error("a refused plant was kept")
	}
}

// noisy is an invented photo, never a real one (CLAUDE.md), over 64 KB.
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

func (s *site) upload(slug string, fields map[string]string, photo []byte) *httptest.ResponseRecorder {
	s.t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}

	if photo != nil {
		part, _ := mw.CreateFormFile("photo", "leaf.jpg")
		_, _ = part.Write(photo)
	}

	_ = mw.Close()

	return s.api(http.MethodPost, "/api/v1/species/"+slug+"/photos", s.key, &body, mw.FormDataContentType())
}

// A photo is corrected after it was added: only what is sent changes, a
// checked photo loses its check for a steward to give again, and sending
// the same again changes nothing.
func TestAPhotoIsCorrectedAndLosesItsCheck(t *testing.T) {
	s := serve(t)
	s.put("winecup", winecup())

	if _, err := s.places.Create(t.Context(), placebus.Fields{Slug: "rain-garden", Name: types.Text{EN: "Rain garden"}}); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		Outcome      string           `json:"outcome"`
		Photo        apiapp.PhotoJSON `json:"photo"`
		CheckCleared bool             `json:"check_cleared"`
	}

	w := s.upload("winecup", map[string]string{"kind": "flower", "source": "ours", "taken_month": "4", "taken_year": "2026"}, noisy(t, 7))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	id := decode[answer](t, w).Photo.ID

	patch := func(body string) *httptest.ResponseRecorder {
		return s.api(http.MethodPatch, "/api/v1/photos/"+id, s.key, strings.NewReader(body), "application/json")
	}

	// A steward checks it.
	pid, _ := types.ParseID(id)
	p, _ := s.photos.ByID(t.Context(), pid)
	f := photobus.FieldsOf(p)
	f.Checked = true
	if _, err := s.photos.Update(t.Context(), pid, f); err != nil {
		t.Fatal(err)
	}

	// The same again: nothing changes, and it stays checked.
	if got := decode[answer](t, patch(`{"kind": "flower"}`)); got.Outcome != "unchanged" || !got.Photo.Checked || got.CheckCleared {
		t.Errorf("sent what is there: %+v", got)
	}

	// Really a leaf, at the rain garden: those change, the month stays, and
	// the check goes.
	got := decode[answer](t, patch(`{"kind": "leaf", "place": "rain-garden"}`))
	if got.Outcome != "updated" || got.Photo.Kind != "leaf" || got.Photo.Place != "rain-garden" || got.Photo.TakenMonth != 4 || got.Photo.TakenYear != 2026 ||
		got.Photo.Checked || !got.CheckCleared {
		t.Errorf("corrected: %+v", got)
	}

	// The place taken away again.
	if got := decode[answer](t, patch(`{"place": ""}`)); got.Photo.Place != "" || got.CheckCleared {
		t.Errorf("place taken away: %+v", got)
	}

	// The day, for the flowering record, with the month and year following
	// it; then a month alone, which says the day is not known.
	got = decode[answer](t, patch(`{"taken_on": "2026-05-02", "in_flower": true}`))
	if got.Photo.TakenOn != "2026-05-02" || got.Photo.TakenMonth != 5 || got.Photo.TakenYear != 2026 || !got.Photo.InFlower {
		t.Errorf("a day, in flower: %+v", got.Photo)
	}
	if got := decode[answer](t, patch(`{"taken_on": "2026-05-02"}`)); got.Outcome != "unchanged" {
		t.Errorf("the same day again: %+v", got)
	}
	got = decode[answer](t, patch(`{"taken_month": 6}`))
	if got.Photo.TakenOn != "" || got.Photo.TakenMonth != 6 || got.Photo.TakenYear != 2026 {
		t.Errorf("a month alone: %+v", got.Photo)
	}

	for name, tc := range map[string]struct {
		body, field string
		code        int
	}{
		"asked to be checked": {`{"checked": true}`, "checked", http.StatusUnprocessableEntity},
		"not a kind":          {`{"kind": "bark"}`, "kind", http.StatusUnprocessableEntity},
		"not a place":         {`{"place": "moon"}`, "place", http.StatusUnprocessableEntity},
		"not a month":         {`{"taken_month": 13}`, "taken_month", http.StatusUnprocessableEntity},
		"not a day":           {`{"taken_on": "May 2"}`, "taken_on", http.StatusUnprocessableEntity},
	} {
		w := patch(tc.body)
		if p := decode[problem](t, w); w.Code != tc.code || p.Error.Field != tc.field {
			t.Errorf("%s: %d %+v", name, w.Code, p.Error)
		}
	}

	for _, missing := range []string{"0123456789abcdef0123456789abcdef", "not-an-id"} {
		if w := s.api(http.MethodPatch, "/api/v1/photos/"+missing, s.key, strings.NewReader(`{}`), "application/json"); w.Code != http.StatusNotFound {
			t.Errorf("a photo that is not there (%s): %d", missing, w.Code)
		}
	}
}

func TestAPhotoArrivesUncheckedAndOnce(t *testing.T) {
	s := serve(t)
	s.put("winecup", winecup())

	if _, err := s.places.Create(t.Context(), placebus.Fields{Slug: "rain-garden", Name: types.Text{EN: "Rain garden"}}); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		Photo     apiapp.PhotoJSON `json:"photo"`
		Duplicate bool             `json:"duplicate"`
	}

	fields := map[string]string{
		"kind": "leaf", "source": "borrowed", "credit": "A. Botanist", "license": "CC BY-SA 4.0",
		"source_url": "https://commons.wikimedia.org/wiki/File:Invented.jpg", "taken_month": "4",
	}
	data := noisy(t, 1)

	w := s.upload("winecup", fields, data)
	first := decode[answer](t, w)

	if w.Code != http.StatusCreated || first.Photo.Checked || first.Duplicate || first.Photo.Kind != "leaf" || first.Photo.SHA256 == "" {
		t.Fatalf("the upload: %d %+v", w.Code, first)
	}

	if first.Photo.FullURL != base+"/photos/"+first.Photo.ID+"/full.jpg" {
		t.Errorf("its full picture is at %q", first.Photo.FullURL)
	}

	w = s.upload("winecup", fields, data)
	again := decode[answer](t, w)

	if w.Code != http.StatusOK || !again.Duplicate || again.Photo.ID != first.Photo.ID {
		t.Errorf("sent again: %d %+v", w.Code, again)
	}

	// It is on the plant, unchecked, and not on the card.
	one := decode[struct {
		Species apiapp.SpeciesJSON `json:"species"`
	}](t, s.api(http.MethodGet, "/api/v1/species/winecup", s.key, nil, ""))

	if len(one.Species.Photos) != 1 || one.Species.Photos[0].Checked {
		t.Errorf("the plant's photos: %+v", one.Species.Photos)
	}

	card := httptest.NewRecorder()
	s.h.ServeHTTP(card, httptest.NewRequest(http.MethodGet, "/plants/winecup?view=weeding", nil))
	if strings.Contains(card.Body.String(), first.Photo.ID) {
		t.Error("an unchecked imported photo is on the card")
	}

	// Ours, at a place, by its slug.
	ours := map[string]string{"kind": "flower", "source": "ours", "place": "rain-garden"}
	if a := decode[answer](t, s.upload("winecup", ours, noisy(t, 2))); a.Photo.Place != "rain-garden" {
		t.Errorf("our photo: %+v", a.Photo)
	}

	// Ours, off the property, by name or not.
	park := map[string]string{"kind": "leaf", "source": "ours", "taken_where": "Pedernales Falls State Park"}
	if a := decode[answer](t, s.upload("winecup", park, noisy(t, 4))); !a.Photo.Elsewhere || a.Photo.TakenWhere != "Pedernales Falls State Park" || a.Photo.Place != "" {
		t.Errorf("our photo from a park: %+v", a.Photo)
	}

	unnamed := map[string]string{"kind": "mature", "source": "ours", "elsewhere": "true"}
	if a := decode[answer](t, s.upload("winecup", unnamed, noisy(t, 5))); !a.Photo.Elsewhere || a.Photo.TakenWhere != "" {
		t.Errorf("our photo from somewhere unnamed: %+v", a.Photo)
	}

	for name, tc := range map[string]struct {
		fields map[string]string
		photo  []byte
		field  string
	}{
		"asked to be checked": {map[string]string{"kind": "leaf", "source": "ours", "checked": "yes"}, noisy(t, 3), "checked"},
		"a place not known":   {map[string]string{"kind": "leaf", "source": "ours", "place": "moon"}, noisy(t, 3), "place"},
		"here and elsewhere":  {map[string]string{"kind": "leaf", "source": "ours", "place": "rain-garden", "taken_where": "A park"}, noisy(t, 3), "taken_where"},
		"here, and not":       {map[string]string{"kind": "leaf", "source": "ours", "place": "rain-garden", "elsewhere": "true"}, noisy(t, 3), "taken_where"},
		"a long where":        {map[string]string{"kind": "leaf", "source": "ours", "taken_where": strings.Repeat("a", 101)}, noisy(t, 3), "taken_where"},
		"no photo":            {map[string]string{"kind": "leaf", "source": "ours"}, nil, "photo"},
		"no licence":          {map[string]string{"kind": "leaf", "source": "borrowed", "credit": "A", "source_url": "https://x.org/a"}, noisy(t, 3), "license"},
		"not a photo":         {map[string]string{"kind": "leaf", "source": "ours"}, []byte("a text file"), "photo"},
	} {
		w := s.upload("winecup", tc.fields, tc.photo)
		if p := decode[problem](t, w); w.Code != http.StatusUnprocessableEntity || p.Error.Field != tc.field {
			t.Errorf("%s: %d %+v", name, w.Code, p.Error)
		}
	}

	if w := s.upload("not-a-plant", fields, data); w.Code != http.StatusNotFound {
		t.Errorf("a photo of a plant that is not there: %d", w.Code)
	}
}

// A plant is listed at a place the way a plant is added: created, then
// unchanged when sent again, then updated -- to pull, too, as the stewards
// decided for tree of heaven -- and taken off again.
func TestAPlantIsListedAtAPlaceAndTakenOff(t *testing.T) {
	s := serve(t)

	bed, err := s.places.Create(t.Context(), placebus.Fields{Slug: "skinny-bed", Name: types.Text{EN: "Skinny bed"}})
	if err != nil {
		t.Fatal(err)
	}

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	list := func(place, species string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)

		return s.api(http.MethodPut, "/api/v1/places/"+place+"/plants/"+species, s.key, bytes.NewReader(b), "application/json")
	}

	type answer struct {
		Outcome string             `json:"outcome"`
		Listing apiapp.ListingJSON `json:"listing"`
	}

	planted := map[string]any{"action": "protect", "planned": true, "note": map[string]string{"en": "6 plants, at the shady end"}}

	w := list("skinny-bed", "winecup", planted)
	if got := decode[answer](t, w); w.Code != http.StatusCreated || got.Outcome != "created" || !got.Listing.Planned || got.Listing.Action != "protect" {
		t.Fatalf("created: %d %+v", w.Code, got)
	}

	if w := list("skinny-bed", "winecup", planted); w.Code != http.StatusOK || decode[answer](t, w).Outcome != "unchanged" {
		t.Errorf("sent again: %d %s", w.Code, w.Body.String())
	}

	planted["note"] = map[string]string{"en": "4 plants, at the shady end"}
	if w := list("skinny-bed", "winecup", planted); w.Code != http.StatusOK || decode[answer](t, w).Outcome != "updated" {
		t.Errorf("a changed note: %d %s", w.Code, w.Body.String())
	}

	// A plant still to plant is never one to pull: the rules say so, and
	// the listing is left as it was.
	w = list("skinny-bed", "winecup", map[string]any{"action": "pull", "planned": true})
	if p := decode[problem](t, w); w.Code != http.StatusUnprocessableEntity || p.Error.Field != "action" || !strings.Contains(p.Error.Problem, "not one to pull") {
		t.Errorf("pull what is being planted: %d %+v", w.Code, p)
	}

	// Pull, with why.
	pull := map[string]any{"action": "pull", "note": map[string]string{"en": "Invasive: take it out, root and all."}}
	if got := decode[answer](t, list("skinny-bed", "winecup", pull)); got.Outcome != "updated" || got.Listing.Action != "pull" || got.Listing.Planned {
		t.Errorf("pull: %+v", got)
	}

	if w := list("skinny-bed", "winecup", map[string]any{"action": "weed"}); w.Code != http.StatusUnprocessableEntity || decode[problem](t, w).Error.Field != "action" {
		t.Errorf("an action that is not one: %d", w.Code)
	}

	// A steward's pull may be turned into protect.
	sp, _ := s.species.BySlug(t.Context(), "winecup")
	if _, err := s.listings.Set(t.Context(), bed.ID, sp.ID, listingbus.Fields{Action: listingbus.Pull}); err != nil {
		t.Fatal(err)
	}

	if w := list("skinny-bed", "winecup", planted); w.Code != http.StatusOK || decode[answer](t, w).Outcome != "updated" {
		t.Errorf("protecting what a steward marked to pull: %d %s", w.Code, w.Body.String())
	}

	// Neither the place nor the plant is made by listing.
	if w := list("no-such-place", "winecup", planted); w.Code != http.StatusNotFound || decode[problem](t, w).Error.Field != "slug" {
		t.Errorf("an unknown place: %d", w.Code)
	}

	if w := list("skinny-bed", "no-such-plant", planted); w.Code != http.StatusNotFound || decode[problem](t, w).Error.Field != "species" {
		t.Errorf("an unknown plant: %d", w.Code)
	}

	// And it reads back.
	type listed struct {
		Place  string               `json:"place"`
		Plants []apiapp.ListingJSON `json:"plants"`
	}

	got := decode[listed](t, s.api(http.MethodGet, "/api/v1/places/skinny-bed/plants", s.key, nil, ""))
	if got.Place != "skinny-bed" || len(got.Plants) != 1 || got.Plants[0].Species != "winecup" || got.Plants[0].Note.EN != "4 plants, at the shady end" ||
		got.Plants[0].CardURL != base+"/plants/winecup" {
		t.Errorf("read back: %+v", got)
	}

	// Taken off: removed, then nothing to do; the plant itself stays.
	unlist := func() string {
		w := s.api(http.MethodDelete, "/api/v1/places/skinny-bed/plants/winecup", s.key, nil, "")
		if w.Code != http.StatusOK {
			t.Fatalf("taking it off: %d %s", w.Code, w.Body.String())
		}

		return decode[answer](t, w).Outcome
	}
	if first, again := unlist(), unlist(); first != "removed" || again != "unchanged" {
		t.Errorf("taken off: %q, then %q", first, again)
	}
	if got := decode[listed](t, s.api(http.MethodGet, "/api/v1/places/skinny-bed/plants", s.key, nil, "")); len(got.Plants) != 0 {
		t.Errorf("still listed: %+v", got.Plants)
	}
	if _, err := s.species.BySlug(t.Context(), "winecup"); err != nil {
		t.Errorf("the plant went with its listing: %v", err)
	}
	if w := s.api(http.MethodDelete, "/api/v1/places/skinny-bed/plants/winecup", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("taking it off with no key: %d", w.Code)
	}
}

// A place is added, read back as its card describes it, changed, put on the
// map and taken off it, the way a plant is: created, then unchanged when sent
// again, then updated. Its slug never changes.
func TestAPlaceIsAddedChangedAndPutOnTheMap(t *testing.T) {
	s := serve(t)

	put := func(slug string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)

		return s.api(http.MethodPut, "/api/v1/places/"+slug, s.key, bytes.NewReader(b), "application/json")
	}

	type answer struct {
		Outcome string           `json:"outcome"`
		Place   apiapp.PlaceJSON `json:"place"`
	}

	garden := map[string]any{
		"name":       map[string]string{"en": "Rain garden", "es": "Jardín de lluvia"},
		"purpose":    map[string]string{"en": "The backdrop of the gathering space."},
		"conditions": map[string]string{"en": "Part shade; wet where the pipes come in."},
		"sort":       10,
	}

	w := put("rain-garden", garden)
	if got := decode[answer](t, w); w.Code != http.StatusCreated || got.Outcome != "created" || got.Place.Name.ES != "Jardín de lluvia" || got.Place.Spot != nil {
		t.Fatalf("created: %d %+v", w.Code, got)
	}

	if got := decode[answer](t, put("rain-garden", garden)); got.Outcome != "unchanged" {
		t.Errorf("sent again: %+v", got)
	}

	// A band inside it.
	band := map[string]any{"name": map[string]string{"en": "Inflow band"}, "parent": "rain-garden", "conditions": map[string]string{"en": "Wettest."}}
	if got := decode[answer](t, put("inflow", band)); got.Outcome != "created" || got.Place.Parent != "rain-garden" {
		t.Errorf("a band: %+v", got)
	}

	garden["photo_point"] = map[string]string{"en": "From the fire-pit bench, facing the wall."}
	if got := decode[answer](t, put("rain-garden", garden)); got.Outcome != "updated" || got.Place.PhotoPoint.EN == "" || got.Place.Purpose.EN == "" {
		t.Errorf("changed: %+v", got)
	}

	// On the map, read back, and off it again; a band is never on it.
	spot := func(slug, method, body string) *httptest.ResponseRecorder {
		var b io.Reader
		if body != "" {
			b = strings.NewReader(body)
		}

		return s.api(method, "/api/v1/places/"+slug+"/spot", s.key, b, "application/json")
	}

	if w := spot("rain-garden", http.MethodPut, `{"x": 410, "y": 220}`); w.Code != http.StatusOK {
		t.Errorf("on the map: %d %s", w.Code, w.Body.String())
	}

	type one struct {
		Place  apiapp.PlaceJSON `json:"place"`
		Inside []string         `json:"inside"`
	}
	got := decode[one](t, s.api(http.MethodGet, "/api/v1/places/rain-garden", s.key, nil, ""))
	if got.Place.Spot == nil || *got.Place.Spot != (apiapp.SpotJSON{X: 410, Y: 220}) || len(got.Inside) != 1 || got.Inside[0] != "inflow" ||
		got.Place.Conditions.EN != "Part shade; wet where the pipes come in." || got.Place.CardURL != base+"/places/rain-garden" {
		t.Errorf("read back: %+v", got)
	}

	type list struct {
		Places []apiapp.PlaceJSON `json:"places"`
		Map    struct{ Width, Height int }
	}
	all := decode[list](t, s.api(http.MethodGet, "/api/v1/places", s.key, nil, ""))
	if len(all.Places) != 2 || all.Map.Width == 0 || all.Map.Height == 0 {
		t.Errorf("the list: %+v", all)
	}

	for name, tc := range map[string]struct {
		w     *httptest.ResponseRecorder
		field string
	}{
		"off the edge":       {spot("rain-garden", http.MethodPut, `{"x": 9000, "y": 1}`), "spot"},
		"half a spot":        {spot("rain-garden", http.MethodPut, `{"x": 1}`), "spot"},
		"a band on the map":  {spot("inflow", http.MethodPut, `{"x": 1, "y": 1}`), "spot"},
		"no name":            {put("nameless", map[string]any{"purpose": map[string]string{"en": "x"}}), "name"},
		"a parent not there": {put("bed", map[string]any{"name": map[string]string{"en": "Bed"}, "parent": "moon"}), "parent"},
		"another slug":       {put("rain-garden", map[string]any{"slug": "rain-bed", "name": map[string]string{"en": "R"}}), "slug"},
		"a station not one":  {put("bed", map[string]any{"name": map[string]string{"en": "Bed"}, "trail_anchor": "moon"}), "trail_anchor"},
	} {
		if p := decode[problem](t, tc.w); tc.w.Code != http.StatusUnprocessableEntity || p.Error.Field != tc.field {
			t.Errorf("%s: %d %+v", name, tc.w.Code, p.Error)
		}
	}

	if w := spot("rain-garden", http.MethodDelete, ""); w.Code != http.StatusOK || decode[one](t, w).Place.Spot != nil {
		t.Errorf("off the map: %d %s", w.Code, w.Body.String())
	}

	if w := put("rain-garden", garden); w.Code != http.StatusOK {
		t.Errorf("a place sent again after its spot changed: %d", w.Code)
	}

	if w := s.api(http.MethodPut, "/api/v1/places/x", "", strings.NewReader(`{}`), "application/json"); w.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", w.Code)
	}
}

// The flowering record reads back from the photos: first and last in flower
// and first seen, each with where and whether it is checked.
func TestThePlantsFloweringRecordReadsFromItsPhotos(t *testing.T) {
	s := serve(t)
	s.put("winecup", winecup())

	for i, f := range []map[string]string{
		{"kind": "leaf", "source": "ours", "taken_on": "2026-03-12"},
		{"kind": "flower", "source": "ours", "taken_on": "2026-04-03"},
		{"kind": "mature", "source": "ours", "taken_on": "2026-05-20", "in_flower": "true", "taken_where": "Pedernales Falls State Park"},
		{"kind": "fruit", "source": "ours", "taken_on": "2026-08-12"},
	} {
		if w := s.upload("winecup", f, noisy(t, uint64(40+i))); w.Code != http.StatusCreated {
			t.Fatalf("upload %d: %d %s", i, w.Code, w.Body.String())
		}
	}

	type record struct {
		Species string              `json:"species"`
		Years   []apiapp.SeasonJSON `json:"years"`
	}

	got := decode[record](t, s.api(http.MethodGet, "/api/v1/species/winecup/flowering", s.key, nil, ""))
	if got.Species != "winecup" || len(got.Years) != 1 {
		t.Fatalf("the record: %+v", got)
	}

	y := got.Years[0]
	if y.Year != 2026 || y.Seen != 4 || y.InFlower != 2 || y.InFruit != 1 ||
		y.FirstFruit == nil || y.FirstFruit.TakenOn != "2026-08-12" || y.LastFruit == nil ||
		y.FirstSeen == nil || y.FirstSeen.TakenOn != "2026-03-12" ||
		y.FirstFlower == nil || y.FirstFlower.TakenOn != "2026-04-03" || y.FirstFlower.Checked ||
		y.LastFlower == nil || y.LastFlower.TakenOn != "2026-05-20" || !y.LastFlower.Elsewhere || y.LastFlower.TakenWhere != "Pedernales Falls State Park" {
		t.Errorf("2026: %+v first %+v last %+v", y, y.FirstFlower, y.LastFlower)
	}

	if w := s.api(http.MethodGet, "/api/v1/species/nothing/flowering", s.key, nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("a plant not there: %d", w.Code)
	}
}

// Every endpoint a chat can use is a tool on /mcp, by a name of its own, and
// its arguments can be told apart: the path's parameters and the body's
// fields are one flat set there (mcpapp).
func TestEveryEndpointIsATool(t *testing.T) {
	s := serve(t)

	idx := decode[apiapp.Index](t, s.api(http.MethodGet, "/api/v1", "", nil, ""))
	seen := map[string]bool{}

	for _, e := range idx.Endpoints {
		offered := e.Path != apiapp.Prefix && e.Path != apiapp.Prefix+"/inbox/{id}/{file}" && (e.Body == nil || e.Body.Encoding == "json")

		switch {
		case offered && e.Tool == "":
			t.Errorf("%s %s has no tool name", e.Method, e.Path)
		case !offered && e.Tool != "":
			t.Errorf("%s %s is a tool, but cannot be one", e.Method, e.Path)
		case seen[e.Tool] && e.Tool != "":
			t.Errorf("two endpoints are %s", e.Tool)
		}

		seen[e.Tool] = true

		var fields []apiapp.Field
		fields = append(fields, e.Query...)
		if e.Body != nil {
			fields = append(fields, e.Body.Fields...)
		}

		for _, f := range fields {
			if strings.Contains(e.Path, "{"+f.Name+"}") {
				t.Errorf("%s %s has a field and a path parameter both called %s", e.Method, e.Path, f.Name)
			}
		}
	}
}
