package translationapp_test

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
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/listing/stores/listingdb"
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

type site struct {
	t      *testing.T
	h      http.Handler
	memory *translationbus.Business
	places *placebus.Business
	cookie *http.Cookie
}

// Through the muxer, signed in as a steward, with the translation memory as
// main makes it, and two places with words Claude has translated -- all but
// the skinny bed's name, which waits.
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
		func() error { return translationdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	memory, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	places := placebus.NewBusiness(placedb.NewStore(db), memory, nil)
	species := speciesbus.NewBusiness(speciesdb.NewStore(db), memory, nil)
	listings := listingbus.NewBusiness(listingdb.NewStore(db), memory, nil)
	workdays := workdaybus.NewBusiness(workdaydb.NewStore(db), memory, nil)
	memory.ReadFrom(places, species, listings, workdays)

	s := &site{t: t, memory: memory, places: places}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: places, Species: species, Listings: listings, Workdays: workdays,
		Photos: photobus.NewBusiness(photodb.NewStore(db), files, nil),
		Users:  users, Translations: memory,
		BaseURL: "https://stewards.example.invalid", Mail: &mail.Recorder{},
	}); err != nil {
		t.Fatal(err)
	}

	for _, f := range []placebus.Fields{
		{Slug: "rain-garden", Name: types.Text{EN: "Rain garden"}, Conditions: types.Text{EN: "Full sun, dry by July"}},
		{Slug: "skinny-bed", Name: types.Text{EN: "Skinny bed"}, Purpose: types.Text{EN: "Plant {count} here"}},
	} {
		if _, err := places.Create(t.Context(), f); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := memory.Translate(t.Context(), translationbus.ByClaude, []translationbus.Upload{
		{Key: translationbus.Key("Rain garden"), From: types.English, Translated: "Jardín pluvial"},
		{Key: translationbus.Key("Full sun, dry by July"), From: types.English, Translated: "Pleno sol, seco en julio"},
		{Key: translationbus.Key("Plant {count} here"), From: types.English, Translated: "Planta {count} aquí"},
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

func (s *site) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(s.cookie)

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func contains(t *testing.T, page string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
}

// The list to check shows each translation under its original, each in its
// language, with where it is read; Looks right takes it off the list and
// comes back at the next one.
func TestAStewardChecksATranslation(t *testing.T) {
	s := serve(t)

	home := s.do(http.MethodGet, "/steward", nil).Body.String()
	contains(t, home, `href="/steward/translations"`, "3 to check")

	list := s.do(http.MethodGet, "/steward/translations", nil).Body.String()
	sun := translationbus.Key("Full sun, dry by July")
	contains(t, list,
		"3 not checked yet", "1 waiting for Claude",
		`<p class="translation-text" lang="en">Full sun, dry by July</p>`,
		`<p class="translation-text" lang="es">Pleno sol, seco en julio</p>`,
		"Spanish, by Claude", "Rain garden: its conditions, for a planter",
		`id="t-`+sun+`"`, "All 3 above look right",
	)

	rain := translationbus.Key("Rain garden")
	w := s.do(http.MethodPost, "/steward/translations/"+sun+"/check", url.Values{"show": {"check"}, "next": {rain}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/translations?done=checked#t-"+rain {
		t.Fatalf("checking: %d %s", w.Code, w.Header().Get("Location"))
	}

	if tr, _ := s.memory.ByKey(sun); !tr.Checked || tr.By != translationbus.ByClaude {
		t.Errorf("after checking: %+v", tr)
	}

	list = s.do(http.MethodGet, "/steward/translations?done=checked", nil).Body.String()
	if strings.Contains(list, `id="t-`+sun+`"`) {
		t.Error("a checked translation is still on the list to check")
	}

	contains(t, list, "Checked.", "2 not checked yet")
	contains(t, s.do(http.MethodGet, "/steward/translations?show=all", nil).Body.String(), `id="t-`+sun+`"`, `<span class="tag tag-ok">Checked</span>`)

	// All the rest, as the page listed them.
	w = s.do(http.MethodPost, "/steward/translations/check", url.Values{"key": {rain, translationbus.Key("Plant {count} here"), "not a key"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("checking all: %d", w.Code)
	}

	contains(t, s.do(http.MethodGet, "/steward/translations", nil).Body.String(), "Nothing to check")
}

// A steward's own translation is shown at once and checked; one that loses a
// placeholder is refused on its row with what they typed.
func TestAStewardChangesATranslation(t *testing.T) {
	s := serve(t)

	rain := translationbus.Key("Rain garden")
	w := s.do(http.MethodPost, "/steward/translations/"+rain, url.Values{"text": {"Jardín de lluvia"}, "show": {"check"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "done=saved") {
		t.Fatalf("saving: %d %s", w.Code, w.Header().Get("Location"))
	}

	p, err := s.places.BySlug(t.Context(), "rain-garden")
	if err != nil || p.Name.ES != "Jardín de lluvia" {
		t.Errorf("the place is called %+v, %v", p.Name, err)
	}

	blank := translationbus.Key("Plant {count} here")
	w = s.do(http.MethodPost, "/steward/translations/"+blank, url.Values{"text": {"Planta tres aquí"}, "show": {"check"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("losing a placeholder: %d", w.Code)
	}

	contains(t, w.Body.String(), "Keep each of {count} exactly as it is", ">Planta tres aquí</textarea>", `<details class="translation-more" open>`)
}

// Sent back, a translation waits for Claude with the steward's note, and
// stays on the screens meanwhile.
func TestAStewardSendsATranslationBack(t *testing.T) {
	s := serve(t)

	sun := translationbus.Key("Full sun, dry by July")

	if w := s.do(http.MethodPost, "/steward/translations/"+sun+"/back", url.Values{"note": {""}}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("an empty note: %d", w.Code)
	}

	w := s.do(http.MethodPost, "/steward/translations/"+sun+"/back", url.Values{"note": {"Dry from July on, not by July"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("sending back: %d", w.Code)
	}

	waiting, err := s.memory.Waiting(t.Context(), 10)
	if err != nil || waiting.Remaining != 2 || waiting.Pending[0].Note != "Dry from July on, not by July" {
		t.Errorf("waiting: %+v, %v", waiting, err)
	}

	contains(t, s.do(http.MethodGet, "/steward/translations?done=back", nil).Body.String(),
		"Sent back.", `<span class="tag tag-sun">Sent back</span>`, "Your note: “Dry from July on, not by July”", "2 waiting for Claude")

	if w := s.do(http.MethodPost, "/steward/translations/not-a-key/check", url.Values{}); w.Code != http.StatusNotFound {
		t.Errorf("a key that is not one: %d", w.Code)
	}
}
