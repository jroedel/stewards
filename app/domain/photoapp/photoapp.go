// Package photoapp is photos over HTTP: the pictures themselves, at
// /photos/<id>/large.jpg and small.jpg, and the stewards' screens that add a
// plant's photos, say what each one is, and check it.
//
// The rules are photobus's. What is decided here is who may see a picture: a
// checked photo is anybody's, as it is on the cards; an unchecked one is only
// a signed-in steward's, so that a photo nobody has compared with the plant
// yet cannot be found by a volunteer at all, by a link or otherwise.
package photoapp

import (
	"cmp"
	"context"
	"embed"
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

// UploadPattern is the one route that takes a file. The muxer gives it its
// own body limit and lets it be multipart, which no other route may be; see
// web.MaxBody for why that is a separate branch rather than a second limit
// inside the first.
const UploadPattern = "POST /steward/species/{id}/photos"

// uploadTime is how long an upload may take to arrive. The server allows 30
// seconds for a whole request, which is generous for a form and not enough
// for 8 MB from a phone on one bar in the garden: at a megabit a second that
// alone is over a minute.
const uploadTime = 5 * time.Minute

// Photos is what this app needs from the photo rules.
type Photos interface {
	Add(ctx context.Context, speciesID types.ID, f photobus.Fields, data []byte) (photobus.Photo, error)
	Update(ctx context.Context, id types.ID, f photobus.Fields) (photobus.Photo, error)
	Delete(ctx context.Context, id types.ID) error
	ByID(ctx context.Context, id types.ID) (photobus.Photo, error)
	ForSpecies(ctx context.Context, speciesID types.ID) ([]photobus.Photo, error)
	Open(ctx context.Context, id types.ID, size photobus.Size) (photobus.Photo, photobus.File, error)
}

// SpeciesReader is what it needs from the species rules.
type SpeciesReader interface {
	ByID(ctx context.Context, id types.ID) (speciesbus.Species, error)
}

// PlaceReader is what it needs from the place rules: the list a photo's
// "where it was taken" is chosen from.
type PlaceReader interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	Photos  Photos
	Species SpeciesReader
	Places  PlaceReader
}

type app struct {
	log     *slog.Logger
	render  *page.Renderer
	photos  Photos
	species SpeciesReader
	places  PlaceReader
}

func newApp(cfg Config) app {
	return app{log: cfg.Log, render: cfg.Render, photos: cfg.Photos, species: cfg.Species, places: cfg.Places}
}

// FileRoutes mounts the pictures. Public, and mounted whether or not sign-in
// is configured: a card shows them to anybody.
func FileRoutes(mux *http.ServeMux, cfg Config) {
	a := newApp(cfg)

	mux.HandleFunc("GET /photos/{id}/{file}", a.file)
}

