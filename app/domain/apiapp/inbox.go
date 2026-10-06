package apiapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/nursery/nurserybus"
	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// The photo inbox is where a steward's photos wait to be sorted, and sorting
// it is the job a steward's Claude is best placed to help with: look at each
// photo, say what plant it is and why, and file it once the steward agrees.
// So the API reads the inbox, serves its pictures to the key, and sorts a
// photo through inboxbus.Sort -- the same rule the sort screen uses -- into a
// plant's photo, a planting, a line of a nursery's stock, or "not sure yet".
//
// Not a discard. Throwing a photo away cannot be undone, and nothing is
// removed through the API: a photo that should go is set aside with a note
// saying so, and a steward discards it on its screen.

// Inbox is what the API needs from the inbox rules.
type Inbox interface {
	Waiting(ctx context.Context) ([]inboxbus.Item, error)
	SetAside(ctx context.Context) ([]inboxbus.Item, error)
	ByID(ctx context.Context, id types.ID) (inboxbus.Item, error)
	Sort(ctx context.Context, id, by types.ID, s inboxbus.Sorting) (inboxbus.Result, error)
	Open(ctx context.Context, id types.ID, size photobus.Size) (inboxbus.Item, photobus.File, error)
}

// InboxItemJSON is an inbox photo as the API shows it. Not its GPS position:
// that is never shown as fact (design.md §3), and the place a steward chose
// is the answer to where.
type InboxItemJSON struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	At        string `json:"at"`
	Place     string `json:"place,omitempty"`
	Note      string `json:"note,omitempty"`
	TakenAt   string `json:"taken_at,omitempty"`
	LargeURL  string `json:"large_url,omitempty"`
	SmallURL  string `json:"small_url,omitempty"`
	FullURL   string `json:"full_url,omitempty"`
	SortURL   string `json:"sort_url"`
	ScreenURL string `json:"screen_url"`

	// Once sorted: what it became.
	Outcome string `json:"outcome,omitempty"`
	Species string `json:"species,omitempty"`
}

// SortIn is the body of a POST to sort an inbox photo.
type SortIn struct {
	Outcome string `json:"outcome"`
	Species string `json:"species"`
	Kind    string `json:"kind"`
	Place   string `json:"place"`
	Note    string `json:"note"`

	// For stock.
	Nursery   string `json:"nursery"`
	NameOnTag string `json:"name_on_tag"`
	PotSize   string `json:"pot_size"`
	Price     string `json:"price"`
	Count     int    `json:"count"`
}

func (a app) inboxEndpoints() []Endpoint {
	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/inbox", NeedsKey: true,
			Summary: "The photos waiting to be sorted, newest first: sent by a steward from the garden or a nursery, with where they were taken if the steward said. ?status=unsure for the ones set aside instead.",
			Returns: `{"status": "new", "photos": [{id, status, at, place, note, taken_at, large_url, small_url, full_url, sort_url, screen_url}]}`,
			handler: a.listInbox,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/inbox/{id}/{file}", NeedsKey: true,
			Summary: "An inbox photo's picture: large.jpg (1600 pixels on its longer side), small.jpg (800), or full_url, the photo at the size it was sent (up to 4096) with the camera's details taken out, for telling apart what the large one blurs -- full.jpg, or full.png for a PNG. Only while it is in the inbox: once sorted, it is the plant's.",
			Returns: "image/jpeg, or image/png for a PNG's full picture", handler: a.inboxPicture,
		},
		{
			Method: http.MethodPost, Path: Prefix + "/inbox/{id}/sort", NeedsKey: true,
			Summary: "Sort a photo: into a plant's photos, into a plant just planted at a place, or set aside as not sure yet. A plant's photo arrives not checked, as every photo from the API does. Sending the same sort twice changes nothing. A photo is discarded by a steward on its screen, never here: set it aside with a note saying why.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "outcome", Type: "string", Required: true, Values: a.sortOutcomes(), Description: "photo: a photo of the plant, to add to its photos. planted: the plant was just planted at the place; it is listed there to protect, off the To plant list, and the photo is added as its young plant. stock: a plant for sale, from a photo taken at a nursery; a line of that nursery's stock on the day, keeping the photo for three months. unsure: set aside, with a question in the note."},
				{Name: "species", Type: "string", Description: "The plant's slug, for photo and planted. It must already be added: PUT /api/v1/species/{slug} first."},
				{Name: "kind", Type: "string", Values: kindNames(), Description: "What it shows, for photo; for planted, leave it out for young."},
				{Name: "place", Type: "string", Description: "A place slug: where it was taken, for photo, or planted, for planted. Leave it out to keep the place the photo was sent with. Never for a photo taken at a nursery."},
				{Name: "note", Type: "string", Description: fmt.Sprintf("For unsure: the question, or why it should go, at most %d characters. For stock: a note on the line.", inboxbus.MaxNote)},
				{Name: "nursery", Type: "string", Description: "For stock: the nursery's name, as GET /api/v1/nursery writes it for one visited before."},
				{Name: "name_on_tag", Type: "string", Description: "For stock: the name on the tag, as the tag writes it. Needed unless species is given; give both when the tag is legible."},
				{Name: "pot_size", Type: "string", Description: `For stock: as the tag writes it, such as "1 gal" or "4 in".`},
				{Name: "price", Type: "string", Description: `For stock: dollars and cents, such as "12.99".`},
				{Name: "count", Type: "integer", Description: "For stock: how many were on the table, if counted."},
			}},
			Returns: `200 {"outcome": "photo", "unchanged": false, "photo": {"id", "photos_url"}, "listing": {...}, "inbox": inbox photo}. 409 when it was already sorted differently, with what it became.`,
			handler: a.sortInbox,
		},
	}
}

