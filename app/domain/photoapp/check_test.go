package photoapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
	"github.com/jroedel/stewards/business/domain/species/speciesbus"
	"github.com/jroedel/stewards/business/types"
)

// add puts a photo straight in through the rules, checked or not.
func (s *site) add(t *testing.T, k photobus.Kind, checked bool, seed uint64) string {
	t.Helper()

	p, err := s.photos.Add(t.Context(), mustID(t, s.penstemon), photobus.Fields{Kind: k, Source: photobus.Ours, Checked: checked}, noise(t, seed))
	if err != nil {
		t.Fatal(err)
	}

	return p.ID.String()
}

func contains(t *testing.T, what, body string, want ...string) {
	t.Helper()

	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s does not show %q", what, w)
		}
	}
}

// The queue from end to end: the photo beside the checked one it is
// compared with, Yes on to the next, Undo back, and an empty queue.
func TestTheCheckQueue(t *testing.T) {
	s := serve(t)

	ref := s.add(t, photobus.Leaf, true, 1)
	leaf := s.add(t, photobus.Leaf, false, 2)
	flower := s.add(t, photobus.Flower, false, 3)

	if w := s.get("/steward/check", false); w.Code == http.StatusOK {
		t.Error("the queue is open to somebody signed out")
	}

	contains(t, "the stewards' front page", s.get("/steward", true).Body.String(), `href="/steward/check"`, "2 waiting")

	page := s.get("/steward/check", true).Body.String()
	contains(t, "the queue", page,
		"2 waiting.",
		"/photos/"+leaf+"/small.jpg", "/photos/"+ref+"/small.jpg", "On the card now",
		"Yes, it shows Brazos penstemon", `action="/steward/check/`+leaf+`"`,
		`data-next="/steward/check?at=`+flower+`"`,
		"/steward/photos/"+leaf+"/edit?from=check")

	w := s.post("/steward/check/"+leaf, url.Values{"checked": {"yes"}})
	want := "/steward/check?done=checked&last=" + leaf + "&at=" + flower
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("Yes: %d %q, want 303 to %q", w.Code, w.Header().Get("Location"), want)
	}

	page = s.get(want, true).Body.String()
	contains(t, "the queue after Yes", page,
		"Checked: Brazos penstemon, leaf close-up.", `action="/steward/check/`+leaf+`"`,
		"This is the last one.", "/photos/"+flower+"/small.jpg",
		"No checked flower close-up of Brazos penstemon yet.")
	if strings.Contains(page, "data-next") {
		t.Error("the last photo has a next one")
	}

	w = s.post("/steward/check/"+leaf, url.Values{"checked": {"no"}})
	want = "/steward/check?at=" + leaf + "&done=unchecked&last=" + leaf
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("Undo: %d %q, want 303 to %q", w.Code, w.Header().Get("Location"), want)
	}

	page = s.get(want, true).Body.String()
	contains(t, "the queue after Undo", page, "Not checked any more.", "2 waiting.", `action="/steward/check/`+leaf+`"`)

	// A link that says a photo was checked when it was not says nothing.
	if page := s.get("/steward/check?done=checked&last="+leaf, true).Body.String(); strings.Contains(page, "Checked:") {
		t.Error("the queue said a photo was checked that is not")
	}

	for _, id := range []string{leaf, flower} {
		if w := s.post("/steward/check/"+id, url.Values{"checked": {"yes"}}); w.Code != http.StatusSeeOther {
			t.Fatalf("Yes: %d", w.Code)
		}
	}

	contains(t, "the empty queue", s.get("/steward/check", true).Body.String(), "Nothing to check.")

	// A photo checked from the queue is on the card.
	if w := s.get("/photos/"+flower+"/small.jpg", false); w.Code != http.StatusOK {
		t.Errorf("signed out, a photo checked from the queue: %d", w.Code)
	}
}

// Change opens the photo's own screen, and saving it goes back to the queue
// at that photo rather than to the plant's photos.
func TestChangingAPhotoFromTheQueueComesBackToIt(t *testing.T) {
	s := serve(t)

	id := s.add(t, photobus.Leaf, false, 1)

	form := s.get("/steward/photos/"+id+"/edit?from=check", true).Body.String()
	contains(t, "the photo's screen from the queue", form, `name="from" value="check"`, `href="/steward/check?at=`+id+`"`)

	fields := url.Values{"from": {"check"}, "kind": {"flower"}, "source": {"ours"}}
	w := s.post("/steward/photos/"+id, fields)
	want := "/steward/check?at=" + id + "&done=saved&last=" + id
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("saving: %d %q, want 303 to %q", w.Code, w.Header().Get("Location"), want)
	}

	contains(t, "the queue after a change", s.get(want, true).Body.String(), "Changes saved.", "Flower close-up")

	// From the plant's photos, it goes back there as it always has.
	fields.Del("from")
	if w := s.post("/steward/photos/"+id, fields); !strings.HasPrefix(w.Header().Get("Location"), s.photosPath()) {
		t.Errorf("saving from the plant's photos went to %q", w.Header().Get("Location"))
	}
}

