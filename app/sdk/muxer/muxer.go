// Package muxer assembles the application's routes into one http.Handler.
//
// It exists so that there is exactly one place to read to learn what URLs this
// service answers, and one place where the middleware order is decided. A
// handler package registers its own routes; it does not decide what wraps them.
package muxer

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/health"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/foundation/sqldb"
	"github.com/jroedel/stewards/foundation/web"
)

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
}

// New builds the handler.
//
// Only /healthz for now. Everything else is a 404 until the first app
// package lands, which is the honest answer for a skeleton: a placeholder
// home page would be copy a volunteer reads, and copy goes through design.md
// and gets its Spanish twin rather than being written in passing here.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil {
		return nil, errors.New("the muxer needs a logger and a database")
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	// The order is outermost first. RequestID before Logging so the request
	// line carries the id; Logging outside Panics so a recovered panic still
	// gets its request line with the 500 on it; headers inside both, so they
	// are set on the recovery's answer too.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.Panics(cfg.Log),
		web.SecureHeaders(page.Policy()),
	), nil
}
