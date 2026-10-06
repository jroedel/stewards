package apiapp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jroedel/stewards/app/domain/apiapp"
	"github.com/jroedel/stewards/business/domain/inbox/inboxbus"
	"github.com/jroedel/stewards/business/domain/listing/listingbus"
	"github.com/jroedel/stewards/business/domain/place/placebus"
	"github.com/jroedel/stewards/business/types"
)

// inboxed puts a photo in the inbox, as the send screen does, and returns
// its id.
func (s *site) inboxed(at inboxbus.At, place types.ID, seed uint64) string {
	s.t.Helper()

	it, err := s.inbox.Add(s.t.Context(), inboxbus.Fields{FromID: s.steward, At: at, PlaceID: place, Note: "by the outlet"}, noisy(s.t, seed))
	if err != nil {
		s.t.Fatal(err)
	}

	return it.ID.String()
}

func (s *site) sortPhoto(id string, body any) *httptest.ResponseRecorder {
	s.t.Helper()

	b, _ := json.Marshal(body)

	return s.api(http.MethodPost, "/api/v1/inbox/"+id+"/sort", s.key, bytes.NewReader(b), "application/json")
}

type inboxList struct {
	Status string                 `json:"status"`
	Photos []apiapp.InboxItemJSON `json:"photos"`
}

type sorted struct {
	Outcome   string `json:"outcome"`
	Unchanged bool   `json:"unchanged"`
	Photo     struct {
		ID        string `json:"id"`
		PhotosURL string `json:"photos_url"`
	} `json:"photo"`
	Listing *apiapp.ListingJSON  `json:"listing"`
	Inbox   apiapp.InboxItemJSON `json:"inbox"`
}

func (s *site) inflow() placebus.Place {
	s.t.Helper()

	p, err := s.places.Create(s.t.Context(), placebus.Fields{Slug: "inflow", Name: types.Text{EN: "Inflow band"}})
	if err != nil {
		s.t.Fatal(err)
	}

	return p
}

// A steward's Claude reads the inbox and looks at each photo, with the key and
// only with it.
func TestTheInboxIsReadWithAKey(t *testing.T) {
	s := serve(t)
	inflow := s.inflow()
	id := s.inboxed(inboxbus.Property, inflow.ID, 1)

	w := s.api(http.MethodGet, "/api/v1/inbox", s.key, nil, "")
	list := decode[inboxList](t, w)

	if w.Code != http.StatusOK || list.Status != "new" || len(list.Photos) != 1 {
		t.Fatalf("the inbox: %d %+v", w.Code, list)
	}

	p := list.Photos[0]
	if p.ID != id || p.At != "property" || p.Place != "inflow" || p.Note != "by the outlet" || p.LargeURL != base+"/api/v1/inbox/"+id+"/large.jpg" || p.FullURL != base+"/api/v1/inbox/"+id+"/full.jpg" {
		t.Errorf("the photo: %+v", p)
	}

	if strings.Contains(w.Body.String(), "lat") {
		t.Error("the inbox shows a GPS position")
	}

	pic := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/large.jpg", s.key, nil, "")
	if pic.Code != http.StatusOK || pic.Header().Get("Content-Type") != "image/jpeg" || pic.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("the picture: %d %q %q", pic.Code, pic.Header().Get("Content-Type"), pic.Header().Get("Cache-Control"))
	}

	if w := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/large.jpg", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("the picture without a key: %d", w.Code)
	}

	full := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/full.jpg", s.key, nil, "")
	if full.Code != http.StatusOK || full.Header().Get("Content-Type") != "image/jpeg" || full.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("the full picture: %d %q %q", full.Code, full.Header().Get("Content-Type"), full.Header().Get("Cache-Control"))
	}

	for _, file := range []string{"original.jpg", "full.png"} {
		if w := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/"+file, s.key, nil, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", file, w.Code)
		}
	}

	if w := s.api(http.MethodGet, "/api/v1/inbox?status=sorted", s.key, nil, ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a status it does not list: %d", w.Code)
	}
}

