package placeapp_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
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
	t      *testing.T
	h      http.Handler
	places *placebus.Business
	cookie *http.Cookie
}

// Through the muxer, signed in as a steward, so every request passes the same
// checks it would in production.
func serve(t *testing.T) *site {
	t.Helper()

	return serveAt(t, "https://stewards.example.invalid")
}

// serveAt is serve with a chosen base_url; "" is sign-in off, as on a server
// whose config predates it.
func serveAt(t *testing.T, baseURL string) *site {
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
		func() error { return userdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	s := &site{t: t, places: placebus.NewBusiness(placedb.NewStore(db), nil)}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: s.places, Users: users,
		Species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		BaseURL: baseURL, Mail: &mail.Recorder{},
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

	return s
}

func (s *site) do(method, path string, form url.Values, signedIn bool) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if signedIn {
		r.AddCookie(s.cookie)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

// getAs reads a page signed out, in a language.
func (s *site) getAs(path, lang string) *httptest.ResponseRecorder {
	s.t.Helper()

	r := httptest.NewRequest(http.MethodGet, path, nil)
	if lang != "" {
		r.Header.Set("Accept-Language", lang)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func (s *site) get(path string) *httptest.ResponseRecorder {
	return s.do(http.MethodGet, path, nil, true)
}

func (s *site) post(path string, form url.Values) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, path, form, true)
}

func rainGarden() url.Values {
	return url.Values{
		"slug":          {"rain-garden"},
		"name_en":       {"Rain garden"},
		"name_es":       {"Jardín de lluvia"},
		"purpose_en":    {"The backdrop of the gathering space."},
		"conditions_en": {"Full sun, wet after storms."},
		"sort":          {"10"},
	}
}

func (s *site) place(slug string) placebus.Place {
	s.t.Helper()

	p, err := s.places.BySlug(s.t.Context(), slug)
	if err != nil {
		s.t.Fatalf("%s: %v", slug, err)
	}

	return p
}

func TestNobodySignedOutReachesTheStewardScreens(t *testing.T) {
	s := serve(t)

	w := s.do(http.MethodGet, "/steward/places/new", nil, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/sign-in?next=%2Fsteward%2Fplaces%2Fnew" {
		t.Errorf("a signed-out GET: %d to %q", w.Code, w.Header().Get("Location"))
	}

	if w := s.do(http.MethodPost, "/steward/places", rainGarden(), false); w.Code != http.StatusForbidden {
		t.Errorf("a signed-out POST: %d, want 403", w.Code)
	}

	if all, _ := s.places.All(t.Context()); len(all) != 0 {
		t.Error("a signed-out POST made a place")
	}
}

// The pilot, as a steward would enter it: the rain garden, then its bands.
func TestAStewardAddsTheRainGardenAndItsBands(t *testing.T) {
	s := serve(t)

	if w := s.post("/steward/places", rainGarden()); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward?done=added" {
		t.Fatalf("adding: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}

	garden := s.place("rain-garden")
	if garden.Name.ES != "Jardín de lluvia" || garden.Sort != 10 || garden.Conditions.EN != "Full sun, wet after storms." {
		t.Errorf("saved as %+v", garden)
	}

	// The link on the list preselects the parent.
	form := s.get("/steward/places/new?parent=" + garden.ID.String()).Body.String()
	if !strings.Contains(form, `<option value="`+garden.ID.String()+`" selected>`) {
		t.Error("adding a smaller place from the rain garden's row does not preselect it")
	}

	for i, band := range []string{"inflow", "middle", "wall-edge"} {
		w := s.post("/steward/places", url.Values{
			"slug": {"rain-garden-" + band}, "name_en": {strings.ToUpper(band[:1]) + band[1:] + " band"},
			"parent": {garden.ID.String()}, "sort": {string(rune('1' + i))},
		})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("adding the %s band: %d\n%s", band, w.Code, w.Body)
		}
	}

	list := s.get("/steward?done=added").Body.String()
	for _, want := range []string{"Place added.", "Rain garden", "Inflow band", "Middle band", "Wall-edge band", "/places/rain-garden"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not show %q", want)
		}
	}

	// The bands are listed under the garden, in their order.
	if i, j, k := strings.Index(list, "Inflow band"), strings.Index(list, "Middle band"), strings.Index(list, "Wall-edge band"); !(strings.Index(list, "Rain garden") < i && i < j && j < k) {
		t.Error("the bands are not listed under the garden in order")
	}

	// And the volunteers' home screen shows the garden, not its bands.
	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	if !strings.Contains(home, "Rain garden") || strings.Contains(home, "Inflow band") {
		t.Error("the home screen should list the garden and not its bands")
	}
}

// A refusal shows the sentence beside the input, and gives back everything
// that was typed so nothing has to be typed twice in the sun.
func TestARefusedSaveKeepsWhatWasTyped(t *testing.T) {
	s := serve(t)

	bad := rainGarden()
	bad.Set("slug", "Rain Garden!")

	w := s.post("/steward/places", bad)
	body := w.Body.String()

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", w.Code)
	}

	for _, want := range []string{
		`<p class="field-problem">Use only lower-case letters, digits and hyphens, such as rain-garden.</p>`,
		`value="Rain Garden!"`, `value="Jardín de lluvia"`, "Full sun, wet after storms.", `value="10"`,
		"Nothing was saved yet.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refused form does not contain %q", want)
		}
	}

	notANumber := rainGarden()
	notANumber.Set("sort", "first")

	if w := s.post("/steward/places", notANumber); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "whole number") {
		t.Errorf("a sort that is not a number: %d", w.Code)
	}

	if all, _ := s.places.All(t.Context()); len(all) != 0 {
		t.Errorf("%d places were saved from refused forms", len(all))
	}
}

