// Package apiapp is the JSON API at /api/v1: how a program -- a script, or a
// steward's own Claude -- adds plants, their photos and where they grow
// without typing them into the screens, and sorts the photo inbox (inbox.go).
//
// # The index is the route table
//
// GET /api/v1 answers with every endpoint, the fields each takes and the
// values a field may have. It is built from the same list that mounts the
// routes (endpoints, below), so an endpoint cannot exist without being in the
// index or be in the index without existing; a test holds both ways. A
// program starts there rather than from documentation that may be older than
// the binary.
//
// # What a key can and cannot do
//
// A key acts as the steward who made it (mid.APIKey), and only through the
// import rules: speciesbus.Import, photobus.Import and listingbus.Import, and
// inboxbus.Sort, which files a photo the way photobus.Import would.
// None will confirm a plant, check a photo, or tell volunteers to pull one. A batch from a program lands as "not yet confirmed"
// and "not checked", and a person ticks those on the screens after looking --
// the wingstem lesson, which applies to Claude as much as to a nursery tag.
//
// Nothing here removes anything, either. Taking a plant or a photo away is
// done by a person on the screens, where the confirmation box is.
package apiapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Prefix is where the API lives. The version is in the path, so a v2 can sit
// beside it while a batch script written for v1 still works.
const Prefix = "/api/v1"

// UploadPattern is the API's one route that takes a file. The muxer gives it
// the photo-sized limit and lets it be multipart, as for the screens'
// upload route.
const UploadPattern = "POST " + Prefix + "/species/{slug}/photos"

// uploadTime is as for the screens' upload: a batch from a laptop on the
// house's wifi is quick, but nothing here should assume so.
const uploadTime = 5 * time.Minute

// Species is what the API needs from the species rules.
type Species interface {
	All(ctx context.Context) ([]speciesbus.Species, error)
	BySlug(ctx context.Context, slug string) (speciesbus.Species, error)
	Import(ctx context.Context, f speciesbus.Fields) (speciesbus.Imported, error)
}

// Places is what it needs from the place rules.
type Places interface {
	All(ctx context.Context) ([]placebus.Place, error)
	BySlug(ctx context.Context, slug string) (placebus.Place, error)
	Import(ctx context.Context, f placebus.Fields) (placebus.Imported, error)
	SetSpot(ctx context.Context, id types.ID, spot *placebus.Spot) (placebus.Place, error)
}

// Listings is what it needs from the listing rules.
type Listings interface {
	ForPlace(ctx context.Context, placeID types.ID) ([]listingbus.Listing, error)
	Import(ctx context.Context, placeID, speciesID types.ID, f listingbus.Fields) (listingbus.Imported, error)
	Unlist(ctx context.Context, placeID, speciesID types.ID) (listingbus.Outcome, error)
}

// Photos is what it needs from the photo rules.
type Photos interface {
	ByID(ctx context.Context, id types.ID) (photobus.Photo, error)
	ForSpecies(ctx context.Context, speciesID types.ID) ([]photobus.Photo, error)
	Import(ctx context.Context, speciesID types.ID, f photobus.Fields, data []byte) (photobus.Photo, bool, error)
	Amend(ctx context.Context, id types.ID, f photobus.Fields) (photobus.Amended, error)
}

// Config is what this app needs.
type Config struct {
	Log      *slog.Logger
	Species  Species
	Places   Places
	Photos   Photos
	Listings Listings

	// Inbox may be nil, and its endpoints are then neither mounted nor in
	// the index. Nursery likewise; it is only offered with an Inbox, which
	// is how stock arrives.
	Inbox   Inbox
	Nursery Nursery

	// BaseURL is the public origin, for the absolute links in answers: a
	// program reading them may be anywhere.
	BaseURL string
}

type app struct {
	log      *slog.Logger
	species  Species
	places   Places
	photos   Photos
	listings Listings
	inbox    Inbox
	nursery  Nursery
	base     string
}

// Routes mounts the API on its own mux. Each route that needs a key is
// behind mid.RequireKey; mid.APIKey, which reads the key, is the muxer's to
// put around the whole of it.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{log: cfg.Log, species: cfg.Species, places: cfg.Places, photos: cfg.Photos, listings: cfg.Listings, inbox: cfg.Inbox, nursery: cfg.Nursery, base: cfg.BaseURL}
	if a.inbox == nil {
		a.nursery = nil
	}
	require := mid.RequireKey()

	for _, e := range a.endpoints() {
		h := http.Handler(e.handler)
		if e.NeedsKey {
			h = require(h)
		}

		mux.Handle(e.Method+" "+e.Path, h)
	}

	// The index with a trailing slash too, since that is how a person types
	// it; and everything else under /api is a JSON 404 that says where the
	// index is, rather than the site's HTML one.
	mux.HandleFunc("GET "+Prefix+"/{$}", a.index)
	mux.HandleFunc("/api/", a.notFound)
}

