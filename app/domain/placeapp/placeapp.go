// Package placeapp is places on screen: the card a volunteer opens (card.go),
// and the stewards' screens -- the list of every place with its bands, and
// the form that adds, edits and removes one.
//
// The stewards' screens are behind sign-in and live under /steward/. The places'
// own addresses, /places/<slug>, are the volunteers' and come next; keeping
// the steward screens under a prefix of their own, and addressing a place by
// its ID there, means no slug ever has to be refused because a screen got to
// the word first ("new", "edit").
//
// The rules are placebus's. This package turns a form into placebus.Fields
// and a placebus.Invalid back into a sentence beside the input it is about;
// it decides nothing about what a place may be.
package placeapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// IndexPath is the stewards' front page, where signing in lands.
const IndexPath = "/steward"

// Places is what this app needs from the place rules.
type Places interface {
	All(ctx context.Context) ([]placebus.Place, error)
	ByID(ctx context.Context, id types.ID) (placebus.Place, error)
	BySlug(ctx context.Context, slug string) (placebus.Place, error)
	Children(ctx context.Context, id types.ID) ([]placebus.Place, error)
	Create(ctx context.Context, f placebus.Fields) (placebus.Place, error)
	Update(ctx context.Context, id types.ID, f placebus.Fields) (placebus.Place, error)
	SetSpot(ctx context.Context, id types.ID, spot *placebus.Spot) (placebus.Place, error)
	Delete(ctx context.Context, id types.ID) error
}

// SpeciesReader is what the place screens need from the species rules: the
// list to choose from and to show.
type SpeciesReader interface {
	All(ctx context.Context) ([]speciesbus.Species, error)
}

// PhotoReader is what the place card needs from the photo rules: a picture
// beside each plant on its "To plant" list.
type PhotoReader interface {
	ForSpecies(ctx context.Context, speciesID types.ID) ([]photobus.Photo, error)
}

// Listings is what they need from the listing rules.
type Listings interface {
	Set(ctx context.Context, placeID, speciesID types.ID, f listingbus.Fields) (listingbus.Listing, error)
	Remove(ctx context.Context, placeID, speciesID types.ID) error
	ForPlace(ctx context.Context, placeID types.ID) ([]listingbus.Listing, error)
}

// InboxCounter is what the stewards' front page needs from the inbox rules:
// how many photos are waiting, which is the inbox's only reminder.
type InboxCounter interface {
	Count(ctx context.Context) (int, error)
}

// Config is what this app needs.
type Config struct {
	Log      *slog.Logger
	Render   *page.Renderer
	Places   Places
	Species  SpeciesReader
	Listings Listings
	Photos   PhotoReader

	// Inbox may be nil, for no inbox: the front page then has no link to
	// one. NurseryStock is whether to link to the nursery stock, which
	// arrives through the inbox.
	Inbox        InboxCounter
	NurseryStock bool
}

type app struct {
	log      *slog.Logger
	render   *page.Renderer
	places   Places
	species  SpeciesReader
	listings Listings
	photos   PhotoReader
	inbox    InboxCounter
	nursery  bool
}

func newApp(cfg Config) app {
	return app{log: cfg.Log, render: cfg.Render, places: cfg.Places, species: cfg.Species, listings: cfg.Listings, photos: cfg.Photos, inbox: cfg.Inbox, nursery: cfg.NurseryStock}
}

// Routes mounts the stewards' screens, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := newApp(cfg)

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + IndexPath:                 a.index,
		"GET /steward/places/new":          a.newForm,
		"POST /steward/places":             a.create,
		"GET /steward/places/{id}/edit":    a.editForm,
		"POST /steward/places/{id}":        a.update,
		"POST /steward/places/{id}/delete": a.remove,
		"POST /steward/places/{id}/spot":   a.setSpot,

		// What grows at the place; plants.go.
		"GET /steward/places/{id}/plants":                   a.plants,
		"POST /steward/places/{id}/plants":                  a.setPlant,
		"POST /steward/places/{id}/plants/{species}/remove": a.removePlant,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type indexRow struct {
	ID, Slug string
	Name     types.Text
	Bands    []indexRow
}

type indexView struct {
	Places []indexRow
	Done   string // what just happened, from the redirect after a save

	// Inbox is whether there is one to link to, and Waiting how many
	// photos are in it.
	Inbox   bool
	Waiting int
	Nursery bool
}

