// Package inboxapp is the photo inbox over HTTP: the screen a steward sends a
// batch of photos from, in the garden or at a nursery, the list of what is
// waiting to be sorted, and the screen each one is sorted on.
//
// The rules are inboxbus's. What is decided here is the shape of a batch.
// Ten photos chosen from a phone's camera roll arrive in one form post, every
// one of them saying the same where, place and note; each is kept or refused
// on its own, so a screenshot chosen by mistake costs that one and not the
// other nine.
//
// A batch reaches the server one of two ways, and both end in the same Add.
//
// The send screen's script (static/send.mjs) sends the photos one request at a
// time, to SendPattern, and says "Sending 3 of 15" while it does. A photo
// larger than 4096 pixels on its longer side is shrunk to that on the phone
// first (static/shrink.mjs), so the server is sent 12 MP at most. That is the
// way a batch normally arrives. It was a single post at first, and the first
// batch of fifteen from a phone showed what that costs: nothing on the screen
// for the minutes it took, a form that could still be changed underneath it,
// and an error page from Apache at the end although every photo had been kept
// -- one request that long outlives somebody's timeout between the phone and
// here. A photo at a time is seconds per request, so no timeout is near it, and
// a dropped signal costs the photo on its way rather than the batch: sending
// the same photos again is safe, because a photo already in the inbox is
// recognised and not added twice.
//
// Without the script -- an old browser, or a header policy that blocks it --
// the form posts the whole batch to UploadPattern as before, and its limits
// below are sized for that. It is a fallback now, not the way a batch is meant
// to arrive, and it is kept because a send screen that does nothing without
// script is worse than a slow one.
package inboxapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
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

// IndexPath is the inbox, where the stewards' front page links to.
const IndexPath = "/steward/inbox"

// UploadPattern is the route a batch is sent to. Like photoapp's, the muxer
// gives it its own body limit and lets it be multipart.
const UploadPattern = "POST " + IndexPath

// MaxPhotos is the most photos one batch may hold. Twenty is two walks round
// the rain garden, and a limit at all is what lets MaxBytes be a number.
const MaxPhotos = 20

// MaxBytes is the body limit on a batch. A phone's photo is 2 to 8 MB, so
// this is twenty ordinary ones; it is not twenty of the largest photobus
// accepts, which would be half a gigabyte of temporary files on a shared
// host. The photos past the first megabyte go to disk as they arrive, not
// into memory, and each is read into memory only while it is being kept.
const MaxBytes = 160 << 20

// uploadTime is how long a batch may take to arrive. Fifty megabytes on one
// bar of signal at a megabit a second is seven minutes, and the steward is
// standing there.
const uploadTime = 15 * time.Minute

// keepTime is how long the photos may take to be kept once they have arrived:
// each is decoded and scaled in turn, a second or two apiece on the shared
// host, behind anybody else's.
const keepTime = 5 * time.Minute

// SendPattern is the route the send screen's script sends each photo of a
// batch to, one per request, with the batch's where, place and note every time.
// The muxer gives it a one-photo body limit, MaxSendBytes.
const SendPattern = "POST " + IndexPath + "/send"

// MaxSendBytes is the body limit on one photo sent by the script: the largest
// photo the inbox keeps, and a megabyte for the text fields and the multipart
// framing.
const MaxSendBytes = inboxbus.MaxBytes + 1<<20

// sendTime is how long one photo may take to arrive: the largest, 25 MB, at
// a megabit a second is under four minutes. keepOneTime is the time to keep
// it once it has, behind any other decode on the host.
const (
	sendTime    = 5 * time.Minute
	keepOneTime = time.Minute
)

// scripts are the send screen's modules: send.mjs, which the page loads, and
// what it imports. They are served from this app rather than with the shared
// stylesheet and fonts, because they are about the inbox and nothing else,
// and behind the same sign-in as every inbox route. Named one by one rather
// than as static/*.mjs, so the tests beside them are never served.
//
//go:embed static/send.mjs static/shrink.mjs static/jpeg.mjs
var scripts embed.FS

// scriptDir is where the modules are served from. They import each other by
// name, relative to it.
const scriptDir = IndexPath + "/static/"