// ------------------------------------------------------------------ the index

// Field is one input an endpoint takes, as the index describes it.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Values      []string `json:"values,omitempty"`
	Description string   `json:"description"`
}

// Body is what an endpoint expects to be sent.
type Body struct {
	Encoding string  `json:"encoding"` // "json" or "multipart"
	Fields   []Field `json:"fields"`
}

// Endpoint is one route, as the index describes it and as Routes mounts it.
type Endpoint struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Summary  string `json:"summary"`
	NeedsKey bool   `json:"needs_key"`
	Body     *Body  `json:"body,omitempty"`
	Returns  string `json:"returns"`

	handler http.HandlerFunc
}

// Index is the answer to GET /api/v1.
type Index struct {
	API            string     `json:"api"`
	Version        string     `json:"version"`
	BaseURL        string     `json:"base_url"`
	Authentication string     `json:"authentication"`
	Rules          []string   `json:"rules"`
	Endpoints      []Endpoint `json:"endpoints"`
}

func (a app) endpoints() []Endpoint {
	text := func(name, what string, required bool) Field {
		return Field{Name: name, Type: "object {en, es}", Required: required, Description: what + ` In English as "en"; "es" is Spanish, and only from a native speaker -- leave it out rather than translate by machine.`}
	}

	size := func(name, what string) Field {
		return Field{Name: name, Type: "object {min, max}", Description: what + " In whole inches, a range or one number in both. 0 for not known."}
	}

	all := []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix, Summary: "This index: every endpoint, what it takes, and the rules.",
			Returns: "This document.", handler: a.index,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/places", NeedsKey: true,
			Summary: "Every place in the garden, in the stewards' order, with what its card says about it: what it is for, its conditions, where to stand for its photo, and where it is on the map. A photo's place is one of these slugs.",
			Returns: `{"places": [place], "map": {"width", "height"}}. A place is {slug, name, parent, purpose, conditions, photo_point, trail_anchor, sort, spot: {x, y} or absent, card_url}.`, handler: a.listPlaces,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/places/{slug}", NeedsKey: true,
			Summary: "One place, as the list gives it, with the smaller places inside it.",
			Returns: `{"place": place, "inside": [slug]}`, handler: a.onePlace,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/places/{slug}", NeedsKey: true,
			Summary: "Add the place at this address, or change it. Send the whole place: a field left out is emptied. Sending what is already there changes nothing. The slug is the place's address on its card, printed on stakes and QR boards, so it is chosen once and never changes. Where it is on the map is set apart, with PUT .../spot.",
			Body: &Body{Encoding: "json", Fields: []Field{
				text("name", "What people call it.", true),
				{Name: "parent", Type: "string", Description: "The slug of the place this one is part of, such as a band of the rain garden; empty for one that stands on its own. One level deep at most."},
				text("purpose", `What it is for: "the backdrop of the gathering space".`, false),
				text("conditions", "What a planter needs to know: sun, slope, soil, wet or dry.", false),
				text("photo_point", `Where to stand, and which way to face, for its photo re-shot each season: "from the fire-pit bench, facing the wall".`, false),
				{Name: "trail_anchor", Type: "string", Values: placebus.TrailAnchors, Description: "The station on the public trail page this place is, if it is one."},
				{Name: "sort", Type: "integer", Description: "Its order in the list, lowest first; ties go by name."},
			}},
			Returns: `201 {"outcome": "created", "place": place}, or 200 with "updated" or "unchanged".`,
			handler: a.putPlace,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/places/{slug}/spot", NeedsKey: true,
			Summary: "Put a place on the map, or move it. The map is a drawing of the property, not a survey: x is across from its west edge and y down from its north edge, in the drawing's units (the list's map gives its size). Place it by its neighbours' spots. Only a place that stands on its own is on the map.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "x", Type: "integer", Required: true, Description: "Across from the west edge."},
				{Name: "y", Type: "integer", Required: true, Description: "Down from the north edge."},
			}},
			Returns: `200 {"place": place}`, handler: a.putSpot,
		},
		{
			Method: http.MethodDelete, Path: Prefix + "/places/{slug}/spot", NeedsKey: true,
			Summary: "Take a place off the map. The place itself stays.",
			Returns: `200 {"place": place}`, handler: a.deleteSpot,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/places/{slug}/plants", NeedsKey: true,
			Summary: "What is listed at a place: each plant, what to do with it there, and whether it is part of the planting.",
			Returns: `{"place": slug, "plants": [{species, action, planned, note, card_url}]}`, handler: a.placePlants,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/places/{slug}/plants/{species}", NeedsKey: true,
			Summary: "List a plant at a place, or change how it is listed. The plant must already be added. Sending what is already there changes nothing. A listing is on the place card as soon as it is made, with nothing for a steward to tick first: a pull tells volunteers to take the plant out the next time they are there, so say why in the note.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "action", Type: "string", Required: true, Values: actionNames(), Description: "protect: leave it. pull: take it out, root and all. careful: it stays or goes as the note says, but handle it with gloves on."},
				{Name: "planned", Type: "boolean", Description: "True when there is planting still to do: none of it is in the ground yet, or more is going in (say how many in the note). It goes on the place's To plant list. False for a plant already growing here, planted or come up on its own: it is protected and on the flowering calendar either way."},
				text("note", `What to know about it here, such as "6 plants, at the shady end".`, false),
			}},
			Returns: `201 {"outcome": "created", "place": slug, "listing": {...}}, or 200 with "updated" or "unchanged".`,
			handler: a.putPlacePlant,
		},
		{
			Method: http.MethodDelete, Path: Prefix + "/places/{slug}/plants/{species}", NeedsKey: true,
			Summary: "Take a plant off a place's list, for one listed in the wrong place. It is off the place card at once. The plant itself, its photos and its other places stay.",
			Returns: `200 {"outcome": "removed", "place": slug, "species": slug}, or "unchanged" when it was not listed there.`,
			handler: a.deletePlacePlant,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/species", NeedsKey: true,
			Summary: "Every plant, by common name. Read this before adding, to see what is already here.",
			Returns: `{"species": [plant]}`, handler: a.listSpecies,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/species/{slug}", NeedsKey: true,
			Summary: "One plant, with its photos.",
			Returns: `{"species": plant} with "photos": [photo]`, handler: a.oneSpecies,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/species/{slug}/flowering", NeedsKey: true,
			Summary: "The plant's flowering record, read from its photos: for each year, newest first, the first and last day it was photographed in flower and the first day it was photographed at all, each with the photo, where it was taken and whether it is checked. Our own dated photos from anywhere count; borrowed ones never do. Volunteers' cards show only the dates from checked photos.",
			Returns: `{"species": slug, "years": [{year, first_flower, last_flower, first_seen, in_flower, seen}]}, each date {photo_id, taken_on, place, elsewhere, taken_where, checked} or absent.`,
			handler: a.flowering,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/species/{slug}", NeedsKey: true,
			Summary: "Add the plant at this address, or change it. Sending what is already there changes nothing. A change to a confirmed plant takes its confirmation away, for a steward to give again.",
			Body: &Body{Encoding: "json", Fields: []Field{
				text("common", "The common name.", true),
				{Name: "scientific", Type: "string", Description: `The scientific name, such as "Callirhoe involucrata".`},
				{Name: "status", Type: "string", Required: true, Values: statusNames(), Description: "What kind of plant it is here."},
				text("flower_color", "The flower's colour in words.", false),
				{Name: "swatches", Type: "array of string", Description: `The flower's colours as #rrggbb, for the bloom strip. "#ffffff" for white.`},
				{Name: "bloom", Type: "array of integer", Description: "The months it flowers, 1 for January to 12 for December."},
				size("height", "Mature height."),
				size("width", "Mature width."),
				{Name: "light", Type: "array of string", Values: names(lightNames), Description: "Every light it grows in."},
				{Name: "water", Type: "array of string", Values: names(waterNames), Description: "Every soil moisture it grows in."},
				text("note", "What a planter needs to know: how to plant it, what to watch for.", false),
				{Name: "sources", Type: "array of object {label, url}", Description: "What the ID was checked against, such as the Wildflower Center's page. A url is optional."},
			}},
			Returns: `201 {"outcome": "created", "species": plant}, or 200 with "updated" or "unchanged". "confirmation_cleared": true when a change took a confirmation away.`,
			handler: a.putSpecies,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/species/{slug}/photos", NeedsKey: true,
			Summary: "Add a photo of the plant. It arrives not checked: volunteers see it once a steward has compared it with the plant and ticked Checked. The same photo sent twice is kept once.",
			Body: &Body{Encoding: "multipart", Fields: []Field{
				{Name: "photo", Type: "file", Required: true, Description: fmt.Sprintf("A JPEG or PNG, at most %d MB, as the camera saved it. Location and camera details are removed from every copy shown.", photobus.MaxBytes>>20)},
				{Name: "kind", Type: "string", Required: true, Values: kindNames(), Description: "What it shows. young: the seedling; leaf, flower: close-ups; mature: the grown plant at full size, in a garden; winter: how it looks in winter, or its seed head."},
				{Name: "source", Type: "string", Required: true, Values: []string{string(photobus.Ours), string(photobus.Borrowed)}, Description: "ours: taken by a steward, here or elsewhere. borrowed: from Wikimedia Commons, iNaturalist or the like, under an open licence."},
				{Name: "credit", Type: "string", Description: "The author, as the photo's page gives it. Required when borrowed; for ours, leave it out unless the photographer wants to be named."},
				{Name: "source_url", Type: "string", Description: "The photo's own page, https. Required when borrowed."},
				{Name: "license", Type: "string", Description: `Its licence's short name, such as "CC BY-SA 4.0". Required when borrowed.`},
				{Name: "taken_month", Type: "integer", Description: "1 to 12. Leave it, taken_year and taken_on out to use the date the camera recorded."},
				{Name: "taken_on", Type: "string", Description: "The day it was taken, such as 2026-04-03, for the flowering record, when the camera did not record it. The month and year follow it."},
				{Name: "in_flower", Type: "boolean", Description: "True when the plant is in flower in the photo, whatever the kind. A flower photo always is."},
				{Name: "taken_year", Type: "integer", Description: "Four figures."},
				{Name: "place", Type: "string", Description: "Where it was taken, as a place slug from /api/v1/places. For ours, taken on the property."},
				{Name: "taken_where", Type: "string", Description: "For ours taken off the property: where, by name, such as a park. The card names it, and shows a photo taken here before it. Not with place."},
				{Name: "elsewhere", Type: "boolean", Description: "true for ours taken off the property when nobody said where. taken_where implies it."},
			}},
			Returns: `201 {"photo": photo, "duplicate": false}, or 200 with the photo already kept and "duplicate": true.`,
			handler: a.addPhoto,
		},
		{
			Method: http.MethodPatch, Path: Prefix + "/photos/{id}", NeedsKey: true,
			Summary: "Change what is said about a plant photo: send only the fields that change, as the photo upload names them. A change to a checked photo takes the check away, for a steward to give again after looking; sending what is already there changes nothing. The picture itself never changes: a different photo is a new one. The id is a photo's id, as GET /api/v1/species/{slug} lists them.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "kind", Type: "string", Values: kindNames(), Description: "What it shows."},
				{Name: "source", Type: "string", Values: []string{string(photobus.Ours), string(photobus.Borrowed)}, Description: "ours or borrowed."},
				{Name: "credit", Type: "string", Description: "The author."},
				{Name: "source_url", Type: "string", Description: "The photo's own page, for a borrowed one."},
				{Name: "license", Type: "string", Description: "Its licence's short name, for a borrowed one."},
				{Name: "taken_month", Type: "integer", Description: "1 to 12, or 0 for not known."},
				{Name: "taken_year", Type: "integer", Description: "Four figures, or 0 for not known."},
				{Name: "taken_on", Type: "string", Description: `The day it was taken, such as "2026-04-03"; "" for not known. Sending only taken_month or taken_year says the day is not known.`},
				{Name: "in_flower", Type: "boolean", Description: "The plant in flower in the photo, whatever its kind. A flower photo always is."},
				{Name: "place", Type: "string", Description: `Where on the property it was taken, as a place slug; "" for not said.`},
				{Name: "elsewhere", Type: "boolean", Description: "Taken off the property."},
				{Name: "taken_where", Type: "string", Description: "Where off the property, such as a park's name."},
			}},
			Returns: `200 {"outcome": "updated" or "unchanged", "photo": photo}. "check_cleared": true when the change took a check away.`,
			handler: a.patchPhoto,
		},
	}

	if a.inbox != nil {
		all = append(all, a.inboxEndpoints()...)
	}

	if a.nursery != nil {
		all = append(all, a.nurseryEndpoints()...)
	}

	return all
}

