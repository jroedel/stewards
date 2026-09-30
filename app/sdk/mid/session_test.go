package mid_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
)

func TestSafeNextKeepsOnlyPathsOnThisSite(t *testing.T) {
	for raw, want := range map[string]string{
		"/places/rain-garden":       "/places/rain-garden",
		"/places/rain-garden?x=1":   "/places/rain-garden?x=1",
		"":                          "",
		"/":                         "",
		"places":                    "",
		"//evil.example/":           "",
		"/\\evil.example/":          "",
		"https://evil.example/":     "",
		"/ok\r\nSet-Cookie: x=1":    "",
		"javascript:alert(1)":       "",
		"/%2F%2Fevil.example":       "/%2F%2Fevil.example",
		"/places/rain-garden#photo": "/places/rain-garden#photo",
	} {
		if got := mid.SafeNext(raw); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", raw, got, want)
		}
	}
}

type authFunc func(context.Context, string) (userbus.User, error)

func (f authFunc) Authenticate(ctx context.Context, presented string) (userbus.User, error) {
	return f(ctx, presented)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// serveSignedIn runs a request with a session cookie through Authenticate and
// Require, and reports what came back and whether the page was reached.
func serveSignedIn(auth mid.Authenticator, method, cookie string) (*httptest.ResponseRecorder, bool) {
	reached := false
	h := mid.Authenticate(quiet, auth)(mid.Require("/sign-in")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, reached = mid.StewardFrom(r.Context())
	})))

	r := httptest.NewRequest(method, "/places/rain-garden/edit", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: mid.SessionCookie, Value: cookie})
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	return w, reached
}

func TestASignedInStewardReachesThePage(t *testing.T) {
	ok := authFunc(func(context.Context, string) (userbus.User, error) {
		return userbus.User{ID: types.NewID(), Enabled: true}, nil
	})

	if _, reached := serveSignedIn(ok, http.MethodGet, "a.b"); !reached {
		t.Error("a good session did not reach the page")
	}
}

func TestNobodySignedInIsSentToSignInAndBack(t *testing.T) {
	w, reached := serveSignedIn(nil, http.MethodGet, "")

	if reached || w.Code != http.StatusSeeOther {
		t.Fatalf("reached = %v, status %d; want a redirect", reached, w.Code)
	}

	if got, want := w.Header().Get("Location"), "/sign-in?next=%2Fplaces%2Frain-garden%2Fedit"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	if w, _ := serveSignedIn(nil, http.MethodPost, ""); w.Code != http.StatusForbidden {
		t.Errorf("a signed-out POST: %d, want 403", w.Code)
	}
}

// A cookie that no longer works is taken away, so the browser stops sending
// it.
func TestAnEndedSessionIsClearedOnTheWayPast(t *testing.T) {
	denied := authFunc(func(context.Context, string) (userbus.User, error) { return userbus.User{}, userbus.ErrDenied })

	w, reached := serveSignedIn(denied, http.MethodGet, "a.b")
	if reached {
		t.Fatal("an ended session reached the page")
	}

	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == mid.SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}

	if !cleared {
		t.Error("the ended session's cookie was not cleared")
	}
}

// A database that cannot be read is not the same as being signed out.
func TestABrokenStoreIsNotASignInPage(t *testing.T) {
	broken := authFunc(func(context.Context, string) (userbus.User, error) {
		return userbus.User{}, errors.New("disk I/O error")
	})

	if w, _ := serveSignedIn(broken, http.MethodGet, "a.b"); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", w.Code)
	}
}
