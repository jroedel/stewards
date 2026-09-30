package authapp_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/muxer"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/place/stores/placedb"
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
)

const (
	base    = "https://stewards.example.invalid"
	secret  = "a-bootstrap-secret-of-thirty-two-or-more"
	steward = "steward@example.org"
)

type site struct {
	h     http.Handler
	users *userbus.Business
	mail  *mail.Recorder
}

// Through the muxer, so every request passes the same checks it would in
// production: the origin, the body limit, the content type and the session.
func serve(t *testing.T, bootstrap, baseURL string) site {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return placedb.Init(t.Context(), db) },
		func() error { return userdb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.DiscardHandler)
	s := site{users: userbus.NewBusiness(log, userdb.NewStore(db), nil), mail: &mail.Recorder{}}

	s.h, err = muxer.New(muxer.Config{
		Log:       log,
		DB:        db,
		Expected:  sqldb.Infrastructure,
		Places:    placebus.NewBusiness(placedb.NewStore(db), nil),
		Users:     s.users,
		BaseURL:   baseURL,
		Mail:      s.mail,
		Bootstrap: bootstrap,
	})
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func (s site) addSteward(t *testing.T) {
	t.Helper()

	e, _ := types.ParseEmail(steward)
	if _, err := s.users.Create(t.Context(), e, "A Steward"); err != nil {
		t.Fatal(err)
	}
}