// The Change sheet: on the queue's page with the photo's plant as its first
// button and every plant in the searchable list; a leaf filed under the
// wrong plant saved as the right plant's flower and checked, on to the next
// photo; saved without the check, it stays in the queue; and what is not
// right comes back with the sheet open in the page, saying what.
func TestTheChangeSheetRefilesAPhoto(t *testing.T) {
	s := serve(t)

	sp, err := s.species.Create(t.Context(), speciesbus.Fields{
		Slug: "turks-cap", Common: types.Text{EN: "Turk's cap", ES: "Monacillo"}, Scientific: "Malvaviscus arboreus", Status: speciesbus.StatusNative,
	})
	if err != nil {
		t.Fatal(err)
	}
	turksCap := sp.ID.String()

	first := s.add(t, photobus.Leaf, false, 1)
	second := s.add(t, photobus.Leaf, false, 2)

	page := s.get("/steward/check?at="+first, true).Body.String()
	contains(t, "the sheet", page,
		`popovertarget="change"`, `id="change"`, ` popover role="dialog"`,
		`action="/steward/check/`+first+`/change"`,
		`<input type="radio" name="species" value="`+s.penstemon+`" checked><span>Brazos penstemon</span>`,
		`data-find-with="species"`, `data-also="Monacillo"`, "Turk&#39;s cap (Malvaviscus arboreus)",
		`name="kind" value="leaf" required checked`,
		`name="checked" value="yes">Save and check it`, `name="checked" value="no">Save without checking`,
		"/static/js/find.")

	// Both a button and the list, different: one is asked for.
	w := s.post("/steward/check/"+first+"/change", url.Values{"species": {s.penstemon}, "species_other": {turksCap}, "kind": {"flower"}, "checked": {"yes"}})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("two plants: %d, want 422", w.Code)
	}

	page = w.Body.String()
	contains(t, "the sheet sent back", page, "Choose one plant: a button or the list, not both.", `class="sheet sheet-open card"`, `href="#change"`)
	if strings.Contains(page, ` popover role="dialog"`) {
		t.Error("the sheet with something to fix is a popover, closed")
	}

	if p, _ := s.photos.ByID(t.Context(), mustID(t, first)); p.Kind != photobus.Leaf || p.SpeciesID.String() != s.penstemon {
		t.Errorf("a refused change was saved: %+v", p)
	}

	// The right plant, from the list, a flower, checked.
	w = s.post("/steward/check/"+first+"/change", url.Values{"species_other": {turksCap}, "kind": {"flower"}, "checked": {"yes"}})
	want := "/steward/check?done=changed-checked&last=" + first + "&at=" + second
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("saving and checking: %d %q, want 303 to %q\n%s", w.Code, w.Header().Get("Location"), want, w.Body.String())
	}

	p, err := s.photos.ByID(t.Context(), mustID(t, first))
	if err != nil {
		t.Fatal(err)
	}

	if p.SpeciesID.String() != turksCap || p.Kind != photobus.Flower || !p.InFlower || !p.Checked {
		t.Errorf("after the sheet: %+v", p)
	}

	contains(t, "the queue after the sheet", s.get(want, true).Body.String(), "Changed and checked: Turk&#39;s cap, flower close-up.", ">Uncheck</button>", "This is the last one.")

	// The sheet's buttons now start with the plant just worked on.
	page = s.get("/steward/check", true).Body.String()
	contains(t, "the next photo's sheet", page, `value="`+turksCap+`"><span>Turk&#39;s cap</span>`)

	// Saved without the check, a photo stays in the queue.
	w = s.post("/steward/check/"+second+"/change", url.Values{"species": {s.penstemon}, "kind": {"young"}, "checked": {"no"}})
	want = "/steward/check?done=changed&last=" + second + "&at=" + second
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("saving without checking: %d %q, want 303 to %q", w.Code, w.Header().Get("Location"), want)
	}

	contains(t, "the queue after saving without the check", s.get(want, true).Body.String(), "Changed: Brazos penstemon, young plant. It waits here to be checked.", "This is the last one.")

	// Nothing said about what it shows.
	w = s.post("/steward/check/"+second+"/change", url.Values{"species": {s.penstemon}, "checked": {"yes"}})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Choose what the photo shows.") {
		t.Errorf("no kind: %d", w.Code)
	}
}
