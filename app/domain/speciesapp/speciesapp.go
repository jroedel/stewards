// Package speciesapp is the stewards' screens for species: the list of every
// plant, and the form that adds, edits and removes one.
//
// Behind sign-in, under /steward/species, and addressed by ID for the same
// reason as the place screens: the species' own addresses, /plants/<slug>,
// are the volunteers', and come with the species card.
//
// The rules are speciesbus's. This turns a form into speciesbus.Fields and a
// speciesbus.Invalid into a sentence beside its input.
package speciesapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
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

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

const path = "/steward/species"

// Species is what this app needs from the species rules.
type Species interface {
	All(ctx context.Context) ([]speciesbus.Species, error)
	ByID(ctx context.Context, id types.ID) (speciesbus.Species, error)
	BySlug(ctx context.Context, slug string) (speciesbus.Species, error)
	Create(ctx context.Context, f speciesbus.Fields) (speciesbus.Species, error)
	Update(ctx context.Context, id types.ID, f speciesbus.Fields) (speciesbus.Species, error)
	Delete(ctx context.Context, id types.ID) error
}

// PlaceReader is what the species card needs from the place rules: the
// places a species is listed at.
type PlaceReader interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// Listings is what it needs from the listing rules.
type Listings interface {
	ForSpecies(ctx context.Context, speciesID types.ID) ([]listingbus.Listing, error)
}

// PhotoReader is what the species card and the list need from the photo
// rules.
type PhotoReader interface {
	ForSpecies(ctx context.Context, speciesID types.ID) ([]photobus.Photo, error)
	BySpecies(ctx context.Context) (map[types.ID][]photobus.Photo, error)
}

// Config is what this app needs.
type Config struct {
	Log      *slog.Logger
	Render   *page.Renderer
	Species  Species
	Places   PlaceReader
	Listings Listings
	Photos   PhotoReader
}

type app struct {
	log      *slog.Logger
	render   *page.Renderer
	species  Species
	places   PlaceReader
	listings Listings
	photos   PhotoReader
}

func newApp(cfg Config) app {
	return app{log: cfg.Log, render: cfg.Render, species: cfg.Species, places: cfg.Places, listings: cfg.Listings, photos: cfg.Photos}
}

// Routes mounts the stewards' screens, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := newApp(cfg)

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + path:                   a.list,
		"GET " + path + "/new":          a.newForm,
		"POST " + path:                  a.create,
		"GET " + path + "/{id}/edit":    a.editForm,
		"POST " + path + "/{id}":        a.update,
		"POST " + path + "/{id}/delete": a.remove,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type listRow struct {
	ID, Slug, Common, Scientific, Status, Bloom string
	Confirmed                                   bool
	Swatches                                    []string

	// Thumb is the id of the photo shown beside the plant (photobus.Lead),
	// or empty when it has none.
	Thumb string
}

type listView struct {
	Species []listRow
	Done    string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	all, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing species for the stewards", err)

		return
	}

	photos, err := a.photos.BySpecies(r.Context())
	if err != nil {
		a.fail(w, r, "listing photos for the stewards' list of plants", err)

		return
	}

	v := listView{}
	for _, sp := range all {
		row := listRow{
			ID: sp.ID.String(), Slug: sp.Slug, Common: sp.Common.In(types.English), Scientific: sp.Scientific,
			Status: sp.Status.Label(), Bloom: sp.Bloom.String(), Confirmed: sp.Confirmed, Swatches: sp.Swatches,
		}

		if p, ok := photobus.Lead(photos[sp.ID]); ok {
			row.Thumb = p.ID.String()
		}

		v.Species = append(v.Species, row)
	}

	// A fixed sentence chosen by a word, never the query echoed.
	switch r.URL.Query().Get("done") {
	case "added":
		v.Done = "Plant added."
	case "saved":
		v.Done = "Changes saved."
	case "removed":
		v.Done = "Plant removed."
	}

	a.render.Render(w, r, http.StatusOK, "steward-species", v)
}

// ------------------------------------------------------------------ the form

type option struct {
	Value, Label string
	Selected     bool
}

type month struct {
	Number, Short, Long string
	On                  bool
}

type source struct {
	N          int // counted from 1, for the label
	Label, URL string
}

// formView holds every value as typed, so a refusal gives back what was sent.
type formView struct {
	ID, Title, Slug, Name string

	Common               page.Box
	Scientific           string
	Statuses             []option
	Confirmed            bool
	Flower               page.Box
	Swatches             string
	Months               []month
	HeightMin, HeightMax string
	WidthMin, WidthMax   string
	Light, Water         []option
	Note                 page.Box
	Sources              []source

	Problems      map[string]string
	DeleteProblem string
}

