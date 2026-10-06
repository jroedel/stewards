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

	"github.com/jroedel/stewards/app/domain/apiapp"
	"github.com/jroedel/stewards/app/domain/authapp"
	"github.com/jroedel/stewards/app/domain/homeapp"
	"github.com/jroedel/stewards/app/domain/inboxapp"
	"github.com/jroedel/stewards/app/domain/nurseryapp"
	"github.com/jroedel/stewards/app/domain/photoapp"
	"github.com/jroedel/stewards/app/domain/placeapp"
	"github.com/jroedel/stewards/app/domain/signupapp"
	"github.com/jroedel/stewards/app/domain/speciesapp"
	"github.com/jroedel/stewards/app/domain/stewardapp"
	"github.com/jroedel/stewards/app/domain/workdayapp"
	"github.com/jroedel/stewards/app/sdk/health"
	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
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
	Photos   *photobus.Business
	Users    *userbus.Business
	Workdays *workdaybus.Business

	// Inbox may be nil, and its screens are then not mounted. Optional
	// rather than required only so that a test about something else need
	// not build one; main always passes it.
	Inbox *inboxbus.Business

	// Nursery may be nil likewise, for no nursery stock screens. It is
	// only mounted with an Inbox, which is how stock arrives.
	Nursery *nurserybus.Business

	// Subscribers may be nil, and with no Mail or no BaseURL it is unused:
	// the email sign-up is mounted only when it can send its welcome.
	Subscribers *subscriberbus.Business

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

// maxUpload is the body limit on the upload route: the largest photo
// photobus accepts, and room for the form's text fields and the multipart
// boundaries around them.
const maxUpload = photobus.MaxBytes + 1<<20

