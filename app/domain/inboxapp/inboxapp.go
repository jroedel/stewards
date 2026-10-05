// Package inboxapp is the photo inbox over HTTP: the screen a steward sends a
// batch of photos from, in the garden or at a nursery, and the list of what is
// waiting to be sorted.
//
// The rules are inboxbus's. What is decided here is the shape of a batch.
// Ten photos chosen from a phone's camera roll arrive in one form post, every
// one of them saying the same where, place and note; each is kept or refused
// on its own, so a screenshot chosen by mistake costs that one and not the
// other nine.
//
// One post rather than a script sending a photo at a time, which would show
// "Sending 3 of 10" and survive a dropped signal better. The pages carry no
// script, and the header policy says why adding the first one deserves a
// rollout of its own (page.Policy). Until then a batch is all one request, and
// its limits below are sized for that.
package inboxapp

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
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

// Inbox is what this app needs from the inbox rules.
type Inbox interface {
	Check(f inboxbus.Fields) (inboxbus.Fields, error)
	Add(ctx context.Context, f inboxbus.Fields, data []byte) (inboxbus.Item, error)
	Waiting(ctx context.Context) ([]inboxbus.Item, error)
	Open(ctx context.Context, id types.ID, size photobus.Size) (inboxbus.Item, photobus.File, error)
}

// PlaceReader is what it needs from the place rules: the list a batch's place
// is chosen from, and the names on the list of what is waiting.
type PlaceReader interface {
	All(ctx context.Context) ([]placebus.Place, error)
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Render *page.Renderer
	Inbox  Inbox
	Places PlaceReader
}

type app struct {
	log    *slog.Logger
	render *page.Renderer
	inbox  Inbox
	places PlaceReader
}

// Routes mounts the inbox, every route behind guard. Its pictures too: an
// inbox photo is a steward's alone, whatever it shows.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{log: cfg.Log, render: cfg.Render, inbox: cfg.Inbox, places: cfg.Places}

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + IndexPath:                  a.list,
		"GET " + IndexPath + "/new":         a.newForm,
		UploadPattern:                       a.upload,
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
	Where         string
	Nursery       bool
	Note          string
}

type day struct {
	Label string
	Items []itemRow
}

type listView struct {
	Count int
	Days  []day
	Done  string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	items, err := a.inbox.Waiting(r.Context())
	if err != nil {
		a.fail(w, r, "listing the inbox", err)

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
	if q := r.URL.Query(); q.Get("done") == "sent" {
		n, _ := strconv.Atoi(q.Get("n"))
		d, _ := strconv.Atoi(q.Get("d"))
		v.Done = sentWords(max(n, 0), max(d, 0))
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

		row := itemRow{
			ID: it.ID.String(), Width: it.Small.Width, Height: it.Small.Height,
			Nursery: it.At == inboxbus.Nursery, Note: it.Note,
			Where: names[it.PlaceID],
		}

		if !it.TakenAt.IsZero() {
			row.Time = it.TakenAt.In(types.Garden).Format("3:04 pm")
		}

		last := &v.Days[len(v.Days)-1]
		last.Items = append(last.Items, row)
	}

	a.render.Render(w, r, http.StatusOK, "steward-inbox", v)
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
	Nursery  bool
	Places   []option
	Note     string
	Problems map[string]string

	// After a batch some of which could not be kept: what was, and each
	// photo that was not, by the name the phone gave it.
	Kept    string
	Refused []refusal

	MaxPhotos int
	MaxMB     int
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

	f := inboxbus.Fields{
		At:   inboxbus.At(r.PostFormValue("at")),
		Note: r.PostFormValue("note"),
	}

	if u, ok := mid.StewardFrom(r.Context()); ok {
		f.FromID = u.ID
	}

	if s := r.PostFormValue("place"); s != "" {
		id, err := types.ParseID(s)
		if err != nil {
			v.Problems["place"] = "Choose the place from the list, or leave it empty."
		}
		f.PlaceID = id
	}

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

// readPhoto is one uploaded file's bytes, or a sentence saying what to do.
func readPhoto(h *multipart.FileHeader) ([]byte, string) {
	if h.Size > inboxbus.MaxBytes {
		return nil, fmt.Sprintf("Larger than %d MB. Send it as the camera saved it, not a video.", inboxbus.MaxBytes>>20)
	}

	file, err := h.Open()
	if err != nil {
		return nil, "It did not arrive whole. Send it again."
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, inboxbus.MaxBytes+1))

	switch {
	case err != nil:
		return nil, "It did not arrive whole. Send it again."
	case len(data) > inboxbus.MaxBytes:
		return nil, fmt.Sprintf("Larger than %d MB. Send it as the camera saved it, not a video.", inboxbus.MaxBytes>>20)
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

	v.Nursery, v.Note = f.At == inboxbus.Nursery, f.Note
	v.MaxPhotos, v.MaxMB = MaxPhotos, MaxBytes>>20

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

// ------------------------------------------------------------------ the pictures

func (a app) file(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return
	}

	var size photobus.Size

	switch r.PathValue("file") {
	case "large.jpg":
		size = photobus.Large
	case "small.jpg":
		size = photobus.Small
	default:
		http.NotFound(w, r)

		return
	}

	_, f, err := a.inbox.Open(r.Context(), id, size)

	switch {
	case errors.Is(err, inboxbus.ErrNotFound):
		http.NotFound(w, r)

		return
	case err != nil:
		a.fail(w, r, "opening an inbox photo", err)

		return
	}
	defer f.Close()

	// Kept nowhere but the steward's screen: a photo nobody has looked at
	// may have somebody in it.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "image/jpeg")

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