func (s site) get(t *testing.T, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

// post sends a form as a browser on one of our own pages would.
func (s site) post(t *testing.T, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, c := range cookies {
		r.AddCookie(c)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

func session(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == mid.SessionCookie && c.Value != "" {
			return c
		}
	}

	return nil
}

var linkInMail = regexp.MustCompile(`https://\S+/sign-in/link\?\S+`)

// The whole way in: ask, receive, open, press, and arrive signed in.
func TestAStewardSignsInByEmailedLink(t *testing.T) {
	s := serve(t, "", base)
	s.addSteward(t)

	w := s.post(t, "/sign-in", url.Values{"email": {"Steward@Example.org"}, "next": {"/places"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Check your email") {
		t.Fatalf("asking for a link: %d\n%s", w.Code, w.Body)
	}

	sent, ok := s.mail.Last()
	if !ok || sent.To != steward {
		t.Fatalf("mail sent: %+v", sent)
	}

	// Built from base_url, never from the request's Host.
	raw := linkInMail.FindString(sent.Text)
	if !strings.HasPrefix(raw, base+"/sign-in/link?t=") {
		t.Fatalf("the link in the mail is %q", raw)
	}

	link, _ := url.Parse(raw)

	// Opening the link, as a mail scanner would, signs nobody in and spends
	// nothing.
	opened := s.get(t, link.RequestURI())
	if opened.Code != http.StatusOK || session(opened) != nil {
		t.Fatalf("opening the link: %d, session %v", opened.Code, session(opened))
	}

	if !strings.Contains(opened.Body.String(), `name="token" value="`+link.Query().Get("t")+`"`) {
		t.Fatal("the page does not carry the token to its button")
	}

	pressed := s.post(t, "/sign-in/link", url.Values{"token": {link.Query().Get("t")}, "next": {link.Query().Get("next")}})
	cookie := session(pressed)

	switch {
	case pressed.Code != http.StatusSeeOther:
		t.Fatalf("pressing the button: %d\n%s", pressed.Code, pressed.Body)
	case pressed.Header().Get("Location") != "/places":
		t.Errorf("landed on %q, want /places", pressed.Header().Get("Location"))
	case cookie == nil:
		t.Fatal("no session cookie")
	case !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode:
		t.Errorf("the session cookie is %+v", cookie)
	}

	home := s.get(t, "/", cookie)
	if !strings.Contains(home.Body.String(), "Signed in as "+steward) {
		t.Error("the home screen does not say who is signed in")
	}

	// The same link a second time.
	if again := s.post(t, "/sign-in/link", url.Values{"token": {link.Query().Get("t")}}); again.Code != http.StatusUnauthorized || session(again) != nil {
		t.Errorf("a link used twice: %d", again.Code)
	}

	// And out again.
	out := s.post(t, "/sign-out", url.Values{}, cookie)
	if out.Code != http.StatusSeeOther {
		t.Fatalf("signing out: %d", out.Code)
	}

	if strings.Contains(s.get(t, "/", cookie).Body.String(), "Signed in as") {
		t.Error("the session outlived signing out")
	}
}

// Whoever asks, the answer is the same page. Only the address differs, and
// that is the one they typed.
func TestTheAnswerIsTheSameForAnAddressWithNoAccount(t *testing.T) {
	s := serve(t, "", base)
	s.addSteward(t)

	known := s.post(t, "/sign-in", url.Values{"email": {steward}})
	unknown := s.post(t, "/sign-in", url.Values{"email": {"stranger@example.org"}})

	if known.Code != unknown.Code {
		t.Errorf("status %d for a steward, %d for a stranger", known.Code, unknown.Code)
	}

	if strings.ReplaceAll(known.Body.String(), steward, "X") != strings.ReplaceAll(unknown.Body.String(), "stranger@example.org", "X") {
		t.Error("the page differs between an address with an account and one without")
	}

	if len(s.mail.Sent) != 1 {
		t.Errorf("%d messages sent, want 1: only the steward gets one", len(s.mail.Sent))
	}
}

// A next that points off the site lands on the home screen instead.
func TestSigningInNeverSendsYouElsewhere(t *testing.T) {
	s := serve(t, "", base)
	s.addSteward(t)

	s.post(t, "/sign-in", url.Values{"email": {steward}, "next": {"//evil.example/"}})

	sent, _ := s.mail.Last()
	if strings.Contains(sent.Text, "evil") {
		t.Fatal("the mail carries the other site's address")
	}

	link, _ := url.Parse(linkInMail.FindString(sent.Text))
	w := s.post(t, "/sign-in/link", url.Values{"token": {link.Query().Get("t")}, "next": {"https://evil.example/"}})

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("landed on %q, want /", got)
	}
}

func TestAWriteFromAnotherSiteIsRefusedBeforeItIsRead(t *testing.T) {
	s := serve(t, "", base)
	s.addSteward(t)

	r := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader("email="+steward))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden || len(s.mail.Sent) != 0 {
		t.Errorf("a cross-site post: %d, %d sent", w.Code, len(s.mail.Sent))
	}
}

func TestTheBootstrapLetsTheFirstStewardInOnce(t *testing.T) {
	s := serve(t, secret, base)

	if w := s.get(t, "/sign-in/first"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "First sign-in") {
		t.Fatalf("the bootstrap page: %d", w.Code)
	}

	wrong := s.post(t, "/sign-in/first", url.Values{"email": {steward}, "secret": {"not-it"}})
	if wrong.Code != http.StatusUnauthorized || session(wrong) != nil {
		t.Fatalf("a wrong secret: %d", wrong.Code)
	}

	right := s.post(t, "/sign-in/first", url.Values{"email": {steward}, "secret": {secret}})
	if right.Code != http.StatusSeeOther || session(right) == nil {
		t.Fatalf("the right secret: %d\n%s", right.Code, right.Body)
	}

	if len(s.mail.Sent) != 0 {
		t.Error("the bootstrap sent mail")
	}

	if again := s.post(t, "/sign-in/first", url.Values{"email": {"other@example.org"}, "secret": {secret}}); again.Code != http.StatusUnauthorized {
		t.Errorf("the secret a second time: %d", again.Code)
	}

	if w := s.get(t, "/sign-in/first"); !strings.Contains(w.Body.String(), "Already set up") {
		t.Error("the spent bootstrap page does not say so")
	}
}

// The footer offers sign-in on every page but the ones that are sign-in,
// which offer the way back instead.
func TestTheSignInPagesDoNotLinkToThemselves(t *testing.T) {
	s := serve(t, secret, base)

	for _, path := range []string{"/sign-in", "/sign-in/first", "/sign-in/link?t=x"} {
		if strings.Contains(s.get(t, path).Body.String(), "Garden stewards: sign in") {
			t.Errorf("%s offers the footer's sign-in link", path)
		}

		if !strings.Contains(s.get(t, path).Body.String(), "Back to the places list") {
			t.Errorf("%s has no way back to the places list", path)
		}
	}

	if !strings.Contains(s.get(t, "/").Body.String(), `href="/sign-in"`) {
		t.Error("the home screen has no way for a steward to sign in")
	}
}

func TestWithNoSecretThereIsNoBootstrapPage(t *testing.T) {
	s := serve(t, "", base)

	if w := s.get(t, "/sign-in/first"); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}

	if strings.Contains(s.get(t, "/sign-in").Body.String(), "/sign-in/first") {
		t.Error("the sign-in page offers a bootstrap that is not there")
	}
}

// The config on the server predates base_url, so the binary must run without
// it, with sign-in off.
func TestWithNoBaseURLSignInIsOff(t *testing.T) {
	s := serve(t, secret, "")

	for _, path := range []string{"/sign-in", "/sign-in/first", "/sign-in/link?t=x"} {
		if w := s.get(t, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, w.Code)
		}
	}

	if w := s.get(t, "/"); w.Code != http.StatusOK {
		t.Errorf("the home screen: %d", w.Code)
	}
}
