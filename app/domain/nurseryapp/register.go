package nurseryapp

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/types"
)

// RegisterPath is the register of nurseries: where each one is, how to reach
// it, and what to know about it, beside the stock that says what it had.
const RegisterPath = "/steward/nurseries"

type nurseryRow struct {
	ID, Name, Address, Website, Phone, Note string

	// MapURL finds the address on OpenStreetMap: a link and nothing more,
	// so no map is drawn here and nobody else's script runs on the page.
	MapURL string

	// Tel is the phone as a link to ring it. A template.URL because
	// html/template allows only http, https and mailto in an href, and
	// rewrites anything else to "#ZgotmplZ"; it is built from the number's
	// digits alone, so nothing a steward typed reaches it but those.
	Tel template.URL

	LastVisit string // "Saturday 3 October 2026", or empty for never
}

type registerView struct {
	Nurseries []nurseryRow
	Done      string
}

func (a app) register(w http.ResponseWriter, r *http.Request) {
	all, err := a.stock.Register(r.Context())
	if err != nil {
		a.fail(w, r, "listing nurseries", err)

		return
	}

	last, err := a.stock.LastVisits(r.Context())
	if err != nil {
		a.fail(w, r, "reading when the nurseries were visited", err)

		return
	}

	v := registerView{Done: map[string]string{"added": "Nursery added.", "saved": "Saved.", "removed": "Nursery removed."}[r.URL.Query().Get("done")]}

	for _, n := range all {
		row := nurseryRow{ID: n.ID.String(), Name: n.Name, Address: n.Address, Website: n.Website, Phone: n.Phone, Note: n.Note}

		if n.Address != "" {
			row.MapURL = "https://www.openstreetmap.org/search?query=" + url.QueryEscape(n.Address)
		}

		if digits := telDigits(n.Phone); digits != "" {
			row.Tel = template.URL("tel:" + digits)
		}

		if day, ok := last[n.ID]; ok {
			row.LastVisit = lastVisitWords(day)
		}

		v.Nurseries = append(v.Nurseries, row)
	}

	a.render.Render(w, r, http.StatusOK, "steward-nurseries", v)
}

// telDigits is a phone number as a tel: link wants it: its digits, and a
// leading + if it had one.
func telDigits(phone string) string {
	var b strings.Builder

	for i, c := range strings.TrimSpace(phone) {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '+' && i == 0:
			b.WriteRune(c)
		}
	}

	if strings.Trim(b.String(), "+") == "" {
		return ""
	}

	return b.String()
}

// ------------------------------------------------------------------ one nursery

type nurseryForm struct {
	ID                                  string // empty for a new one
	Name, Address, Website, Phone, Note string
	Visited                             bool // and so not to be removed
	Problems                            map[string]string
	DeleteProblem                       string
}

func formOf(n nurserybus.Nursery) nurseryForm {
	return nurseryForm{
		ID: n.ID.String(), Name: n.Name, Address: n.Address, Website: n.Website, Phone: n.Phone, Note: n.Note,
		Problems: map[string]string{},
	}
}

func (a app) newNursery(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, r, http.StatusOK, "steward-nursery-form", nurseryForm{Problems: map[string]string{}})
}

func (a app) createNursery(w http.ResponseWriter, r *http.Request) {
	f, v, ok := a.nurseryFields(w, r)
	if !ok {
		return
	}

	n, err := a.stock.CreateNursery(r.Context(), f)
	if a.saved(w, r, &v, err, "adding a nursery") {
		http.Redirect(w, r, RegisterPath+"?done=added#nursery-"+n.ID.String(), http.StatusSeeOther)
	}
}

func (a app) editNursery(w http.ResponseWriter, r *http.Request) {
	n, ok := a.loadNursery(w, r)
	if !ok {
		return
	}

	v := formOf(n)
	if v.Visited, ok = a.visited(w, r, n.ID); !ok {
		return
	}

	a.render.Render(w, r, http.StatusOK, "steward-nursery-form", v)
}