// spareSources is how many empty source rows the form offers beyond those
// filled: there is no script to add a row, so the rows are there already.
const spareSources = 2

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, r, http.StatusOK, "species-form", fill(formView{Title: "Add a plant"}, speciesbus.Fields{}))
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.load(w, r)
	if !ok {
		return
	}

	a.render.Render(w, r, http.StatusOK, "species-form", viewOf(sp, mid.LangFrom(r.Context())))
}

func (a app) create(w http.ResponseWriter, r *http.Request) {
	f, v, ok := read(w, r)
	if !ok {
		return
	}

	v.Title = "Add a plant"

	if len(v.Problems) == 0 {
		if _, err := a.species.Create(r.Context(), f); err != nil {
			a.refuse(w, r, v, f, err)

			return
		}

		http.Redirect(w, r, path+"?done=added", http.StatusSeeOther)

		return
	}

	a.render.Render(w, r, http.StatusUnprocessableEntity, "species-form", fill(v, f))
}

func (a app) update(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.load(w, r)
	if !ok {
		return
	}

	f, v, ok := read(w, r)
	if !ok {
		return
	}

	v.ID, v.Slug, v.Name, v.Title = sp.ID.String(), sp.Slug, sp.Common.In(types.English), "Edit "+sp.Common.In(types.English)

	if len(v.Problems) == 0 {
		if _, err := a.species.Update(r.Context(), sp.ID, f); err != nil {
			a.refuse(w, r, v, f, err)

			return
		}

		http.Redirect(w, r, path+"?done=saved", http.StatusSeeOther)

		return
	}

	a.render.Render(w, r, http.StatusUnprocessableEntity, "species-form", fill(v, f))
}

// remove deletes a species, only with the box ticked, as for places.
func (a app) remove(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.load(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := viewOf(sp, mid.LangFrom(r.Context()))
	v.DeleteProblem = "Tick the box to confirm, then press Remove again."

	if r.PostFormValue("confirm") == "yes" {
		err := a.species.Delete(r.Context(), sp.ID)

		invalid, isInvalid := errors.AsType[speciesbus.Invalid](err)

		switch {
		case err == nil:
			http.Redirect(w, r, path+"?done=removed", http.StatusSeeOther)

			return
		case isInvalid:
			v.DeleteProblem = page.Sentence(invalid.Problem)
		default:
			a.fail(w, r, "removing a species", err)

			return
		}
	}

	a.render.Render(w, r, http.StatusUnprocessableEntity, "species-form", v)
}

// ------------------------------------------------------------------ the parts

func (a app) load(w http.ResponseWriter, r *http.Request) (speciesbus.Species, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return speciesbus.Species{}, false
	}

	sp, err := a.species.ByID(r.Context(), id)
	switch {
	case errors.Is(err, speciesbus.ErrNotFound):
		http.Error(w, "That plant is not here any more. It may have been removed. Go back to the list of plants.", http.StatusNotFound)

		return speciesbus.Species{}, false
	case err != nil:
		a.fail(w, r, "reading a species", err)

		return speciesbus.Species{}, false
	}

	return sp, true
}

// read turns the posted form into Fields and a view of what was typed. It
// refuses only the shape of an input -- a size that is not a number -- and
// leaves every rule about a species to speciesbus.
func read(w http.ResponseWriter, r *http.Request) (speciesbus.Fields, formView, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return speciesbus.Fields{}, formView{}, false
	}

	get, l := r.PostFormValue, mid.LangFrom(r.Context())

	v := formView{
		Slug:   get("slug"),
		Common: page.Typed(get("common"), l), Scientific: get("scientific"),
		Confirmed: get("confirmed") == "yes",
		Flower:    page.Typed(get("flower"), l), Swatches: get("swatches"),
		HeightMin: get("height_min"), HeightMax: get("height_max"),
		WidthMin: get("width_min"), WidthMax: get("width_max"),
		Note:     page.Typed(get("note"), l),
		Problems: map[string]string{},
	}

	f := speciesbus.Fields{
		Slug:        v.Slug,
		Common:      v.Common.Text(l),
		Scientific:  v.Scientific,
		Status:      speciesbus.Status(get("status")),
		Confirmed:   v.Confirmed,
		FlowerColor: v.Flower.Text(l),
		Swatches:    strings.FieldsFunc(v.Swatches, func(r rune) bool { return r == ',' || r == ' ' }),
		Note:        v.Note.Text(l),
	}

	for _, raw := range r.PostForm["bloom"] {
		if n, err := strconv.Atoi(raw); err == nil {
			f.Bloom = f.Bloom.With(time.Month(n))
		}
	}

	for _, raw := range r.PostForm["light"] {
		n, _ := strconv.Atoi(raw)
		f.Light |= speciesbus.Light(n)
	}

	for _, raw := range r.PostForm["water"] {
		n, _ := strconv.Atoi(raw)
		f.Water |= speciesbus.Water(n)
	}

	inches := func(field, raw string) int {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return 0
		}

		n, err := strconv.Atoi(raw)
		if err != nil {
			v.Problems[field] = "Write sizes as whole inches, such as 18. A foot is 12."
		}

		return n
	}

	f.Height = speciesbus.Size{Min: inches("height", v.HeightMin), Max: inches("height", v.HeightMax)}
	f.Width = speciesbus.Size{Min: inches("width", v.WidthMin), Max: inches("width", v.WidthMax)}

	labels, urls := r.PostForm["source_label"], r.PostForm["source_url"]
	for i := range labels {
		src := speciesbus.Source{Label: labels[i]}
		if i < len(urls) {
			src.URL = urls[i]
		}

		f.Sources = append(f.Sources, src)
		v.Sources = append(v.Sources, source{Label: src.Label, URL: src.URL})
	}

	return f, v, true
}

