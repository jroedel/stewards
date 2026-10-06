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
//
// A batch can also start in the phone's own photos app, where a steward finds
// the photos on its map and shares them to the stewards app. That is Chrome's
// share target, and it needs the app installed from Chrome, which every
// steward's page offers through the manifest (ManifestPath). The share is caught on the phone by a
// service worker (static/share-worker.mjs) and lands on the send screen with
// the photos already chosen, so it, too, ends in the script sending a photo
// at a time. SharePattern on the server is only for a share the worker missed.
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
// and behind the same sign-in as every inbox route but one, the share
// target's worker (see Routes). Named one by one rather than as
// static/*.mjs, so the tests beside them are never served.
//
//go:embed static/send.mjs static/shrink.mjs static/jpeg.mjs static/share.mjs static/share-worker.mjs
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

// SharePattern is the share target in the manifest: where Chrome posts photos
// a steward shares to the installed app from the phone's photos app, as one
// multipart form with each photo under "photo".
//
// The service worker share.mjs registers takes that post on the phone and
// never sends it here. This route is for when it did not: the app was
// installed and used before the inbox was next opened, or Chrome cleared the
// worker for room. The muxer gives it the batch's limits, because it is a
// batch, and all it does with one is read it to the end and ask for it again
// -- by then the worker is registered, because the send screen it redirects
// to registers it. Reading it rather than keeping it, because a batch that
// arrives this way has not been told where it was taken, and the inbox
// should never guess that for a whole batch.
const SharePattern = "POST " + IndexPath + "/shared"

// ManifestPath is the web app manifest every steward's page links, which is
// what lets Chrome install the stewards app and list it in the share sheet.
const ManifestPath = IndexPath + "/app.webmanifest"

// ShareScript is the script every steward's page loads with the manifest. It
// registers the share target's worker, so an app installed from any of those
// pages is ready for its first share; on the send screen it also puts the
// shared photos in the form.
const ShareScript = scriptDir + "share.mjs"

// workerName is the share target's service worker, among the scripts. Served
// with Service-Worker-Allowed, because its scope, SharePattern, is not under
// the directory it is served from.
const workerName = "share-worker.mjs"

// manifestJSON is the stewards app as Chrome installs it.
//
// Its start is the stewards' front page, and its scope the whole site, so
// that Android can open a sign-in link from email in the app, rather than in
// another browser whose cookies the app cannot see. The icons are the isotype on white, and
// a maskable one with it small enough for any launcher's crop.
//
// The share target takes any image, though the inbox keeps JPEG and PNG
// alone: Android lists an app in the share sheet only when it takes every
// type in the selection, and a phone may say image/* of a mixed one. A photo
// the inbox cannot keep is refused on its own, as from the file input.
var manifestJSON = []byte(`{
  "id": "/steward",
  "name": "Garden stewards",
  "short_name": "Stewards",
  "description": "Send photos from the garden to the stewards' photo inbox, and look after the places and plants.",
  "start_url": "/steward",
  "scope": "/",
  "display": "standalone",
  "background_color": "#FFFFFF",
  "theme_color": "#FFFFFF",
  "icons": [
    {"src": "/static/img/stewards-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
    {"src": "/static/img/stewards-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
    {"src": "/static/img/stewards-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ],
  "share_target": {
    "action": "` + IndexPath + `/shared",
    "method": "POST",
    "enctype": "multipart/form-data",
    "params": {
      "files": [{"name": "photo", "accept": ["image/*"]}]
    }
  }
}
`)

