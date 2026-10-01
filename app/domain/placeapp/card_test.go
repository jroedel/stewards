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
			"slug": {b.slug}, "name_en": {b.en}, "name_es": {b.es}, "purpose_en": {b.purpose},
			"parent": {garden.ID.String()}, "sort": {string(rune('1' + i))},
		})
		if w.Code != http.StatusSeeOther {
			s.t.Fatalf("adding %s: %d", b.slug, w.Code)
		}
	}
}

// A volunteer, signed out, from the list to the garden to a band.
func TestAVolunteerFindsTheRainGardenAndItsBands(t *testing.T) {
	s := serve(t)
	s.pilot()

	home := s.getAs("/", "").Body.String()
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
		`href="/"><span>‹ All places</span></a>`,
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

// In Spanish: the Spanish a steward wrote, and English marked where there is
// none -- never a guess.
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

func TestAStationsSpaceLinksToItsPrayer(t *testing.T) {
	s := serve(t)

	s.post("/steward/places", url.Values{"slug": {"st-francis"}, "name_en": {"St. Francis garden"}, "trail_anchor": {"francis"}})

	body := s.getAs("/places/st-francis", "").Body.String()
	if !strings.Contains(body, `href="https://schoenstatt-fathers.us/trail/#francis"`) || !strings.Contains(body, `<span class="station-name">St. Francis</span>`) {
		t.Error("the station's space does not link to its prayer")
	}

	s.post("/steward/places", url.Values{"slug": {"switchbacks"}, "name_en": {"Switchbacks"}})
	if strings.Contains(s.getAs("/places/switchbacks", "").Body.String(), "/trail/#") {
		t.Error("a place that is not a station links to one")
	}
}

func TestAnUnknownPlaceSaysWhatToDo(t *testing.T) {
	s := serve(t)

	w := s.getAs("/places/no-such-place", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "We can't find that place") || !strings.Contains(w.Body.String(), `<a class="onward" href="/">`) {
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
