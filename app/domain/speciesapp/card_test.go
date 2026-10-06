package speciesapp_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/stewards/business/domain/photo/photobus"
)

// at adds a place and lists the penstemon there, through the stewards'
// screens, and returns nothing: the card is what is under test.
func (s *site) at(slug, name, parent, action string, planned bool) {
	s.t.Helper()

	form := url.Values{"slug": {slug}, "name_en": {name}}
	if parent != "" {
		p, err := s.places.BySlug(s.t.Context(), parent)
		if err != nil {
			s.t.Fatal(err)
		}
		form.Set("parent", p.ID.String())
	}

	if w := s.post("/steward/places", form); w.Code != http.StatusSeeOther {
		s.t.Fatalf("adding %s: %d", slug, w.Code)
	}

	if action == "" {
		return
	}

	p, _ := s.places.BySlug(s.t.Context(), slug)
	sp, _ := s.species.BySlug(s.t.Context(), "brazos-penstemon")

	listing := url.Values{"species": {sp.ID.String()}, "action": {action}}
	if planned {
		listing.Set("planned", "yes")
	}

	if w := s.post("/steward/places/"+p.ID.String()+"/plants", listing); w.Code != http.StatusSeeOther {
		s.t.Fatalf("listing at %s: %d", slug, w.Code)
	}
}

func TestAVolunteerReadsAPlantForPlanting(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	s.at("rain-garden", "Rain garden", "", "", false)
	s.at("rain-garden-inflow", "Inflow band", "rain-garden", "protect", true)

	w := s.do(http.MethodGet, "/plants/brazos-penstemon", nil, false)
	body := w.Body.String()

	if w.Code != http.StatusOK {
		t.Fatalf("the card: %d", w.Code)
	}

	for _, want := range []string{
		"<title>Brazos penstemon · Garden stewards</title>",
		"<h1>Brazos penstemon</h1>",
		"<i>Penstemon tenuis</i>",
		"Native",
		`href="/plants/brazos-penstemon" aria-current="page">Planting`,
		"Purple-pink",
		"Mar–May",
		"24 in tall",
		"Full sun · Part shade",
		"Poor drainage OK. Red winter leaves.",
		"No flower photo yet.",
		"No full-size photo yet.",
		"Where it grows here",
		`href="/places/rain-garden-inflow"`,
		"Rain garden: Inflow band",
		"ID checked against",
		`href="https://www.wildflower.org/plants/result.php?id_plant=PETE4"`,
		"The Natural Gardener list, Sep 2026",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the planting view does not show %q", want)
		}
	}

	// Three months lit on the strip, March to May.
	if n := strings.Count(body, "strip-month is-on"); n != 3 {
		t.Errorf("%d months lit on the bloom strip, want 3", n)
	}

	for _, unwanted := range []string{"Not yet confirmed", "Edit this plant", "No leaf photo yet."} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the planting view shows %q", unwanted)
		}
	}
}

func TestAVolunteerReadsAPlantForWeeding(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	s.at("skinny-bed", "Skinny bed", "", "careful", false)

	body := s.do(http.MethodGet, "/plants/brazos-penstemon?view=weeding", nil, false).Body.String()

	for _, want := range []string{
		`href="/plants/brazos-penstemon?view=weeding" aria-current="page">Weeding`,
		"No young-plant photo yet.",
		"No leaf photo yet.",
		`href="/places/skinny-bed"`,
		"Careful: wear gloves",
		"Not sure it is this one? Leave it.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the weeding view does not show %q", want)
		}
	}

	if strings.Contains(body, "No flower photo yet.") || strings.Contains(body, "Mature size") {
		t.Error("the weeding view shows the planting facts")
	}
}