// scriptTags is each module's ETag: a hash of what it holds, worked out once.
//
// A phone asks again every time the page loads, and is told 304 when nothing
// changed. Not a hash in the address and a year's cache, as the stylesheet
// has: the page could name send.mjs by its hash, but send.mjs imports
// shrink.mjs by a plain name, so after a deploy a phone would run a new
// send.mjs with a shrink.mjs from before it.
var scriptTags = func() map[string]string {
	tags := map[string]string{}

	names, _ := fs.Glob(scripts, "static/*.mjs")
	for _, name := range names {
		body, _ := scripts.ReadFile(name)
		sum := sha256.Sum256(body)
		tags[path.Base(name)] = `"` + hex.EncodeToString(sum[:])[:16] + `"`
	}

	return tags
}()

// Inbox is what this app needs from the inbox rules.
type Inbox interface {
	Check(f inboxbus.Fields) (inboxbus.Fields, error)
	Add(ctx context.Context, f inboxbus.Fields, data []byte) (inboxbus.Item, error)
	Waiting(ctx context.Context) ([]inboxbus.Item, error)
	SetAside(ctx context.Context) ([]inboxbus.Item, error)
	ByID(ctx context.Context, id types.ID) (inboxbus.Item, error)
	Sort(ctx context.Context, id, by types.ID, s inboxbus.Sorting) (inboxbus.Result, error)
	Open(ctx context.Context, id types.ID, size photobus.Size) (inboxbus.Item, photobus.File, error)
}

// NurseryReader is what it needs from the nursery rules: the nurseries a
// steward has been to, to choose from rather than type.
type NurseryReader interface {
	Nurseries(ctx context.Context) ([]string, error)
	LastNursery(ctx context.Context, seen time.Time) (string, error)
}

// SpeciesReader is what it needs from the species rules: the plants a photo
// is sorted to.
type SpeciesReader interface {
	All(ctx context.Context) ([]speciesbus.Species, error)
}

// PlaceReader is what it needs from the place rules: the list a batch's place
// is chosen from, and the names on the list of what is waiting.
type PlaceReader interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	Inbox   Inbox
	Places  PlaceReader
	Species SpeciesReader

	// Nursery may be nil, and a nursery photo is then not offered as
	// stock.
	Nursery NurseryReader
}

type app struct {
	log     *slog.Logger
	render  *page.Renderer
	inbox   Inbox
	places  PlaceReader
	species SpeciesReader
	nursery NurseryReader
}

// Routes mounts the inbox, every route behind guard. Its pictures too: an
// inbox photo is a steward's alone, whatever it shows.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{log: cfg.Log, render: cfg.Render, inbox: cfg.Inbox, places: cfg.Places, species: cfg.Species, nursery: cfg.Nursery}

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + IndexPath:                  a.list,
		"GET " + IndexPath + "/new":         a.newForm,
		UploadPattern:                       a.upload,
		SendPattern:                         a.sendOne,
		"GET " + scriptDir + "{file}":       a.script,
		"GET " + IndexPath + "/{id}":        a.sortForm,
		"POST " + IndexPath + "/{id}":       a.sort,
		"GET " + IndexPath + "/{id}/{file}": a.file,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type itemRow struct {
	ID            string
	Width, Height int
	Time          string
	Where         string // the place here, if said
	Here          bool   // on the property
	Away          string // otherwise: "At a nursery: Natural Gardener"
	Note          string
}

type day struct {
	Label string
	Items []itemRow
}

type listView struct {
	Count int
	Days  []day
	Aside []itemRow
	Done  string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	items, err := a.inbox.Waiting(r.Context())
	if err != nil {
		a.fail(w, r, "listing the inbox", err)

		return
	}

	aside, err := a.inbox.SetAside(r.Context())
	if err != nil {
		a.fail(w, r, "listing the photos set aside", err)

		return
	}

	names, err := a.placeNames(r.Context())
	if err != nil {
		a.fail(w, r, "naming places for the inbox", err)

		return
	}

	v := listView{Count: len(items)}

	// The counts are numbers read back from the query, never the query
	// echoed: a link somebody else made can put nothing but a number here.
	// The other confirmations are fixed sentences chosen by a word.
	switch q := r.URL.Query(); q.Get("done") {
	case "sent":
		n, _ := strconv.Atoi(q.Get("n"))
		d, _ := strconv.Atoi(q.Get("d"))
		v.Done = sentWords(max(n, 0), max(d, 0))
	default:
		v.Done = sortedWords(q.Get("done"))
	}

	for _, it := range aside {
		v.Aside = append(v.Aside, rowOf(it, names))
	}

	for _, it := range items {
		when := it.When().In(types.Garden)
		label := when.Format("Monday 2 January")
		if when.Year() != time.Now().In(types.Garden).Year() {
			label = when.Format("Monday 2 January 2006")
		}

		if len(v.Days) == 0 || v.Days[len(v.Days)-1].Label != label {
			v.Days = append(v.Days, day{Label: label})
		}

		last := &v.Days[len(v.Days)-1]
		last.Items = append(last.Items, rowOf(it, names))
	}

	a.render.Render(w, r, http.StatusOK, "steward-inbox", v)
}

