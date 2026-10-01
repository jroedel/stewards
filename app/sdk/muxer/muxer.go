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

	"github.com/jroedel/stewards/app/domain/authapp"
	"github.com/jroedel/stewards/app/domain/homeapp"
	"github.com/jroedel/stewards/app/domain/placeapp"
	"github.com/jroedel/stewards/app/domain/speciesapp"
	"github.com/jroedel/stewards/app/domain/stewardapp"
	"github.com/jroedel/stewards/app/sdk/health"
	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sqldb"
	"github.com/jroedel/stewards/foundation/web"
)

// Config is everything the routes need, gathered by main and passed in.
type Config struct {
	Log      *slog.Logger
	DB       *sql.DB
	Expected sqldb.Expected
	Places   *placebus.Business
	Species  *speciesbus.Business
	Listings *listingbus.Business
	Users    *userbus.Business

	// BaseURL is the public origin. Empty means sign-in is off: its routes
	// are not mounted, and with no other write in the app, the origin check
	// then has nothing to let through.
	BaseURL string

	// Mail may be nil: no relay configured. Bootstrap may be empty: no
	// one-time secret.
	Mail      mail.Sender
	Bootstrap string
}

// maxBody is the most any write here may send. A sign-in form is a few
// hundred bytes; this is room for the place edit form's longest notes in both
// languages, and nothing like room for a photo, which will need its own route
// outside this limit rather than a second MaxBody inside it (see
// web.MaxBody).
const maxBody = 64 << 10

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Places == nil || cfg.Species == nil || cfg.Listings == nil || cfg.Users == nil {
		return nil, errors.New("the muxer needs a logger, a database, and the place, species, listing and steward rules")
	}

	// One renderer holding every app's pages: the stylesheet has one hashed
	// path, and net/http panics on a pattern registered twice.
	render, err := page.NewRenderer(cfg.Log, homeapp.Templates, authapp.Templates, placeapp.Templates, speciesapp.Templates, stewardapp.Templates)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+render.StylesheetPath(), render.Stylesheet())
	mux.HandleFunc("GET /static/fonts/{file}", render.Files("fonts"))
	mux.HandleFunc("GET /static/img/{file}", render.Files("img"))

	homeapp.New(cfg.Log, render, cfg.Places).Routes(mux)
	places := placeapp.Config{Log: cfg.Log, Render: render, Places: cfg.Places, Species: cfg.Species, Listings: cfg.Listings}
	species := speciesapp.Config{Log: cfg.Log, Render: render, Species: cfg.Species, Places: cfg.Places, Listings: cfg.Listings}

	placeapp.CardRoutes(mux, places)
	speciesapp.CardRoutes(mux, species)

	if cfg.BaseURL != "" {
		authapp.Routes(mux, authapp.Config{
			Log:       cfg.Log,
			Render:    render,
			Users:     cfg.Users,
			Mail:      cfg.Mail,
			BaseURL:   cfg.BaseURL,
			Bootstrap: cfg.Bootstrap,
		})

		// Everything a steward edits, behind one guard. With sign-in off
		// these are not mounted either: a page that can only ever redirect
		// to a 404 is worse than the 404.
		guard := mid.Require(authapp.SignInPath)

		placeapp.Routes(mux, places, guard)
		speciesapp.Routes(mux, species, guard)
		stewardapp.Routes(mux, stewardapp.Config{
			Log:     cfg.Log,
			Render:  render,
			Users:   cfg.Users,
			Mail:    cfg.Mail,
			BaseURL: cfg.BaseURL,
		}, guard)
	} else {
		cfg.Log.Warn("sign-in is off: [server] base_url is not set")
	}

	// The order is outermost first. RequestID before Logging so the request
	// line carries the id; Logging outside Panics so a recovered panic still
	// gets its request line with the 500 on it; headers inside both, so they
	// are set on the recovery's answer too.
	//
	// Then the three checks on a write, cheapest first, and all before
	// anything reads a body or a session: where it came from, how big it
	// is, and that it is a form. Every write in this app is a browser
	// posting a form we rendered, so they apply to every route rather than
	// to a list somebody has to remember to add to.
	//
	// Who is signed in comes after them, so a refused write costs no
	// database read; the language innermost, since only the pages need it.
	return web.Wrap(mux,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.Panics(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.SameOriginOnly(cfg.BaseURL),
		web.MaxBody(maxBody),
		web.FormEncodedOnly(),
		mid.Authenticate(cfg.Log, cfg.Users),
		mid.Lang(),
	), nil
}