// Routes mounts the stewards' screens, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := newApp(cfg)

	for pattern, h := range map[string]http.HandlerFunc{
		"GET /steward/species/{id}/photos": a.list,
		UploadPattern:                      a.upload,
		"GET /steward/photos/{id}/edit":    a.editForm,
		"POST /steward/photos/{id}":        a.update,
		"POST /steward/photos/{id}/delete": a.remove,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the pictures

func (a app) file(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	name := r.PathValue("file")

	size, ok := photobus.ServedSize(name)
	if !ok {
		http.NotFound(w, r)

		return
	}

	p, f, err := a.photos.Open(r.Context(), id, size)

	switch {
	case errors.Is(err, photobus.ErrNotFound):
		http.NotFound(w, r)

		return
	case err != nil:
		a.fail(w, r, "opening a photo", err)

		return
	}
	defer f.Close()

	// Not there, rather than forbidden, for somebody who may not see it: a
	// 403 would say there is a photo here worth asking about.
	_, steward := mid.StewardFrom(r.Context())
	if (!p.Checked && !steward) || name != photobus.ServedName(size, p.Format) {
		http.NotFound(w, r)

		return
	}

	// A picture never changes under its address -- a different photo is a
	// new id -- but it can stop being checked, or be removed. A day is the
	// compromise: a phone in the garden keeps it between visits, and a
	// photo taken down is gone from every phone by tomorrow. An unchecked one
	// is a steward's alone and is not kept anywhere.
	if p.Checked {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}

	w.Header().Set("Content-Type", photobus.ContentType(name))

	var modified time.Time
	if st, err := f.Stat(); err == nil {
		modified = st.ModTime()
	}

	http.ServeContent(w, r, "", modified, f)
}

// ------------------------------------------------------------------ a plant's photos

type option struct {
	Value, Label string
	Selected     bool
}

type photoRow struct {
	ID, Caption   string
	Width, Height int
	Checked       bool
	Shown         bool // the one volunteers see for this kind
}

type section struct {
	Kind, Label string
	Photos      []photoRow
}

// fieldsView is the part of a form both screens share: what a steward says
// about a photo.
type fieldsView struct {
	Kinds, Months, Places []option
	Ours                  bool
	TakenYear             string
	TakenOn               string
	TakenWhere            string
	Credit, SourceURL     string
	License               string
	Checked, InFlower     bool
}

type listView struct {
	SpeciesID, Name, Slug string
	Sections              []section
	Missing               int
	Fields                fieldsView
	Problems              map[string]string
	Done                  string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.loadSpecies(w, r)
	if !ok {
		return
	}

	f := photobus.Fields{Kind: photobus.Kind(r.URL.Query().Get("kind")), Source: photobus.Ours}

	v := listView{Problems: map[string]string{}}

	switch r.URL.Query().Get("done") {
	case "added":
		v.Done = "Photo added."
	case "saved":
		v.Done = "Photo saved."
	case "removed":
		v.Done = "Photo removed."
	}

	a.showList(w, r, http.StatusOK, sp, v, f)
}

// upload takes one photo and what the steward says about it.
func (a app) upload(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.loadSpecies(w, r)
	if !ok {
		return
	}

	// Both deadlines, because the photo is decoded and scaled after it
	// arrives, and that is on the write side of the clock.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTime))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadTime + time.Minute))

	v := listView{Problems: map[string]string{}}

	// The form's text fields are a few hundred bytes; a megabyte in memory
	// and the photo goes to a temporary file, which RemoveAll takes away.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			v.Problems["photo"] = fmt.Sprintf("That photo is larger than %d MB. Send it as the camera saved it, not a video or a screen recording.", photobus.MaxBytes>>20)
			a.showList(w, r, http.StatusRequestEntityTooLarge, sp, v, photobus.Fields{Source: photobus.Ours})

			return
		}

		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}
	defer r.MultipartForm.RemoveAll()

	f := fieldsFrom(r, v.Problems)

	data, problem := readPhoto(r)
	if problem != "" {
		v.Problems["photo"] = problem
	}

	if len(v.Problems) == 0 {
		_, err := a.photos.Add(r.Context(), sp.ID, f, data)

		invalid, isInvalid := errors.AsType[photobus.Invalid](err)
		dup, isDup := errors.AsType[photobus.Duplicate](err)

		switch {
		case err == nil:
			http.Redirect(w, r, listPath(sp.ID)+"?done=added#"+string(f.Kind), http.StatusSeeOther)

			return
		case isDup:
			v.Problems["photo"] = fmt.Sprintf("That photo is already here, under %s. Choose a different one.", dup.Photo.Kind.Label())
		case isInvalid:
			v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
		default:
			a.fail(w, r, "adding a photo", err)

			return
		}
	}

	a.showList(w, r, http.StatusUnprocessableEntity, sp, v, f)
}