func (a app) index(w http.ResponseWriter, r *http.Request) {
	all, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places for the stewards", err)

		return
	}

	// Top-level places in list order, each followed by its bands in theirs.
	// All is already in list order, so one pass keeps it.
	var v indexView
	at := map[types.ID]int{}

	for _, p := range all {
		if p.TopLevel() {
			at[p.ID] = len(v.Places)
			v.Places = append(v.Places, indexRow{ID: p.ID.String(), Slug: p.Slug, Name: p.Name})
		}
	}

	for _, p := range all {
		if i, ok := at[p.ParentID]; ok && !p.TopLevel() {
			v.Places[i].Bands = append(v.Places[i].Bands, indexRow{ID: p.ID.String(), Slug: p.Slug, Name: p.Name})
		}
	}

	// A count that cannot be read is no reason to keep a steward from the
	// places: logged, and the link shown without it.
	if a.inbox != nil {
		v.Inbox, v.Nursery = true, a.nursery

		if v.Waiting, err = a.inbox.Count(r.Context()); err != nil {
			a.log.WarnContext(r.Context(), "counting the inbox", "request_id", web.RequestIDFrom(r.Context()), "error", err)
		}
	}

	// The confirmation is a fixed sentence chosen by a word in the query, not
	// the query echoed: a link somebody else made cannot put words on this
	// page.
	switch r.URL.Query().Get("done") {
	case "added":
		v.Done = "Place added."
	case "saved":
		v.Done = "Changes saved."
	case "removed":
		v.Done = "Place removed."
	case "mapped":
		v.Done = "Its spot on the map is saved."
	case "unmapped":
		v.Done = "It is off the map."
	}

	a.render.Render(w, r, http.StatusOK, "steward-places", v)
}

// ------------------------------------------------------------------ the form

// option is one choice in a select.
type option struct {
	Value, Label string
	Selected     bool
}

// formView is the form as the steward sees it: every value as they typed it,
// so a refusal gives back exactly what was sent rather than what was saved.
type formView struct {
	ID    string // empty for a new place
	Title string

	Slug                       string
	NameEN, NameES             string
	PurposeEN, PurposeES       string
	ConditionsEN, ConditionsES string
	PhotoPointEN, PhotoPointES string
	Sort                       string
	Parents, Anchors           []option
	CanHaveParent              bool
	Problems                   map[string]string
	DeleteProblem              string
	PlaceName                  string

	// The map, for a place that is saved and stands on its own: where it
	// is now, if anywhere, and the other places on the map as landmarks.
	OnMap       bool
	Spot        *placebus.Spot
	Others      []placebus.Spot
	SpotProblem string
}

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	all, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places for a new one", err)

		return
	}

	v := formView{Title: "Add a place", Sort: "0", CanHaveParent: true}
	v.options(all, types.ID{}, r.URL.Query().Get("parent"), "")

	a.render.Render(w, r, http.StatusOK, "place-form", v)
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	p, all, ok := a.load(w, r)
	if !ok {
		return
	}

	v := viewOf(p)
	v.CanHaveParent = !hasBands(all, p.ID)
	v.options(all, p.ID, p.ParentID.String(), p.TrailAnchor)
	v.mapOf(p, all)

	a.render.Render(w, r, http.StatusOK, "place-form", v)
}

func (a app) create(w http.ResponseWriter, r *http.Request) {
	f, v, ok := a.read(w, r)
	if !ok {
		return
	}

	v.Title = "Add a place"

	if len(v.Problems) > 0 {
		a.again(w, r, v, types.ID{}, f)

		return
	}

	if _, err := a.places.Create(r.Context(), f); err != nil {
		a.refuse(w, r, v, types.ID{}, f, err)

		return
	}

	http.Redirect(w, r, IndexPath+"?done=added", http.StatusSeeOther)
}

func (a app) update(w http.ResponseWriter, r *http.Request) {
	p, _, ok := a.load(w, r)
	if !ok {
		return
	}

	f, v, ok := a.read(w, r)
	if !ok {
		return
	}

	v.ID, v.Slug, v.Title, v.PlaceName = p.ID.String(), p.Slug, "Edit "+p.Name.EN, p.Name.EN

	if len(v.Problems) > 0 {
		a.again(w, r, v, p.ID, f)

		return
	}

	if _, err := a.places.Update(r.Context(), p.ID, f); err != nil {
		a.refuse(w, r, v, p.ID, f, err)

		return
	}

	http.Redirect(w, r, IndexPath+"?done=saved", http.StatusSeeOther)
}