func (a app) refuse(w http.ResponseWriter, r *http.Request, v formView, f speciesbus.Fields, err error) {
	invalid, ok := errors.AsType[speciesbus.Invalid](err)
	if !ok {
		a.fail(w, r, "saving a species", err)

		return
	}

	v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
	a.render.Render(w, r, http.StatusUnprocessableEntity, "species-form", fill(v, f))
}

// fill sets the choices -- status, months, light, water -- from f, and pads
// the source rows with spares.
func fill(v formView, f speciesbus.Fields) formView {
	v.Statuses = []option{{Value: "", Label: "Not set yet", Selected: f.Status == ""}}
	for _, s := range speciesbus.Statuses {
		v.Statuses = append(v.Statuses, option{Value: string(s), Label: s.Label(), Selected: f.Status == s})
	}

	v.Months = nil
	for m := time.January; m <= time.December; m++ {
		v.Months = append(v.Months, month{
			Number: strconv.Itoa(int(m)), Short: m.String()[:3], Long: m.String(), On: f.Bloom.Has(m),
		})
	}

	v.Light = []option{
		{Value: strconv.Itoa(int(speciesbus.FullSun)), Label: "Full sun", Selected: f.Light&speciesbus.FullSun != 0},
		{Value: strconv.Itoa(int(speciesbus.PartShade)), Label: "Part shade", Selected: f.Light&speciesbus.PartShade != 0},
		{Value: strconv.Itoa(int(speciesbus.Shade)), Label: "Shade", Selected: f.Light&speciesbus.Shade != 0},
	}

	v.Water = []option{
		{Value: strconv.Itoa(int(speciesbus.Dry)), Label: "Dry", Selected: f.Water&speciesbus.Dry != 0},
		{Value: strconv.Itoa(int(speciesbus.Moist)), Label: "Moist", Selected: f.Water&speciesbus.Moist != 0},
		{Value: strconv.Itoa(int(speciesbus.Wet)), Label: "Wet", Selected: f.Water&speciesbus.Wet != 0},
	}

	// Drop rows typed blank before padding, so a refused form does not grow
	// two more empty rows every time it comes back.
	kept := v.Sources[:0:0]
	for _, s := range v.Sources {
		if strings.TrimSpace(s.Label) != "" || strings.TrimSpace(s.URL) != "" {
			kept = append(kept, s)
		}
	}

	v.Sources = kept
	for range spareSources {
		v.Sources = append(v.Sources, source{})
	}

	for i := range v.Sources {
		v.Sources[i].N = i + 1
	}

	return v
}

// viewOf is sp in the form, on a page in l.
func viewOf(sp speciesbus.Species, l types.Lang) formView {
	size := func(n int) string {
		if n == 0 {
			return ""
		}

		return strconv.Itoa(n)
	}

	v := formView{
		ID: sp.ID.String(), Title: "Edit " + sp.Common.In(types.English), Slug: sp.Slug, Name: sp.Common.In(types.English),
		Common: page.BoxOf(sp.Common, l), Scientific: sp.Scientific,
		Confirmed: sp.Confirmed,
		Flower:    page.BoxOf(sp.FlowerColor, l), Swatches: strings.Join(sp.Swatches, " "),
		HeightMin: size(sp.Height.Min), HeightMax: size(sp.Height.Max),
		WidthMin: size(sp.Width.Min), WidthMax: size(sp.Width.Max),
		Note: page.BoxOf(sp.Note, l),
	}

	for _, src := range sp.Sources {
		v.Sources = append(v.Sources, source{Label: src.Label, URL: src.URL})
	}

	return fill(v, speciesbus.Fields{Status: sp.Status, Bloom: sp.Bloom, Light: sp.Light, Water: sp.Water})
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
