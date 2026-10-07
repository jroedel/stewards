package placeapp_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// plant adds a species the way a steward would, on its own screen, and
// returns its id.
func (s *site) plant(slug, name, swatch string, bloom ...string) string {
	s.t.Helper()

	w := s.post("/steward/species", url.Values{
		"slug": {slug}, "common": {name}, "status": {"native"},
		"swatches": {swatch}, "bloom": bloom, "height_min": {"24"},
	})
	if w.Code != http.StatusSeeOther {
		s.t.Fatalf("adding %s: %d\n%s", slug, w.Code, w.Body.String())
	}

	sp, err := s.species.BySlug(s.t.Context(), slug)
	if err != nil {
		s.t.Fatal(err)
	}

	return sp.ID.String()
}

// list sets how a plant is listed at a place, through the plants screen.
func (s *site) list(placeID, speciesID, action string, planned bool, note string) *httptest.ResponseRecorder {
	s.t.Helper()

	form := url.Values{"species": {speciesID}, "action": {action}, "note": {note}}
	if planned {
		form.Set("planned", "yes")
	}

	return s.post("/steward/places/"+placeID+"/plants", form)
}

func TestAStewardListsPlantsAtAPlace(t *testing.T) {
	s := serve(t)
	s.pilot()

	inflow := s.place("rain-garden-inflow").ID.String()
	path := "/steward/places/" + inflow + "/plants"

	// Before there are any species, the screen says where to make one.
	if body := s.get(path).Body.String(); !strings.Contains(body, "There are no plants to choose from yet") {
		t.Error("the empty screen does not say to add a plant first")
	}

	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F", "3", "4", "5")
	johnson := s.plant("johnsongrass", "Johnsongrass", "#7A8B3C")

	if w := s.list(inflow, penstemon, "protect", true, "Six around the pipe outlets"); w.Code != http.StatusSeeOther ||
		w.Header().Get("Location") != path+"?done=saved" {
		t.Fatalf("listing the penstemon: %d %s", w.Code, w.Header().Get("Location"))
	}

	if w := s.list(inflow, johnson, "pull", false, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("listing the johnsongrass: %d", w.Code)
	}

	body := s.get(path).Body.String()
	for _, want := range []string{"Brazos penstemon", "Johnsongrass", `value="Six around the pipe outlets"`, "Every plant is already listed here"} {
		if !strings.Contains(body, want) {
			t.Errorf("the plants screen does not show %q", want)
		}
	}

	// Saving a row again changes it rather than adding a second.
	if w := s.list(inflow, penstemon, "careful", false, ""); w.Code != http.StatusSeeOther {
		t.Fatalf("changing the penstemon: %d", w.Code)
	}

	if body := s.get(path).Body.String(); strings.Count(body, "Take Brazos penstemon off this list") != 1 ||
		!strings.Contains(body, `<option value="careful" selected>`) {
		t.Error("the change did not replace the listing")
	}

	// Taking one off, twice: the second is already done.
	for range 2 {
		if w := s.post(path+"/"+johnson+"/remove", url.Values{}); w.Code != http.StatusSeeOther ||
			w.Header().Get("Location") != path+"?done=removed" {
			t.Fatalf("taking the johnsongrass off: %d", w.Code)
		}
	}

	if body := s.get(path).Body.String(); strings.Contains(body, "Take Johnsongrass off") {
		t.Error("the johnsongrass is still listed")
	}
}

