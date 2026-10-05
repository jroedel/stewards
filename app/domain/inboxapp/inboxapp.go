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
		Nursery: it.At == inboxbus.Nursery, Note: it.Note,
		Where: names[it.PlaceID],
	}

	if !it.TakenAt.IsZero() {
		row.Time = it.TakenAt.In(types.Garden).Format("3:04 pm")
	}

	return row
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

// ------------------------------------------------------------------ sorting one

type sortView struct {
	ID            string
	Width, Height int

	// What is known of it.
	Taken    string
	Nursery  bool
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

	// A nursery photo is not of anything planted here; the choice is not
	// offered rather than refused after it is made.
	if it.At == inboxbus.Nursery && as == string(inboxbus.AsPlanted) {
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
	v.Nursery, v.Where, v.Note = it.At == inboxbus.Nursery, names[it.PlaceID], it.Note

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

		// The nursery already recorded for the photo's day, if any: a
		// morning's twenty tags are one nursery, and typing it once is
		// enough.
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
