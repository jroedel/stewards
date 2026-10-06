package page_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/types"
)

func renderer(t *testing.T) *page.Renderer {
	t.Helper()

	fsys := fstest.MapFS{
		"templates/hello.html":   {Data: []byte(`{{define "content"}}<p>{{say .Lang .Data}}</p>{{end}}`)},
		"templates/broken.html":  {Data: []byte(`{{define "content"}}{{.Data.NoSuchField}}{{end}}`)},
		"templates/scripts.html": {Data: []byte(`{{define "content"}}<script type="module" src="{{$.Script "swap.mjs"}}"></script><script type="module" src="{{$.Script "find.mjs"}}"></script>{{end}}`)},
		"templates/no-such.html": {Data: []byte(`{{define "content"}}<script type="module" src="{{$.Script "nothing.mjs"}}"></script>{{end}}`)},
	}

	rn, err := page.NewRenderer(slog.New(slog.DiscardHandler), fsys)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}

	return rn
}

// render runs a request through Lang, as the muxer does, so the page is in
// the language the request asked for.
func render(t *testing.T, rn *page.Renderer, name string, data any, accept string) *httptest.ResponseRecorder {
	t.Helper()

	h := mid.Lang()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rn.Render(w, r, http.StatusOK, name, data)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", accept)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec
}

func TestAPageIsInTheLanguageAskedFor(t *testing.T) {
	rn := renderer(t)
	text := types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}

	rec := render(t, rn, "hello", text, "es")
	body := rec.Body.String()

	for _, want := range []string{`<html lang="es">`, "Jardín de lluvia", `hreflang="en"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the Spanish page has no %s", want)
		}
	}

	if got := rec.Header().Get("Content-Language"); got != "es" {
		t.Errorf("Content-Language = %q", got)
	}

	// A shared cache must not give a Spanish page to an English request.
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Cookie") || !strings.Contains(vary, "Accept-Language") {
		t.Errorf("Vary = %q", vary)
	}
}

// English shown in a Spanish page is marked, so a screen reader switches
// voice. Spanish in a Spanish page, and English in an English one, are not.
func TestEnglishStandingInForSpanishIsMarked(t *testing.T) {
	englishOnly := types.Text{EN: "Fire pit"}
	both := types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}

	if got := page.Say(types.Spanish, englishOnly); got != `<span lang="en">Fire pit</span>` {
		t.Errorf("Spanish page, English-only text: %s", got)
	}

	if got := page.Say(types.Spanish, both); got != "Jardín de lluvia" {
		t.Errorf("Spanish page, Spanish text: %s", got)
	}

	if got := page.Say(types.English, englishOnly); got != "Fire pit" {
		t.Errorf("English page: %s", got)
	}

	// Copy is escaped: a place name is typed by a person.
	if got := page.Say(types.English, types.Text{EN: "<b>Beds</b> & walls"}); got != "&lt;b&gt;Beds&lt;/b&gt; &amp; walls" {
		t.Errorf("not escaped: %s", got)
	}
}

// A template that fails halfway is an error page, never half a page under 200.
func TestAPageThatFailsIsNotHalfSent(t *testing.T) {
	rec := render(t, renderer(t), "broken", types.Text{EN: "x"}, "en")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}

	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("part of the page was sent")
	}
}

func TestTwoAppsCannotDefineTheSamePage(t *testing.T) {
	one := fstest.MapFS{"templates/index.html": {Data: []byte(`{{define "content"}}{{end}}`)}}
	two := fstest.MapFS{"templates/index.html": {Data: []byte(`{{define "content"}}{{end}}`)}}

	if _, err := page.NewRenderer(slog.New(slog.DiscardHandler), one, two); err == nil {
		t.Error("two index pages were accepted")
	}
}

func TestAnIDIsShownByItsFirstEightCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"cbb9fac4a3766e1f52ce8a2c6fa24021": "cbb9fac4",
		"cbb9fac4":                         "cbb9fac4",
		"":                                 "",
	} {
		if got := page.Short(in); got != want {
			t.Errorf("Short(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAProblemBecomesASentence(t *testing.T) {
	for in, want := range map[string]string{
		"give the place a name in English": "Give the place a name in English.",
		"already one.":                     "Already one.",
		"  ñandú first ":                   "Ñandú first.",
		"":                                 "",
		"is it?":                           "Is it?",
	} {
		if got := page.Sentence(in); got != want {
			t.Errorf("Sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// A shared script is linked under a name holding its hash, and served from
// there for ever; the tests beside the scripts are not served, and a page
// naming a script there is none of fails rather than linking nothing.
func TestTheSharedScriptsAreServedByTheirHash(t *testing.T) {
	rn := renderer(t)

	body := render(t, rn, "scripts", nil, "en").Body.String()

	for _, name := range []string{"swap", "find"} {
		m := regexp.MustCompile(`src="(/static/js/` + name + `\.[0-9a-f]{12}\.mjs)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no %s.mjs with its hash in the page:\n%s", name, body)
		}

		r := httptest.NewRequest(http.MethodGet, m[1], nil)
		r.SetPathValue("file", strings.TrimPrefix(m[1], "/static/js/"))
		rec := httptest.NewRecorder()
		rn.Scripts().ServeHTTP(rec, r)

		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
			t.Errorf("%s: status %d, type %q", m[1], rec.Code, rec.Header().Get("Content-Type"))
		}

		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s is not cached for ever: %q", m[1], rec.Header().Get("Cache-Control"))
		}
	}

	for _, file := range []string{"find_test.mjs", "find.mjs", "swap.000000000000.mjs"} {
		r := httptest.NewRequest(http.MethodGet, "/static/js/"+file, nil)
		r.SetPathValue("file", file)
		rec := httptest.NewRecorder()
		rn.Scripts().ServeHTTP(rec, r)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", file, rec.Code)
		}
	}

	if rec := render(t, rn, "no-such", nil, "en"); rec.Code != http.StatusInternalServerError {
		t.Errorf("a page naming a script that is not there: status %d, want 500", rec.Code)
	}
}