// Inbox is what this app needs from the inbox rules.
type Inbox interface {
	Check(f inboxbus.Fields) (inboxbus.Fields, error)
	Add(ctx context.Context, f inboxbus.Fields, data []byte) (inboxbus.Item, error)
	Waiting(ctx context.Context) ([]inboxbus.Item, error)
	SetAside(ctx context.Context) ([]inboxbus.Item, error)
	ByID(ctx context.Context, id types.ID) (inboxbus.Item, error)
	Sort(ctx context.Context, id, by types.ID, s inboxbus.Sorting) (inboxbus.Result, error)
	Open(ctx context.Context, id types.ID, size photobus.Size) (inboxbus.Item, photobus.File, error)
	RecentPlants(ctx context.Context, n int) ([]types.ID, error)
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
//
// But for two, which are files in the binary rather than anybody's data:
// the manifest, which Chrome fetches without cookies, and the share target's
// worker, which it fetches again on its own schedule, whether the session
// has run out or not.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{log: cfg.Log, render: cfg.Render, inbox: cfg.Inbox, places: cfg.Places, species: cfg.Species, nursery: cfg.Nursery}

	mux.HandleFunc("GET "+ManifestPath, a.manifest)
	mux.HandleFunc("GET "+scriptDir+workerName, func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("file", workerName)
		a.script(w, r)
	})

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + IndexPath:                  a.list,
		"GET " + IndexPath + "/new":         a.newForm,
		"GET " + TogetherPath:               a.together,
		UploadPattern:                       a.upload,
		SendPattern:                         a.sendOne,
		SharePattern:                        a.shared,
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

	// Tags and Notes are where the day's photos were taken and what they
	// were sent with, each once: the grid of a day's photos has no room
	// for them under every picture, and a day is mostly one batch.
	Tags, Notes []string
	Open        bool // a photo with no place said
}

type listView struct {
	Count int
	Days  []day
	Aside []itemRow
	Done  string

	// Problem is why the photos chosen to sort together were not.
	Problem string
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
	case "none-chosen":
		v.Problem = "No photos were chosen. Tap the photos of one plant, then Sort the chosen photos together."
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
		row := rowOf(it, names)
		last.Items = append(last.Items, row)

		switch {
		case !row.Here:
			last.Tags = appendOnce(last.Tags, row.Away)
		case row.Where != "":
			last.Tags = appendOnce(last.Tags, row.Where)
		default:
			last.Open = true
		}

		if row.Note != "" {
			last.Notes = appendOnce(last.Notes, row.Note)
		}
	}

	a.render.Render(w, r, http.StatusOK, "steward-inbox", v)
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}

	return append(list, s)
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
		return "Added to the plant's photos. It is shown to volunteers once a steward checks it."
	case "photo-checked":
		return "Added to the plant's photos, checked. Volunteers see it now."
	case "planted":
		return "Listed as planted there, and added to the plant's photos to be checked."
	case "planted-checked":
		return "Listed as planted there, and added to the plant's photos, checked."
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

	// Shared is why photos shared from the phone are not in the form, when
	// the server knows; share.mjs says the rest.
	Shared string
}

type refusal struct {
	Name, Problem string
}

func (a app) newForm(w http.ResponseWriter, r *http.Request) {
	// On the property, every time the screen opens. A nursery setting that
	// carried over would file next week's garden photos as stock.
	v := newView{Problems: map[string]string{}, Shared: sharedWords(r.URL.Query().Get("shared"))}
	a.showNew(w, r, http.StatusOK, v, inboxbus.Fields{At: inboxbus.Property})
}

// sharedWords is the sentence for a share that did not arrive, chosen by the
// word the redirect carries: "again" from the server, "lost" from the worker.
// Nothing for a count, which is a share that did, and is share.mjs's to
// describe.
func sharedWords(shared string) string {
	switch shared {
	case "again":
		return "Those photos did not arrive: the app was not ready for them yet. It is now. Share them again from your photos app."
	case "lost":
		return "Those photos did not arrive: the phone could not keep them for this page, perhaps for want of room. Share them again, or choose them below."
	}

	return ""
}

// shared is a share the phone's worker did not catch: see SharePattern. It
// reads the batch to the end, so the phone hears an answer rather than a
// connection closed while it was still sending, and sends the steward to the
// send screen, which registers the worker and asks for the share again.
func (a app) shared(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadTime))

	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		a.log.Info("a share that reached the server was cut off", "id", web.RequestIDFrom(r.Context()), "err", err)
	}

	http.Redirect(w, r, IndexPath+"/new?shared=again", http.StatusSeeOther)
}

// manifest serves the app's manifest. Cached for a day rather than for ever:
// Chrome reads it again to update an installed app, and a change to it
// should reach the phones that installed it within the week.
func (a app) manifest(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/manifest+json")
	h.Set("Cache-Control", "public, max-age=86400")

	http.ServeContent(w, r, "app.webmanifest", startup, bytes.NewReader(manifestJSON))
}