// New builds the handler.
func New(cfg Config) (http.Handler, error) {
	if cfg.Log == nil || cfg.DB == nil || cfg.Places == nil || cfg.Species == nil || cfg.Listings == nil || cfg.Photos == nil || cfg.Users == nil || cfg.Workdays == nil {
		return nil, errors.New("the muxer needs a logger, a database, and the place, species, listing, photo, steward and work-day rules")
	}

	// One renderer holding every app's pages: the stylesheet has one hashed
	// path, and net/http panics on a pattern registered twice.
	render, err := page.NewRenderer(cfg.Log, homeapp.Templates, authapp.Templates, placeapp.Templates, speciesapp.Templates, photoapp.Templates, inboxapp.Templates, nurseryapp.Templates, stewardapp.Templates, workdayapp.Templates, signupapp.Templates)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	mux.Handle("GET /healthz", health.Handler(cfg.Log, cfg.DB, cfg.Expected))

	mux.HandleFunc("GET "+render.StylesheetPath(), render.Stylesheet())
	mux.HandleFunc("GET "+render.SwapPath(), render.Swap())
	mux.HandleFunc("GET /static/fonts/{file}", render.Files("fonts"))
	mux.HandleFunc("GET /static/img/{file}", render.Files("img"))

	signUp := cfg.BaseURL != "" && cfg.Mail != nil && cfg.Subscribers != nil
	homeapp.New(cfg.Log, render, cfg.Places, cfg.Workdays, signUp).Routes(mux)
	places := placeapp.Config{Log: cfg.Log, Render: render, Places: cfg.Places, Species: cfg.Species, Listings: cfg.Listings, Photos: cfg.Photos}
	// Assigned only when there is one: a nil *inboxbus.Business inside
	// the interface is not a nil interface, and the front page would call
	// it.
	if cfg.Inbox != nil {
		places.Inbox = cfg.Inbox
		places.NurseryStock = cfg.Nursery != nil
	}
	species := speciesapp.Config{Log: cfg.Log, Render: render, Species: cfg.Species, Places: cfg.Places, Listings: cfg.Listings, Photos: cfg.Photos}
	photos := photoapp.Config{Log: cfg.Log, Render: render, Photos: cfg.Photos, Species: cfg.Species, Places: cfg.Places}

	placeapp.CardRoutes(mux, places)
	speciesapp.CardRoutes(mux, species)
	photoapp.FileRoutes(mux, photos)

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
		photoapp.Routes(mux, photos, guard)

		if cfg.Inbox != nil {
			inboxCfg := inboxapp.Config{Log: cfg.Log, Render: render, Inbox: cfg.Inbox, Places: cfg.Places, Species: cfg.Species}
			if cfg.Nursery != nil {
				inboxCfg.Nursery = cfg.Nursery
			}

			inboxapp.Routes(mux, inboxCfg, guard)

			// The stewards app, installable from any steward's page, with
			// the inbox's share target: its manifest, and the script that
			// registers the worker a share is caught by.
			render.OfferApp(inboxapp.ManifestPath, inboxapp.ShareScript)

			if cfg.Nursery != nil {
				nurseryapp.Routes(mux, nurseryapp.Config{Log: cfg.Log, Render: render, Stock: cfg.Nursery, Species: cfg.Species}, guard)
			}
		}
		workdayapp.Routes(mux, workdayapp.Config{Log: cfg.Log, Render: render, Days: cfg.Workdays}, guard)

		if signUp {
			signupapp.Routes(mux, signupapp.Config{
				Log: cfg.Log, Render: render, List: cfg.Subscribers, Mail: cfg.Mail, BaseURL: cfg.BaseURL,
			}, guard)
		} else {
			cfg.Log.Warn("the email sign-up is off: there is no [mail] relay")
		}
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

	// The API on a mux of its own, so that nothing under /api is ever
	// reached through the cookie's chain or the cookie through the API's.
	// Mounted only with sign-in on, like the screens: a key is made on one.
	api := http.NewServeMux()
	if cfg.BaseURL != "" {
		apiCfg := apiapp.Config{
			Log: cfg.Log, Species: cfg.Species, Places: cfg.Places, Photos: cfg.Photos, Listings: cfg.Listings, BaseURL: cfg.BaseURL,
		}

		// As for the front page: a nil *Business is not a nil interface.
		if cfg.Inbox != nil {
			apiCfg.Inbox = cfg.Inbox
		}

		if cfg.Nursery != nil {
			apiCfg.Nursery = cfg.Nursery
		}

		apiapp.Routes(api, apiCfg)
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
	//
	// How big and what shape are a fork rather than a line. The upload
	// routes are the writes that are a file: they get a photo-sized limit,
	// or a batch-sized one for the inbox's form without its script and for
	// a share from the phone that its worker missed, and must be multipart. The rest of the API keeps 64 KB and must be
	// JSON; every other route keeps 64 KB and form encoding. Separate
	// branches into the muxes, rather than one limit with exceptions inside
	// it, because a limit can only tighten (web.MaxBody): an upload must
	// never pass through the small one at all.
	//
	// And who is asking is decided per branch: a session cookie for the
	// pages, an API key for the API, never both.
	inner := web.Wrap(mux,
		mid.Authenticate(cfg.Log, cfg.Users),
		mid.Lang(),
	)
	apiInner := web.Wrap(api, mid.APIKey(cfg.Log, cfg.Users))

	shape := http.NewServeMux()
	shape.Handle(photoapp.UploadPattern, web.Wrap(inner, web.MaxBody(maxUpload), web.MultipartOnly()))
	shape.Handle(inboxapp.UploadPattern, web.Wrap(inner, web.MaxBody(inboxapp.MaxBytes), web.MultipartOnly()))
	shape.Handle(inboxapp.SendPattern, web.Wrap(inner, web.MaxBody(inboxapp.MaxSendBytes), web.MultipartOnly()))
	shape.Handle(inboxapp.SharePattern, web.Wrap(inner, web.MaxBody(inboxapp.MaxBytes), web.MultipartOnly()))
	shape.Handle(apiapp.UploadPattern, web.Wrap(apiInner, web.MaxBody(maxUpload), web.MultipartOnly()))
	shape.Handle("/api/", web.Wrap(apiInner, web.MaxBody(maxBody), web.JSONOnly()))
	shape.Handle("/", web.Wrap(inner, web.MaxBody(maxBody), web.FormEncodedOnly()))

	return web.Wrap(shape,
		web.RequestID(),
		web.Logging(cfg.Log),
		web.Panics(cfg.Log),
		web.SecureHeaders(page.Policy()),
		web.SameOriginOnly(cfg.BaseURL),
	), nil
}