func (a app) listInbox(w http.ResponseWriter, r *http.Request) {
	status := cmp.Or(r.URL.Query().Get("status"), string(inboxbus.New))

	var (
		items []inboxbus.Item
		err   error
	)

	switch inboxbus.Status(status) {
	case inboxbus.New:
		items, err = a.inbox.Waiting(r.Context())
	case inboxbus.Unsure:
		items, err = a.inbox.SetAside(r.Context())
	default:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("status", "Send status=new, for the photos waiting, or status=unsure, for the ones set aside."))

		return
	}

	if err != nil {
		a.fail(w, r, "listing the inbox", err)

		return
	}

	places, species, err := a.slugs(r.Context())
	if err != nil {
		a.fail(w, r, "naming the inbox's places", err)

		return
	}

	out := []InboxItemJSON{}
	for _, it := range items {
		out = append(out, a.inboxItemOf(it, places, species))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"status": status, "photos": out})
}

func (a app) inboxPicture(w http.ResponseWriter, r *http.Request) {
	id, err := types.ParseID(r.PathValue("id"))
	name := r.PathValue("file")
	size, ok := photobus.ServedSize(name)

	noSuch := func() {
		web.WriteJSON(w, http.StatusNotFound, web.Problem("", fmt.Sprintf("There is no such picture. An inbox photo's are at the addresses GET %s/inbox gives: large_url, small_url and full_url.", Prefix)))
	}

	if err != nil || !ok {
		noSuch()

		return
	}

	it, f, err := a.inbox.Open(r.Context(), id, size)

	switch {
	case errors.Is(err, inboxbus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("id", "That photo is not waiting in the inbox: it may have been sorted, and is then on its plant's photos."))

		return
	case err != nil:
		a.fail(w, r, "opening an inbox photo", err)

		return
	}
	defer f.Close()

	if name != photobus.ServedName(size, it.Format) {
		noSuch()

		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", photobus.ContentType(name))

	var modified time.Time
	if st, err := f.Stat(); err == nil {
		modified = st.ModTime()
	}

	http.ServeContent(w, r, "", modified, f)
}

func (a app) sortInbox(w http.ResponseWriter, r *http.Request) {
	var in SortIn
	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	id, err := types.ParseID(r.PathValue("id"))
	if err != nil {
		web.WriteJSON(w, http.StatusNotFound, web.Problem("id", fmt.Sprintf("There is no such inbox photo. GET %s/inbox lists the ones waiting.", Prefix)))

		return
	}

	it, err := a.inbox.ByID(r.Context(), id)

	switch {
	case errors.Is(err, inboxbus.ErrNotFound):
		web.WriteJSON(w, http.StatusNotFound, web.Problem("id", fmt.Sprintf("There is no such inbox photo. GET %s/inbox lists the ones waiting.", Prefix)))

		return
	case err != nil:
		a.fail(w, r, "reading an inbox photo", err)

		return
	}

	outcome := inboxbus.Outcome(in.Outcome)

	switch outcome {
	case inboxbus.AsPhoto, inboxbus.AsPlanted, inboxbus.AsUnsure:
	case inboxbus.AsStock:
		if a.nursery == nil {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("outcome", "Nursery stock is not kept here. Send photo or unsure."))

			return
		}
	case inboxbus.AsDiscard:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("outcome", "A photo is discarded by a steward on its screen, after looking, not through the API. Send unsure, with a note saying why it should go."))

		return
	default:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("outcome", "Send photo, planted or unsure."))

		return
	}

	s := inboxbus.Sorting{Outcome: outcome, Kind: photobus.Kind(in.Kind), Note: in.Note}

	if outcome == inboxbus.AsStock {
		price, err := nurserybus.ParsePrice(in.Price)
		if err != nil {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("price", page.Sentence(err.Error())))

			return
		}

		s.Nursery = in.Nursery
		s.Stock = nurserybus.Fields{NameOnTag: in.NameOnTag, PotSize: in.PotSize, PriceCents: price, Count: in.Count, Note: in.Note}
	}

	var sp speciesbus.Species

	if in.Species != "" {
		var ok bool
		if sp, ok = a.speciesAt(w, r, in.Species); !ok {
			return
		}

		s.SpeciesID, s.Stock.SpeciesID = sp.ID, sp.ID
	}

	if in.Place != "" {
		if it.At == inboxbus.Nursery {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("place", "That photo was taken at a nursery, so it is of no place here. Leave place out."))

			return
		}

		pl, err := a.places.BySlug(r.Context(), in.Place)
		if err != nil {
			web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("place", fmt.Sprintf("No place has the slug %q. GET %s/places lists the ones there are.", in.Place, Prefix)))

			return
		}

		s.PlaceID = pl.ID
	}

	steward, _ := mid.StewardFrom(r.Context())

	res, err := a.inbox.Sort(r.Context(), it.ID, steward.ID, s)

	invalid, isInvalid := errors.AsType[inboxbus.Invalid](err)
	already, isAlready := errors.AsType[inboxbus.AlreadySorted](err)

	switch {
	case isInvalid:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem(invalid.Field, page.Sentence(invalid.Problem)))

		return
	case isAlready:
		places, species, err := a.slugs(r.Context())
		if err != nil {
			a.fail(w, r, "naming a sorted photo's plant", err)

			return
		}

		body := web.Problem("id", page.Sentence(already.Error())+". GET it from the inbox to see what it became.")

		web.WriteJSON(w, http.StatusConflict, map[string]any{"error": body.Error, "inbox": a.inboxItemOf(already.Item, places, species)})

		return
	case err != nil:
		a.fail(w, r, "sorting an inbox photo", err)

		return
	}

	if !res.Unchanged {
		a.log.InfoContext(r.Context(), "inbox photo sorted", "inbox_id", it.ID.String(), "outcome", outcome,
			"species", sp.Slug, "photo_id", res.Photo.ID.String(), "user_id", steward.ID.String())
	}

	places, species, err := a.slugs(r.Context())
	if err != nil {
		a.fail(w, r, "naming a sorted photo's plant", err)

		return
	}

	out := map[string]any{
		"outcome":   outcome,
		"unchanged": res.Unchanged,
		"inbox":     a.inboxItemOf(res.Item, places, species),
	}

	if photoID := res.Item.PhotoID; !photoID.Zero() {
		out["photo"] = map[string]any{
			"id":         photoID.String(),
			"duplicate":  res.Duplicate,
			"photos_url": a.base + "/steward/species/" + res.Item.SpeciesID.String() + "/photos",
		}
	}

	if outcome == inboxbus.AsPlanted && !res.Unchanged {
		out["listing"] = a.listingOf(res.Listing, sp.Slug)
	}

	if outcome == inboxbus.AsStock && !res.Unchanged {
		out["line"] = a.lineOf(res.Line, species, true)
	}

	web.WriteJSON(w, http.StatusOK, out)
}