func rowOf(it inboxbus.Item, names map[types.ID]string) itemRow {
	row := itemRow{
		ID: it.ID.String(), Width: it.Small.Width, Height: it.Small.Height,
		Here: it.At.Here(), Away: awayWords(it), Note: it.Note,
		Where: names[it.PlaceID],
	}

	if !it.TakenAt.IsZero() {
		row.Time = it.TakenAt.In(types.Garden).Format("3:04 pm")
	}

	return row
}

// awayWords is where off the property a photo was taken, as its tag says it:
// "At a nursery: Natural Gardener", "Somewhere else: Pedernales Falls State
// Park", or the choice alone when nothing more was said. Empty on the
// property.
func awayWords(it inboxbus.Item) string {
	switch {
	case it.At.Here():
		return ""
	case it.Site == "":
		return it.At.Label()
	}

	return it.At.Label() + ": " + it.Site
}

// sortedWords is the sentence after a photo is sorted, chosen by the word
// the redirect carries; nothing for a word it does not know.
func sortedWords(done string) string {
	switch done {
	case "photo":
		return "Added to the plant's photos. It is shown to volunteers once a steward checks it there."
	case "planted":
		return "Listed as planted there, and added to the plant's photos to be checked."
	case "stock":
		return "Added to the nursery's stock."
	case "unsure":
		return "Set aside, with the question."
	case "discard":
		return "Discarded."
	case "taken":
		return "That photo had already been sorted, by somebody else or on another screen."
	}

	return ""
}

// sentWords is the sentence after a batch: how many are now in the inbox,
// and how many were there already.
func sentWords(kept, already int) string {
	var s string

	switch kept {
	case 0:
		s = "No new photos."
	case 1:
		s = "1 photo is in the inbox."
	default:
		s = fmt.Sprintf("%d photos are in the inbox.", kept)
	}

	switch already {
	case 0:
	case 1:
		s += " 1 was there already."
	default:
		s += fmt.Sprintf(" %d were there already.", already)
	}

	return s
}

// ------------------------------------------------------------------ sending

type option struct {
	Value, Label string
	Selected     bool
}

type newView struct {
	At       string // property, nursery or elsewhere
	Places   []option
	Note     string
	Problems map[string]string

	// The nursery, offered from the register, or where else, as given.
	Nursery, Where string
	Nurseries      []string

	// After a batch some of which could not be kept: what was, and each
	// photo that was not, by the name the phone gave it.
	Kept    string
	Refused []refusal

	MaxPhotos int
	MaxMB     int

	// Script is the send screen's script, and SendURL where it sends each
	// photo.
	Script, SendURL string
}

type refusal struct {
	Name, Problem string
}

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	// On the property, every time the screen opens. A nursery setting that
	// carried over would file next week's garden photos as stock.
	a.showNew(w, r, http.StatusOK, newView{Problems: map[string]string{}}, inboxbus.Fields{At: inboxbus.Property})
}

