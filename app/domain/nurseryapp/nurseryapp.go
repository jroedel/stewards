// Package nurseryapp is nursery stock over HTTP: the stewards' list of what
// each nursery had when one of them last walked round it, for planning a bed
// about to be planted, and the screen a line is corrected on; and the
// register of the nurseries themselves (register.go).
//
// Stock arrives by sorting nursery photos in the inbox (inboxapp). What is
// decided here is only how it reads: the latest visit to each nursery first,
// because that is what is on the tables now, and the earlier visits folded
// away below it, because they are history.
package nurseryapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// IndexPath is the stock list.
const IndexPath = "/steward/nursery"

// keptFor is how long a line's photo is kept, as inboxbus keeps it. A copy,
// not an import of the inbox's rules for one number: a visit within it has
// its photos, and one older does not, so the page never shows a picture that
// is gone.
const keptFor = 90 * 24 * time.Hour

// Stock is what this app needs from the nursery rules.
type Stock interface {
	All(ctx context.Context) ([]nurserybus.Stock, error)
	Line(ctx context.Context, id types.ID) (nurserybus.Line, error)
	Update(ctx context.Context, id types.ID, f nurserybus.Fields) (nurserybus.Line, error)

	Register(ctx context.Context) ([]nurserybus.Nursery, error)
	Nursery(ctx context.Context, id types.ID) (nurserybus.Nursery, error)
	CreateNursery(ctx context.Context, f nurserybus.NurseryFields) (nurserybus.Nursery, error)
	UpdateNursery(ctx context.Context, id types.ID, f nurserybus.NurseryFields) (nurserybus.Nursery, error)
	DeleteNursery(ctx context.Context, id types.ID) error
	LastVisits(ctx context.Context) (map[types.ID]time.Time, error)
}

// SpeciesReader is what it needs from the species rules: the plants a line
// may be matched to.
type SpeciesReader interface {
	All(ctx context.Context) ([]speciesbus.Species, error)
}

// Config is what this app needs.
type Config struct {
	Log     *slog.Logger
	Render  *page.Renderer
	Stock   Stock
	Species SpeciesReader

	// Now may be nil, for the real clock.
	Now func() time.Time
}

type app struct {
	log     *slog.Logger
	render  *page.Renderer
	stock   Stock
	species SpeciesReader
	now     func() time.Time
}

// Routes mounts the screens, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{log: cfg.Log, render: cfg.Render, stock: cfg.Stock, species: cfg.Species, now: cfg.Now}
	if a.now == nil {
		a.now = time.Now
	}

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + IndexPath:                      a.list,
		"GET " + IndexPath + "/lines/{id}/edit": a.editForm,
		"POST " + IndexPath + "/lines/{id}":     a.update,

		"GET " + RegisterPath:                   a.register,
		"GET " + RegisterPath + "/new":          a.newNursery,
		"POST " + RegisterPath:                  a.createNursery,
		"GET " + RegisterPath + "/{id}/edit":    a.editNursery,
		"POST " + RegisterPath + "/{id}":        a.updateNursery,
		"POST " + RegisterPath + "/{id}/delete": a.removeNursery,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

type lineRow struct {
	ID        string
	InboxID   string // the photo, while it is kept
	Name      string // the plant's common name, once matched
	Slug      string
	Status    string
	Native    bool
	NameOnTag string
	Facts     string // pot · price · count
	Note      string
}

type visitRow struct {
	NurseryID string // its entry in the register
	Nursery   string
	Day       string
	Lines     []lineRow
}

type listView struct {
	Current []visitRow // the latest visit to each nursery
	Earlier []visitRow
	Done    string
}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	all, err := a.stock.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing nursery stock", err)

		return
	}

	plants, err := a.plantsByID(r.Context())
	if err != nil {
		a.fail(w, r, "listing plants for nursery stock", err)

		return
	}

	var v listView
	if r.URL.Query().Get("done") == "saved" {
		v.Done = "Saved."
	}

	cutoff := a.now().Add(-keptFor)
	seen := map[types.ID]bool{}

	// All is the most recent visit first, so the first of each nursery is
	// its latest.
	for _, st := range all {
		row := visitRow{NurseryID: st.Visit.NurseryID.String(), Nursery: st.Visit.Nursery, Day: lastVisitWords(st.Visit.Day)}
		withPhotos := st.Visit.Day.After(cutoff)

		for _, l := range st.Lines {
			row.Lines = append(row.Lines, rowOf(l, plants, withPhotos))
		}

		if !seen[st.Visit.NurseryID] {
			seen[st.Visit.NurseryID] = true
			v.Current = append(v.Current, row)
		} else {
			v.Earlier = append(v.Earlier, row)
		}
	}

	a.render.Render(w, r, http.StatusOK, "steward-nursery", v)
}

func rowOf(l nurserybus.Line, plants map[types.ID]speciesbus.Species, withPhotos bool) lineRow {
	row := lineRow{ID: l.ID.String(), NameOnTag: l.NameOnTag, Note: l.Note}

	if withPhotos && !l.InboxID.Zero() {
		row.InboxID = l.InboxID.String()
	}

	if sp, ok := plants[l.SpeciesID]; ok {
		row.Name, row.Slug = sp.Common.EN, sp.Slug
		row.Status, row.Native = sp.Status.Label(), sp.Status == speciesbus.StatusNative
	}

	var facts []string
	if l.PotSize != "" {
		facts = append(facts, l.PotSize)
	}

	if p := nurserybus.PriceWords(l.PriceCents); p != "" {
		facts = append(facts, p)
	}

	if l.Count > 0 {
		facts = append(facts, strconv.Itoa(l.Count)+" there")
	}

	row.Facts = strings.Join(facts, " · ")

	return row
}