func (a app) index(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, Index{
		API:     "Garden stewards",
		Version: "v1",
		BaseURL: a.base,
		Authentication: "Send Authorization: Bearer <key> on every endpoint but this one. A steward makes a key at " +
			a.base + "/steward/keys; it acts as that steward, is shown once, and lasts 90 days.",
		Rules: []string{
			"Nothing sent here is confirmed or checked. A steward confirms a plant, and checks a photo, on its screen after looking. Sending confirmed or checked is refused.",
			"Nothing is removed through the API, an inbox photo included. A person does that on the screens.",
			"An import never marks a plant to pull at a place. A steward does that on the place's Plants screen.",
			"Sending a plant exactly as it is already changes nothing, and the same photo twice is kept once, so a batch can safely be sent again.",
			"A plant's slug is its address, /plants/<slug>, and cannot change once made: lower-case letters, numbers and hyphens, such as winecup.",
			"Sizes are whole inches. Months are 1 to 12.",
			`Every refusal is {"error": {"field": "...", "problem": "..."}}, with a sentence saying what to fix.`,
		},
		Endpoints: a.endpoints(),
	})
}

func (a app) notFound(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusNotFound, web.Problem("",
		fmt.Sprintf("There is no %s %s. GET %s lists every endpoint.", r.Method, r.URL.Path, Prefix)))
}

