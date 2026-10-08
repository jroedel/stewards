package placeapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The rain garden and its bands, entered the way a steward would.
func (s *site) pilot() {
	s.t.Helper()

	s.post("/steward/places", rainGarden())
	garden := s.place("rain-garden")

	for i, b := range []struct{ slug, en, es, purpose string }{
		{"rain-garden-inflow", "Inflow band", "Banda de entrada", "Where the water comes in. Plants that like wet feet."},
		{"rain-garden-middle", "Middle band", "", ""},
		{"rain-garden-wall-edge", "Wall edge band", "", "Low plants, so nothing blocks the view."},
	} {
		w := s.post("/steward/places", url.Values{
			"slug": {b.slug}, "name": {b.en}, "purpose": {b.purpose},
			"parent": {garden.ID.String()}, "sort": {string(rune('1' + i))},
		})
		if w.Code != http.StatusSeeOther {
			s.t.Fatalf("adding %s: %d", b.slug, w.Code)
		}
	}

	// Claude's Spanish for some of it, and not yet for the rest.
	s.translate("Rain garden", "Jardín de lluvia", "Inflow band", "Banda de entrada")
}

// A volunteer, signed out, from the list to the garden to a band.
func TestAVolunteerFindsTheRainGardenAndItsBands(t *testing.T) {
	s := serve(t)
	s.pilot()

	// The home page's "Explore the trail" leads to the list.
	if !strings.Contains(s.getAs("/", "").Body.String(), `href="/places"`) {
		t.Fatal("the home page does not lead to the list of places")
	}

	home := s.getAs("/places", "").Body.String()
	if !strings.Contains(home, `href="/places/rain-garden"`) {
		t.Fatal("the list does not link to the rain garden")
	}

	w := s.getAs("/places/rain-garden", "")
	body := w.Body.String()

	if w.Code != http.StatusOK {
		t.Fatalf("the rain garden: %d", w.Code)
	}

	for _, want := range []string{
		"<title>Rain garden · Garden stewards</title>",
		"<h1>Rain garden</h1>",
		"The backdrop of the gathering space.",
		"Full sun, wet after storms.",
		"Inside this place",
		"Not sure what something is? Leave it.",
		`href="/places"><span>‹ All places</span></a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the card does not show %q", want)
		}
	}

	// The bands, linked, in the stewards' order.
	i := strings.Index(body, `href="/places/rain-garden-inflow"`)
	j := strings.Index(body, `href="/places/rain-garden-middle"`)
	k := strings.Index(body, `href="/places/rain-garden-wall-edge"`)
	if i < 0 || !(i < j && j < k) {
		t.Error("the bands are not linked in order")
	}

	band := s.getAs("/places/rain-garden-inflow", "").Body.String()
	if !strings.Contains(band, `Part of <a href="/places/rain-garden">Rain garden</a>`) || strings.Contains(band, "Inside this place") {
		t.Error("the band's card does not lead back to the garden")
	}

	// No edit link for somebody signed out.
	if strings.Contains(body, "Edit this place") {
		t.Error("a volunteer is offered the edit link")
	}
}

func TestASignedInStewardCanEditFromTheCard(t *testing.T) {
	s := serve(t)
	s.pilot()

	garden := s.place("rain-garden")
	if !strings.Contains(s.get("/places/rain-garden").Body.String(), `href="/steward/places/`+garden.ID.String()+`/edit"`) {
		t.Error("a steward is not offered the edit link")
	}
}

// In Spanish: Claude's translations, and English marked where there is none
// yet -- never a guess made on the fly.
func TestTheCardIsInThePhonesLanguage(t *testing.T) {
	s := serve(t)
	s.pilot()

	body := s.getAs("/places/rain-garden", "es-MX,es;q=0.9").Body.String()

	for _, want := range []string{
		"<h1>Jardín de lluvia</h1>",
		"<title>Jardín de lluvia · Garden stewards</title>",
		`<span lang="en">The backdrop of the gathering space.</span>`,
		`Banda de entrada`,
		`<span lang="en">Middle band</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Spanish card does not contain %q", want)
		}
	}
}