// Sorted into a plant's photos: unchecked, once, and a second different sort
// is told what the first made of it.
func TestAPhotoIsSortedThroughTheAPIOnce(t *testing.T) {
	s := serve(t)

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	id := s.inboxed(inboxbus.Property, types.ID{}, 2)
	as := map[string]any{"outcome": "photo", "species": "winecup", "kind": "flower"}

	w := s.sortPhoto(id, as)
	got := decode[sorted](t, w)

	if w.Code != http.StatusOK || got.Outcome != "photo" || got.Unchanged || got.Photo.ID == "" || got.Inbox.Status != "sorted" || got.Inbox.Species != "winecup" || got.Inbox.LargeURL != "" {
		t.Fatalf("sorting: %d\n%s", w.Code, w.Body.String())
	}

	sp, _ := s.species.BySlug(t.Context(), "winecup")
	if got.Photo.PhotosURL != base+"/steward/species/"+sp.ID.String()+"/photos" {
		t.Errorf("photos_url %q", got.Photo.PhotosURL)
	}

	photos, _ := s.photos.ForSpecies(t.Context(), sp.ID)
	if len(photos) != 1 || photos[0].Checked || photos[0].ID.String() != got.Photo.ID {
		t.Errorf("the plant's photos: %+v", photos)
	}

	if again := decode[sorted](t, s.sortPhoto(id, as)); !again.Unchanged {
		t.Error("the same sort sent again was not unchanged")
	}

	w = s.sortPhoto(id, map[string]any{"outcome": "unsure", "note": "or is it?"})
	if p := decode[problem](t, w); w.Code != http.StatusConflict || !strings.Contains(p.Error.Problem, "already sorted, as photo") {
		t.Errorf("a different sort after: %d %s", w.Code, w.Body.String())
	}

	if w := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/large.jpg", s.key, nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("a sorted photo's picture: %d", w.Code)
	}
}

func TestAPlantingIsSortedThroughTheAPI(t *testing.T) {
	s := serve(t)
	inflow := s.inflow()

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	id := s.inboxed(inboxbus.Property, types.ID{}, 3)

	w := s.sortPhoto(id, map[string]any{"outcome": "planted", "species": "winecup", "place": "inflow"})
	got := decode[sorted](t, w)

	if w.Code != http.StatusOK || got.Listing == nil || got.Listing.Action != "protect" || got.Listing.Planned {
		t.Fatalf("sorting a planting: %d\n%s", w.Code, w.Body.String())
	}

	listed, _ := s.listings.ForPlace(t.Context(), inflow.ID)
	if len(listed) != 1 || listed[0].Action != listingbus.Protect {
		t.Errorf("listed at the inflow: %+v", listed)
	}
}

// What the API will not do: discard, check, or put a nursery photo at a
// place.
func TestTheAPIDoesNotDiscardOrCheck(t *testing.T) {
	s := serve(t)
	s.inflow()

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	id := s.inboxed(inboxbus.Property, types.ID{}, 4)
	nursery := s.inboxed(inboxbus.Nursery, types.ID{}, 5)

	for name, tc := range map[string]struct {
		id    string
		body  map[string]any
		field string
		want  string
	}{
		"discard":        {id, map[string]any{"outcome": "discard"}, "outcome", "discarded by a steward on its screen"},
		"checked":        {id, map[string]any{"outcome": "photo", "species": "winecup", "kind": "leaf", "checked": true}, "", `"checked" is not a field this takes`},
		"no outcome":     {id, map[string]any{"species": "winecup"}, "outcome", "Send photo, planted or unsure."},
		"no such plant":  {id, map[string]any{"outcome": "photo", "species": "frostweed", "kind": "leaf"}, "species", "PUT /api/v1/species/frostweed adds it first"},
		"nursery placed": {nursery, map[string]any{"outcome": "photo", "species": "winecup", "kind": "leaf", "place": "inflow"}, "place", "taken off the property"},
		"no such place":  {id, map[string]any{"outcome": "photo", "species": "winecup", "kind": "leaf", "place": "moon"}, "place", `No place has the slug "moon"`},
	} {
		w := s.sortPhoto(tc.id, tc.body)
		p := decode[problem](t, w)

		if w.Code < 400 || p.Error.Field != tc.field || !strings.Contains(p.Error.Problem, tc.want) {
			t.Errorf("%s: %d %+v", name, w.Code, p.Error)
		}
	}

	if list := decode[inboxList](t, s.api(http.MethodGet, "/api/v1/inbox", s.key, nil, "")); len(list.Photos) != 2 {
		t.Errorf("after the refusals the inbox has %d photos, want both", len(list.Photos))
	}
}