func (a app) upload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTime))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadTime + keepTime))

	v := newView{Problems: map[string]string{}}

	// A megabyte in memory, for the few text fields; the photos go to
	// temporary files, which RemoveAll takes away.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			v.Problems["photo"] = fmt.Sprintf("That is more than %d MB at once. Send the photos in two or three goes.", MaxBytes>>20)
			a.showNew(w, r, http.StatusRequestEntityTooLarge, v, inboxbus.Fields{At: inboxbus.Property})

			return
		}

		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}
	defer r.MultipartForm.RemoveAll()

	f := fieldsOf(r, v.Problems)

	headers := r.MultipartForm.File["photo"]

	switch {
	case len(headers) == 0:
		v.Problems["photo"] = "Choose the photos to send."
	case len(headers) > MaxPhotos:
		v.Problems["photo"] = fmt.Sprintf("That is %d photos. Send up to %d at a time.", len(headers), MaxPhotos)
	}

	// The batch's own fields once, before any photo is read.
	checked, err := a.inbox.Check(f)
	if invalid, ok := errors.AsType[inboxbus.Invalid](err); ok {
		v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
	} else if err != nil {
		a.fail(w, r, "checking a batch for the inbox", err)

		return
	}

	if len(v.Problems) > 0 {
		a.showNew(w, r, http.StatusUnprocessableEntity, v, f)

		return
	}

	kept, already := 0, 0

	for _, h := range headers {
		data, problem := readPhoto(h)
		if problem == "" {
			_, err := a.inbox.Add(r.Context(), checked, data)

			invalid, isInvalid := errors.AsType[inboxbus.Invalid](err)
			_, isDup := errors.AsType[inboxbus.Duplicate](err)

			switch {
			case err == nil:
				kept++

				continue
			case isDup:
				already++

				continue
			case isInvalid && invalid.Field == "photo":
				problem = page.Sentence(invalid.Problem)
			case isInvalid:
				// The place went while the photos were on their way:
				// the same for every photo after this one, so the
				// batch stops here and says so once.
				v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
			case r.Context().Err() != nil:
				problem = "The connection dropped before this one was kept. Send it again."
			default:
				a.fail(w, r, "adding a photo to the inbox", err)

				return
			}
		}

		if len(v.Problems) > 0 {
			break
		}

		v.Refused = append(v.Refused, refusal{Name: h.Filename, Problem: problem})
	}

	if len(v.Refused) == 0 && len(v.Problems) == 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?done=sent&n=%d&d=%d", IndexPath, kept, already), http.StatusSeeOther)

		return
	}

	v.Kept = sentWords(kept, already)

	status := http.StatusOK
	if kept == 0 {
		status = http.StatusUnprocessableEntity
	}

	a.showNew(w, r, status, v, f)
}

// fieldsOf is what a batch says about every photo in it, as the form sent it,
// noting in problems a place that is not one from the list.
func fieldsOf(r *http.Request, problems map[string]string) inboxbus.Fields {
	f := inboxbus.Fields{
		At:   inboxbus.At(r.PostFormValue("at")),
		Note: r.PostFormValue("note"),
	}

	// The form has a field for each: the nursery, chosen from the register,
	// and anywhere else, written. Whichever goes with the choice of where
	// is the one that counts; the other may hold what was typed before the
	// choice was changed.
	switch f.At {
	case inboxbus.Nursery:
		f.Site = r.PostFormValue("nursery")
	case inboxbus.Elsewhere:
		f.Site = r.PostFormValue("where")
	}

	if u, ok := mid.StewardFrom(r.Context()); ok {
		f.FromID = u.ID
	}

	if s := r.PostFormValue("place"); s != "" {
		id, err := types.ParseID(s)
		if err != nil {
			problems["place"] = "Choose the place from the list, or leave it empty."
		}
		f.PlaceID = id
	}

	return f
}

// sent is the answer to the script for a photo that is in the inbox:
// "kept" when it arrived now, "already" when it was there before.
type sent struct {
	Outcome string `json:"outcome"`
}

