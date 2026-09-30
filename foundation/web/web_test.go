package web_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/foundation/web"
)

// The chain is written outermost first, and that is the order it must run in.
// Reversed, Logging would sit inside Panics and a panicking request would
// leave no request line at all.
func TestWrapRunsTheFirstMiddlewareOutermost(t *testing.T) {
	var order []string

	mark := func(name string) web.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := web.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mark("outer"), nil, mark("inner"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := strings.Join(order, " "); got != "outer inner handler" {
		t.Errorf("ran as %q, want outer inner handler", got)
	}
}

// A panic becomes a 500 with a sentence a person can act on, and the request
// line still gets written with the 500 on it and the same id as the panic.
func TestAPanicIsOneLoggedFiveHundred(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	h := web.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), web.RequestID(), web.Logging(log), web.Panics(log))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/places?phone=5125550100", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}

	if !strings.Contains(rec.Body.String(), "garden stewards") {
		t.Errorf("the body does not tell the volunteer what to do: %q", rec.Body.String())
	}

	out := logs.String()

	for _, want := range []string{"msg=panic", "msg=request", "status=500", "path=/places"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log has no %s:\n%s", want, out)
		}
	}

	// The query string never reaches the log.
	if strings.Contains(out, "5125550100") {
		t.Errorf("the query string was logged:\n%s", out)
	}
}

func TestSecureHeadersSetsThePolicyAndNosniff(t *testing.T) {
	h := web.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		web.SecureHeaders(func(*http.Request) web.Policy {
			return web.Policy{ContentSecurityPolicy: "default-src 'none'"}
		}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'" {
		t.Errorf("Content-Security-Policy = %q", got)
	}

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}

	// An empty field is not sent at all, rather than sent empty.
	if _, ok := rec.Header()["X-Frame-Options"]; ok {
		t.Error("an empty FrameOptions was sent")
	}
}

// A port already taken is a startup error, not a log line claiming to listen
// followed by a quiet death.
func TestServeFailsWhenThePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer taken.Close()

	err = web.Serve(t.Context(), slog.New(slog.DiscardHandler), time.Second,
		taken.Addr().String(), http.NotFoundHandler())
	if err == nil {
		t.Fatal("Serve took a port that was already in use")
	}
}

// Cancelling the context is the ordinary way the server stops, on SIGTERM
// from the supervisor, and it must end cleanly rather than as an error.
func TestServeStopsCleanlyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- web.Serve(ctx, slog.New(slog.DiscardHandler), time.Second,
			"127.0.0.1:0", http.NotFoundHandler())
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a cancelled Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after its context was cancelled")
	}
}