// Not sure yet, with the reason: what a Claude does with a photo it thinks
// should go, for a steward to decide.
func TestAPhotoSetAsideThroughTheAPIIsListedAsUnsure(t *testing.T) {
	s := serve(t)
	id := s.inboxed(inboxbus.Property, types.ID{}, 6)

	if w := s.sortPhoto(id, map[string]any{"outcome": "unsure", "note": "blurred; discard?"}); w.Code != http.StatusOK {
		t.Fatalf("setting it aside: %d %s", w.Code, w.Body.String())
	}

	list := decode[inboxList](t, s.api(http.MethodGet, "/api/v1/inbox?status=unsure", s.key, nil, ""))
	if len(list.Photos) != 1 || list.Photos[0].Note != "blurred; discard?" || list.Photos[0].LargeURL == "" {
		t.Errorf("set aside: %+v", list)
	}

	if list := decode[inboxList](t, s.api(http.MethodGet, "/api/v1/inbox", s.key, nil, "")); len(list.Photos) != 0 {
		t.Error("a photo set aside is still waiting")
	}
}

type nurseryList struct {
	Visits []apiapp.VisitJSON `json:"visits"`
}

// A steward's Claude reads a tag and files it as stock; the stock is read
// back, with each line's photo, and a line is corrected.
func TestNurseryStockIsSortedReadAndCorrectedThroughTheAPI(t *testing.T) {
	s := serve(t)

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	id := s.inboxed(inboxbus.Nursery, types.ID{}, 7)

	w := s.sortPhoto(id, map[string]any{
		"outcome": "stock", "nursery": "Natural Gardener", "name_on_tag": "Callirhoe involucrata",
		"species": "winecup", "pot_size": "4 in", "price": "4.99", "count": 20,
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"line"`) {
		t.Fatalf("sorting as stock: %d\n%s", w.Code, w.Body.String())
	}

	list := decode[nurseryList](t, s.api(http.MethodGet, "/api/v1/nursery", s.key, nil, ""))
	if len(list.Visits) != 1 || !list.Visits[0].Latest || len(list.Visits[0].Lines) != 1 {
		t.Fatalf("the stock: %+v", list)
	}

	l := list.Visits[0].Lines[0]
	if l.Species != "winecup" || l.Price != "$4.99" || l.Count != 20 || l.PhotoURL != base+"/api/v1/inbox/"+id+"/large.jpg" {
		t.Errorf("the line: %+v", l)
	}

	if w := s.api(http.MethodGet, "/api/v1/inbox/"+id+"/large.jpg", s.key, nil, ""); w.Code != http.StatusOK {
		t.Errorf("the line's photo: %d", w.Code)
	}

	b, _ := json.Marshal(map[string]any{"name_on_tag": "Callirhoe involucrata", "pot_size": "4 in", "price": "3.99"})
	w = s.api(http.MethodPut, "/api/v1/nursery/lines/"+l.ID, s.key, bytes.NewReader(b), "application/json")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"price": "$3.99"`) || strings.Contains(w.Body.String(), `"species"`) {
		t.Errorf("correcting: %d\n%s", w.Code, w.Body.String())
	}

	// The nursery named in the sort is in the register, and the visit
	// points at it; a second sort under another spelling is the same one.
	other := s.inboxed(inboxbus.Nursery, types.ID{}, 9)
	if w := s.sortPhoto(other, map[string]any{"outcome": "stock", "nursery": "natural gardener", "name_on_tag": "Malvaviscus arboreus"}); w.Code != http.StatusOK {
		t.Fatalf("a second tag: %d\n%s", w.Code, w.Body.String())
	}

	reg := decode[struct {
		Nurseries []apiapp.NurseryJSON `json:"nurseries"`
	}](t, s.api(http.MethodGet, "/api/v1/nurseries", s.key, nil, ""))

	if len(reg.Nurseries) != 1 || reg.Nurseries[0].Name != "Natural Gardener" || reg.Nurseries[0].LastVisit == "" {
		t.Fatalf("the register: %+v", reg)
	}

	if list := decode[nurseryList](t, s.api(http.MethodGet, "/api/v1/nursery", s.key, nil, "")); len(list.Visits) != 1 || list.Visits[0].NurseryID != reg.Nurseries[0].ID || len(list.Visits[0].Lines) != 2 {
		t.Errorf("the stock after a second tag: %+v", list)
	}

	if w := s.api(http.MethodGet, "/api/v1/nurseries", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("the register without a key: %d", w.Code)
	}

	// Stock only from a nursery photo.
	garden := s.inboxed(inboxbus.Property, types.ID{}, 8)
	if w := s.sortPhoto(garden, map[string]any{"outcome": "stock", "nursery": "Natural Gardener", "name_on_tag": "x"}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a garden photo as stock: %d", w.Code)
	}
}

// A photo from a park says so, and where; it can be a plant's photo, with no
// place here, and nothing else.
func TestAPhotoFromElsewhereThroughTheAPI(t *testing.T) {
	s := serve(t)
	inflow := s.inflow()

	if w := s.put("winecup", winecup()); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d", w.Code)
	}

	it, err := s.inbox.Add(t.Context(), inboxbus.Fields{FromID: s.steward, At: inboxbus.Elsewhere, Site: "Pedernales Falls State Park"}, noisy(t, 61))
	if err != nil {
		t.Fatal(err)
	}
	id := it.ID.String()

	list := decode[inboxList](t, s.api(http.MethodGet, "/api/v1/inbox", s.key, nil, ""))
	if len(list.Photos) != 1 || list.Photos[0].At != "elsewhere" || list.Photos[0].Where != "Pedernales Falls State Park" || list.Photos[0].Place != "" {
		t.Fatalf("the inbox: %+v", list)
	}

	if w := s.sortPhoto(id, map[string]any{"outcome": "planted", "species": "winecup", "place": inflow.Slug}); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("planted at a place here: %d", w.Code)
	}

	if w := s.sortPhoto(id, map[string]any{"outcome": "photo", "species": "winecup", "kind": "flower", "place": inflow.Slug}); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "off the property") {
		t.Errorf("a photo given a place here: %d", w.Code)
	}

	if w := s.sortPhoto(id, map[string]any{"outcome": "photo", "species": "winecup", "kind": "flower"}); w.Code != http.StatusOK {
		t.Errorf("a photo of the plant: %d\n%s", w.Code, w.Body.String())
	}

	// The plant's photo says where it was taken, as the batch said.
	one := decode[struct {
		Species apiapp.SpeciesJSON `json:"species"`
	}](t, s.api(http.MethodGet, "/api/v1/species/winecup", s.key, nil, ""))

	if len(one.Species.Photos) != 1 || !one.Species.Photos[0].Elsewhere || one.Species.Photos[0].TakenWhere != "Pedernales Falls State Park" {
		t.Errorf("the plant's photos: %+v", one.Species.Photos)
	}
}