// readPhoto is the uploaded file's bytes, or a sentence saying what to do.
func readPhoto(r *http.Request) ([]byte, string) {
	file, _, err := r.FormFile("photo")
	if err != nil {
		return nil, "Choose a photo to send."
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, photobus.MaxBytes+1))

	switch {
	case err != nil:
		return nil, "The photo did not arrive whole. Try once more, nearer the house if the signal is weak."
	case len(data) > photobus.MaxBytes:
		return nil, fmt.Sprintf("That photo is larger than %d MB. Send it as the camera saved it.", photobus.MaxBytes>>20)
	case len(data) == 0:
		return nil, "Choose a photo to send."
	}

	return data, ""
}

func (a app) showList(w http.ResponseWriter, r *http.Request, status int, sp speciesbus.Species, v listView, f photobus.Fields) {
	photos, err := a.photos.ForSpecies(r.Context(), sp.ID)
	if err != nil {
		a.fail(w, r, "listing a species' photos", err)

		return
	}

	places, names, err := a.placeOptions(r.Context(), f.PlaceID)
	if err != nil {
		a.fail(w, r, "listing places for a photo", err)

		return
	}

	v.SpeciesID, v.Name, v.Slug = sp.ID.String(), sp.Common.EN, sp.Slug
	v.Fields = fieldsOf(f, places)

	for _, k := range photobus.Kinds {
		s := section{Kind: string(k), Label: k.Label()}
		best, hasBest := photobus.Best(photos, k)

		for _, p := range photos {
			if p.Kind != k {
				continue
			}

			s.Photos = append(s.Photos, photoRow{
				ID: p.ID.String(), Caption: caption(p, names), Width: p.Small.Width, Height: p.Small.Height,
				Checked: p.Checked, Shown: hasBest && p.ID == best.ID,
			})
		}

		if !hasBest {
			v.Missing++
		}

		v.Sections = append(v.Sections, s)
	}

	a.render.Render(w, r, status, "steward-photos", v)
}

// ------------------------------------------------------------------ one photo

type editView struct {
	ID, SpeciesID, Name string
	Kind                string
	Width, Height       int
	Original            string
	Slug                string // the plant's, for the page that zooms in
	Fields              fieldsView
	Problems            map[string]string
	DeleteProblem       string
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	p, sp, ok := a.loadPhoto(w, r)
	if !ok {
		return
	}

	a.showEdit(w, r, http.StatusOK, p, sp, editView{Problems: map[string]string{}}, photobus.FieldsOf(p))
}

