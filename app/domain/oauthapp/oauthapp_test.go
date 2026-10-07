package oauthapp_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/jroedel/stewards/business/domain/user/stores/userdb"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/oauth"
	"github.com/jroedel/stewards/foundation/sqldb"
)

const (
	base = "https://stewards.example.invalid"

	claudeAI = "https://claude.ai/oauth/test-client-metadata"
	callback = "https://claude.ai/api/mcp/auth_callback"

	// RFC 7636, appendix B.
	verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

// clients is the metadata documents this test knows, in place of the
// network: Claude's, and one on a host that is not trusted, which must never
// be read at all.
type clients struct{ t *testing.T }

func (c clients) Fetch(_ context.Context, id string) (oauth.Client, error) {
	if id != claudeAI {
		c.t.Errorf("a document was read for %s", id)

		return oauth.Client{}, oauth.ErrBadClient
	}

	return oauth.Client{ID: claudeAI, Name: "Claude", RedirectURIs: []string{callback}}, nil
}

type site struct {
	t    *testing.T
	h    http.Handler
	mine *http.Cookie
}

// Through the muxer, so every request passes the checks it would in
// production: the origin, the body's shape, and the session.
func serve(t *testing.T) site {
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

	h, err := muxer.New(muxer.Config{
		Log: log, DB: db, Expected: sqldb.Infrastructure,
		Places:  placebus.NewBusiness(placedb.NewStore(db), nil),
		Species: speciesbus.NewBusiness(speciesdb.NewStore(db), nil), Listings: listingbus.NewBusiness(listingdb.NewStore(db), nil), Photos: photos(t, db),
		Users: users, Workdays: workdaybus.NewBusiness(workdaydb.NewStore(db), nil),
		BaseURL:      base,
		OAuthClients: clients{t},
	})
	if err != nil {
		t.Fatal(err)
	}

	e, _ := types.ParseEmail("steward@example.org")
	if _, err := users.Create(t.Context(), e, ""); err != nil {
		t.Fatal(err)
	}

	req, _ := users.RequestSignIn(t.Context(), e)

	_, cookie, err := users.SignIn(t.Context(), req.Secret)
	if err != nil {
		t.Fatal(err)
	}

	return site{t: t, h: h, mine: &http.Cookie{Name: mid.SessionCookie, Value: cookie}}
}

func photos(t *testing.T, db *sql.DB) *photobus.Business {
	t.Helper()

	files, err := photofs.NewStore(filepath.Join(t.TempDir(), "photo-files"))
	if err != nil {
		t.Fatal(err)
	}

	return photobus.NewBusiness(photodb.NewStore(db), files, nil)
}

// do sends a request as a browser on this site would, or, with site "",
// as a program on a server does: no Sec-Fetch-Site, no Origin.
func (s site) do(method, path string, form url.Values, cookie *http.Cookie, fetchSite string) *httptest.ResponseRecorder {
	s.t.Helper()

	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	if fetchSite != "" {
		r.Header.Set("Sec-Fetch-Site", fetchSite)
	}

	if cookie != nil {
		r.AddCookie(cookie)
	}

	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)

	return w
}

// asking is the request Claude sends a steward's browser with.
func asking() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {claudeAI},
		"redirect_uri":          {callback},
		"state":                 {"the-programs-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {base + "/mcp"},
	}
}

func authorize(q url.Values) string { return "/oauth/authorize?" + q.Encode() }

// sentBack is where a redirect to the program goes, and its query.
func sentBack(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()

	to, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || err != nil || to.Scheme+"://"+to.Host+to.Path != callback {
		t.Fatalf("not sent back to the program: %d %q\n%s", w.Code, w.Header().Get("Location"), w.Body)
	}

	q := to.Query()
	if q.Get("state") != "the-programs-state" || q.Get("iss") != base {
		t.Errorf("the answer's state and iss: %v", q)
	}

	return q
}