// remove deletes a place, and only with the box ticked. There is no script on
// these pages to ask "are you sure?", and a delete one tap away from Save is
// how a band a volunteer was told to plant in disappears.
func (a app) remove(w http.ResponseWriter, r *http.Request) {
	p, all, ok := a.load(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	problem := "Tick the box to confirm, then press Remove again."

	if r.PostFormValue("confirm") == "yes" {
		err := a.places.Delete(r.Context(), p.ID)

		invalid, isInvalid := errors.AsType[placebus.Invalid](err)

		switch {
		case err == nil:
			http.Redirect(w, r, IndexPath+"?done=removed", http.StatusSeeOther)

			return
		case isInvalid:
			problem = page.Sentence(invalid.Problem)
		default:
			a.fail(w, r, "removing a place", err)

			return
		}
	}

	v := viewOf(p)
	v.CanHaveParent = !hasBands(all, p.ID)
	v.options(all, p.ID, p.ParentID.String(), p.TrailAnchor)
	v.mapOf(p, all)
	v.DeleteProblem = problem

	a.render.Render(w, r, http.StatusUnprocessableEntity, "place-form", v)
}

// ------------------------------------------------------------------ the map

// The picker is drawn at a fixed size, so that a tap's position in it, which
// is what the browser sends, can be turned into the drawing's units. Without
// script that is the only way to know where on a picture somebody tapped:
// an <input type="image"> sends the tap as at.x and at.y in CSS pixels from
// its top-left corner, and nothing about how wide it was drawn. A map that
// stretched to fit would send a different number for the same spot on every
// screen. 280 is what fits inside a card on a 360-pixel phone; the box
// scrolls sideways on anything narrower rather than squash the drawing.
//
// app.css draws .map-pick at this size, and the template's width and height
// attributes are these numbers too; the three move together.
const (
	pickWidth  = 280
	pickHeight = pickWidth * placebus.MapHeight / placebus.MapWidth
)

// setSpot puts the place where the steward tapped, or takes it off the map.
//
// A tap at exactly 0,0 is refused rather than saved. It is what a browser
// sends when the picture is pressed from the keyboard, with no tap at all,
// and saving it would put the place in the north-west corner of the woods.
func (a app) setSpot(w http.ResponseWriter, r *http.Request) {
	p, all, ok := a.load(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	var spot *placebus.Spot
	done := "unmapped"

	if r.PostFormValue("off") != "yes" {
		x, errX := strconv.Atoi(r.PostFormValue("at.x"))
		y, errY := strconv.Atoi(r.PostFormValue("at.y"))

		if errX != nil || errY != nil || (x == 0 && y == 0) {
			a.spotAgain(w, r, p, all, "Tap the map where the place is. The spot is saved as soon as you tap.")

			return
		}

		spot = &placebus.Spot{
			X: (x*placebus.MapWidth + pickWidth/2) / pickWidth,
			Y: (y*placebus.MapHeight + pickHeight/2) / pickHeight,
		}
		done = "mapped"
	}

	_, err := a.places.SetSpot(r.Context(), p.ID, spot)

	invalid, isInvalid := errors.AsType[placebus.Invalid](err)

	switch {
	case err == nil:
		http.Redirect(w, r, IndexPath+"?done="+done, http.StatusSeeOther)
	case isInvalid:
		a.spotAgain(w, r, p, all, page.Sentence(invalid.Problem))
	default:
		a.fail(w, r, "setting a place's spot on the map", err)
	}
}

// spotAgain shows the form once more with a problem at the map.
func (a app) spotAgain(w http.ResponseWriter, r *http.Request, p placebus.Place, all []placebus.Place, problem string) {
	v := viewOf(p)
	v.CanHaveParent = !hasBands(all, p.ID)
	v.options(all, p.ID, p.ParentID.String(), p.TrailAnchor)
	v.mapOf(p, all)
	v.SpotProblem = problem

	a.render.Render(w, r, http.StatusUnprocessableEntity, "place-form", v)
}

// mapOf fills in the map for a place that can be on it.
func (v *formView) mapOf(p placebus.Place, all []placebus.Place) {
	if !p.TopLevel() {
		return
	}

	v.OnMap, v.Spot = true, p.Spot

	for _, o := range all {
		if o.ID != p.ID && o.TopLevel() && o.Spot != nil {
			v.Others = append(v.Others, *o.Spot)
		}
	}
}

// ------------------------------------------------------------------ the parts

// load reads the place named in the path, answering for it if there is none.
func (a app) load(w http.ResponseWriter, r *http.Request) (placebus.Place, []placebus.Place, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return placebus.Place{}, nil, false
	}

	p, err := a.places.ByID(r.Context(), id)
	switch {
	case errors.Is(err, placebus.ErrNotFound):
		http.Error(w, "That place is not here any more. It may have been removed. Go back to the list of places.", http.StatusNotFound)

		return placebus.Place{}, nil, false
	case err != nil:
		a.fail(w, r, "reading a place", err)

		return placebus.Place{}, nil, false
	}

	all, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return placebus.Place{}, nil, false
	}

	return p, all, true
}

