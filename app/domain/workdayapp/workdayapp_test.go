package workdayapp_test

import (
	"database/sql"
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
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type site struct {
	t      *testing.T
	h      http.Handler
	days   *workdaybus.Business
	cookie *http.Cookie
}

// Thursday 1 October 2026, nine in the morning in Austin.
var now = time.Date(2026, 10, 1, 9, 0, 0, 0, types.Garden)

// Through the muxer, signed in as a steward, with the work-day rules on a
// fixed clock.
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

	log := slog.New(slog.DiscardHandler)
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)
	s := &site{t: t, days: workdaybus.NewBusiness(workdaydb.NewStore(db), func() time.Time { return now })}

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:   placebus.NewBusiness(placedb.NewStore(db), nil),
		Species:  speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil),
		Photos:   photos(t, db),
		Users:    users,
		Workdays: s.days,
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

func day(date, starts, ends, title string) url.Values {
	return url.Values{"date": {date}, "starts": {starts}, "ends": {ends}, "title_en": {title}}
}

// A steward schedules a day, and it is on the home page at once, at the
// hours they typed in Austin time.
func TestAStewardSchedulesADayAndTheHomePageShowsIt(t *testing.T) {
	s := serve(t)

	f := day("2026-10-10", "08:00", "11:30", "Planting the rain garden")
	f.Set("details_en", "Meet at the fire pit.")

	w := s.do(http.MethodPost, "/steward/days", f, true)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/days?done=added" {
		t.Fatalf("adding a day: %d %s\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}

	up, _ := s.days.Upcoming(t.Context())
	if len(up) != 1 || !up[0].Starts.Equal(time.Date(2026, 10, 10, 8, 0, 0, 0, types.Garden)) || !up[0].Ends.Equal(time.Date(2026, 10, 10, 11, 30, 0, 0, types.Garden)) {
		t.Fatalf("stored %+v, want 8:00–11:30 on the 10th in Austin", up)
	}

	list := s.do(http.MethodGet, "/steward/days?done=added", nil, true).Body.String()
	for _, want := range []string{"Day added.", "Saturday, October 10", "8:00–11:30 am", "Planting the rain garden", "/steward/days/" + up[0].ID.String() + "/edit"} {
		if !strings.Contains(list, want) {
			t.Errorf("the stewards' list has no %q", want)
		}
	}

	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	for _, want := range []string{"Saturday, October 10", "8:00–11:30 am", "Planting the rain garden", "Meet at the fire pit."} {
		if !strings.Contains(home, want) {
			t.Errorf("the home page has no %q", want)
		}
	}

	// The edit form gives back what was saved, in the inputs' own formats.
	form := s.do(http.MethodGet, "/steward/days/"+up[0].ID.String()+"/edit", nil, true).Body.String()
	for _, want := range []string{`value="2026-10-10"`, `value="08:00"`, `value="11:30"`, `value="Planting the rain garden"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the edit form has no %s", want)
		}
	}
}

func TestADayIsEditedAndRemoved(t *testing.T) {
	s := serve(t)

	d, err := s.days.Create(t.Context(), workdaybus.Fields{
		Starts: time.Date(2026, 10, 10, 8, 0, 0, 0, types.Garden), Ends: time.Date(2026, 10, 10, 11, 0, 0, 0, types.Garden),
		Title: types.Text{EN: "Weeding"},
	})
	if err != nil {
		t.Fatal(err)
	}

	path := "/steward/days/" + d.ID.String()

	w := s.do(http.MethodPost, path, day("2026-10-17", "13:00", "16:00", "Weeding the skinny bed"), true)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("saving: %d\n%s", w.Code, w.Body)
	}

	got, _ := s.days.ByID(t.Context(), d.ID)
	if got.Title.EN != "Weeding the skinny bed" || got.Starts.In(types.Garden).Hour() != 13 || got.Starts.In(types.Garden).Day() != 17 {
		t.Errorf("after the edit: %+v", got)
	}

	// Remove asks for the box.
	if w := s.do(http.MethodPost, path+"/delete", url.Values{}, true); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Tick the box") {
		t.Errorf("removing without the box: %d", w.Code)
	}

	if w := s.do(http.MethodPost, path+"/delete", url.Values{"confirm": {"yes"}}, true); w.Code != http.StatusSeeOther {
		t.Errorf("removing: %d", w.Code)
	}

	if up, _ := s.days.Upcoming(t.Context()); len(up) != 0 {
		t.Errorf("still upcoming after removal: %v", up)
	}

	if w := s.do(http.MethodGet, path+"/edit", nil, true); w.Code != http.StatusNotFound {
		t.Errorf("editing a removed day: %d, want 404", w.Code)
	}
}

// A refusal is a sentence beside the input, and the form holds what was
// typed.
func TestAMistakeIsSaidBesideItsInput(t *testing.T) {
	s := serve(t)

	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"no date":        {day("", "08:00", "11:00", "Mulching"), "Choose the date of the day."},
		"ends first":     {day("2026-10-10", "11:00", "08:00", "Mulching"), "The end time is before the start. Check am and pm."},
		"already over":   {day("2026-09-26", "08:00", "11:00", "Mulching"), "That day is already over. Check the date."},
		"no title":       {day("2026-10-10", "08:00", "11:00", ""), "Say in a few words what you will work on."},
		"a time garbled": {day("2026-10-10", "8 o'clock", "11:00", "Mulching"), "Write the start time as hours and minutes"},
	} {
		w := s.do(http.MethodPost, "/steward/days", tc.form, true)
		body := w.Body.String()

		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, tc.want) || !strings.Contains(body, "Nothing was saved yet.") {
			t.Errorf("%s: %d, want 422 saying %q", name, w.Code, tc.want)
		}

		if title := tc.form.Get("title_en"); title != "" && !strings.Contains(body, `value="`+title+`"`) {
			t.Errorf("%s: the form lost what was typed", name)
		}
	}

	if up, _ := s.days.Upcoming(t.Context()); len(up) != 0 {
		t.Errorf("a refused day was saved: %v", up)
	}
}

// The screens are a steward's: signed out, a page sends you to sign in, a
// form is refused, and nothing is saved.
func TestTheScreensNeedSignIn(t *testing.T) {
	s := serve(t)

	for _, path := range []string{"/steward/days", "/steward/days/new"} {
		if w := s.do(http.MethodGet, path, nil, false); w.Code != http.StatusSeeOther {
			t.Errorf("GET %s signed out: %d", path, w.Code)
		}
	}

	if w := s.do(http.MethodPost, "/steward/days", day("2026-10-10", "08:00", "11:00", "x"), false); w.Code != http.StatusForbidden {
		t.Errorf("POST signed out: %d, want 403", w.Code)
	}

	if up, _ := s.days.Upcoming(t.Context()); len(up) != 0 {
		t.Error("a day was added signed out")
	}
}

func photos(t *testing.T, db *sql.DB) *photobus.Business {
	t.Helper()

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	return photobus.NewBusiness(photodb.NewStore(db), files, nil)
}