func TestAPlannedPlantIsNotOneToPull(t *testing.T) {
	s := serve(t)
	s.pilot()

	inflow := s.place("rain-garden-inflow").ID.String()
	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F")

	w := s.list(inflow, penstemon, "pull", true, "kept note")
	body := w.Body.String()

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("planned and pull: %d, want 422", w.Code)
	}

	for _, want := range []string{"A plant still to plant here is not one to pull.", "Nothing was saved yet.", `value="kept note"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not show %q", want)
		}
	}

	if w := s.list(inflow, penstemon, "", false, ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("no action chosen: %d, want 422", w.Code)
	}
}

func TestNobodySignedOutListsAPlant(t *testing.T) {
	s := serve(t)
	s.pilot()

	inflow := s.place("rain-garden-inflow").ID.String()
	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F")

	form := url.Values{"species": {penstemon}, "action": {"protect"}}
	for _, w := range []*httptest.ResponseRecorder{
		s.do(http.MethodGet, "/steward/places/"+inflow+"/plants", nil, false),
		s.do(http.MethodPost, "/steward/places/"+inflow+"/plants", form, false),
		s.do(http.MethodPost, "/steward/places/"+inflow+"/plants/"+penstemon+"/remove", url.Values{}, false),
	} {
		if w.Code == http.StatusOK || w.Code == http.StatusSeeOther && !strings.HasPrefix(w.Header().Get("Location"), "/sign-in") {
			t.Errorf("signed out: %d %s", w.Code, w.Header().Get("Location"))
		}
	}

	if body := s.getAs("/places/rain-garden-inflow", "").Body.String(); strings.Contains(body, "Brazos penstemon") {
		t.Error("a signed-out post listed the plant")
	}
}

// The card a volunteer opens: what is still to plant, when it flowers, and
// what to leave and what to take out.
func TestTheCardSaysWhatToProtectAndWhatToPull(t *testing.T) {
	s := serve(t)
	s.pilot()

	inflow := s.place("rain-garden-inflow").ID.String()

	// Nothing listed yet: the empty card, still with the advice.
	if body := s.getAs("/places/rain-garden-inflow", "").Body.String(); !strings.Contains(body, "Not sure what something is? Leave it.") ||
		strings.Contains(body, "panel-protect") {
		t.Error("an empty band's card is wrong")
	}

	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F", "3", "4", "5")
	sedge := s.plant("cherokee-sedge", "Cherokee sedge", "#6B8E4E")
	johnson := s.plant("johnsongrass", "Johnsongrass", "#7A8B3C")
	nettle := s.plant("bull-nettle", "Bull nettle", "#FFFFFF")
	frogfruit := s.plant("frogfruit", "Frogfruit", "#F4F0F7", "5", "6")

	s.list(inflow, penstemon, "protect", true, "Six around the pipe outlets")
	s.list(inflow, sedge, "protect", true, "")
	s.list(inflow, johnson, "pull", false, "Pull before it seeds")
	s.list(inflow, nettle, "careful", false, "")
	s.list(inflow, frogfruit, "protect", false, "Already spreading by the steps")

	body := s.getAs("/places/rain-garden-inflow", "").Body.String()

	for _, want := range []string{
		"To plant",
		"the note says how many more",
		`href="/plants/brazos-penstemon"`,
		"Six around the pipe outlets",
		"When it flowers",
		`fill="#b0418f"`,
		"Leave these. They grow here.",
		"Take these out, root and all.",
		"Pull before it seeds",
		"Wear gloves near these.",
		`href="/plants/bull-nettle"`,
		"Not sure what something is? Leave it.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the card does not show %q", want)
		}
	}

	// The calendar has a row for every protected plant with bloom months,
	// planted or still to plant -- a bed keeps its calendar the day its
	// planting is done -- and the penstemon's row fills March to May: three
	// cells. (Swatches are stored lowercase, whatever the steward typed.)
	cal := body[strings.Index(body, `class="calendar"`):strings.Index(body, "</table>")]
	if strings.Contains(cal, "Cherokee sedge") || strings.Contains(cal, "Johnsongrass") || strings.Contains(cal, "Bull nettle") {
		t.Error("the calendar shows a plant with no bloom months, or one that is not protected")
	}

	if !strings.Contains(cal, "Frogfruit") {
		t.Error("the calendar leaves out a protected plant that is already planted")
	}

	// Frogfruit is growing, not to plant: in Protect, not on To plant.
	toPlant := body[strings.Index(body, `id="planned-h"`):strings.Index(body, `class="calendar"`)]
	if strings.Contains(toPlant, "Frogfruit") || !strings.Contains(toPlant, "Brazos penstemon") {
		t.Error("the To plant list holds the wrong plants")
	}

	if n := strings.Count(cal, `fill="#b0418f"`); n != 3 {
		t.Errorf("the penstemon fills %d months of the calendar, want 3", n)
	}

	// Johnsongrass is in the Pull panel, not the Protect one.
	protect := body[strings.Index(body, "panel-protect"):strings.Index(body, "panel-pull")]
	if strings.Contains(protect, "Johnsongrass") || !strings.Contains(protect, "Cherokee sedge") {
		t.Error("the Protect panel holds the wrong plants")
	}

	// The garden's own card counts what each band holds.
	if garden := s.getAs("/places/rain-garden", "").Body.String(); !strings.Contains(garden, "2 to plant · 1 to pull") {
		t.Error("the band's row on the garden's card does not count its plants")
	}
}

func TestAListedPlaceOrPlantCannotBeRemoved(t *testing.T) {
	s := serve(t)
	s.pilot()

	middle := s.place("rain-garden-middle").ID.String()
	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F")
	s.list(middle, penstemon, "protect", true, "")

	confirm := url.Values{"confirm": {"yes"}}

	w := s.post("/steward/places/"+middle+"/delete", confirm)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Middle band still has plants listed, or photos taken there,") {
		t.Errorf("removing a place with plants: %d", w.Code)
	}

	w = s.post("/steward/species/"+penstemon+"/delete", confirm)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Brazos penstemon is still listed at a place or has photos.") {
		t.Errorf("removing a listed plant: %d", w.Code)
	}

	s.post("/steward/places/"+middle+"/plants/"+penstemon+"/remove", url.Values{})

	if w := s.post("/steward/places/"+middle+"/delete", confirm); w.Code != http.StatusSeeOther {
		t.Errorf("removing the place once its list is empty: %d", w.Code)
	}
}
