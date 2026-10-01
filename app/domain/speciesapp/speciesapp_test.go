package speciesapp_test

import (
	"bytes"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	s := &site{
		t:       t,
		species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		places:  placebus.NewBusiness(placedb.NewStore(db), nil),
		photos:  photos(t, db),
	}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: s.places, Species: s.species, Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil), Photos: s.photos, Users: users,
		BaseURL: "https://stewards.example.invalid", Mail: &mail.Recorder{},
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

func (s *site) get(path string) *httptest.ResponseRecorder {
	return s.do(http.MethodGet, path, nil, true)
}

func (s *site) post(path string, form url.Values) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, path, form, true)
}

// Brazos penstemon as the planting guide has it: every kind of field the
// form has, including two runs of light and a source with no web address.
func penstemon() url.Values {
	return url.Values{
		"slug":         {"brazos-penstemon"},
		"common_en":    {"Brazos penstemon"},
		"scientific":   {"Penstemon tenuis"},
		"status":       {"native"},
		"flower_en":    {"Purple-pink"},
		"swatches":     {"#B0418F"},
		"bloom":        {"3", "4", "5"},
		"height_min":   {"24"},
		"height_max":   {"24"},
		"width_min":    {"24"},
		"light":        {"1", "2"},
		"water":        {"2", "4"},
		"note_en":      {"Poor drainage OK. Red winter leaves."},
		"source_label": {"Lady Bird Johnson Wildflower Center", "The Natural Gardener list, Sep 2026", ""},
		"source_url":   {"https://www.wildflower.org/plants/result.php?id_plant=PETE4", "", ""},
		"confirmed":    {"yes"},
	}
}

func TestSignedOutThereAreNoSpeciesScreens(t *testing.T) {
	s := serve(t)

	if w := s.do(http.MethodGet, "/steward/species", nil, false); w.Code != http.StatusSeeOther {
		t.Errorf("a signed-out GET: %d", w.Code)
	}

	if w := s.do(http.MethodPost, "/steward/species", penstemon(), false); w.Code != http.StatusForbidden {
		t.Errorf("a signed-out POST: %d", w.Code)
	}
}

// Everything the form sends arrives in the record.
func TestAStewardAddsAPlant(t *testing.T) {
	s := serve(t)

	if w := s.post("/steward/species", penstemon()); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/species?done=added" {
		t.Fatalf("adding: %d\n%s", w.Code, w.Body)
	}

	sp, err := s.species.BySlug(t.Context(), "brazos-penstemon")
	if err != nil {
		t.Fatal(err)
	}

	switch {
	case sp.Status != speciesbus.StatusNative || !sp.Confirmed:
		t.Errorf("status %q, confirmed %v", sp.Status, sp.Confirmed)
	case sp.Bloom != types.MonthsOf(time.March, time.April, time.May):
		t.Errorf("bloom %v", sp.Bloom)
	case sp.Height != (speciesbus.Size{Min: 24, Max: 24}) || sp.Width != (speciesbus.Size{Min: 24, Max: 24}):
		t.Errorf("size %+v × %+v", sp.Height, sp.Width)
	case sp.Light != speciesbus.FullSun|speciesbus.PartShade || sp.Water != speciesbus.Moist|speciesbus.Wet:
		t.Errorf("light %b, water %b", sp.Light, sp.Water)
	case len(sp.Sources) != 2 || sp.Sources[1].Label != "The Natural Gardener list, Sep 2026" || sp.Sources[1].URL != "":
		t.Errorf("sources %+v", sp.Sources)
	case len(sp.Swatches) != 1 || sp.Swatches[0] != "#b0418f":
		t.Errorf("swatches %v", sp.Swatches)
	}

	list := s.get("/steward/species?done=added").Body.String()
	for _, want := range []string{"Plant added.", "Brazos penstemon", "<i>Penstemon tenuis</i>", "ID confirmed", "Blooms Mar–May", `fill="#b0418f"`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not show %q", want)
		}
	}
}

// The edit form shows what was saved, so saving it again changes nothing.
func TestTheEditFormHoldsWhatWasSaved(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	sp, _ := s.species.BySlug(t.Context(), "brazos-penstemon")

	form := s.get("/steward/species/" + sp.ID.String() + "/edit").Body.String()

	for _, want := range []string{
		`value="Brazos penstemon"`, `value="Penstemon tenuis"`, `<option value="native" selected>`,
		`name="bloom" value="3" checked`, `name="bloom" value="5" checked`,
		`name="light" value="2" checked`, `name="confirmed" value="yes" checked`,
		`value="The Natural Gardener list, Sep 2026"`, "Source 4", "/plants/brazos-penstemon",
	} {
		if !strings.Contains(form, want) {
			t.Errorf("the edit form does not hold %q", want)
		}
	}

	if strings.Contains(form, `name="bloom" value="6" checked`) || strings.Contains(form, `name="slug"`) {
		t.Error("the edit form shows a month not saved, or offers the address")
	}
}