// ------------------------------------------------------------------ places

// PlaceJSON is a place as the API shows it.
// ------------------------------------------------------------------ species

// TextJSON is a types.Text.
type TextJSON struct {
	EN string `json:"en"`
	ES string `json:"es,omitempty"`
}

// SizeJSON is a speciesbus.Size, in inches.
type SizeJSON struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// SourceJSON is a speciesbus.Source.
type SourceJSON struct {
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
}

// SpeciesIn is the body of a PUT.
type SpeciesIn struct {
	// Slug may be sent, and must then be the one in the path.
	Slug string `json:"slug,omitempty"`

	Common      TextJSON     `json:"common"`
	Scientific  string       `json:"scientific"`
	Status      string       `json:"status"`
	FlowerColor TextJSON     `json:"flower_color"`
	Swatches    []string     `json:"swatches"`
	Bloom       []int        `json:"bloom"`
	Height      SizeJSON     `json:"height"`
	Width       SizeJSON     `json:"width"`
	Light       []string     `json:"light"`
	Water       []string     `json:"water"`
	Note        TextJSON     `json:"note"`
	Sources     []SourceJSON `json:"sources"`

	// Confirmed is accepted so that sending it is refused with a reason
	// (speciesbus.Import) rather than as a field nobody has heard of.
	Confirmed bool `json:"confirmed,omitempty"`
}