func (a app) update(w http.ResponseWriter, r *http.Request) {
	p, sp, ok := a.loadPhoto(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := editView{Problems: map[string]string{}}
	f := fieldsFrom(r, v.Problems)

	if len(v.Problems) == 0 {
		_, err := a.photos.Update(r.Context(), p.ID, f)

		invalid, isInvalid := errors.AsType[photobus.Invalid](err)

		switch {
		case err == nil:
			http.Redirect(w, r, listPath(sp.ID)+"?done=saved#"+string(f.Kind), http.StatusSeeOther)

			return
		case errors.Is(err, photobus.ErrNotFound):
			http.Error(w, "That photo is not here any more. It may have been removed. Go back to the plant's photos.", http.StatusNotFound)

			return
		case isInvalid:
			v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
		default:
			a.fail(w, r, "saving a photo", err)

			return
		}
	}

	a.showEdit(w, r, http.StatusUnprocessableEntity, p, sp, v, f)
}

// remove deletes a photo, only with the box ticked, as for places and plants.
func (a app) remove(w http.ResponseWriter, r *http.Request) {
	p, sp, ok := a.loadPhoto(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := editView{Problems: map[string]string{}, DeleteProblem: "Tick the box to confirm, then press Remove again."}

	if r.PostFormValue("confirm") == "yes" {
		switch err := a.photos.Delete(r.Context(), p.ID); {
		case err == nil, errors.Is(err, photobus.ErrNotFound):
			http.Redirect(w, r, listPath(sp.ID)+"?done=removed", http.StatusSeeOther)

			return
		default:
			a.fail(w, r, "removing a photo", err)

			return
		}
	}

	a.showEdit(w, r, http.StatusUnprocessableEntity, p, sp, v, photobus.FieldsOf(p))
}

func (a app) showEdit(w http.ResponseWriter, r *http.Request, status int, p photobus.Photo, sp speciesbus.Species, v editView, f photobus.Fields) {
	places, _, err := a.placeOptions(r.Context(), f.PlaceID)
	if err != nil {
		a.fail(w, r, "listing places for a photo", err)

		return
	}

	v.ID, v.SpeciesID, v.Name = p.ID.String(), sp.ID.String(), sp.Common.EN
	v.Kind = p.Kind.Label()
	v.Width, v.Height = p.Small.Width, p.Small.Height
	v.Original = fmt.Sprintf("%d × %d kept for the cards. Zoomed in, it is the photo as it was sent, with where it was taken and the camera's details taken out.", p.Large.Width, p.Large.Height)
	v.Slug = sp.Slug
	v.Fields = fieldsOf(f, places)

	a.render.Render(w, r, status, "photo-form", v)
}

// ------------------------------------------------------------------ the parts

// fieldsFrom reads what a steward said about a photo, noting anything that is
// not even the right shape.
func fieldsFrom(r *http.Request, problems map[string]string) photobus.Fields {
	f := photobus.Fields{
		Kind:      photobus.Kind(r.PostFormValue("kind")),
		Source:    photobus.Source(r.PostFormValue("source")),
		Credit:    r.PostFormValue("credit"),
		SourceURL: r.PostFormValue("source_url"),
		License:   r.PostFormValue("license"),
		Checked:   r.PostFormValue("checked") == "yes",
		InFlower:  r.PostFormValue("in_flower") == "yes",
	}

	if day, err := photobus.Day(strings.TrimSpace(r.PostFormValue("taken_on"))); err != nil {
		problems["taken_on"] = "Choose the day, or leave it empty."
	} else {
		f.TakenAt = day
	}

	// "Somewhere else" is a choice in the place list rather than a box of
	// its own, because a photo is either at one of the places here or not
	// here at all, and two controls could say both. Its name is read only
	// with it: the box stays in the form, hidden, when another place is
	// chosen, and a name left in it would otherwise send the photo off the
	// property.
	switch s := r.PostFormValue("place"); s {
	case "":
	case elsewhere:
		f.Elsewhere, f.TakenWhere = true, r.PostFormValue("taken_where")
	default:
		id, err := types.ParseID(s)
		if err != nil {
			problems["place"] = "Choose the place from the list, or leave it empty."
		}
		f.PlaceID = id
	}

	if s := strings.TrimSpace(r.PostFormValue("taken_year")); s != "" {
		y, err := strconv.Atoi(s)
		if err != nil {
			problems["taken_year"] = "Write the year as four figures, such as 2027, or leave it empty."
		}
		f.TakenYear = y
	}

	if s := r.PostFormValue("taken_month"); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			problems["taken_month"] = "Choose the month from the list."
		}
		f.TakenMonth = m
	}

	return f
}

// elsewhere is the place list's value for a photo taken off the property.
const elsewhere = "elsewhere"