func TestClaudeSignsInAsAStewardAndItsKeyReachesTheAPI(t *testing.T) {
	s := serve(t)

	// What Claude reads first.
	var meta map[string]any
	w := s.do(http.MethodGet, "/.well-known/oauth-authorization-server", nil, nil, "")
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil || meta["issuer"] != base || meta["authorization_endpoint"] != base+"/oauth/authorize" || meta["client_id_metadata_document_supported"] != true {
		t.Fatalf("the metadata: %d %s", w.Code, w.Body)
	}

	// The steward's browser, signed out: sent to sign in, and back here.
	w = s.do(http.MethodGet, authorize(asking()), nil, nil, "")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/sign-in?next=%2Foauth%2Fauthorize%3F") {
		t.Fatalf("signed out: %d %q", w.Code, w.Header().Get("Location"))
	}

	// Signed in: the page, which may send its form on to claude.ai.
	w = s.do(http.MethodGet, authorize(asking()), nil, s.mine, "")
	body := w.Body.String()

	if w.Code != http.StatusOK || !strings.Contains(body, "Let Claude work as you?") || !strings.Contains(body, "Claude (claude.ai)") {
		t.Fatalf("the page: %d\n%s", w.Code, body)
	}

	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://claude.ai;") {
		t.Errorf("the page's policy: %s", csp)
	}

	// Allow.
	form := asking()
	form.Set("answer", "allow")

	code := sentBack(t, s.do(http.MethodPost, "/oauth/authorize", form, s.mine, "same-origin")).Get("code")
	if code == "" {
		t.Fatal("no code")
	}

	// Claude's server trades it, as a program: no cookie, no browser.
	trade := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {claudeAI},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {base + "/mcp"},
	}

	w = s.do(http.MethodPost, "/oauth/token", trade, nil, "")

	var tok oauth.Token
	if err := json.Unmarshal(w.Body.Bytes(), &tok); w.Code != http.StatusOK || err != nil || tok.TokenType != "Bearer" || !strings.HasPrefix(tok.AccessToken, userbus.APIKeyPrefix) {
		t.Fatalf("the trade: %d %s", w.Code, w.Body)
	}

	if w.Header().Get("Cache-Control") != "no-store" || tok.ExpiresIn < int64(userbus.APIKeyLife.Seconds())-60 {
		t.Errorf("Cache-Control %q, expires_in %d", w.Header().Get("Cache-Control"), tok.ExpiresIn)
	}

	// The key is an ordinary one: the API takes it, and the keys screen
	// lists it by the program's name.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/species", nil)
	r.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	api := httptest.NewRecorder()
	s.h.ServeHTTP(api, r)

	if api.Code != http.StatusOK {
		t.Errorf("the API with Claude's key: %d %s", api.Code, api.Body)
	}

	if keys := s.do(http.MethodGet, "/steward/keys", nil, s.mine, "").Body.String(); !strings.Contains(keys, "Claude (claude.ai)") {
		t.Error("the keys screen does not list Claude's key")
	}

	// Once.
	w = s.do(http.MethodPost, "/oauth/token", trade, nil, "")

	var refused oauth.TokenError
	if err := json.Unmarshal(w.Body.Bytes(), &refused); w.Code != http.StatusBadRequest || err != nil || refused.Code != oauth.InvalidGrant {
		t.Errorf("a second trade: %d %s", w.Code, w.Body)
	}
}

func TestAStewardWhoSaysNoSendsClaudeAwayEmptyHanded(t *testing.T) {
	s := serve(t)

	form := asking()
	form.Set("answer", "deny")

	q := sentBack(t, s.do(http.MethodPost, "/oauth/authorize", form, s.mine, "same-origin"))
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Errorf("saying no: %v", q)
	}
}

// A program or a redirect that cannot be trusted is answered on our own
// page; nothing is sent anywhere.
func TestAnUntrustedRequestStaysOnOurPage(t *testing.T) {
	s := serve(t)

	for name, change := range map[string][2]string{
		"another program":             {"client_id", "https://some-app.example/client"},
		"not https":                   {"client_id", "http://claude.ai/oauth/test-client-metadata"},
		"a look-alike":                {"client_id", "https://claude.ai.evil.example/client"},
		"a redirect it does not list": {"redirect_uri", "https://evil.example/cb"},
	} {
		q := asking()
		q.Set(change[0], change[1])

		w := s.do(http.MethodGet, authorize(q), nil, s.mine, "")
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Errorf("%s, asking: %d %q", name, w.Code, w.Header().Get("Location"))
		}

		// And the same through the form, whose hidden fields are the
		// steward's browser's to change.
		q.Set("answer", "allow")

		w = s.do(http.MethodPost, "/oauth/authorize", q, s.mine, "same-origin")
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Errorf("%s, allowing: %d %q", name, w.Code, w.Header().Get("Location"))
		}
	}
}

// Everything else wrong with a request goes back to the program, which is
// how it finds out.
func TestARequestWithoutPKCEGoesBackWithAnError(t *testing.T) {
	s := serve(t)

	for _, drop := range []string{"code_challenge", "code_challenge_method", "response_type"} {
		q := asking()
		q.Del(drop)

		if got := sentBack(t, s.do(http.MethodGet, authorize(q), nil, s.mine, "")); got.Get("error") == "" || got.Get("code") != "" {
			t.Errorf("without %s: %v", drop, got)
		}
	}
}

// The yes is a form post, so another site cannot make a signed-in steward's
// browser send it: a code made that way would go to whoever started that
// sign-in at claude.ai, with the steward's account behind it.
func TestAnotherSiteCannotSayYesForAsteward(t *testing.T) {
	s := serve(t)

	form := asking()
	form.Set("answer", "allow")

	if w := s.do(http.MethodPost, "/oauth/authorize", form, s.mine, "cross-site"); w.Code != http.StatusForbidden {
		t.Errorf("a cross-site yes: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestTheTokenEndpointAnswersInTheRFCsWords(t *testing.T) {
	s := serve(t)

	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"a refresh":      {url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}}, "unsupported_grant_type"},
		"no code":        {url.Values{"grant_type": {"authorization_code"}, "client_id": {claudeAI}, "redirect_uri": {callback}, "code_verifier": {verifier}}, "invalid_request"},
		"a made-up code": {url.Values{"grant_type": {"authorization_code"}, "code": {types.NewID().String() + ".AAAAAAAAAAAAAAAAAAAAAAAAAA"}, "client_id": {claudeAI}, "redirect_uri": {callback}, "code_verifier": {verifier}}, "invalid_grant"},
	} {
		w := s.do(http.MethodPost, "/oauth/token", tc.form, nil, "")

		var got oauth.TokenError
		if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusBadRequest || err != nil || got.Code != tc.want {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}