func (a app) updateNursery(w http.ResponseWriter, r *http.Request) {
	n, ok := a.loadNursery(w, r)
	if !ok {
		return
	}

	f, v, ok := a.nurseryFields(w, r)
	if !ok {
		return
	}

	v.ID = n.ID.String()
	if v.Visited, ok = a.visited(w, r, n.ID); !ok {
		return
	}

	_, err := a.stock.UpdateNursery(r.Context(), n.ID, f)
	if a.saved(w, r, &v, err, "changing a nursery") {
		http.Redirect(w, r, RegisterPath+"?done=saved#nursery-"+n.ID.String(), http.StatusSeeOther)
	}
}

func (a app) removeNursery(w http.ResponseWriter, r *http.Request) {
	n, ok := a.loadNursery(w, r)
	if !ok {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	v := formOf(n)
	v.DeleteProblem = "Tick the box to confirm, then press Remove again."

	if r.PostFormValue("confirm") == "yes" {
		err := a.stock.DeleteNursery(r.Context(), n.ID)

		switch {
		case err == nil:
			http.Redirect(w, r, RegisterPath+"?done=removed", http.StatusSeeOther)

			return
		case errors.Is(err, nurserybus.ErrInUse):
			v.Visited = true
			v.DeleteProblem = "This nursery has visits in the nursery stock, so it stays with them. Change its name or its note instead."
		case errors.Is(err, nurserybus.ErrNotFound):
			http.Redirect(w, r, RegisterPath+"?done=removed", http.StatusSeeOther)

			return
		default:
			a.fail(w, r, "removing a nursery", err)

			return
		}
	}

	a.render.Render(w, r, http.StatusUnprocessableEntity, "steward-nursery-form", v)
}

// nurseryFields reads the form. ok is false once it has answered.
func (a app) nurseryFields(w http.ResponseWriter, r *http.Request) (nurserybus.NurseryFields, nurseryForm, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return nurserybus.NurseryFields{}, nurseryForm{}, false
	}

	get := r.PostFormValue
	v := nurseryForm{
		Name: get("name"), Address: get("address"), Website: get("website"), Phone: get("phone"), Note: get("note"),
		Problems: map[string]string{},
	}

	return nurserybus.NurseryFields{Name: v.Name, Address: v.Address, Website: v.Website, Phone: v.Phone, Note: v.Note}, v, true
}

// saved answers a create or an update that did not go through, and is true
// for the caller to redirect when it did.
func (a app) saved(w http.ResponseWriter, r *http.Request, v *nurseryForm, err error, what string) bool {
	invalid, isInvalid := errors.AsType[nurserybus.Invalid](err)

	switch {
	case err == nil:
		return true
	case isInvalid:
		v.Problems[invalid.Field] = page.Sentence(invalid.Problem)
		a.render.Render(w, r, http.StatusUnprocessableEntity, "steward-nursery-form", *v)
	case errors.Is(err, nurserybus.ErrNotFound):
		http.Error(w, "That nursery is not in the register. Go back to the nurseries.", http.StatusNotFound)
	default:
		a.fail(w, r, what, err)
	}

	return false
}

// visited is whether the nursery has a visit, which keeps it in the register.
func (a app) visited(w http.ResponseWriter, r *http.Request, id types.ID) (bool, bool) {
	last, err := a.stock.LastVisits(r.Context())
	if err != nil {
		a.fail(w, r, "reading a nursery's visits", err)

		return false, false
	}

	_, ok := last[id]

	return ok, true
}

func (a app) loadNursery(w http.ResponseWriter, r *http.Request) (nurserybus.Nursery, bool) {
	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)

		return nurserybus.Nursery{}, false
	}

	n, err := a.stock.Nursery(r.Context(), id)

	switch {
	case errors.Is(err, nurserybus.ErrNotFound):
		http.Error(w, "That nursery is not in the register. Go back to the nurseries.", http.StatusNotFound)

		return nurserybus.Nursery{}, false
	case err != nil:
		a.fail(w, r, "reading a nursery", err)

		return nurserybus.Nursery{}, false
	}

	return n, true
}

// lastVisitWords is a visit's day as the screens write it.
func lastVisitWords(day time.Time) string {
	return day.In(types.Garden).Format("Monday 2 January 2006")
}