// read turns the posted form into Fields, and into a view holding exactly
// what was typed. The only refusals here are the ones about the shape of an
// input -- a sort that is not a number -- and every rule about what a place
// may be is placebus's.
func (a app) read(w http.ResponseWriter, r *http.Request) (placebus.Fields, formView, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return placebus.Fields{}, formView{}, false
	}

	get := r.PostFormValue

	v := formView{
		Slug:   get("slug"),
		NameEN: get("name_en"), NameES: get("name_es"),
		PurposeEN: get("purpose_en"), PurposeES: get("purpose_es"),
		ConditionsEN: get("conditions_en"), ConditionsES: get("conditions_es"),
		PhotoPointEN: get("photo_point_en"), PhotoPointES: get("photo_point_es"),
		Sort:          get("sort"),
		CanHaveParent: true,
		Problems:      map[string]string{},
	}

	f := placebus.Fields{
		Slug:        v.Slug,
		Name:        types.Text{EN: v.NameEN, ES: v.NameES},
		Purpose:     types.Text{EN: v.PurposeEN, ES: v.PurposeES},
		Conditions:  types.Text{EN: v.ConditionsEN, ES: v.ConditionsES},
		PhotoPoint:  types.Text{EN: v.PhotoPointEN, ES: v.PhotoPointES},
		TrailAnchor: get("trail_anchor"),
	}

	if raw := get("parent"); raw != "" {
		id, err := types.ParseID(raw)
		if err != nil {
			v.Problems["parent"] = "Choose the place this is inside from the list."
		}

		f.ParentID = id
	}

	if s := strings.TrimSpace(v.Sort); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			v.Problems["sort"] = "Write the order as a whole number, such as 10. Lower comes first."
		}

		f.Sort = n
	}

	return f, v, true
}

// refuse shows the form again after a save that did not happen: beside the
// input a rule was about, or as a 500 when no rule was involved.
func (a app) refuse(w http.ResponseWriter, r *http.Request, v formView, self types.ID, f placebus.Fields, err error) {
	invalid, ok := errors.AsType[placebus.Invalid](err)
	if !ok {
		a.fail(w, r, "saving a place", err)

		return
	}

	v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
	a.again(w, r, v, self, f)
}

// again shows the form once more with its problems marked, holding what was
// typed. 422, so a run of refused saves is visible in the request log.
func (a app) again(w http.ResponseWriter, r *http.Request, v formView, self types.ID, f placebus.Fields) {
	all, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	v.CanHaveParent = self.Zero() || !hasBands(all, self)
	v.options(all, self, f.ParentID.String(), f.TrailAnchor)

	// The map is the place as saved, not as typed: the spot is its own
	// form, and this refusal did not touch it.
	for _, p := range all {
		if p.ID == self && !self.Zero() {
			v.mapOf(p, all)
		}
	}

	a.render.Render(w, r, http.StatusUnprocessableEntity, "place-form", v)
}

// options fills the two selects.
//
// A place can go inside a top-level place other than itself, and only when it
// has no bands of its own: placebus refuses the others, and a list that
// offers them is a list that invites a refusal.
func (v *formView) options(all []placebus.Place, self types.ID, parent, anchor string) {
	v.Parents = []option{{Value: "", Label: "Nothing: it stands on its own", Selected: parent == ""}}

	for _, p := range all {
		if p.TopLevel() && p.ID != self {
			v.Parents = append(v.Parents, option{Value: p.ID.String(), Label: p.Name.EN, Selected: p.ID.String() == parent})
		}
	}

	v.Anchors = []option{{Value: "", Label: "Not a station", Selected: anchor == ""}}

	for _, t := range placebus.TrailAnchors {
		v.Anchors = append(v.Anchors, option{Value: t, Label: stationName[t] + " (#" + t + ")", Selected: t == anchor})
	}
}

// stationName is how the public trail page heads each anchor's section,
// copied from it on 2026-09-30. The anchors are fixed by the QR codes on the
// boards (placebus.TrailAnchors); these are only their labels in the form.
var stationName = map[string]string{
	"joseph":   "St. Joseph",
	"reinisch": "Franz Reinisch",
	"pozzobon": "João Pozzobon",
	"francis":  "St. Francis",
	"therese":  "St. Thérèse of Lisieux",
}

func viewOf(p placebus.Place) formView {
	return formView{
		ID: p.ID.String(), Title: "Edit " + p.Name.EN, PlaceName: p.Name.EN,
		Slug:   p.Slug,
		NameEN: p.Name.EN, NameES: p.Name.ES,
		PurposeEN: p.Purpose.EN, PurposeES: p.Purpose.ES,
		ConditionsEN: p.Conditions.EN, ConditionsES: p.Conditions.ES,
		PhotoPointEN: p.PhotoPoint.EN, PhotoPointES: p.PhotoPoint.ES,
		Sort: strconv.Itoa(p.Sort),
	}
}

func hasBands(all []placebus.Place, id types.ID) bool {
	for _, p := range all {
		if p.ParentID == id && !id.Zero() {
			return true
		}
	}

	return false
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
