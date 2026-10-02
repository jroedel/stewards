package homeapp_test

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	"github.com/jroedel/stewards/foundation/sqldb"
)

// Through the muxer, so the page is tested as served: with its middleware,
// its stylesheet and its language.
func server(t *testing.T) (http.Handler, *placebus.Business) {
	t.Helper()

	h, places, _, _ := serverWithDays(t)

	return h, places
}

// serverWithDays is server with the work-day rules on a clock the test sets,
// starting at nine on Thursday 1 October 2026 in Austin.
func serverWithDays(t *testing.T) (http.Handler, *placebus.Business, *workdaybus.Business, *time.Time) {
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
		func() error { return workdaydb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	places := placebus.NewBusiness(placedb.NewStore(db), nil)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, types.Garden)
	days := workdaybus.NewBusiness(workdaydb.NewStore(db), func() time.Time { return clock })

	h, err := muxer.New(muxer.Config{
		Log:      slog.New(slog.DiscardHandler),
		DB:       db,
		Expected: sqldb.Infrastructure,
		Places:   places,
		Species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil), Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil), Photos: photos(t, db),
		Users:    userbus.NewBusiness(slog.New(slog.DiscardHandler), userdb.NewStore(db), nil),
		Workdays: days,
	})
	if err != nil {
		t.Fatalf("muxer.New: %v", err)
	}

	return h, places, days, &clock
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

func TestWithNoPlacesTheListSaysSo(t *testing.T) {
	h, _ := server(t)

	code, body := get(t, h, "/places", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}

	for _, want := range []string{"Where are you working?", "No places have been added yet."} {
		if !strings.Contains(body, want) {
			t.Errorf("the list of places has no %q", want)
		}
	}
}

func TestTheListShowsPlacesWithoutTheirBands(t *testing.T) {
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

	_, body := get(t, h, "/places", "")

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
	_, es := get(t, h, "/places", "es")
	if !strings.Contains(es, "Jardín de lluvia") {
		t.Error("the Spanish page does not use the Spanish name")
	}
	if !strings.Contains(es, `<span lang="en">Fire pit</span>`) {
		t.Error("an English-only name in the Spanish page is not marked lang=en")
	}
}

// The map has a numbered marker for each place on it, in list order, and the
// list carries the same numbers; a place not on the map yet is listed with
// none, and with no place on it the map is not drawn at all.
func TestTheMapNumbersThePlacesOnItAsTheListDoes(t *testing.T) {
	h, places := server(t)

	var made []placebus.Place
	for i, slug := range []string{"fire-pit", "st-joseph", "rain-garden"} {
		p, err := places.Create(t.Context(), placebus.Fields{Slug: slug, Name: types.Text{EN: slug}, Sort: i})
		if err != nil {
			t.Fatalf("Create %s: %v", slug, err)
		}
		made = append(made, p)
	}

	if _, body := get(t, h, "/places", ""); strings.Contains(body, "<svg") || strings.Contains(body, "row-pin") {
		t.Error("with nothing on the map, the map is drawn anyway")
	}

	// The fire pit and the rain garden on the map; St. Joseph not yet.
	for _, p := range []placebus.Place{made[0], made[2]} {
		if _, err := places.SetSpot(t.Context(), p.ID, &placebus.Spot{X: 600 - p.Sort, Y: 420}); err != nil {
			t.Fatal(err)
		}
	}

	_, body := get(t, h, "/places", "")

	for _, want := range []string{
		`<a href="/places/fire-pit" aria-label="1. fire-pit">`,
		`<a href="/places/rain-garden" aria-label="2. rain-garden">`,
		`<circle cx="598" cy="420" r="27"`,
		`href="/places/fire-pit"><span class="row-pin" aria-hidden="true">1</span>`,
		`href="/places/rain-garden"><span class="row-pin" aria-hidden="true">2</span>`,
		`href="/places/st-joseph"><div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the map and its list have no %s", want)
		}
	}

	if strings.Contains(body, `aria-label="3.`) || strings.Contains(body, `<a href="/places/st-joseph" aria-label`) {
		t.Error("a place that is not on the map has a marker")
	}
}

// What the QR code on the trail's signs leads to, before any day is on the
// calendar: the welcome, the way to the places, and the way to the prayers
// for a pilgrim who scanned the wrong sign.
func TestTheHomePageWelcomesANewcomer(t *testing.T) {
	h, _ := server(t)

	code, body := get(t, h, "/", "")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}

	for _, want := range []string{
		"Come help in the garden", "special skills", "Upcoming stewardship days",
		"No days are scheduled just now.", `href="/places"`, "https://schoenstatt-fathers.us/trail/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the home page has no %q", want)
		}
	}
}

func TestTheHomePageListsTheDaysComingUp(t *testing.T) {
	h, _, days, clock := serverWithDays(t)

	on := func(day, from, to int, title string) workdaybus.Fields {
		return workdaybus.Fields{
			Starts: time.Date(2026, 10, day, from, 0, 0, 0, types.Garden),
			Ends:   time.Date(2026, 10, day, to, 0, 0, 0, types.Garden),
			Title:  types.Text{EN: title},
		}
	}

	for _, f := range []workdaybus.Fields{on(24, 8, 11, "Mulching the paths"), on(10, 8, 11, "Planting the rain garden"), on(1, 8, 11, "Weeding the skinny bed")} {
		if _, err := days.Create(t.Context(), f); err != nil {
			t.Fatal(err)
		}
	}

	_, body := get(t, h, "/", "")

	today, tenth, later := strings.Index(body, "Weeding the skinny bed"), strings.Index(body, "Planting the rain garden"), strings.Index(body, "Mulching the paths")
	if today < 0 || !(today < tenth && tenth < later) {
		t.Errorf("want the days soonest first, today's at the top")
	}

	if !strings.Contains(body, "Saturday, October 10") || !strings.Contains(body, "8:00–11:00 am") {
		t.Error("a day's date and hours are not written out")
	}

	// At nine on the first the morning's work is under way.
	if !strings.Contains(body, "Happening now") || strings.Contains(body, "No days are scheduled") {
		t.Error("the day under way is not marked as happening now")
	}

	// Once it is over it leaves the page, and nothing is happening now.
	*clock = time.Date(2026, 10, 1, 12, 0, 0, 0, types.Garden)

	_, body = get(t, h, "/", "")
	if strings.Contains(body, "Weeding the skinny bed") || strings.Contains(body, "Happening now") {
		t.Error("a day that is over is still on the home page")
	}

	// In Spanish, the English is marked until a native speaker writes it.
	_, es := get(t, h, "/", "es")
	if !strings.Contains(es, `<span lang="en">Planting the rain garden</span>`) {
		t.Error("an English-only title on the Spanish page is not marked lang=en")
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