// startup is the modification time reported for the manifest, which is in
// the binary and so changes only when the binary does.
var startup = time.Now()

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

	if name == workerName {
		h.Set("Service-Worker-Allowed", strings.TrimPrefix(SharePattern, "POST "))
	}

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

	// InFlower, InFruit and Checked are the boxes as they were sent, kept
	// when the form comes back.
	InFlower, InFruit, Checked bool

	// Recent is the plants last sorted to, offered as one tap each before
	// the whole list in Species (inboxbus.RecentPlants has why).
	Recent []option

	// Position is where this photo is in the inbox, "3 of 12", and Next
	// the next one's screen: Skip goes there, and swap.mjs fetches it
	// ahead. Both empty for a photo set aside, which is not in the order.
	// For one of a group (together.go), both are within the group.
	Position, Next string

	// Group is the group the photo is being sorted with, as its address
	// carries it, and Plant the plant chosen for it so far: both sent
	// again with the form, so the next screen is one of the group too.
	// Chosen is how many it holds, and Carried whether the plant was
	// chosen on an earlier photo of it rather than here.
	Group, Plant string
	Chosen       int
	Carried      bool

	group group

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

	// The form for what a photo most often is opens at once, rather than a
	// choice of forms first: that was a page load per photo spent saying
	// "a plant" again. A plant's photo, or for a photo taken at a nursery,
	// a line of its stock; the other outcomes are a tap away.
	stock := it.At == inboxbus.Nursery && a.nursery != nil

	as := r.URL.Query().Get("as")
	switch inboxbus.Outcome(as) {
	case inboxbus.AsPhoto, inboxbus.AsPlanted, inboxbus.AsStock, inboxbus.AsUnsure, inboxbus.AsDiscard:
	default:
		as = ""
	}

	if as == string(inboxbus.AsStock) && !stock {
		as = ""
	}

	// A photo from off the property is not of anything planted here; the
	// choice is not offered rather than refused after it is made.
	if !it.At.Here() && as == string(inboxbus.AsPlanted) {
		as = ""
	}

	if as == "" {
		as = string(inboxbus.AsPhoto)
		if stock {
			as = string(inboxbus.AsStock)
		}
	}

	v := sortView{As: as, Problems: map[string]string{}, Done: sortedWords(r.URL.Query().Get("done"))}
	s := inboxbus.Sorting{Outcome: inboxbus.Outcome(as), PlaceID: it.PlaceID}

	// One of a group, after the first: the plant chosen for the group is
	// chosen here too, for a photo or a planting.
	var plant types.ID
	if v.group, plant = groupOf(r.URL.Query(), it); !plant.Zero() && (s.Outcome == inboxbus.AsPhoto || s.Outcome == inboxbus.AsPlanted) {
		s.SpeciesID, v.Carried = plant, true
	}

	v.Plant = plantOf(plant)
	a.showSort(w, r, http.StatusOK, it, v, s)
}

