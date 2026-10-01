package mid

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/foundation/web"
)

// KeyAuthenticator is the slice of userbus the API needs.
type KeyAuthenticator interface {
	AuthenticateAPIKey(ctx context.Context, presented string) (userbus.User, error)
}

// APIKey settles which steward an API request is from, by its
// "Authorization: Bearer stw_…" header and nothing else.
//
// Nothing else meaning the session cookie is never looked at here. A browser
// sends a cookie with every request to this host, including one a page on
// another site makes it send; an API that honoured the cookie would be a
// set of writes any web page could make in a signed-in steward's name. A key
// is never sent by a browser on its own, so a request carrying one was
// written by whoever holds it.
//
// Like Authenticate, it refuses nobody: the index is public, and RequireKey
// on each other route does the refusing.
func APIKey(log *slog.Logger, auth KeyAuthenticator) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, key, found := strings.Cut(r.Header.Get("Authorization"), " ")
			if !found || !strings.EqualFold(scheme, "Bearer") {
				next.ServeHTTP(w, r)

				return
			}

			u, err := auth.AuthenticateAPIKey(r.Context(), strings.TrimSpace(key))

			switch {
			case errors.Is(err, userbus.ErrDenied):
				// A key that was sent and refused is answered as one, not
				// as no key: a program sending a revoked key should hear
				// so, not be told it forgot to send one.
				refuseKey(w, "That API key is not one this site knows, or it has expired or been revoked. Make a new one on the stewards' API keys screen.")

				return
			case err != nil:
				log.Error("an API key could not be checked", "request_id", web.RequestIDFrom(r.Context()), "error", err)
				web.WriteJSON(w, http.StatusInternalServerError, web.Problem("", "Something went wrong at our end. Try again in a few minutes."))

				return
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stewardKey, u)))
		})
	}
}

// RequireKey refuses an API request with no steward behind it.
func RequireKey() web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := StewardFrom(r.Context()); !ok {
				refuseKey(w, "This needs an API key: send it as Authorization: Bearer stw_…. A steward makes one on the API keys screen.")

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func refuseKey(w http.ResponseWriter, problem string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="stewards"`)
	web.WriteJSON(w, http.StatusUnauthorized, web.Problem("", problem))
}
