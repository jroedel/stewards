package inboxapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Two photos of one plant chosen in the inbox, out of three: the plant is
// named on the first, comes chosen on the second, and after the second the
// inbox again, with the third still waiting.
func TestPhotosChosenTogetherNameThePlantOnce(t *testing.T) {
	s := serve(t)

	ids := s.sent(property(), file{"IMG_0040.JPG", noisy(t, 40)}, file{"IMG_0041.JPG", noisy(t, 41)}, file{"IMG_0042.JPG", noisy(t, 42)})
	first, other, second := ids[0], ids[1], ids[2]
	plant := s.penstemon.ID.String()

	list := s.get("/steward/inbox", true).Body.String()
	for _, id := range ids {
		if !strings.Contains(list, `name="photo" value="`+id+`"`) {
			t.Errorf("photo %s cannot be chosen in the inbox", id)
		}
	}
	if !strings.Contains(list, `action="/steward/inbox/together"`) {
		t.Error("the inbox has no form to sort chosen photos together")
	}

	// Chosen in any order, they go in the inbox's.
	w := s.get("/steward/inbox/together?photo="+second+"&photo="+first, true)
	group := first + "." + second
	if want := "/steward/inbox/" + first + "?group=" + group; w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("choosing two: %d %q, want %q", w.Code, w.Header().Get("Location"), want)
	}

	page := s.get("/steward/inbox/"+first+"?group="+group, true).Body.String()
	for _, want := range []string{
		"1 of the 2 chosen", `name="group" value="` + group + `"`,
		`href="/steward/inbox/` + second + `?group=` + group + `" data-swap>Skip`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the first of the group does not show %q", want)
		}
	}

	w = s.post("/steward/inbox/"+first, url.Values{"as": {"photo"}, "group": {group}, "species_other": {plant}, "kind": {"leaf"}})
	if want := "/steward/inbox/" + second + "?done=photo&group=" + group + "&plant=" + plant; w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("sorting the first: %d %q, want %q\n%s", w.Code, w.Header().Get("Location"), want, w.Body.String())
	}

	page = s.get("/steward/inbox/"+second+"?done=photo&group="+group+"&plant="+plant, true).Body.String()
	for _, want := range []string{
		"2 of the 2 chosen", "Chosen for the 2 photos you picked together.",
		`name="species" value="` + plant + `" checked`, `name="plant" value="` + plant + `"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the second of the group does not show %q", want)
		}
	}

	w = s.post("/steward/inbox/"+second, url.Values{"as": {"photo"}, "group": {group}, "plant": {plant}, "species": {plant}, "kind": {"flower"}})
	if want := "/steward/inbox?done=photo"; w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
		t.Fatalf("sorting the last of the group: %d %q, want %q", w.Code, w.Header().Get("Location"), want)
	}

	if photos, _ := s.photos.ForSpecies(t.Context(), s.penstemon.ID); len(photos) != 2 {
		t.Errorf("the plant has %d photos, want the two chosen", len(photos))
	}

	if waiting := s.waitingIDs(); len(waiting) != 1 || waiting[0] != other {
		t.Errorf("waiting %v, want only the photo not chosen", waiting)
	}
}

// None chosen is the inbox again, saying how; one chosen is that photo.
func TestChoosingNoneOrOne(t *testing.T) {
	s := serve(t)
	ids := s.sent(property(), file{"IMG_0050.JPG", noisy(t, 50)}, file{"IMG_0051.JPG", noisy(t, 51)})

	w := s.get("/steward/inbox/together", true)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox?done=none-chosen" {
		t.Errorf("none chosen: %d %q", w.Code, w.Header().Get("Location"))
	}

	if list := s.get("/steward/inbox?done=none-chosen", true).Body.String(); !strings.Contains(list, `class="problem"`) || !strings.Contains(list, "No photos were chosen.") {
		t.Error("the inbox does not say no photos were chosen")
	}

	w = s.get("/steward/inbox/together?photo="+ids[1], true)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/steward/inbox/"+ids[1] {
		t.Errorf("one chosen: %d %q", w.Code, w.Header().Get("Location"))
	}

	// A group in an address that does not hold the photo is no group.
	if page := s.get("/steward/inbox/"+ids[0]+"?group="+ids[1]+".ffffffffffffffffffffffffffffffff", true).Body.String(); strings.Contains(page, "chosen") || strings.Contains(page, `name="group"`) {
		t.Error("a photo outside the group in its address is sorted as one of it")
	}
}

// waitingIDs is the photos still waiting, in the inbox's order.
func (s *site) waitingIDs() []string {
	s.t.Helper()

	items, err := s.inbox.Waiting(s.t.Context())
	if err != nil {
		s.t.Fatal(err)
	}

	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID.String())
	}

	return ids
}
