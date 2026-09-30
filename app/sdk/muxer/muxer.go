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

	"github.com/jroedel/stewards/app/domain/homeapp"
	"github.com/jroedel/stewards/app/sdk/health"
	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/foundation/sqldb"
	"github.com/jroedel/stewards/foundation/web"
)

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
	Places   *placebus.Business
}

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Places == nil {
		return nil, errors.New("the muxer needs a logger, a database and the place rules")
	}

	// One renderer holding every app's pages: the stylesheet has one hashed
	// path, and net/http panics on a pattern registered twice.
	render, err := page.NewRenderer(cfg.Log, homeapp.Templates)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+render.StylesheetPath(), render.Stylesheet())
	mux.HandleFunc("GET /static/fonts/{file}", render.Files("fonts"))
	mux.HandleFunc("GET /static/img/{file}", render.Files("img"))

	homeapp.New(cfg.Log, render, cfg.Places).Routes(mux)

	// The order is outermost first. RequestID before Logging so the request
	// line carries the id; Logging outside Panics so a recovered panic still
	// gets its request line with the 500 on it; headers inside both, so they
	// are set on the recovery's answer too; the language innermost, since
	// only the pages need it.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.Panics(cfg.Log),
		web.SecureHeaders(page.Policy()),
		mid.Lang(),
	), nil
}