// SpeciesJSON is a plant as the API shows it.
type SpeciesJSON struct {
	Slug        string       `json:"slug"`
	Common      TextJSON     `json:"common"`
	Scientific  string       `json:"scientific,omitempty"`
	Status      string       `json:"status"`
	Confirmed   bool         `json:"confirmed"`
	FlowerColor TextJSON     `json:"flower_color"`
	Swatches    []string     `json:"swatches"`
	Bloom       []int        `json:"bloom"`
	Height      SizeJSON     `json:"height"`
	Width       SizeJSON     `json:"width"`
	Light       []string     `json:"light"`
	Water       []string     `json:"water"`
	Note        TextJSON     `json:"note"`
	Sources     []SourceJSON `json:"sources"`
	CardURL     string       `json:"card_url"`
	StewardURL  string       `json:"steward_url"`
	PhotosURL   string       `json:"photos_url"`
	Photos      []PhotoJSON  `json:"photos,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

func (a app) listSpecies(w http.ResponseWriter, r *http.Request) {
	all, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing species", err)

		return
	}

	out := []SpeciesJSON{}
	for _, sp := range all {
		out = append(out, a.speciesOf(sp))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"species": out})
}

func (a app) oneSpecies(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.loadSpecies(w, r)
	if !ok {
		return
	}

	out, err := a.withPhotos(r.Context(), sp)
	if err != nil {
		a.fail(w, r, "reading a species' photos", err)

		return
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"species": out})
}

func (a app) putSpecies(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	var in SpeciesIn
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	if in.Slug != "" && in.Slug != slug {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("slug",
			fmt.Sprintf("The body says %q and the address says %q. A plant's slug cannot change; send it to /api/v1/species/%s.", in.Slug, slug, in.Slug)))

		return
	}

	f, field, problem := fieldsOf(slug, in)
	if problem != "" {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(field, problem))

		return
	}

	got, err := a.species.Import(r.Context(), f)

	invalid, isInvalid := errors.AsType[speciesbus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "importing a species", err)

		return
	}

	steward, _ := mid.StewardFrom(r.Context())
	if got.Outcome != speciesbus.Unchanged {
		a.log.InfoContext(r.Context(), "species imported", "slug", got.Species.Slug, "outcome", got.Outcome,
			"confirmation_cleared", got.Unconfirmed, "user_id", steward.ID.String())
	}

	status := http.StatusOK
	if got.Outcome == speciesbus.Created {
		status = http.StatusCreated
	}

	web.WriteJSON(w, status, map[string]any{
		"outcome":              got.Outcome,
		"confirmation_cleared": got.Unconfirmed,
		"species":              a.speciesOf(got.Species),
	})
}

// fieldsOf turns a body into speciesbus.Fields, naming the field when a value
// is not one of those the index lists. The rules about what makes a good
// plant are speciesbus's; this is only the shape.
func fieldsOf(slug string, in SpeciesIn) (speciesbus.Fields, string, string) {
	f := speciesbus.Fields{
		Slug:        slug,
		Common:      types.Text{EN: in.Common.EN, ES: in.Common.ES},
		Scientific:  in.Scientific,
		Status:      speciesbus.Status(in.Status),
		Confirmed:   in.Confirmed,
		FlowerColor: types.Text{EN: in.FlowerColor.EN, ES: in.FlowerColor.ES},
		Swatches:    in.Swatches,
		Height:      speciesbus.Size{Min: in.Height.Min, Max: in.Height.Max},
		Width:       speciesbus.Size{Min: in.Width.Min, Max: in.Width.Max},
		Note:        types.Text{EN: in.Note.EN, ES: in.Note.ES},
	}

	if !slices.Contains(speciesbus.Statuses, f.Status) {
		return f, "status", fmt.Sprintf("Status is one of %s.", strings.Join(statusNames(), ", "))
	}

	for _, m := range in.Bloom {
		if m < 1 || m > 12 {
			return f, "bloom", fmt.Sprintf("Bloom months are 1 to 12; %d is not one.", m)
		}
		f.Bloom = f.Bloom.With(time.Month(m))
	}

	for _, l := range in.Light {
		v, ok := lightNames[l]
		if !ok {
			return f, "light", fmt.Sprintf("Light is any of %s; %q is not one.", strings.Join(names(lightNames), ", "), l)
		}
		f.Light |= v
	}

	for _, wa := range in.Water {
		v, ok := waterNames[wa]
		if !ok {
			return f, "water", fmt.Sprintf("Water is any of %s; %q is not one.", strings.Join(names(waterNames), ", "), wa)
		}
		f.Water |= v
	}

	for _, s := range in.Sources {
		f.Sources = append(f.Sources, speciesbus.Source{Label: s.Label, URL: s.URL})
	}

	return f, "", ""
}

func (a app) speciesOf(sp speciesbus.Species) SpeciesJSON {
	out := SpeciesJSON{
		Slug: sp.Slug, Common: textOf(sp.Common), Scientific: sp.Scientific, Status: string(sp.Status),
		Confirmed: sp.Confirmed, FlowerColor: textOf(sp.FlowerColor), Swatches: append([]string{}, sp.Swatches...),
		Bloom: []int{}, Height: SizeJSON(sp.Height), Width: SizeJSON(sp.Width),
		Light: []string{}, Water: []string{}, Note: textOf(sp.Note), Sources: []SourceJSON{},
		CardURL:    a.base + "/plants/" + sp.Slug,
		StewardURL: a.base + "/steward/species/" + sp.ID.String() + "/edit",
		PhotosURL:  a.base + "/steward/species/" + sp.ID.String() + "/photos",
		CreatedAt:  sp.CreatedAt.UTC(), UpdatedAt: sp.UpdatedAt.UTC(),
	}

	for _, m := range sp.Bloom.Each() {
		out.Bloom = append(out.Bloom, int(m))
	}

	for _, name := range names(lightNames) {
		if sp.Light&lightNames[name] != 0 {
			out.Light = append(out.Light, name)
		}
	}

	for _, name := range names(waterNames) {
		if sp.Water&waterNames[name] != 0 {
			out.Water = append(out.Water, name)
		}
	}

	for _, s := range sp.Sources {
		out.Sources = append(out.Sources, SourceJSON{Label: s.Label, URL: s.URL})
	}

	return out
}

func (a app) withPhotos(ctx context.Context, sp speciesbus.Species) (SpeciesJSON, error) {
	out := a.speciesOf(sp)

	photos, err := a.photos.ForSpecies(ctx, sp.ID)
	if err != nil {
		return out, err
	}

	places, err := a.places.All(ctx)
	if err != nil {
		return out, err
	}

	slugs := map[types.ID]string{}
	for _, p := range places {
		slugs[p.ID] = p.Slug
	}

	out.Photos = []PhotoJSON{}
	for _, p := range photos {
		out.Photos = append(out.Photos, a.photoOf(p, slugs))
	}

	return out, nil
}

// ------------------------------------------------------------------ photos

// PhotoJSON is a photo as the API shows it.
type PhotoJSON struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Source     string    `json:"source"`
	Credit     string    `json:"credit,omitempty"`
	SourceURL  string    `json:"source_url,omitempty"`
	License    string    `json:"license,omitempty"`
	TakenYear  int       `json:"taken_year,omitempty"`
	TakenMonth int       `json:"taken_month,omitempty"`
	TakenOn    string    `json:"taken_on,omitempty"`
	InFlower   bool      `json:"in_flower"`
	Place      string    `json:"place,omitempty"`
	Elsewhere  bool      `json:"elsewhere"`
	TakenWhere string    `json:"taken_where,omitempty"`
	Checked    bool      `json:"checked"`
	SHA256     string    `json:"sha256,omitempty"`
	LargeURL   string    `json:"large_url"`
	SmallURL   string    `json:"small_url"`
	FullURL    string    `json:"full_url"`
	EditURL    string    `json:"edit_url"`
	CreatedAt  time.Time `json:"created_at"`
}

func (a app) photoOf(p photobus.Photo, places map[types.ID]string) PhotoJSON {
	return PhotoJSON{
		ID: p.ID.String(), Kind: string(p.Kind), Source: string(p.Source),
		Credit: p.Credit, SourceURL: p.SourceURL, License: p.License,
		TakenYear: p.TakenYear, TakenMonth: p.TakenMonth, Place: places[p.PlaceID],
		TakenOn: photobus.DayOf(p.TakenAt), InFlower: p.InFlower,
		Elsewhere: p.Elsewhere, TakenWhere: p.TakenWhere,
		Checked: p.Checked, SHA256: p.SHA256,
		LargeURL:  a.base + "/photos/" + p.ID.String() + "/large.jpg",
		SmallURL:  a.base + "/photos/" + p.ID.String() + "/small.jpg",
		FullURL:   a.base + "/photos/" + p.ID.String() + "/" + photobus.ServedName(photobus.Full, p.Format),
		EditURL:   a.base + "/steward/photos/" + p.ID.String() + "/edit",
		CreatedAt: p.CreatedAt.UTC(),
	}
}

func (a app) addPhoto(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTime))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadTime + time.Minute))

	sp, ok := a.loadSpecies(w, r)
	if !ok {
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			web.WriteJSON(w, http.StatusRequestEntityTooLarge, web.Problem("photo", fmt.Sprintf("The photo is larger than %d MB.", photobus.MaxBytes>>20)))

			return
		}

		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", "The body could not be read as a multipart form. Send it as curl -F does."))

		return
	}
	defer r.MultipartForm.RemoveAll()

	places, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places", err)

		return
	}

	f, field, problem := photoFieldsOf(r, places)
	if problem != "" {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(field, problem))

		return
	}

	file, _, err := r.FormFile("photo")
	if err != nil {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("photo", "Send the photo as a file field called photo."))

		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, photobus.MaxBytes+1))
	if err != nil || len(data) > photobus.MaxBytes {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("photo", "The photo did not arrive whole, or is too large."))

		return
	}

	p, duplicate, err := a.photos.Import(r.Context(), sp.ID, f, data)

	invalid, isInvalid := errors.AsType[photobus.Invalid](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "importing a photo", err)

		return
	}

	slugs := map[types.ID]string{}
	for _, pl := range places {
		slugs[pl.ID] = pl.Slug
	}

	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	} else {
		steward, _ := mid.StewardFrom(r.Context())
		a.log.InfoContext(r.Context(), "photo imported", "species", sp.Slug, "photo_id", p.ID.String(), "kind", p.Kind, "user_id", steward.ID.String())
	}

	web.WriteJSON(w, status, map[string]any{"photo": a.photoOf(p, slugs), "duplicate": duplicate})
}

// photoFieldsOf reads the multipart fields into photobus.Fields.
func photoFieldsOf(r *http.Request, places []placebus.Place) (photobus.Fields, string, string) {
	f := photobus.Fields{
		Kind:      photobus.Kind(r.PostFormValue("kind")),
		Source:    photobus.Source(r.PostFormValue("source")),
		Credit:    r.PostFormValue("credit"),
		SourceURL: r.PostFormValue("source_url"),
		License:   r.PostFormValue("license"),

		// Read so that sending it is refused with photobus.Import's reason.
		Checked: r.PostFormValue("checked") != "" && r.PostFormValue("checked") != "false",

		InFlower: r.PostFormValue("in_flower") != "" && r.PostFormValue("in_flower") != "false",
	}

	day, err := photobus.Day(strings.TrimSpace(r.PostFormValue("taken_on")))
	if err != nil {
		return f, "taken_on", "taken_on is a day, such as 2026-04-03."
	}
	f.TakenAt = day

	for name, dst := range map[string]*int{"taken_month": &f.TakenMonth, "taken_year": &f.TakenYear} {
		if s := strings.TrimSpace(r.PostFormValue(name)); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				return f, name, fmt.Sprintf("%s is a whole number; %q is not one.", name, s)
			}
			*dst = n
		}
	}

	if slug := strings.TrimSpace(r.PostFormValue("place")); slug != "" {
		i := slices.IndexFunc(places, func(p placebus.Place) bool { return p.Slug == slug })
		if i < 0 {
			return f, "place", fmt.Sprintf("No place has the slug %q. GET %s/places lists them.", slug, Prefix)
		}
		f.PlaceID = places[i].ID
	}

	f.TakenWhere = r.PostFormValue("taken_where")
	f.Elsewhere = r.PostFormValue("elsewhere") == "true"

	// The screens make these one choice, so they cannot both be said
	// there; here they can, and photobus would quietly keep the place.
	if !f.PlaceID.Zero() && (f.Elsewhere || strings.TrimSpace(f.TakenWhere) != "") {
		return f, "taken_where", "place is a place here, and taken_where or elsewhere says the photo was taken off the property. Send one or the other."
	}

	return f, "", ""
}

// ------------------------------------------------------------------ the parts

var lightNames = map[string]speciesbus.Light{"full_sun": speciesbus.FullSun, "part_shade": speciesbus.PartShade, "shade": speciesbus.Shade}

var waterNames = map[string]speciesbus.Water{"dry": speciesbus.Dry, "moist": speciesbus.Moist, "wet": speciesbus.Wet}

// names is a vocabulary's words, in the order of their bits: sun to shade,
// dry to wet, which is how a person reads them.
func names[V ~uint8](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}

	slices.SortFunc(out, func(x, y string) int { return cmp.Compare(m[x], m[y]) })

	return out
}

func statusNames() []string {
	out := make([]string, 0, len(speciesbus.Statuses))
	for _, s := range speciesbus.Statuses {
		out = append(out, string(s))
	}

	return out
}

func kindNames() []string {
	out := make([]string, 0, len(photobus.Kinds))
	for _, k := range photobus.Kinds {
		out = append(out, string(k))
	}

	return out
}

func textOf(t types.Text) TextJSON { return TextJSON{EN: t.EN, ES: t.ES} }

func (a app) loadSpecies(w http.ResponseWriter, r *http.Request) (speciesbus.Species, bool) {
	slug := r.PathValue("slug")

	sp, err := a.species.BySlug(r.Context(), slug)

	switch {
	case errors.Is(err, speciesbus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("slug",
			fmt.Sprintf("No plant has the slug %q. PUT %s/species/%s adds it; GET %s/species lists the ones there are.", slug, Prefix, slug, Prefix)))

		return speciesbus.Species{}, false
	case err != nil:
		a.fail(w, r, "reading a species", err)

		return speciesbus.Species{}, false
	}

	return sp, true
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	web.WriteJSON(w, http.StatusInternalServerError, web.Problem("", "Something went wrong at our end. Try again in a few minutes."))
}
