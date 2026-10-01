package stewardapp

import (
	"errors"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// This file is the screen where a steward makes and revokes their own API
// keys, for a program -- their Claude, a script -- to add plants and photos
// through /api/v1. Each steward sees only their own keys: a key acts as
// whoever made it, so nobody else has a reason to hold or end one.
//
// A new key is shown once, on the page the form returns, and never again;
// that page is not a redirect, because the key must not be in a URL, and it
// is marked no-store so the back button does not bring it back from a cache.

const keysPath = "/steward/keys"

func mountKeys(mux *http.ServeMux, a app, guard web.Middleware) {
	mux.Handle("GET "+keysPath, guard(http.HandlerFunc(a.keys)))
	mux.Handle("POST "+keysPath, guard(http.HandlerFunc(a.makeKey)))
	mux.Handle("POST "+keysPath+"/{id}/revoke", guard(http.HandlerFunc(a.revokeKey)))
}

type keyRow struct {
	ID, Name                   string
	Created, Expires, LastUsed string
}

type keysView struct {
	Keys    []keyRow
	NewKey  string // shown once
	Index   string // the API's index, to say where to start
	Name    string
	Problem string
	Done    string
	Max     int
}

func (a app) keys(w http.ResponseWriter, r *http.Request) {
	v := keysView{}
	if r.URL.Query().Get("done") == "revoked" {
		v.Done = "Key revoked. Anything still using it is refused from now on."
	}

	a.showKeys(w, r, http.StatusOK, v)
}

func (a app) makeKey(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	_, key, err := a.cfg.Users.CreateAPIKey(r.Context(), me.ID, r.PostFormValue("name"))

	invalid, isInvalid := errors.AsType[userbus.Invalid](err)

	switch {
	case isInvalid:
		a.showKeys(w, r, http.StatusUnprocessableEntity, keysView{Name: r.PostFormValue("name"), Problem: page.Sentence(invalid.Problem)})

		return
	case err != nil:
		a.fail(w, r, "making an API key", err)

		return
	}

	w.Header().Set("Cache-Control", "no-store")
	a.showKeys(w, r, http.StatusOK, keysView{NewKey: key})
}

func (a app) revokeKey(w http.ResponseWriter, r *http.Request) {
	me, _ := mid.StewardFrom(r.Context())

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	switch err := a.cfg.Users.RevokeAPIKey(r.Context(), me.ID, id); {
	case err == nil, errors.Is(err, userbus.ErrNotFound):
		// Already gone, or never this steward's: either way there is no
		// key of theirs by that id now, which is what was asked.
		http.Redirect(w, r, keysPath+"?done=revoked", http.StatusSeeOther)
	default:
		a.fail(w, r, "revoking an API key", err)
	}
}

func (a app) showKeys(w http.ResponseWriter, r *http.Request, status int, v keysView) {
	me, _ := mid.StewardFrom(r.Context())

	keys, err := a.cfg.Users.APIKeys(r.Context(), me.ID)
	if err != nil {
		a.fail(w, r, "listing API keys", err)

		return
	}

	for _, k := range keys {
		row := keyRow{ID: k.ID.String(), Name: k.Name, Created: day(k.CreatedAt), Expires: day(k.ExpiresAt), LastUsed: "never"}
		if !k.LastUsedAt.IsZero() {
			row.LastUsed = day(k.LastUsedAt)
		}

		v.Keys = append(v.Keys, row)
	}

	v.Index = a.cfg.BaseURL + "/api/v1"
	v.Max = userbus.MaxAPIKeys

	a.cfg.Render.Render(w, r, status, "steward-keys", v)
}

// day is a date as a steward reads it, in the garden's own time zone so that
// "used today" means today in Texas.
func day(t time.Time) string {
	return t.In(garden).Format("2 January 2006")
}

// garden is the garden's time zone. Loaded once; on a machine with no zone
// database it falls back to UTC rather than failing a page over a date.
var garden = func() *time.Location {
	if loc, err := time.LoadLocation("America/Chicago"); err == nil {
		return loc
	}

	return time.UTC
}()