// sendOne keeps one photo of a batch the send screen's script is sending a
// photo at a time, and answers in JSON, for the script to count.
//
// A refusal names its field, as the API's do, because the script does two
// different things with one: a "photo" problem is about that photo alone, so
// it is listed and the next is sent; any other field is the same for every
// photo in the batch, so the script stops and shows it beside the form.
func (a app) sendOne(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(sendTime))
	_ = rc.SetWriteDeadline(time.Now().Add(sendTime + keepOneTime))

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			web.WriteJSON(w, http.StatusRequestEntityTooLarge, web.Problem("photo", tooLarge))

			return
		}

		web.WriteJSON(w, http.StatusBadRequest, web.Problem("photo", notWhole))

		return
	}
	defer r.MultipartForm.RemoveAll()

	problems := map[string]string{}
	f := fieldsOf(r, problems)

	if problem, ok := problems["place"]; ok {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("place", problem))

		return
	}

	headers := r.MultipartForm.File["photo"]
	if len(headers) != 1 {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("photo", "Send one photo at a time here."))

		return
	}

	checked, err := a.inbox.Check(f)
	if invalid, ok := errors.AsType[inboxbus.Invalid](err); ok {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	} else if err != nil {
		a.failJSON(w, r, "checking a photo for the inbox", err)

		return
	}

	data, problem := readPhoto(headers[0])
	if problem != "" {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("photo", problem))

		return
	}

	_, err = a.inbox.Add(r.Context(), checked, data)

	invalid, isInvalid := errors.AsType[inboxbus.Invalid](err)
	_, isDup := errors.AsType[inboxbus.Duplicate](err)

	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusCreated, sent{Outcome: "kept"})
	case isDup:
		web.WriteJSON(w, http.StatusOK, sent{Outcome: "already"})
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))
	case r.Context().Err() != nil:
		// The phone has gone: nobody is left to answer. The script tries
		// this photo again when it can, and Add knows it if it was kept.
	default:
		a.failJSON(w, r, "adding a photo to the inbox", err)
	}
}

// script serves one of the send screen's modules, to be asked for again on
// every page load (scriptTags says why).
func (a app) script(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")

	tag, ok := scriptTags[name]
	if !ok {
		http.NotFound(w, r)

		return
	}

	body, err := scripts.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)

		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "private, no-cache")
	h.Set("ETag", tag)

	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}

// The sentences a photo is refused with, where both ways of sending say them.
var (
	tooLarge = fmt.Sprintf("Larger than %d MB. Send it as the camera saved it, not a video.", inboxbus.MaxBytes>>20)
	notWhole = "It did not arrive whole. Send it again."
)

// readPhoto is one uploaded file's bytes, or a sentence saying what to do.
func readPhoto(h *multipart.FileHeader) ([]byte, string) {
	if h.Size > inboxbus.MaxBytes {
		return nil, tooLarge
	}

	file, err := h.Open()
	if err != nil {
		return nil, notWhole
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, inboxbus.MaxBytes+1))

	switch {
	case err != nil:
		return nil, notWhole
	case len(data) > inboxbus.MaxBytes:
		return nil, tooLarge
	case len(data) == 0:
		return nil, "It arrived empty. Send it again."
	}

	return data, ""
}

func (a app) showNew(w http.ResponseWriter, r *http.Request, status int, v newView, f inboxbus.Fields) {
	places, err := a.places.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing places for the inbox", err)

		return
	}

	v.At, v.Note = string(f.At), f.Note

	switch f.At {
	case inboxbus.Nursery:
		v.Nursery = f.Site
	case inboxbus.Elsewhere:
		v.Where = f.Site
	}

	if a.nursery != nil {
		if v.Nurseries, err = a.nursery.Nurseries(r.Context()); err != nil {
			a.fail(w, r, "listing nurseries for the send screen", err)

			return
		}
	}
	v.MaxPhotos, v.MaxMB = MaxPhotos, MaxBytes>>20
	v.Script, v.SendURL = scriptDir+"send.mjs", strings.TrimPrefix(SendPattern, "POST ")

	byID := map[types.ID]placebus.Place{}
	for _, p := range places {
		byID[p.ID] = p
	}

	v.Places = []option{{Value: "", Label: "Not sure, or more than one", Selected: f.PlaceID.Zero()}}
	for _, p := range places {
		v.Places = append(v.Places, option{Value: p.ID.String(), Label: placeName(p, byID), Selected: p.ID == f.PlaceID})
	}

	a.render.Render(w, r, status, "steward-inbox-new", v)
}

// ------------------------------------------------------------------ sorting one

type sortView struct {
	ID            string
	Width, Height int

	// What is known of it.
	Taken    string
	Here     bool   // on the property: a planting is offered, and a place
	Away     string // otherwise: "At a nursery: Natural Gardener"
	Where    string
	Note     string
	Unsure   bool
	Question string

	// As is the outcome being filled in; empty for the choice of one.
	As string

	Species, Kinds, Places []option
	NoPlants               bool
	Problems               map[string]string
	Done                   string

	// Stock is whether a nursery photo can be stock, and the line's fields
	// as given or as offered.
	Stock     bool
	Nurseries []string
	NurseryAt string
	NameOnTag string
	PotSize   string
	Price     string
	Count     string
	StockNote string

	// Sorted is set once it has been: what it became, and where to see it.
	Sorted          string
	SeeAt, SeeLabel string
}