// The card's own words wait for Claude beside the stewards', each saying
// where it is read; translated, they are on the Spanish card at once, and a
// saint's name takes its Spanish form.
func TestTheCardsOwnWordsWaitForClaude(t *testing.T) {
	s := serve(t)
	s.pilot()
	s.post("/steward/places", url.Values{"slug": {"st-francis"}, "name": {"St. Francis garden"}, "trail_anchor": {"francis"}})

	waiting, err := s.memory.Waiting(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]string{}
	for _, p := range waiting.Pending {
		found[p.Source] = strings.Join(p.Where, "; ")
	}

	for text, where := range map[string]string{
		"Conditions":       "the place card, which a volunteer reads standing in the garden (Conditions)",
		"{count} to plant": "(ToPlant)",
		"St. Francis":      "(Stations[francis])",
		"October":          "dates, times and sizes",
	} {
		if !strings.Contains(found[text], where) {
			t.Errorf("%q waits as %q, want it to say %q", text, found[text], where)
		}
	}

	s.translate("Conditions", "Condiciones", "St. Francis", "San Francisco", "Protect", "Proteger")

	body := s.getAs("/places/rain-garden", "es").Body.String()
	for _, want := range []string{"<h2>Condiciones</h2>", `<span lang="en">Not sure what something is? Leave it.</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the Spanish card does not contain %q", want)
		}
	}

	if body := s.getAs("/places/st-francis", "es").Body.String(); !strings.Contains(body, `<span class="station-name">San Francisco</span>`) {
		t.Error("the station's name is not in Spanish")
	}

	if body := s.getAs("/places/rain-garden", "en").Body.String(); !strings.Contains(body, "<h2>Conditions</h2>") {
		t.Error("the English card lost its heading")
	}
}

func TestAStationsSpaceLinksToItsPrayer(t *testing.T) {
	s := serve(t)

	s.post("/steward/places", url.Values{"slug": {"st-francis"}, "name": {"St. Francis garden"}, "trail_anchor": {"francis"}})

	body := s.getAs("/places/st-francis", "").Body.String()
	if !strings.Contains(body, `href="https://schoenstatt-fathers.us/trail/#francis"`) || !strings.Contains(body, `<span class="station-name">St. Francis</span>`) {
		t.Error("the station's space does not link to its prayer")
	}

	s.post("/steward/places", url.Values{"slug": {"switchbacks"}, "name": {"Switchbacks"}})
	if strings.Contains(s.getAs("/places/switchbacks", "").Body.String(), "/trail/#") {
		t.Error("a place that is not a station links to one")
	}
}

func TestAnUnknownPlaceSaysWhatToDo(t *testing.T) {
	s := serve(t)

	w := s.getAs("/places/no-such-place", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "We can't find that place") || !strings.Contains(w.Body.String(), `<a class="onward" href="/places">`) {
		t.Errorf("an unknown place: %d\n%s", w.Code, w.Body)
	}
}

// The card is for volunteers, so it must not depend on sign-in being set up.
func TestTheCardWorksWithSignInOff(t *testing.T) {
	s := serveAt(t, "")

	if w := s.getAs("/places/anything", ""); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "We can't find that place") {
		t.Errorf("with sign-in off the card route is not mounted: %d", w.Code)
	}
}

// A steward adds to a list from the card: a "+ Add" under each list, whose
// form fixes the list's action, offers only plants not yet listed, and comes
// back to the card at that list. A volunteer sees none of it.
func TestAStewardAddsToAListFromTheCard(t *testing.T) {
	s := serve(t)
	s.pilot()

	garden := s.place("rain-garden").ID.String()
	sedge := s.plant("cherokee-sedge", "Cherokee sedge", "#7a8b3c")
	winecup := s.plant("winecup", "Winecup", "#8e1b4b")

	if w := s.list(garden, sedge, "protect", true, "in the inflow"); w.Code != http.StatusSeeOther {
		t.Fatalf("listing the sedge: %d", w.Code)
	}

	body := s.get("/places/rain-garden").Body.String()

	for _, id := range []string{"add-planned", "add-protect", "add-pull", "add-careful"} {
		if !strings.Contains(body, `popovertarget="`+id+`"`) || !strings.Contains(body, `id="`+id+`" class="add-popover" popover`) {
			t.Errorf("the card has no %s", id)
		}
	}

	// Each form says its own action, and the Planned one says planned.
	pull := body[strings.Index(body, `id="add-pull"`):]
	pull = pull[:strings.Index(pull, "</form>")]

	for _, want := range []string{`name="action" value="pull"`, `name="return" value="card"`, ">Winecup</option>", `name="note"`} {
		if !strings.Contains(pull, want) {
			t.Errorf("the Pull form does not have %s", want)
		}
	}

	if strings.Contains(pull, "Cherokee sedge") || strings.Contains(pull, `name="planned"`) {
		t.Error("the Pull form offers a plant already listed here, or says planned")
	}

	planned := body[strings.Index(body, `id="add-planned"`):]
	if planned = planned[:strings.Index(planned, "</form>")]; !strings.Contains(planned, `name="planned" value="yes"`) || !strings.Contains(planned, `name="action" value="protect"`) {
		t.Error("the Planned form does not list as protect and planned")
	}

	// Adding from it comes back to the card, at the list.
	w := s.post("/steward/places/"+garden+"/plants", url.Values{
		"species": {winecup}, "action": {"pull"}, "note": {"seedlings along the path"}, "return": {"card"},
	})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/places/rain-garden#pull-h" {
		t.Fatalf("adding from the card: %d %q", w.Code, w.Header().Get("Location"))
	}

	body = s.get("/places/rain-garden").Body.String()
	if !strings.Contains(body, "seedlings along the path") {
		t.Error("the card does not list what was added")
	}

	// A volunteer sees the lists, and no way to change them.
	if public := s.getAs("/places/rain-garden", "").Body.String(); strings.Contains(public, "add-popover") || strings.Contains(public, "+ Add") {
		t.Error("a volunteer is shown the add forms")
	}
}

// The return word is a word, not an address: anything but "card" goes back
// to the Plants screen, as before.
func TestAddingFromThePlantsScreenStaysThere(t *testing.T) {
	s := serve(t)
	s.pilot()

	garden := s.place("rain-garden").ID.String()
	sp := s.plant("winecup", "Winecup", "#8e1b4b")

	w := s.post("/steward/places/"+garden+"/plants", url.Values{"species": {sp}, "action": {"protect"}, "return": {"https://example.com/"}})
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/steward/places/"+garden+"/plants?done=saved" {
		t.Errorf("a return that is not card: %d %q", w.Code, loc)
	}
}

// From To plant to growing and back, from the card: the Growing button takes
// a plant off To plant and leaves it protected with its note; "plant more"
// puts one that is growing back on To plant, keeping its note unless a new
// one is typed.
func TestAPlantGoesFromToPlantToGrowingAndBack(t *testing.T) {
	s := serve(t)
	s.pilot()

	garden := s.place("rain-garden").ID.String()
	penstemon := s.plant("brazos-penstemon", "Brazos penstemon", "#B0418F", "3", "4")

	if w := s.list(garden, penstemon, "protect", true, "6 plants, in the middle"); w.Code != http.StatusSeeOther {
		t.Fatalf("listing: %d", w.Code)
	}

	toPlant := func() string {
		body := s.get("/places/rain-garden").Body.String()

		return body[strings.Index(body, `id="planned-h"`):strings.Index(body, `class="panels`)]
	}

	if got := toPlant(); !strings.Contains(got, "Growing ✓") || !strings.Contains(got, `name="species" value="`+penstemon+`"`) {
		t.Fatal("a To plant row has no Growing button")
	}

	if strings.Contains(s.getAs("/places/rain-garden", "").Body.String(), "Growing ✓") {
		t.Error("a volunteer is shown the Growing button")
	}

	// Growing: what the button sends.
	w := s.post("/steward/places/"+garden+"/plants", url.Values{"species": {penstemon}, "action": {"protect"}, "return": {"card"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/places/rain-garden#protect-h" {
		t.Fatalf("growing: %d %q", w.Code, w.Header().Get("Location"))
	}

	body := s.get("/places/rain-garden").Body.String()
	protect := body[strings.Index(body, "panel-protect"):strings.Index(body, "panel-pull")]

	if got := toPlant(); strings.Contains(got, `<div class="row-name">Brazos penstemon`) {
		t.Error("a growing plant is still on To plant")
	}

	if !strings.Contains(protect, "Brazos penstemon") || !strings.Contains(protect, "6 plants, in the middle") {
		t.Error("a growing plant left Protect, or lost its note")
	}

	// It is offered under "plant more" now, not as a new plant.
	if got := toPlant(); !strings.Contains(got, `<optgroup label="Already growing here: plant more"><option value="`+penstemon+`">Brazos penstemon</option>`) {
		t.Error("the To plant form does not offer the growing penstemon to plant more of")
	}

	// Plant more, with no note: back on To plant, note kept.
	s.post("/steward/places/"+garden+"/plants", url.Values{"species": {penstemon}, "action": {"protect"}, "planned": {"yes"}, "return": {"card"}})
	if got := toPlant(); !strings.Contains(got, "6 plants, in the middle") {
		t.Error("planting more lost the note")
	}

	// And a note typed replaces it.
	s.post("/steward/places/"+garden+"/plants", url.Values{
		"species": {penstemon}, "action": {"protect"}, "planned": {"yes"}, "return": {"card"}, "note": {"6 in, 3 more by the wall"},
	})
	if got := toPlant(); !strings.Contains(got, "6 in, 3 more by the wall") || strings.Contains(got, "6 plants, in the middle") {
		t.Error("a new note did not replace the old one")
	}
}
