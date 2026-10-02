package signupapp_test

import (
	"database/sql"
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
	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
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
	list   *subscriberbus.Business
	mail   *mail.Recorder
	log    *strings.Builder
	cookie *http.Cookie
}

// Through the muxer, with a recorded mailbox and a log the test can read.
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
		func() error { return subscriberdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	s := &site{t: t, list: subscriberbus.NewBusiness(subscriberdb.NewStore(db), nil), mail: &mail.Recorder{}, log: &strings.Builder{}}
	log := slog.New(slog.NewTextHandler(s.log, nil))
	users := userbus.NewBusiness(log, userdb.NewStore(db), nil)

	if s.h, err = muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:      placebus.NewBusiness(placedb.NewStore(db), nil),
		Species:     speciesbus.NewBusiness(speciesdb.NewStore(db), nil),
		Listings:    listingbus.NewBusiness(listingdb.NewStore(db), nil),
		Photos:      photos(t, db),
		Users:       users,
		Workdays:    workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		Subscribers: s.list,
		BaseURL:     "https://stewards.example.invalid", Mail: s.mail,
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

// link is the address in the last email that starts with prefix.
func (s *site) link(prefix string) string {
	s.t.Helper()

	m, ok := s.mail.Last()
	if !ok {
		s.t.Fatal("no email was sent")
	}

	at := strings.Index(m.Text, prefix)
	if at < 0 {
		s.t.Fatalf("the email has no %s link:\n%s", prefix, m.Text)
	}

	return strings.Fields(m.Text[at:])[0]
}

// token is the t= of a link.
func token(t *testing.T, link string) string {
	t.Helper()

	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}

	return u.Query().Get("t")
}

// The whole of it, as a newcomer does it: the form on the home page, on the
// list at once, the welcome, and later the way off the list.
func TestANewcomerSignsUpAndLeaves(t *testing.T) {
	s := serve(t)

	home := s.do(http.MethodGet, "/", nil, false).Body.String()
	if !strings.Contains(home, `action="/subscribe"`) || !strings.Contains(home, `name="website"`) {
		t.Fatal("the home page has no sign-up form, or the form has no honeypot")
	}

	w := s.do(http.MethodPost, "/subscribe", url.Values{"email": {" Walker@Example.org "}, "website": {""}}, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/subscribe/sent" {
		t.Fatalf("signing up: %d %s", w.Code, w.Header().Get("Location"))
	}

	if page := s.do(http.MethodGet, "/subscribe/sent", nil, false).Body.String(); !strings.Contains(page, "You&#39;re on the list") {
		t.Errorf("the page after the form:\n%s", page)
	}

	on, _ := s.list.All(t.Context())
	if len(on) != 1 || on[0].Email.String() != "walker@example.org" {
		t.Fatalf("on the list: %+v", on)
	}

	m, _ := s.mail.Last()
	if len(s.mail.Sent) != 1 || m.To != "walker@example.org" || !strings.HasPrefix(m.Text, "Thank you for signing up") || !strings.Contains(m.Text, "If you did not sign up for this") {
		t.Errorf("the welcome: %d sent, to %q:\n%s", len(s.mail.Sent), m.To, m.Text)
	}

	leave := s.link("https://stewards.example.invalid/unsubscribe?t=")

	// Signing up again sends nothing more.
	s.do(http.MethodPost, "/subscribe", url.Values{"email": {"walker@example.org"}}, false)
	if len(s.mail.Sent) != 1 {
		t.Errorf("signing up again sent %d emails in all, want 1", len(s.mail.Sent))
	}

	// The steward sees them, and the Bcc line.
	list := s.do(http.MethodGet, "/steward/subscribers", nil, true).Body.String()
	if !strings.Contains(list, "walker@example.org") || !strings.Contains(list, "Bcc") {
		t.Errorf("the stewards' list does not show them")
	}

	// Opening the unsubscribe link changes nothing; pressing its button does.
	if page := s.do(http.MethodGet, strings.TrimPrefix(leave, "https://stewards.example.invalid"), nil, false).Body.String(); !strings.Contains(page, "Stop these emails?") {
		t.Errorf("the unsubscribe page has no button")
	}

	if on, _ := s.list.All(t.Context()); len(on) != 1 {
		t.Fatal("opening the unsubscribe link took them off")
	}

	if w := s.do(http.MethodPost, "/unsubscribe", url.Values{"t": {token(t, leave)}}, false); !strings.Contains(w.Body.String(), "off the list") {
		t.Errorf("unsubscribing: %d", w.Code)
	}

	if on, _ := s.list.All(t.Context()); len(on) != 0 {
		t.Error("still on the list after unsubscribing")
	}

	// And no address reached the log, from any of it.
	if strings.Contains(strings.ToLower(s.log.String()), "walker@example.org") {
		t.Errorf("an address was logged:\n%s", s.log)
	}
}

// A link from the first version's confirmation emails lands on a page that
// says what to do, not on a 404.
func TestAnOldConfirmLinkSaysToSignUpAgain(t *testing.T) {
	s := serve(t)

	w := s.do(http.MethodGet, "/subscribe/confirm?t=whatever", nil, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "No need to confirm any more") {
		t.Errorf("an old confirm link: %d\n%s", w.Code, w.Body)
	}
}

// The page after the form says the same whoever is asking, and the honeypot
// and the caps send nothing.
func TestTheFormTellsAStrangerNothing(t *testing.T) {
	s := serve(t)

	bot := s.do(http.MethodPost, "/subscribe", url.Values{"email": {"bot@example.org"}, "website": {"http://spam.example"}}, false)
	if bot.Code != http.StatusSeeOther || bot.Header().Get("Location") != "/subscribe/sent" || len(s.mail.Sent) != 0 {
		t.Errorf("the honeypot: %d %s, %d sent", bot.Code, bot.Header().Get("Location"), len(s.mail.Sent))
	}

	if on, _ := s.list.All(t.Context()); len(on) != 0 {
		t.Error("the honeypot's address is on the list")
	}

	// Signed up already, or not: the same answer.
	for i := range 3 {
		w := s.do(http.MethodPost, "/subscribe", url.Values{"email": {"walker@example.org"}}, false)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/subscribe/sent" {
			t.Errorf("request %d: %d", i+1, w.Code)
		}
	}

	// A typo is the one thing said differently, and it is about the typing.
	w := s.do(http.MethodPost, "/subscribe", url.Values{"email": {"walker.example.org"}}, false)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "doesn&#39;t look like an email address") || !strings.Contains(w.Body.String(), `value="walker.example.org"`) {
		t.Errorf("a typo: %d\n%s", w.Code, w.Body)
	}
}

func TestTheListIsAStewards(t *testing.T) {
	s := serve(t)

	if w := s.do(http.MethodGet, "/steward/subscribers", nil, false); w.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", w.Code)
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
