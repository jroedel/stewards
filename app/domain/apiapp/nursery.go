package apiapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Nursery stock is what the nurseries had, for planning a bed: lines arrive
// by sorting nursery photos (inbox.go), and are read and corrected here. A
// steward's Claude turns the latest visits into a bed's candidate list by
// reading these with each matched plant's light, water and status.

// Nursery is what the API needs from the nursery rules.
type Nursery interface {
	All(ctx context.Context) ([]nurserybus.Stock, error)
	Update(ctx context.Context, id types.ID, f nurserybus.Fields) (nurserybus.Line, error)
	Register(ctx context.Context) ([]nurserybus.Nursery, error)
	LastVisits(ctx context.Context) (map[types.ID]time.Time, error)
}

// NurseryJSON is a nursery in the register as the API shows it. Read only:
// the register is a steward's to change, on the screens, and a stock sort
// that names a new nursery adds it there by its name and nothing more.
type NurseryJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address,omitempty"`
	Website   string `json:"website,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Note      string `json:"note,omitempty"`
	LastVisit string `json:"last_visit,omitempty"`
}

// VisitJSON is a visit as the API shows it.
type VisitJSON struct {
	NurseryID string     `json:"nursery_id"`
	Nursery   string     `json:"nursery"`
	Day       string     `json:"day"`
	Latest    bool       `json:"latest"`
	Lines     []LineJSON `json:"lines"`
}

// LineJSON is a line of stock as the API shows it.
type LineJSON struct {
	ID        string `json:"id"`
	Species   string `json:"species,omitempty"`
	NameOnTag string `json:"name_on_tag,omitempty"`
	PotSize   string `json:"pot_size,omitempty"`
	Price     string `json:"price,omitempty"`
	Count     int    `json:"count,omitempty"`
	Note      string `json:"note,omitempty"`
	PhotoURL  string `json:"photo_url,omitempty"`
}

// LineIn is the body of a PUT to correct a line.
type LineIn struct {
	Species   string `json:"species"`
	NameOnTag string `json:"name_on_tag"`
	PotSize   string `json:"pot_size"`
	Price     string `json:"price"`
	Count     int    `json:"count"`
	Note      string `json:"note"`
}

func (a app) nurseryEndpoints() []Endpoint {
	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/nurseries", NeedsKey: true,
			Summary: "The register of nurseries the stewards buy from, by name: where each is, how to reach it, a note on what to know there, and the day it was last visited. A stock sort's nursery is one of these names; a name not here is added to the register as it is written. Changed only by a steward, on the screens.",
			Returns: `{"nurseries": [{id, name, address, website, phone, note, last_visit}]}`,
			handler: a.listNurseries,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/nursery", NeedsKey: true,
			Summary: "Nursery stock: every visit to a nursery, the most recent first, with what it had. latest is true for each nursery's most recent visit, which is what is on its tables now; species is the plant here a line was matched to, for its light, water and status from GET /api/v1/species/{slug}.",
			Returns: `{"visits": [{nursery_id, nursery, day, latest, lines: [{id, species, name_on_tag, pot_size, price, count, note, photo_url}]}]}`,
			handler: a.listNursery,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/nursery/lines/{id}", NeedsKey: true,
			Summary: "Correct a line of nursery stock: a tag read wrongly, a plant matched, a price. Send the whole line; a field left out is emptied.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "species", Type: "string", Description: "The plant here it is, by slug; leave it out for not matched."},
				{Name: "name_on_tag", Type: "string", Description: "The name on the tag. Needed unless species is given."},
				{Name: "pot_size", Type: "string", Description: `As the tag writes it, such as "1 gal".`},
				{Name: "price", Type: "string", Description: `Dollars and cents, such as "12.99".`},
				{Name: "count", Type: "integer", Description: "How many were on the table."},
				{Name: "note", Type: "string", Description: "A note on the line."},
			}},
			Returns: `200 {"line": line}`,
			handler: a.putLine,
		},
	}
}

func (a app) listNursery(w http.ResponseWriter, r *http.Request) {
	all, err := a.nursery.All(r.Context())
	if err != nil {
		a.fail(w, r, "listing nursery stock", err)

		return
	}

	_, species, err := a.slugs(r.Context())
	if err != nil {
		a.fail(w, r, "naming nursery stock's plants", err)

		return
	}

	cutoff := time.Now().Add(-inboxbus.StockKept)
	seen := map[types.ID]bool{}
	out := []VisitJSON{}

	for _, st := range all {
		v := VisitJSON{
			NurseryID: st.Visit.NurseryID.String(), Nursery: st.Visit.Nursery,
			Day: st.Visit.Day.In(types.Garden).Format(time.DateOnly), Lines: []LineJSON{},
		}

		if !seen[st.Visit.NurseryID] {
			seen[st.Visit.NurseryID], v.Latest = true, true
		}

		for _, l := range st.Lines {
			v.Lines = append(v.Lines, a.lineOf(l, species, st.Visit.Day.After(cutoff)))
		}

		out = append(out, v)
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"visits": out})
}

func (a app) listNurseries(w http.ResponseWriter, r *http.Request) {
	all, err := a.nursery.Register(r.Context())
	if err != nil {
		a.fail(w, r, "listing nurseries", err)

		return
	}

	last, err := a.nursery.LastVisits(r.Context())
	if err != nil {
		a.fail(w, r, "reading when the nurseries were visited", err)

		return
	}

	out := []NurseryJSON{}
	for _, n := range all {
		j := NurseryJSON{ID: n.ID.String(), Name: n.Name, Address: n.Address, Website: n.Website, Phone: n.Phone, Note: n.Note}
		if day, ok := last[n.ID]; ok {
			j.LastVisit = day.In(types.Garden).Format(time.DateOnly)
		}

		out = append(out, j)
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"nurseries": out})
}

func (a app) putLine(w http.ResponseWriter, r *http.Request) {
	var in LineIn
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		web.WriteJSON(w, http.StatusNotFound, web.Problem("id", fmt.Sprintf("There is no such line. GET %s/nursery lists them.", Prefix)))

		return
	}

	price, err := nurserybus.ParsePrice(in.Price)
	if err != nil {
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("price", page.Sentence(err.Error())))

		return
	}

	f := nurserybus.Fields{NameOnTag: in.NameOnTag, PotSize: in.PotSize, PriceCents: price, Count: in.Count, Note: in.Note}

	if in.Species != "" {
		sp, ok := a.speciesAt(w, r, in.Species)
		if !ok {
			return
		}

		f.SpeciesID = sp.ID
	}

	l, err := a.nursery.Update(r.Context(), id, f)

	invalid, isInvalid := errors.AsType[nurserybus.Invalid](err)

	switch {
	case errors.Is(err, nurserybus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("id", fmt.Sprintf("There is no such line. GET %s/nursery lists them.", Prefix)))

		return
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case err != nil:
		a.fail(w, r, "correcting a line of nursery stock", err)

		return
	}

	_, species, err := a.slugs(r.Context())
	if err != nil {
		a.fail(w, r, "naming a line's plant", err)

		return
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"line": a.lineOf(l, species, false)})
}

// lineOf is a line as the API shows it, with its photo's address when
// withPhoto: while the inbox still keeps it.
func (a app) lineOf(l nurserybus.Line, species map[types.ID]string, withPhoto bool) LineJSON {
	out := LineJSON{
		ID: l.ID.String(), Species: species[l.SpeciesID], NameOnTag: l.NameOnTag, PotSize: l.PotSize,
		Price: nurserybus.PriceWords(l.PriceCents), Count: l.Count, Note: l.Note,
	}

	if withPhoto && !l.InboxID.Zero() {
		out.PhotoURL = a.base + Prefix + "/inbox/" + l.InboxID.String() + "/large.jpg"
	}

	return out
}