func TestEditingKeepsTheAddress(t *testing.T) {
	s := serve(t)
	s.post("/steward/places", rainGarden())
	garden := s.place("rain-garden")

	form := s.get("/steward/places/" + garden.ID.String() + "/edit").Body.String()
	if !strings.Contains(form, "/places/rain-garden</strong>. It cannot change") || strings.Contains(form, `name="slug"`) {
		t.Error("the edit form offers to change the address")
	}

	changed := rainGarden()
	changed.Set("name_en", "The rain garden")
	changed.Set("slug", "somewhere-else")

	if w := s.post("/steward/places/"+garden.ID.String(), changed); w.Code != http.StatusSeeOther {
		t.Fatalf("saving: %d\n%s", w.Code, w.Body)
	}

	if got := s.place("rain-garden"); got.Name.EN != "The rain garden" {
		t.Errorf("after the edit: %+v", got)
	}
}

// A place with bands cannot itself go inside another, and the form says so
// instead of offering a list that would be refused.
func TestAPlaceWithBandsStandsOnItsOwn(t *testing.T) {
	s := serve(t)
	s.post("/steward/places", rainGarden())
	garden := s.place("rain-garden")
	s.post("/steward/places", url.Values{"slug": {"inflow"}, "name_en": {"Inflow band"}, "parent": {garden.ID.String()}})
	s.post("/steward/places", url.Values{"slug": {"switchbacks"}, "name_en": {"Switchbacks"}})

	form := s.get("/steward/places/" + garden.ID.String() + "/edit").Body.String()
	if strings.Contains(form, `<select id="parent"`) || !strings.Contains(form, "so it stands on its own") {
		t.Error("the garden, which has a band, is offered a parent")
	}

	// A band's own list offers top-level places only: not itself, not a band.
	band := s.place("inflow")
	bandForm := s.get("/steward/places/" + band.ID.String() + "/edit").Body.String()
	if strings.Contains(bandForm, `<option value="`+band.ID.String()+`"`) || !strings.Contains(bandForm, "Switchbacks") {
		t.Error("the band's parent list is wrong")
	}
}

func TestRemovingAPlaceNeedsTheBoxTicked(t *testing.T) {
	s := serve(t)
	s.post("/steward/places", rainGarden())
	garden := s.place("rain-garden")
	s.post("/steward/places", url.Values{"slug": {"inflow"}, "name_en": {"Inflow band"}, "parent": {garden.ID.String()}})
	band := s.place("inflow")

	if w := s.post("/steward/places/"+band.ID.String()+"/delete", url.Values{}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Tick the box") {
		t.Errorf("removing without the box: %d", w.Code)
	}

	// The garden still has its band, so placebus refuses, and says why.
	w := s.post("/steward/places/"+garden.ID.String()+"/delete", url.Values{"confirm": {"yes"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Rain garden has 1 smaller places inside it. Move or remove those first.") {
		t.Errorf("removing a place with a band: %d\n%s", w.Code, w.Body)
	}

	if w := s.post("/steward/places/"+band.ID.String()+"/delete", url.Values{"confirm": {"yes"}}); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward?done=removed" {
		t.Fatalf("removing the band: %d", w.Code)
	}

	if _, err := s.places.BySlug(t.Context(), "inflow"); err == nil {
		t.Error("the band is still there")
	}

	if w := s.get("/steward/places/" + band.ID.String() + "/edit"); w.Code != http.StatusNotFound {
		t.Errorf("editing a removed place: %d, want 404", w.Code)
	}
}

// The confirmation after a save is chosen by a word; anything else in the
// query shows none. (The language toggle's link carries the query along,
// escaped, so the words themselves may be in an href; a tag may not.)
func TestTheConfirmationIsNotAnEcho(t *testing.T) {
	s := serve(t)

	body := s.get("/steward?done=" + url.QueryEscape("<b>hello</b>")).Body.String()
	if strings.Contains(body, "<b>hello") || strings.Contains(body, `class="done"`) {
		t.Error("the query reached the page")
	}
}
