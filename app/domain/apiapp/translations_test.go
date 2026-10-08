package apiapp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jroedel/stewards/app/domain/apiapp"
)

type pendingAnswer struct {
	Pending   []apiapp.PendingJSON `json:"pending"`
	Remaining int                  `json:"remaining"`
	Glossary  []apiapp.TextJSON    `json:"glossary"`
}

type resultsAnswer struct {
	Results []apiapp.ResultJSON `json:"results"`
}

func (s *site) translate(body any) *http.Response {
	s.t.Helper()

	b, _ := json.Marshal(body)

	return s.api(http.MethodPut, "/api/v1/translations", s.key, bytes.NewReader(b), "application/json").Result()
}

// The whole of what Claude does: add a plant in English, find it waiting,
// send its Spanish, and see the plant answer in both languages, with nothing
// waiting after.
func TestClaudeTranslatesWhatIsWaiting(t *testing.T) {
	s := serve(t)

	plant := winecup()
	plant["note"] = map[string]string{"en": "Cut back after it seeds."}

	if w := s.put("winecup", plant); w.Code != http.StatusCreated {
		t.Fatalf("adding the plant: %d %s", w.Code, w.Body.String())
	}

	if w := s.api(http.MethodGet, "/api/v1/translations/pending", "", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without a key: %d", w.Code)
	}

	w := s.api(http.MethodGet, "/api/v1/translations/pending", s.key, nil, "")
	got := decode[pendingAnswer](t, w)

	if w.Code != http.StatusOK || got.Remaining != 2 || len(got.Pending) != 2 {
		t.Fatalf("pending: %d %s", w.Code, w.Body.String())
	}

	name, note := got.Pending[0], got.Pending[1]
	switch {
	case name.Text != "Winecup" || !name.Name || name.WrittenIn != "en" || len(name.Where) != 1 || name.Where[0] != "the common name of Callirhoe involucrata":
		t.Errorf("the name: %+v", name)
	case note.Text != "Cut back after it seeds." || note.Name:
		t.Errorf("the note: %+v", note)
	}

	res := s.translate(map[string]any{"translations": []map[string]string{
		{"key": name.Key, "from": "en", "text": "Copa de vino"},
		{"key": note.Key, "from": "en", "text": "Córtala después de que dé semilla."},
		{"key": "0123456789abcdef", "from": "en", "text": "Algo"},
	}})

	var results resultsAnswer
	if err := json.NewDecoder(res.Body).Decode(&results); err != nil || res.StatusCode != http.StatusOK || len(results.Results) != 3 {
		t.Fatalf("translating: %d %+v %v", res.StatusCode, results, err)
	}

	if r := results.Results; r[0].Outcome != "created" || r[1].Outcome != "created" || r[2].Outcome != "refused" || r[2].Field != "key" || r[2].Problem == "" {
		t.Errorf("results %+v", r)
	}

	// The plant answers in both languages at once.
	w = s.api(http.MethodGet, "/api/v1/species/winecup", s.key, nil, "")
	sp := decode[struct {
		Species apiapp.SpeciesJSON `json:"species"`
	}](t, w)

	if sp.Species.Common.ES != "Copa de vino" || sp.Species.Note.ES != "Córtala después de que dé semilla." {
		t.Errorf("the plant: %+v", sp.Species)
	}

	// Sending it back as the API gave it changes nothing.
	plant["common"] = map[string]string{"en": "Winecup", "es": "Copa de vino"}
	if w := s.put("winecup", plant); decode[map[string]any](t, w)["outcome"] != "unchanged" {
		t.Errorf("sent back: %s", w.Body.String())
	}

	got = decode[pendingAnswer](t, s.api(http.MethodGet, "/api/v1/translations/pending", s.key, nil, ""))
	if got.Remaining != 0 || len(got.Pending) != 0 || len(got.Glossary) != 1 || got.Glossary[0].ES != "Copa de vino" {
		t.Errorf("after: %+v", got)
	}

	// And the list of what is made has both, the newest first.
	list := decode[struct {
		Translations []apiapp.TranslationJSON `json:"translations"`
		Total        int                      `json:"total"`
	}](t, s.api(http.MethodGet, "/api/v1/translations?checked=no&limit=1", s.key, nil, ""))

	if list.Total != 2 || len(list.Translations) != 1 || list.Translations[0].By != "claude" || !list.Translations[0].InUse {
		t.Errorf("list: %+v", list)
	}
}

func TestATranslationBatchIsRefusedWhole(t *testing.T) {
	s := serve(t)

	for _, tc := range []struct {
		body   any
		status int
	}{
		{map[string]any{}, http.StatusUnprocessableEntity},
		{map[string]any{"translations": make([]map[string]string, 201)}, http.StatusUnprocessableEntity},
		{"not an object", http.StatusBadRequest},
	} {
		if res := s.translate(tc.body); res.StatusCode != tc.status {
			t.Errorf("%v: %d, want %d", tc.body, res.StatusCode, tc.status)
		}
	}

	for _, path := range []string{
		"/api/v1/translations/pending?limit=0",
		"/api/v1/translations/pending?limit=many",
		"/api/v1/translations?checked=maybe",
		"/api/v1/translations?offset=-1",
	} {
		if w := s.api(http.MethodGet, path, s.key, nil, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// A steward who speaks Spanish has Claude send their words in Spanish: they
// wait for an English translation, which Claude sends from Spanish, and the
// place answers in both.
func TestWordsSentInSpanishAreTranslatedIntoEnglish(t *testing.T) {
	s := serve(t)

	body, _ := json.Marshal(map[string]any{
		"name":       map[string]string{"en": "Hummingbird bed"},
		"conditions": map[string]string{"es": "Sombra por la tarde. Se seca rápido."},
	})

	if w := s.api(http.MethodPut, "/api/v1/places/hummingbird-bed", s.key, bytes.NewReader(body), "application/json"); w.Code != http.StatusCreated {
		t.Fatalf("adding the place: %d %s", w.Code, w.Body.String())
	}

	got := decode[pendingAnswer](t, s.api(http.MethodGet, "/api/v1/translations/pending", s.key, nil, ""))

	var spanish apiapp.PendingJSON
	for _, p := range got.Pending {
		if p.WrittenIn == "es" {
			spanish = p
		}
	}

	if spanish.Text != "Sombra por la tarde. Se seca rápido." {
		t.Fatalf("pending: %+v", got.Pending)
	}

	s.translate(map[string]any{"translations": []map[string]string{
		{"key": spanish.Key, "from": "es", "text": "Shade in the afternoon. Dries out fast."},
	}})

	place := decode[struct {
		Place apiapp.PlaceJSON `json:"place"`
	}](t, s.api(http.MethodGet, "/api/v1/places/hummingbird-bed", s.key, nil, ""))

	if c := place.Place.Conditions; c.EN != "Shade in the afternoon. Dries out fast." || c.ES != "Sombra por la tarde. Se seca rápido." {
		t.Errorf("the conditions: %+v", c)
	}

	// Sending its Spanish back as it is changes nothing.
	if w := s.api(http.MethodPut, "/api/v1/places/hummingbird-bed", s.key, bytes.NewReader(body), "application/json"); decode[map[string]any](t, w)["outcome"] != "unchanged" {
		t.Errorf("sent again: %s", w.Body.String())
	}
}
