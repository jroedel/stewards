package homeapp_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Through the muxer, so the page is tested as served: with its middleware,
// its stylesheet and its language.
func server(t *testing.T) (http.Handler, *placebus.Business) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
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
			t.Fatalf("Init: %v", err)
		}
	}

	places := placebus.NewBusiness(placedb.NewStore(db), nil)

	h, err := muxer.New(muxer.Config{
		Log:      slog.New(slog.DiscardHandler),
		DB:       db,
		Expected: sqldb.Infrastructure,
		Places:   places,
		Species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil), Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil), Photos: photos(t, db),
		Users: userbus.NewBusiness(slog.New(slog.DiscardHandler), userdb.NewStore(db), nil),
	})
	if err != nil {
		t.Fatalf("muxer.New: %v", err)
	}

	return h, places
}

func get(t *testing.T, h http.Handler, path, accept string) (int, string) {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		r.Header.Set("Accept-Language", accept)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec.Code, rec.Body.String()
}

func TestWithNoPlacesTheHomeScreenSaysSo(t *testing.T) {
	h, _ := server(t)

	code, body := get(t, h, "/", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}

	for _, want := range []string{"Where are you working?", "No places have been added yet.", "https://schoenstatt-fathers.us/trail/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the home screen has no %q", want)
		}
	}
}

func TestTheHomeScreenListsPlacesWithoutTheirBands(t *testing.T) {
	h, places := server(t)

	garden, err := places.Create(t.Context(), placebus.Fields{
		Slug: "rain-garden", Name: types.Text{EN: "Rain garden", ES: "Jardín de lluvia"},
		Purpose: types.Text{EN: "The backdrop of the fire-pit benches"}, Sort: 2,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, f := range []placebus.Fields{
		{Slug: "fire-pit", Name: types.Text{EN: "Fire pit"}, Sort: 1},
		{Slug: "rain-garden-inflow", Name: types.Text{EN: "Inflow"}, ParentID: garden.ID},
	} {
		if _, err := places.Create(t.Context(), f); err != nil {
			t.Fatalf("Create %s: %v", f.Slug, err)
		}
	}

	_, body := get(t, h, "/", "")

	fire, rain := strings.Index(body, "Fire pit"), strings.Index(body, "Rain garden")
	if fire < 0 || rain < 0 || fire > rain {
		t.Errorf("want the fire pit then the rain garden, in the chosen order")
	}

	if strings.Contains(body, "Inflow") {
		t.Error("a band is listed as a place of its own")
	}

	if !strings.Contains(body, "The backdrop of the fire-pit benches") {
		t.Error("the place's purpose is not shown")
	}

	// In Spanish: the Spanish name where it is written, and the English
	// marked where it is not.
	_, es := get(t, h, "/", "es")
	if !strings.Contains(es, "Jardín de lluvia") {
		t.Error("the Spanish page does not use the Spanish name")
	}
	if !strings.Contains(es, `<span lang="en">Fire pit</span>`) {
		t.Error("an English-only name in the Spanish page is not marked lang=en")
	}
}

func TestAnUnknownAddressIsNotTheHomeScreen(t *testing.T) {
	h, _ := server(t)

	if code, _ := get(t, h, "/no-such-page", ""); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}

// Everything the page links to is served, and cacheable: the stylesheet by
// its hash, the fonts and the logo by their names.
func TestWhatThePageLinksToIsServed(t *testing.T) {
	h, _ := server(t)

	_, body := get(t, h, "/", "")

	start := strings.Index(body, `href="/static/app.`)
	if start < 0 {
		t.Fatal("the page links no stylesheet")
	}
	css := body[start+len(`href="`):]
	css = css[:strings.Index(css, `"`)]

	for _, path := range []string{css, "/static/fonts/inter-var.woff2", "/static/fonts/ubuntu-var.woff2", "/static/img/logo-horizontal.svg", "/static/img/isotype-512.png"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}

		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=31536000") {
			t.Errorf("%s: Cache-Control %q, want it cached", path, cc)
		}
	}

	if code, _ := get(t, h, "/static/fonts/nope.woff2", ""); code != http.StatusNotFound {
		t.Errorf("an unknown font: status %d", code)
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