// sortOutcomes is what the index offers: stock only where it is kept, and
// never discard.
func (a app) sortOutcomes() []string {
	out := []string{string(inboxbus.AsPhoto), string(inboxbus.AsPlanted)}
	if a.nursery != nil {
		out = append(out, string(inboxbus.AsStock))
	}

	return append(out, string(inboxbus.AsUnsure))
}

func (a app) inboxItemOf(it inboxbus.Item, places, species map[types.ID]string) InboxItemJSON {
	out := InboxItemJSON{
		ID: it.ID.String(), Status: string(it.Status), At: string(it.At),
		Place: places[it.PlaceID], Note: it.Note,
		SortURL:   a.base + Prefix + "/inbox/" + it.ID.String() + "/sort",
		ScreenURL: a.base + "/steward/inbox/" + it.ID.String(),
		Outcome:   string(it.Outcome), Species: species[it.SpeciesID],
	}

	if !it.TakenAt.IsZero() {
		out.TakenAt = it.TakenAt.In(types.Garden).Format(time.RFC3339)
	}

	if it.HasPictures() {
		out.LargeURL = a.base + Prefix + "/inbox/" + it.ID.String() + "/large.jpg"
		out.SmallURL = a.base + Prefix + "/inbox/" + it.ID.String() + "/small.jpg"
		out.FullURL = a.base + Prefix + "/inbox/" + it.ID.String() + "/" + photobus.ServedName(photobus.Full, it.Format)
	}

	return out
}

// slugs is every place's and plant's slug by id.
func (a app) slugs(ctx context.Context) (map[types.ID]string, map[types.ID]string, error) {
	pls, err := a.places.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	sps, err := a.species.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	places, species := map[types.ID]string{}, map[types.ID]string{}
	for _, p := range pls {
		places[p.ID] = p.Slug
	}

	for _, sp := range sps {
		species[sp.ID] = sp.Slug
	}

	return places, species, nil
}
