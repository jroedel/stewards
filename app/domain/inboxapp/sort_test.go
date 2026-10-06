package inboxapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// The plant last sorted to is a button on the next photo, and a button is
// all the plant needs. Ticked as sure, the photo is checked as it is made,
// and the next screen says so.
func TestTheLastPlantIsOneTapAndSureIsChecked(t *testing.T) {
	s := serve(t)

	turksCap, err := s.species.Create(t.Context(), speciesbus.Fields{Slug: "turks-cap", Common: types.Text{EN: "Turk's cap"}})
	if err != nil {
		t.Fatal(err)
	}

	ids := s.sent(property(), file{"IMG_0030.JPG", noisy(t, 30)}, file{"IMG_0031.JPG", noisy(t, 31)}, file{"IMG_0032.JPG", noisy(t, 32)})

	if first := s.get("/steward/inbox/"+ids[0], true).Body.String(); strings.Contains(first, `class="chip"`) {
		t.Error("before any sort, a plant is offered as a button")
	}

	w := s.post("/steward/inbox/"+ids[0], url.Values{"as": {"photo"}, "species_other": {turksCap.ID.String()}, "kind": {"leaf"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("sorting from the list: %d\n%s", w.Code, w.Body.String())
	}

	second := s.get("/steward/inbox/"+ids[1], true).Body.String()
	if !strings.Contains(second, `name="species" value="`+turksCap.ID.String()+`"`) || !strings.Contains(second, "Another plant") {
		t.Error("the plant just sorted to is not a button on the next photo")
	}

	w = s.post("/steward/inbox/"+ids[1], url.Values{"as": {"photo"}, "species": {turksCap.ID.String()}, "kind": {"flower"}, "checked": {"yes"}})
	if want := "/steward/inbox/" + ids[2] + "?done=photo-checked"; w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("sorting by the button, sure: %d %q, want %q\n%s", w.Code, w.Header().Get("Location"), want, w.Body.String())
	}

	if next := s.get("/steward/inbox/"+ids[2]+"?done=photo-checked", true).Body.String(); !strings.Contains(next, "Volunteers see it now.") {
		t.Error("the next screen does not say the photo was checked")
	}

	photos, _ := s.photos.ForSpecies(t.Context(), turksCap.ID)
	checked := map[string]bool{}
	for _, p := range photos {
		checked[string(p.Kind)] = p.Checked
	}

	if len(photos) != 2 || checked["leaf"] || !checked["flower"] {
		t.Errorf("Turk's cap's photos checked %v, want the leaf not and the flower so", checked)
	}

	// A button and the list naming different plants: nothing is guessed,
	// and nothing is saved.
	w = s.post("/steward/inbox/"+ids[2], url.Values{"as": {"photo"}, "species": {turksCap.ID.String()}, "species_other": {s.penstemon.ID.String()}, "kind": {"leaf"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Choose one plant: a button or the list, not both.") {
		t.Errorf("a button and the list both: %d", w.Code)
	}

	if n, _ := s.inbox.Count(t.Context()); n != 1 {
		t.Errorf("%d left to sort, want the last one still there", n)
	}
}