func (a app) sortForm(w http.ResponseWriter, r *http.Request) {
	it, ok := a.loadItem(w, r)
	if !ok {
		return
	}

	as := r.URL.Query().Get("as")
	switch inboxbus.Outcome(as) {
	case inboxbus.AsPhoto, inboxbus.AsPlanted, inboxbus.AsStock, inboxbus.AsUnsure, inboxbus.AsDiscard:
	default:
		as = ""
	}

	if as == string(inboxbus.AsStock) && (it.At != inboxbus.Nursery || a.nursery == nil) {
		as = ""
	}

	// A photo from off the property is not of anything planted here; the
	// choice is not offered rather than refused after it is made.
	if !it.At.Here() && as == string(inboxbus.AsPlanted) {
		as = ""
	}

	v := sortView{As: as, Problems: map[string]string{}, Done: sortedWords(r.URL.Query().Get("done"))}
	a.showSort(w, r, http.StatusOK, it, v, inboxbus.Sorting{Outcome: inboxbus.Outcome(as), PlaceID: it.PlaceID})
}

func (a app) sort(w http.ResponseWriter, r *http.Request) {
	it, ok := a.loadItem(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := sortView{As: r.PostFormValue("as"), Problems: map[string]string{}}
	s := inboxbus.Sorting{
		Outcome: inboxbus.Outcome(v.As),
		Kind:    photobus.Kind(r.PostFormValue("kind")),
		Note:    r.PostFormValue("note"),
	}

	if s.Outcome == inboxbus.AsStock {
		v.NurseryAt, v.NameOnTag, v.PotSize = r.PostFormValue("nursery"), r.PostFormValue("name_on_tag"), r.PostFormValue("pot_size")
		v.Price, v.Count, v.StockNote = r.PostFormValue("price"), r.PostFormValue("count"), r.PostFormValue("stock_note")
		s.Nursery = v.NurseryAt
		s.Stock = nurserybus.Fields{NameOnTag: v.NameOnTag, PotSize: v.PotSize, Note: v.StockNote}

		var err error
		if s.Stock.PriceCents, err = nurserybus.ParsePrice(v.Price); err != nil {
			v.Problems["price"] = page.Sentence(err.Error())
		}

		if c := strings.TrimSpace(v.Count); c != "" {
			if s.Stock.Count, err = strconv.Atoi(c); err != nil {
				v.Problems["count"] = "Write how many there were as a number, or leave it empty."
			}
		}
	}

	for field, dst := range map[string]*types.ID{"species": &s.SpeciesID, "place": &s.PlaceID} {
		if raw := r.PostFormValue(field); raw != "" {
			id, err := types.ParseID(raw)
			if err != nil {
				v.Problems[field] = "Choose from the list."
			}
			*dst = id
		}
	}

	if s.Outcome == inboxbus.AsStock {
		s.Stock.SpeciesID = s.SpeciesID
	}

	// A discard is the one sort that cannot be undone, so it asks, as
	// removing a plant or a place does.
	if s.Outcome == inboxbus.AsDiscard && r.PostFormValue("confirm") != "yes" {
		v.Problems["confirm"] = "Tick the box to confirm, then press Discard again."
	}

	if len(v.Problems) > 0 {
		a.showSort(w, r, http.StatusUnprocessableEntity, it, v, s)

		return
	}

	// Where to go after: the next photo in the inbox, so a batch is sorted
	// in one go rather than from the list each time. Found before sorting,
	// while this one still has its place in the order.
	next, err := a.nextAfter(r.Context(), it.ID)
	if err != nil {
		a.fail(w, r, "finding the next photo to sort", err)

		return
	}

	var by types.ID
	if u, ok := mid.StewardFrom(r.Context()); ok {
		by = u.ID
	}

	_, err = a.inbox.Sort(r.Context(), it.ID, by, s)

	invalid, isInvalid := errors.AsType[inboxbus.Invalid](err)
	_, isTaken := errors.AsType[inboxbus.AlreadySorted](err)

	switch {
	case err == nil:
		to := IndexPath + "?done=" + string(s.Outcome)
		if !next.Zero() {
			to = IndexPath + "/" + next.String() + "?done=" + string(s.Outcome)
		}

		http.Redirect(w, r, to, http.StatusSeeOther)

		return
	case isTaken:
		http.Redirect(w, r, IndexPath+"?done=taken", http.StatusSeeOther)

		return
	case isInvalid:
		v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
	default:
		a.fail(w, r, "sorting an inbox photo", err)

		return
	}

	a.showSort(w, r, http.StatusUnprocessableEntity, it, v, s)
}

// nextAfter is the photo to sort after this one: the next in the inbox's
// order, or the first if this was the last, or none if this was the only one.
func (a app) nextAfter(ctx context.Context, id types.ID) (types.ID, error) {
	items, err := a.inbox.Waiting(ctx)
	if err != nil {
		return types.ID{}, err
	}

	at := slices.IndexFunc(items, func(it inboxbus.Item) bool { return it.ID == id })

	for i := range items {
		if c := items[(at+1+i)%len(items)]; c.ID != id {
			return c.ID, nil
		}
	}

	return types.ID{}, nil
}

func (a app) showSort(w http.ResponseWriter, r *http.Request, status int, it inboxbus.Item, v sortView, s inboxbus.Sorting) {
	names, err := a.placeNames(r.Context())
	if err != nil {
		a.fail(w, r, "naming places for the inbox", err)

		return
	}

	v.ID, v.Width, v.Height = it.ID.String(), it.Large.Width, it.Large.Height
	v.Here, v.Away, v.Where, v.Note = it.At.Here(), awayWords(it), names[it.PlaceID], it.Note

	if it.Status == inboxbus.Unsure {
		v.Unsure, v.Question = true, it.Note
	}

	if !it.TakenAt.IsZero() {
		v.Taken = it.TakenAt.In(types.Garden).Format("Monday 2 January 2006, 3:04 pm")
	}

	switch it.Status {
	case inboxbus.Sorted:
		v.Sorted = "This photo has been sorted: " + sortedWords(string(it.Outcome))

		switch {
		case it.Outcome == inboxbus.AsStock:
			v.SeeAt, v.SeeLabel = "/steward/nursery", "See the nursery stock"
		case !it.SpeciesID.Zero():
			v.SeeAt, v.SeeLabel = "/steward/species/"+it.SpeciesID.String()+"/photos", "See the plant's photos"
		}
	case inboxbus.Discarded:
		v.Sorted = "This photo was discarded."
	}

	if v.Sorted != "" {
		a.render.Render(w, r, http.StatusOK, "steward-inbox-sort", v)

		return
	}

	plants, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing plants to sort a photo to", err)

		return
	}

	v.NoPlants = len(plants) == 0

	if v.Stock = it.At == inboxbus.Nursery && a.nursery != nil; v.Stock && v.As == string(inboxbus.AsStock) {
		if v.Nurseries, err = a.nursery.Nurseries(r.Context()); err != nil {
			a.fail(w, r, "listing the nurseries", err)

			return
		}

		// The nursery the batch was sent from, or else the one already
		// recorded for the photo's day: a morning's twenty tags are one
		// nursery, and saying it once is enough.
		if v.NurseryAt == "" {
			v.NurseryAt = it.Site
		}

		if v.NurseryAt == "" {
			if v.NurseryAt, err = a.nursery.LastNursery(r.Context(), it.When()); err != nil {
				a.fail(w, r, "finding the day's nursery", err)

				return
			}
		}
	}

	v.Species = []option{{Value: "", Label: "Choose the plant", Selected: s.SpeciesID.Zero()}}
	for _, sp := range plants {
		label := sp.Common.EN
		if sp.Scientific != "" {
			label += " (" + sp.Scientific + ")"
		}

		v.Species = append(v.Species, option{Value: sp.ID.String(), Label: label, Selected: sp.ID == s.SpeciesID})
	}

	kind := s.Kind
	if kind == "" && s.Outcome == inboxbus.AsPlanted {
		kind = photobus.Young
	}

	v.Kinds = []option{{Value: "", Label: "Choose what it shows", Selected: kind == ""}}
	for _, k := range photobus.Kinds {
		v.Kinds = append(v.Kinds, option{Value: string(k), Label: k.Label(), Selected: k == kind})
	}

	place := s.PlaceID
	if place.Zero() {
		place = it.PlaceID
	}

	v.Places = []option{{Value: "", Label: "Not said, or not here", Selected: place.Zero()}}
	for id, name := range names {
		v.Places = append(v.Places, option{Value: id.String(), Label: name, Selected: id == place})
	}

	slices.SortStableFunc(v.Places[1:], func(x, y option) int { return strings.Compare(x.Label, y.Label) })

	a.render.Render(w, r, status, "steward-inbox-sort", v)
}