func TestAPlantNotYetConfirmedSaysSo(t *testing.T) {
	s := serve(t)

	form := penstemon()
	form.Del("confirmed")
	form.Set("source_label", "")
	form.Set("source_url", "")
	s.post("/steward/species", form)

	body := s.do(http.MethodGet, "/plants/brazos-penstemon", nil, false).Body.String()

	for _, want := range []string{"Not yet confirmed", "Not listed at any place yet."} {
		if !strings.Contains(body, want) {
			t.Errorf("the card does not show %q", want)
		}
	}

	if strings.Contains(body, "ID checked against") {
		t.Error("an unconfirmed plant claims to have been checked")
	}
}

func TestAStewardCanEditFromThePlantCard(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())

	sp, _ := s.species.BySlug(t.Context(), "brazos-penstemon")

	if body := s.get("/plants/brazos-penstemon").Body.String(); !strings.Contains(body, `href="/steward/species/`+sp.ID.String()+`/edit"`) {
		t.Error("a signed-in steward is not offered the edit link")
	}
}

func TestAnUnknownPlantSaysWhatToDo(t *testing.T) {
	s := serve(t)

	w := s.do(http.MethodGet, "/plants/no-such-plant", nil, false)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "We can't find that plant") {
		t.Errorf("an unknown plant: %d", w.Code)
	}
}

// A volunteer reads when the plant was seen in flower, from checked photos
// alone, a day off the property naming where.
func TestAVolunteerReadsWhenItWasSeenInFlower(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	sp, _ := s.species.BySlug(t.Context(), "brazos-penstemon")

	add := func(shade uint8, day string, checked bool, where string) {
		on, err := photobus.Day(day)
		if err != nil {
			t.Fatal(err)
		}

		f := photobus.Fields{Kind: photobus.Flower, Source: photobus.Ours, TakenAt: on, Checked: checked, TakenWhere: where}
		if _, err := s.photos.Add(t.Context(), sp.ID, f, picture(t, shade)); err != nil {
			t.Fatal(err)
		}
	}

	add(40, "2026-04-03", true, "")
	add(80, "2026-05-20", true, "Pedernales Falls State Park")
	add(120, "2026-06-30", false, "") // not checked: not for volunteers yet
	add(160, "2025-04-10", true, "")

	body := s.do(http.MethodGet, "/plants/brazos-penstemon", nil, false).Body.String()
	want := "Seen in flower: 2026: 3 Apr – 20 May (Pedernales Falls State Park) · 2025: 10 Apr"
	if !strings.Contains(body, want) {
		t.Errorf("the card does not say %q", want)
	}
	if strings.Contains(body, "30 Jun") {
		t.Error("the card shows a day from an unchecked photo")
	}
}

// A fruit photo is shown in both views, and when the plant was seen in fruit
// is said beside when it was seen in flower.
func TestAVolunteerSeesItsFruit(t *testing.T) {
	s := serve(t)
	s.post("/steward/species", penstemon())
	sp, _ := s.species.BySlug(t.Context(), "brazos-penstemon")

	for i, day := range []string{"2026-08-12", "2026-10-03"} {
		on, _ := photobus.Day(day)
		f := photobus.Fields{Kind: photobus.Fruit, Source: photobus.Ours, TakenAt: on, Checked: true}
		if _, err := s.photos.Add(t.Context(), sp.ID, f, picture(t, uint8(30+40*i))); err != nil {
			t.Fatal(err)
		}
	}

	planting := s.do(http.MethodGet, "/plants/brazos-penstemon", nil, false).Body.String()
	if !strings.Contains(planting, "Seen in fruit: 2026: 12 Aug – 3 Oct") || !strings.Contains(planting, "<strong>Fruit or seed</strong>") {
		t.Error("the planting view does not show the fruit")
	}

	weeding := s.do(http.MethodGet, "/plants/brazos-penstemon?view=weeding", nil, false).Body.String()
	if !strings.Contains(weeding, "<strong>Fruit or seed</strong>") || !strings.Contains(weeding, "No young-plant photo yet.") {
		t.Error("the weeding view does not show the fruit")
	}
}