// plantOf is a plant's id for the form, or nothing.
func plantOf(id types.ID) string {
	if id.Zero() {
		return ""
	}

	return id.String()
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

	v := sortView{
		As: r.PostFormValue("as"), Problems: map[string]string{},
		InFlower: r.PostFormValue("in_flower") != "", InFruit: r.PostFormValue("in_fruit") != "",
		Checked: r.PostFormValue("checked") == "yes",
	}
	s := inboxbus.Sorting{
		Outcome:  inboxbus.Outcome(v.As),
		Kind:     photobus.Kind(r.PostFormValue("kind")),
		InFlower: v.InFlower,
		InFruit:  v.InFruit,
		Note:     r.PostFormValue("note"),
	}

	if s.Outcome == inboxbus.AsPhoto || s.Outcome == inboxbus.AsPlanted {
		s.Checked = v.Checked
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

	// The plant is a button among the recent ones, or from the whole list.
	// Both, naming different plants, is a slip of the thumb that the screen
	// does not guess about.
	species := r.PostFormValue("species")
	switch other := r.PostFormValue("species_other"); {
	case other != "" && species != "" && other != species:
		v.Problems["species"] = "Choose one plant: a button or the list, not both."
	case other != "":
		species = other
	}

	for _, f := range []struct {
		field, raw string
		dst        *types.ID
	}{
		{"species", species, &s.SpeciesID},
		{"place", r.PostFormValue("place"), &s.PlaceID},
	} {
		if f.raw == "" {
			continue
		}

		id, err := types.ParseID(f.raw)
		if err != nil {
			v.Problems[f.field] = "Choose from the list."
		}
		*f.dst = id
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
	// in one go rather than from the list each time -- or, sorting a group,
	// the next of the group, and the inbox after its last. Found before
	// sorting, while this one still has its place in the order.
	g, plant := groupOf(r.PostForm, it)
	v.group, v.Plant = g, plantOf(plant)

	var next types.ID
	var err error

	if g != nil {
		open, err := a.inInbox(r)
		if err != nil {
			a.fail(w, r, "finding the next photo to sort", err)

			return
		}

		next, _ = g.after(it.ID, open)
	} else if next, err = a.nextAfter(r.Context(), it.ID); err != nil {
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
		done := string(s.Outcome)
		if s.Checked {
			done += "-checked"
		}

		if !s.SpeciesID.Zero() {
			plant = s.SpeciesID
		}

		to := IndexPath + "?done=" + done
		if !next.Zero() {
			to = sortURL(next, g, plant, done)
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

// tileWords is each kind's name on its button.
var tileWords = map[photobus.Kind]string{
	photobus.Young:  "Young plant",
	photobus.Leaf:   "Leaf",
	photobus.Flower: "Flower",
	photobus.Fruit:  "Fruit or seed",
	photobus.Mature: "Mature plant",
	photobus.Winter: "In winter",
}

// recentPlants is how many of the plants last sorted to are offered as a
// button each: a row or two on a phone.
const recentPlants = 6

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

	recent, err := a.inbox.RecentPlants(r.Context(), recentPlants)
	if err != nil {
		a.fail(w, r, "finding the plants sorted to lately", err)

		return
	}

	byID := map[types.ID]speciesbus.Species{}
	for _, sp := range plants {
		byID[sp.ID] = sp
	}

	chosen := false
	for _, id := range recent {
		if sp, ok := byID[id]; ok {
			v.Recent = append(v.Recent, option{Value: id.String(), Label: sp.Common.EN, Selected: id == s.SpeciesID})
			chosen = chosen || id == s.SpeciesID
		}
	}

	waiting, err := a.inbox.Waiting(r.Context())
	if err != nil {
		a.fail(w, r, "listing the inbox", err)

		return
	}

	switch i := slices.IndexFunc(waiting, func(w inboxbus.Item) bool { return w.ID == it.ID }); {
	case v.group != nil:
		open, err := a.inInbox(r)
		if err != nil {
			a.fail(w, r, "listing the inbox", err)

			return
		}

		plant, _ := types.ParseID(v.Plant)
		v.Group, v.Chosen = v.group.String(), len(v.group)
		v.Position = fmt.Sprintf("%d of the %d chosen", slices.Index(v.group, it.ID)+1, len(v.group))

		if next, ok := v.group.after(it.ID, open); ok {
			v.Next = sortURL(next, v.group, plant, "")
		}
	case i >= 0:
		v.Position = fmt.Sprintf("%d of %d", i+1, len(waiting))

		if len(waiting) > 1 {
			v.Next = IndexPath + "/" + waiting[(i+1)%len(waiting)].ID.String()
		}
	}

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

	// The whole list, chosen from only when the plant is not one of the
	// buttons: a plant chosen by its button is not chosen here as well.
	first := "Choose the plant"
	if len(v.Recent) > 0 {
		first = "Another plant"
	}

	v.Species = []option{{Value: "", Label: first, Selected: s.SpeciesID.Zero() || chosen}}
	for _, sp := range plants {
		label := sp.Common.EN
		if sp.Scientific != "" {
			label += " (" + sp.Scientific + ")"
		}

		v.Species = append(v.Species, option{Value: sp.ID.String(), Label: label, Selected: sp.ID == s.SpeciesID && !chosen})
	}

	kind := s.Kind
	if kind == "" && s.Outcome == inboxbus.AsPlanted {
		kind = photobus.Young
	}

	// Shown as six buttons, three across a phone, under "What it shows":
	// the short name of each fits a button on one line, where "Leaf
	// close-up" broke in the middle of a word.
	for _, k := range photobus.Kinds {
		v.Kinds = append(v.Kinds, option{Value: string(k), Label: tileWords[k], Selected: k == kind})
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