func (a app) loadItem(w http.ResponseWriter, r *http.Request) (inboxbus.Item, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return inboxbus.Item{}, false
	}

	it, err := a.inbox.ByID(r.Context(), id)

	switch {
	case errors.Is(err, inboxbus.ErrNotFound):
		http.Error(w, "That photo is not in the inbox. Go back to the inbox.", http.StatusNotFound)

		return inboxbus.Item{}, false
	case err != nil:
		a.fail(w, r, "reading an inbox photo", err)

		return inboxbus.Item{}, false
	}

	return it, true
}

// ------------------------------------------------------------------ the pictures

type zoomView struct {
	ID             string
	LargeW, LargeH int
	Full           string // full.jpg, or full.png for a PNG
}

// zoom is a photo on a page of its own at full size, the way a plant card's
// photo opens: the large picture under the full one while that arrives, and
// the phone's own pinch to look closer. A grass's seed head or the hairs on a
// stem are what decides a sort, and 1600 pixels blur them.
func (a app) zoom(w http.ResponseWriter, r *http.Request) {
	it, ok := a.loadItem(w, r)
	if !ok {
		return
	}

	// Sorted or thrown away since the link was drawn: the sort screen says
	// which.
	if !it.HasPictures() {
		http.Redirect(w, r, IndexPath+"/"+it.ID.String(), http.StatusFound)

		return
	}

	a.render.Render(w, r, http.StatusOK, "steward-inbox-zoom", zoomView{
		ID: it.ID.String(), LargeW: it.Large.Width, LargeH: it.Large.Height,
		Full: photobus.ServedName(photobus.Full, it.Format),
	})
}

