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

const base = "https://stewards.example.invalid"

type site struct {
	t        *testing.T
	h        http.Handler
	species  *speciesbus.Business
	places   *placebus.Business
	listings *listingbus.Business
	cookie   *http.Cookie
	key      string
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
		t:        t,
		species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		places:   placebus.NewBusiness(placedb.NewStore(db), nil),
		listings: listingbus.NewBusiness(listingdb.NewStore(db), nil),
	}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: s.places, Species: s.species, Users: users,
		Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		Photos:   photobus.NewBusiness(photodb.NewStore(db), files, nil),
		Listings: s.listings,
		BaseURL:  base, Mail: &mail.Recorder{},
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

	for name, tc := range map[string]struct {
		fields map[string]string
		photo  []byte
		field  string
	}{
		"asked to be checked": {map[string]string{"kind": "leaf", "source": "ours", "checked": "yes"}, noisy(t, 3), "checked"},
		"a place not known":   {map[string]string{"kind": "leaf", "source": "ours", "place": "moon"}, noisy(t, 3), "place"},
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
// unchanged when sent again, then updated. And never to pull: a listing is on
// the place card at once, with no box for a steward to tick first.
func TestAPlantIsListedAtAPlaceButNeverToPull(t *testing.T) {
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

	// Pull is refused with the reason, and the listing is left as it was.
	w = list("skinny-bed", "winecup", map[string]any{"action": "pull"})
	if p := decode[problem](t, w); w.Code != http.StatusUnprocessableEntity || p.Error.Field != "action" || !strings.Contains(p.Error.Problem, "Plants screen") {
		t.Errorf("pull: %d %+v", w.Code, p)
	}

	if w := list("skinny-bed", "winecup", map[string]any{"action": "weed"}); w.Code != http.StatusUnprocessableEntity || decode[problem](t, w).Error.Field != "action" {
		t.Errorf("an action that is not one: %d", w.Code)
	}

	// A steward's pull may be turned into protect: the safe direction.
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
}