// The rule the plan puts first, seen from the form: ticking "confirmed" with
// nothing to confirm it against is refused, and nothing typed is lost.
func TestConfirmingNeedsASource(t *testing.T) {
	s := serve(t)

	f := penstemon()
	f.Set("source_label", "")
	f.Del("source_url")

	w := s.post("/steward/species", f)
	body := w.Body.String()

	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "An ID is confirmed by what it was checked against.") {
		t.Fatalf("confirming with no source: %d", w.Code)
	}

	for _, want := range []string{`value="Brazos penstemon"`, `name="bloom" value="4" checked`, "Poor drainage OK."} {
		if !strings.Contains(body, want) {
			t.Errorf("the refused form lost %q", want)
		}
	}

	if all, _ := s.species.All(t.Context()); len(all) != 0 {
		t.Error("a refused plant was saved")
	}
}

func TestASizeThatIsNotANumberIsRefusedBeforeSaving(t *testing.T) {
	s := serve(t)

	f := penstemon()
	f.Set("height_max", "2 ft")

	if w := s.post("/steward/species", f); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "whole inches") {
		t.Errorf("a size in words: %d", w.Code)
	}

	if all, _ := s.species.All(t.Context()); len(all) != 0 {
		t.Error("it was saved anyway")
	}
}

func TestRemovingAPlantNeedsTheBoxTicked(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	sp, _ := s.species.BySlug(t.Context(), "brazos-penstemon")

	if w := s.post("/steward/species/"+sp.ID.String()+"/delete", url.Values{}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("without the box: %d", w.Code)
	}

	if w := s.post("/steward/species/"+sp.ID.String()+"/delete", url.Values{"confirm": {"yes"}}); w.Header().Get("Location") != "/steward/species?done=removed" {
		t.Errorf("with the box: %d", w.Code)
	}

	if w := s.get("/steward/species/" + sp.ID.String() + "/edit"); w.Code != http.StatusNotFound {
		t.Errorf("editing a removed plant: %d", w.Code)
	}
}

// photos is the photo rules over this test's database and a directory of its
// own.
func photos(t *testing.T, db *sql.DB) *photobus.Business {
	t.Helper()

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	return photobus.NewBusiness(photodb.NewStore(db), files, nil)
}

// The list shows a photo beside each plant that has one, the flower before
// the grown plant even while neither is checked: the photos an import
// brings in all arrive unchecked, and the list is where a steward finds the
// plant to check them.
func TestTheListShowsEachPlantsFlower(t *testing.T) {
	s := serve(t)

	if w := s.post("/steward/species", penstemon()); w.Code != http.StatusSeeOther {
		t.Fatalf("adding a plant: %d", w.Code)
	}

	sp, err := s.species.BySlug(t.Context(), "brazos-penstemon")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.species.Create(t.Context(), speciesbus.Fields{Slug: "frogfruit", Common: types.Text{EN: "Frogfruit"}}); err != nil {
		t.Fatal(err)
	}

	add := func(k photobus.Kind, shade uint8) photobus.Photo {
		p, err := s.photos.Add(t.Context(), sp.ID, photobus.Fields{Kind: k, Source: photobus.Ours}, picture(t, shade))
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	mature := add(photobus.Mature, 40)
	flower := add(photobus.Flower, 200)

	body := s.get("/steward/species").Body.String()

	if !strings.Contains(body, `src="/photos/`+flower.ID.String()+`/small.jpg"`) {
		t.Error("the list does not show the plant's flower")
	}

	if strings.Contains(body, mature.ID.String()) {
		t.Error("the list shows the full-size photo, not the flower")
	}

	if n := strings.Count(body, `class="row-thumb"`); n != 1 {
		t.Errorf("%d pictures in the list, want one: frogfruit has no photo", n)
	}
}

// picture is an invented photo, never a real one (CLAUDE.md), in one shade
// so two of them are different files.
func picture(t *testing.T, shade uint8) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := range 300 {
		for x := range 400 {
			img.Set(x, y, color.RGBA{shade, uint8(x), uint8(y), 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}
