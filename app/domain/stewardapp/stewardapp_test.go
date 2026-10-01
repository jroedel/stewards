package stewardapp_test

import (
	"errors"
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

const base = "https://stewards.example.invalid"

type site struct {
	t     *testing.T
	h     http.Handler
	users *userbus.Business
	mail  *mail.Recorder
	me    userbus.User
	mine  *http.Cookie
}

func serve(t *testing.T, withMail bool) *site {
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
	s := &site{t: t, users: userbus.NewBusiness(log, userdb.NewStore(db), nil), mail: &mail.Recorder{}}

	cfg := muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places: placebus.NewBusiness(placedb.NewStore(db), nil), Users: s.users,
		Species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		BaseURL: base,
	}
	if withMail {
		cfg.Mail = s.mail
	}

	if s.h, err = muxer.New(cfg); err != nil {
		t.Fatal(err)
	}

	s.me, s.mine = s.steward("steward@example.org")

	return s
}

// steward makes an account and signs it in, returning its session cookie.
func (s *site) steward(addr string) (userbus.User, *http.Cookie) {
	s.t.Helper()

	e, _ := types.ParseEmail(addr)

	u, err := s.users.Create(s.t.Context(), e, "")
	if err != nil {
		s.t.Fatal(err)
	}

	req, _ := s.users.RequestSignIn(s.t.Context(), e)

	_, value, err := s.users.SignIn(s.t.Context(), req.Secret)
	if err != nil {
		s.t.Fatal(err)
	}

	return u, &http.Cookie{Name: mid.SessionCookie, Value: value}
}

func (s *site) do(method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if cookie != nil {
		r.AddCookie(cookie)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func TestAStewardAddsAnotherWhoIsToldHowToSignIn(t *testing.T) {
	s := serve(t, true)

	w := s.do(http.MethodPost, "/steward/stewards", url.Values{"email": {"New@Example.org"}, "name": {"A New Steward"}}, s.mine)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/stewards?done=sent" {
		t.Fatalf("adding: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}

	sent, ok := s.mail.Last()
	if !ok || sent.To != "new@example.org" {
		t.Fatalf("the note: %+v", sent)
	}

	// The address of the sign-in page, and no credential of any kind.
	if !strings.Contains(sent.Text, base+"/sign-in\r\n") || strings.Contains(sent.Text, "/sign-in/link") || strings.Contains(sent.Text, "?t=") {
		t.Errorf("the note is not a plain pointer to sign-in:\n%s", sent.Text)
	}

	list := s.do(http.MethodGet, "/steward/stewards?done=sent", nil, s.mine).Body.String()
	for _, want := range []string{"A New Steward", "new@example.org", "We emailed them", `<span class="tag">You</span>`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not show %q", want)
		}
	}

	// And they can ask for a link.
	e, _ := types.ParseEmail("new@example.org")
	if req, _ := s.users.RequestSignIn(t.Context(), e); !req.Sendable() {
		t.Error("the new steward cannot ask for a link")
	}
}

func TestWithNoMailThePageSaysToTellThem(t *testing.T) {
	s := serve(t, false)

	w := s.do(http.MethodPost, "/steward/stewards", url.Values{"email": {"new@example.org"}}, s.mine)
	if w.Header().Get("Location") != "/steward/stewards?done=added" {
		t.Fatalf("adding with no mail: %q", w.Header().Get("Location"))
	}

	if !strings.Contains(s.do(http.MethodGet, "/steward/stewards?done=added", nil, s.mine).Body.String(), "Tell them to sign in at "+base+"/sign-in") {
		t.Error("the page does not say to tell them")
	}
}

func TestAnAddressIsAddedOnce(t *testing.T) {
	s := serve(t, true)

	w := s.do(http.MethodPost, "/steward/stewards", url.Values{"email": {"steward@example.org"}}, s.mine)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "A steward already has that address.") {
		t.Errorf("adding an address twice: %d", w.Code)
	}

	w = s.do(http.MethodPost, "/steward/stewards", url.Values{"email": {"not an address"}, "name": {"Kept"}}, s.mine)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `value="Kept"`) {
		t.Errorf("a bad address: %d, and the name should be kept", w.Code)
	}

	if len(s.mail.Sent) != 0 {
		t.Error("a refused add sent a note")
	}
}

func TestTurningAStewardOffSignsThemOut(t *testing.T) {
	s := serve(t, true)
	them, theirs := s.steward("other@example.org")

	w := s.do(http.MethodPost, "/steward/stewards/"+them.ID.String()+"/access", url.Values{"enabled": {"no"}}, s.mine)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/stewards?done=off" {
		t.Fatalf("turning off: %d %q", w.Code, w.Header().Get("Location"))
	}

	if w := s.do(http.MethodGet, "/steward", nil, theirs); w.Code != http.StatusSeeOther {
		t.Errorf("a turned-off steward still reaches the steward pages: %d", w.Code)
	}

	if !strings.Contains(s.do(http.MethodGet, "/steward/stewards", nil, s.mine).Body.String(), "Turned off") {
		t.Error("the list does not show them turned off")
	}

	if w := s.do(http.MethodPost, "/steward/stewards/"+them.ID.String()+"/access", url.Values{"enabled": {"yes"}}, s.mine); w.Header().Get("Location") != "/steward/stewards?done=on" {
		t.Errorf("turning back on: %q", w.Header().Get("Location"))
	}
}

// There is no button to turn yourself off, and a hand-made post is refused
// with the reason.
func TestYouCannotTurnYourselfOff(t *testing.T) {
	s := serve(t, true)

	if strings.Contains(s.do(http.MethodGet, "/steward/stewards", nil, s.mine).Body.String(), "/steward/stewards/"+s.me.ID.String()+"/access") {
		t.Error("the list offers to turn off your own account")
	}

	w := s.do(http.MethodPost, "/steward/stewards/"+s.me.ID.String()+"/access", url.Values{"enabled": {"no"}}, s.mine)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "You cannot turn off your own account.") {
		t.Errorf("turning yourself off: %d", w.Code)
	}

	if _, err := s.users.Authenticate(t.Context(), s.mine.Value); errors.Is(err, userbus.ErrDenied) {
		t.Error("the refused change signed me out")
	}
}

func TestSignedOutThereIsNoStewardList(t *testing.T) {
	s := serve(t, true)

	if w := s.do(http.MethodGet, "/steward/stewards", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d, want a redirect to sign in", w.Code)
	}

	if w := s.do(http.MethodPost, "/steward/stewards", url.Values{"email": {"x@example.org"}}, nil); w.Code != http.StatusForbidden {
		t.Errorf("a signed-out add: %d, want 403", w.Code)
	}
}