func fieldsOf(f photobus.Fields, places []option) fieldsView {
	v := fieldsView{
		Ours: f.Source != photobus.Borrowed, TakenWhere: f.TakenWhere,
		Credit: f.Credit, SourceURL: f.SourceURL, License: f.License, Checked: f.Checked,
		InFlower: f.InFlower, TakenOn: photobus.DayOf(f.TakenAt),
	}

	// The list from placeOptions, with "Somewhere else" after "Not said":
	// copied, so the captions' list is not the one changed.
	v.Places = slices.Clone(places)
	if len(v.Places) > 0 && f.Elsewhere {
		v.Places[0].Selected = false
	}
	v.Places = slices.Insert(v.Places, min(1, len(v.Places)), option{Value: elsewhere, Label: "Somewhere else, off the property", Selected: f.Elsewhere})

	if f.TakenYear != 0 {
		v.TakenYear = strconv.Itoa(f.TakenYear)
	}

	v.Kinds = []option{{Value: "", Label: "Choose a kind", Selected: f.Kind == ""}}
	for _, k := range photobus.Kinds {
		v.Kinds = append(v.Kinds, option{Value: string(k), Label: k.Label(), Selected: k == f.Kind})
	}

	v.Months = []option{{Value: "", Label: "Not known", Selected: f.TakenMonth == 0}}
	for m := time.January; m <= time.December; m++ {
		v.Months = append(v.Months, option{Value: strconv.Itoa(int(m)), Label: m.String(), Selected: int(m) == f.TakenMonth})
	}

	return v
}

// placeOptions is every place for the "where it was taken" list, a band named
// with the place it is in, and the same names by id for the captions.
func (a app) placeOptions(ctx context.Context, chosen types.ID) ([]option, map[types.ID]string, error) {
	all, err := a.places.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	byID := map[types.ID]placebus.Place{}
	for _, p := range all {
		byID[p.ID] = p
	}

	names := map[types.ID]string{}
	opts := []option{{Value: "", Label: "Not said", Selected: chosen.Zero()}}

	for _, p := range all {
		name := p.Name.EN
		if parent, ok := byID[p.ParentID]; ok && !p.TopLevel() {
			name = parent.Name.EN + ": " + name
		}

		names[p.ID] = name
		opts = append(opts, option{Value: p.ID.String(), Label: name, Selected: p.ID == chosen})
	}

	return opts, names, nil
}

// caption is the stewards' one line about a photo: whose, where, when.
func caption(p photobus.Photo, places map[types.ID]string) string {
	var parts []string

	if p.Source == photobus.Borrowed {
		parts = append(parts, p.Credit, p.License)
	} else {
		parts = append(parts, cmp.Or(p.Credit, "Our photo"))
		if name, ok := places[p.PlaceID]; ok {
			parts = append(parts, name)
		}

		if p.Elsewhere {
			parts = append(parts, cmp.Or(p.TakenWhere, "not taken here"))
		}
	}

	if when := takenWords(p.TakenYear, p.TakenMonth); when != "" {
		parts = append(parts, when)
	}

	return strings.Join(parts, " · ")
}

// takenWords is "April 2027", "April", "2027" or nothing.
func takenWords(year, month int) string {
	var parts []string
	if month >= 1 && month <= 12 {
		parts = append(parts, time.Month(month).String())
	}

	if year != 0 {
		parts = append(parts, strconv.Itoa(year))
	}

	return strings.Join(parts, " ")
}

func listPath(speciesID types.ID) string { return "/steward/species/" + speciesID.String() + "/photos" }

func (a app) loadSpecies(w http.ResponseWriter, r *http.Request) (speciesbus.Species, bool) {
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

func (a app) loadPhoto(w http.ResponseWriter, r *http.Request) (photobus.Photo, speciesbus.Species, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return photobus.Photo{}, speciesbus.Species{}, false
	}

	p, err := a.photos.ByID(r.Context(), id)

	switch {
	case errors.Is(err, photobus.ErrNotFound):
		http.Error(w, "That photo is not here any more. It may have been removed. Go back to the plant's photos.", http.StatusNotFound)

		return photobus.Photo{}, speciesbus.Species{}, false
	case err != nil:
		a.fail(w, r, "reading a photo", err)

		return photobus.Photo{}, speciesbus.Species{}, false
	}

	sp, err := a.species.ByID(r.Context(), p.SpeciesID)
	if err != nil {
		a.fail(w, r, "reading a photo's species", err)

		return photobus.Photo{}, speciesbus.Species{}, false
	}

	return p, sp, true
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