// ------------------------------------------------------------------ one line

type option struct {
	Value, Label string
	Selected     bool

	// Also is more to find a plant by in a searchable list, not shown: see
	// plantOption.
	Also string
}

// plantOption is a plant as a list of plants offers it: its common name with
// its scientific one, so that either finds it in the list's search box, and
// its Spanish name to be found by as well, which is not shown (the page
// package's find.mjs, data-also).
func plantOption(sp speciesbus.Species, selected bool) option {
	o := option{Value: sp.ID.String(), Label: sp.Common.EN, Selected: selected}
	if sp.Scientific != "" {
		o.Label += " (" + sp.Scientific + ")"
	}

	if sp.Common.ES != sp.Common.EN {
		o.Also = sp.Common.ES
	}

	return o
}

type editView struct {
	ID, InboxID string
	Species     []option
	NameOnTag   string
	PotSize     string
	Price       string
	Count       string
	Note        string
	Problems    map[string]string
}

func (a app) editForm(w http.ResponseWriter, r *http.Request) {
	l, ok := a.loadLine(w, r)
	if !ok {
		return
	}

	v := editView{
		NameOnTag: l.NameOnTag, PotSize: l.PotSize, Note: l.Note,
		Price: strings.TrimPrefix(nurserybus.PriceWords(l.PriceCents), "$"), Problems: map[string]string{},
	}

	if l.Count > 0 {
		v.Count = strconv.Itoa(l.Count)
	}

	a.showEdit(w, r, http.StatusOK, l, v, l.SpeciesID)
}

func (a app) update(w http.ResponseWriter, r *http.Request) {
	l, ok := a.loadLine(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := editView{
		NameOnTag: r.PostFormValue("name_on_tag"), PotSize: r.PostFormValue("pot_size"), Note: r.PostFormValue("note"),
		Price: r.PostFormValue("price"), Count: r.PostFormValue("count"), Problems: map[string]string{},
	}

	f := nurserybus.Fields{NameOnTag: v.NameOnTag, PotSize: v.PotSize, Note: v.Note}

	if raw := r.PostFormValue("species"); raw != "" {
		id, err := types.ParseID(raw)
		if err != nil {
			v.Problems["species"] = "Choose from the list."
		}
		f.SpeciesID = id
	}

	var err error
	if f.PriceCents, err = nurserybus.ParsePrice(v.Price); err != nil {
		v.Problems["price"] = page.Sentence(err.Error())
	}

	if c := strings.TrimSpace(v.Count); c != "" {
		if f.Count, err = strconv.Atoi(c); err != nil {
			v.Problems["count"] = "Write how many there were as a number, or leave it empty."
		}
	}

	if len(v.Problems) == 0 {
		_, err := a.stock.Update(r.Context(), l.ID, f)

		invalid, isInvalid := errors.AsType[nurserybus.Invalid](err)

		switch {
		case err == nil:
			http.Redirect(w, r, IndexPath+"?done=saved#line-"+l.ID.String(), http.StatusSeeOther)

			return
		case isInvalid:
			v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
		default:
			a.fail(w, r, "saving a line of nursery stock", err)

			return
		}
	}

	a.showEdit(w, r, http.StatusUnprocessableEntity, l, v, f.SpeciesID)
}

func (a app) showEdit(w http.ResponseWriter, r *http.Request, status int, l nurserybus.Line, v editView, chosen types.ID) {
	plants, err := a.species.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing plants for nursery stock", err)

		return
	}

	all, err := a.stock.All(r.Context())
	if err != nil {
		a.fail(w, r, "reading a line's visit", err)

		return
	}

	// The photo, if its visit is recent enough for it to be kept: the
	// visit's day is what the inbox prunes by, not when it was sorted.
	v.ID = l.ID.String()
	for _, st := range all {
		if st.Visit.ID == l.VisitID && !l.InboxID.Zero() && st.Visit.Day.After(a.now().Add(-keptFor)) {
			v.InboxID = l.InboxID.String()
		}
	}

	v.Species = []option{{Value: "", Label: "Not matched yet", Selected: chosen.Zero()}}
	for _, sp := range plants {
		v.Species = append(v.Species, plantOption(sp, sp.ID == chosen))
	}

	a.render.Render(w, r, status, "steward-nursery-line", v)
}

// ------------------------------------------------------------------ the parts

func (a app) plantsByID(ctx context.Context) (map[types.ID]speciesbus.Species, error) {
	all, err := a.species.All(ctx)
	if err != nil {
		return nil, err
	}

	by := map[types.ID]speciesbus.Species{}
	for _, sp := range all {
		by[sp.ID] = sp
	}

	return by, nil
}

func (a app) loadLine(w http.ResponseWriter, r *http.Request) (nurserybus.Line, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return nurserybus.Line{}, false
	}

	l, err := a.stock.Line(r.Context(), id)

	switch {
	case errors.Is(err, nurserybus.ErrNotFound):
		http.Error(w, "That line of nursery stock is not here. Go back to the nursery stock.", http.StatusNotFound)

		return nurserybus.Line{}, false
	case err != nil:
		a.fail(w, r, "reading a line of nursery stock", err)

		return nurserybus.Line{}, false
	}

	return l, true
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