func (a app) file(w http.ResponseWriter, r *http.Request) {
	// The page that zooms in shares this route rather than having its own:
	// a pattern ending in /{id}/zoom and the scripts' /static/{file} would
	// both match /steward/inbox/static/zoom, which the mux refuses.
	if r.PathValue("file") == "zoom" {
		a.zoom(w, r)

		return
	}

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

	it, f, err := a.inbox.Open(r.Context(), id, size)

	switch {
	case errors.Is(err, inboxbus.ErrNotFound):
		http.NotFound(w, r)

		return
	case err != nil:
		a.fail(w, r, "opening an inbox photo", err)

		return
	}
	defer f.Close()

	if name != photobus.ServedName(size, it.Format) {
		http.NotFound(w, r)

		return
	}

	// Kept nowhere but the steward's screen: a photo nobody has looked at
	// may have somebody in it.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", photobus.ContentType(name))

	var modified time.Time
	if st, err := f.Stat(); err == nil {
		modified = st.ModTime()
	}

	http.ServeContent(w, r, "", modified, f)
}

// ------------------------------------------------------------------ the parts

// placeNames is every place's name by id, a band named with the place it is
// in.
func (a app) placeNames(ctx context.Context) (map[types.ID]string, error) {
	all, err := a.places.All(ctx)
	if err != nil {
		return nil, err
	}

	byID := map[types.ID]placebus.Place{}
	for _, p := range all {
		byID[p.ID] = p
	}

	names := map[types.ID]string{}
	for _, p := range all {
		names[p.ID] = placeName(p, byID)
	}

	return names, nil
}

func placeName(p placebus.Place, byID map[types.ID]placebus.Place) string {
	if parent, ok := byID[p.ParentID]; ok && !p.TopLevel() {
		return parent.Name.EN + ": " + p.Name.EN
	}

	return p.Name.EN
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}

// failJSON is fail for the script, which reads the sentence out of the JSON.
func (a app) failJSON(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	web.WriteJSON(w, http.StatusInternalServerError, web.Problem("", "Something went wrong on our end. Try again in a few minutes."))
}
