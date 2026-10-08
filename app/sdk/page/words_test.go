package page_test

import (
	"log/slog"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/types"
)

// memory is a translation memory in a map, by the English.
type memory map[string]string

func (m memory) Fill(t types.Text) types.Text {
	if es, ok := m[t.EN]; ok {
		return types.Text{EN: t.EN, ES: es}
	}

	return t
}

func say(t *testing.T, m memory, l types.Lang, v any) string {
	t.Helper()

	fsys := fstest.MapFS{
		"templates/say.html":   {Data: []byte(`{{define "content"}}<p>{{say .Lang .Data}}</p>{{end}}`)},
		"templates/words.html": {Data: []byte(`{{define "content"}}<img alt="{{words .Lang .Data}}">{{end}}`)},
		"templates/strip.html": {Data: []byte(`{{define "content"}}<p>{{range .Data}}{{initial $.Lang .}}{{end}}</p>{{end}}`)},
	}

	rn, err := page.NewRenderer(slog.New(slog.DiscardHandler), fsys)
	if err != nil {
		t.Fatal(err)
	}

	if m != nil {
		rn.Translate(m)
	}

	name := "say"
	switch w := v.(type) {
	case []types.Text:
		name = "strip"
	case words:
		name, v = "words", w.v
	}

	body := render(t, rn, name, v, string(l)).Body.String()

	start := strings.Index(body, "<main>") + len("<main>")
	end := strings.Index(body, "</main>")

	return strings.TrimSpace(body[start:end])
}

type words struct{ v any }

// The screens' own words are looked up as the page is written: a heading
// Claude has translated is Spanish on a Spanish page, and one it has not is
// English marked as English.
func TestTheScreensOwnWordsAreLookedUp(t *testing.T) {
	m := memory{"Protect": "Proteger"}

	if got := say(t, m, types.Spanish, types.Text{EN: "Protect"}); got != "<p>Proteger</p>" {
		t.Errorf("translated: %s", got)
	}

	if got := say(t, m, types.Spanish, types.Text{EN: "Pull"}); got != `<p><span lang="en">Pull</span></p>` {
		t.Errorf("waiting: %s", got)
	}

	if got := say(t, nil, types.Spanish, types.Text{EN: "Protect"}); got != `<p><span lang="en">Protect</span></p>` {
		t.Errorf("without a memory: %s", got)
	}
}

// A phrase is translated with its placeholders, and what goes into them is
// put in afterwards, in the page's language: Spanish word order for a date,
// and a month name translated by itself.
func TestAPhraseIsTranslatedBeforeItIsFilledIn(t *testing.T) {
	m := memory{
		"{weekday}, {month} {day}": "{weekday} {day} de {month}",
		"Saturday":                 "sábado",
		"October":                  "octubre",
		"{count} to plant":         "{count} para plantar",
	}

	day := page.Date(time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC))

	if got := say(t, m, types.Spanish, day); got != "<p>sábado 10 de octubre</p>" {
		t.Errorf("a Spanish date: %s", got)
	}

	if got := say(t, m, types.English, day); got != "<p>Saturday, October 10</p>" {
		t.Errorf("an English date: %s", got)
	}

	// A phrase still waiting is English, marked, round what is put in it --
	// which is in the page's language, marked as nothing.
	m2 := memory{"October": "octubre", "Saturday": "sábado"}
	if got := say(t, m2, types.Spanish, day); got != `<p><span lang="en">sábado, octubre 10</span></p>` {
		t.Errorf("a phrase waiting: %s", got)
	}

	list := page.List{Sep: " · ", Items: []any{
		page.Put(types.Text{EN: "{count} to plant"}, "count", 4),
		page.Put(types.Text{EN: "{count} to pull"}, "count", 1),
	}}

	if got := say(t, m, types.Spanish, list); got != `<p>4 para plantar · <span lang="en">1 to pull</span></p>` {
		t.Errorf("a list: %s", got)
	}

	// What is put in is escaped like anything else: a place's name is typed
	// by a person.
	where := page.Put(types.Text{EN: "{day} ({where})"}, "day", "3 April", "where", "<b>Park</b>")
	if got := say(t, nil, types.English, where); got != "<p>3 April (&lt;b&gt;Park&lt;/b&gt;)</p>" {
		t.Errorf("escaping: %s", got)
	}

	if got := say(t, m, types.Spanish, words{page.Put(types.Text{EN: "{count} to plant"}, "count", 2)}); got != `<img alt="2 para plantar">` {
		t.Errorf("as an attribute: %s", got)
	}
}

// A month strip is headed by each month's initial, from its name in the
// page's language: E for enero.
func TestAMonthsInitialIsFromItsTranslation(t *testing.T) {
	m := memory{"January": "enero", "February": "febrero"}

	if got := say(t, m, types.Spanish, page.Months()[:3]); got != "<p>EFM</p>" {
		t.Errorf("Spanish: %s", got)
	}

	if got := say(t, m, types.English, page.Months()[:3]); got != "<p>JFM</p>" {
		t.Errorf("English: %s", got)
	}
}

func TestAMonthSetIsSaidRunByRun(t *testing.T) {
	set := types.MonthsOf(time.March, time.April, time.May, time.September)

	if got := page.Run(set); say(t, nil, types.English, got) != "<p>March to May, September</p>" {
		t.Errorf("Run: %s", say(t, nil, types.English, got))
	}

	if got := page.Run(0); len(got.Items) != 0 {
		t.Errorf("an empty set: %+v", got)
	}
}

// The catalog lists every piece of copy once per place it is held, with the
// field it is in, so a translator can tell a heading from a kind of photo.
// A text with both halves -- the Vatican's own Spanish -- is listed as it
// is, and the memory counts it as translated already.
func TestTheCatalogListsEachPieceOfCopy(t *testing.T) {
	type cardWords struct {
		Title types.Text
		Kinds map[string]types.Text
		Quote types.Text
		Empty types.Text
		hide  types.Text
	}

	cat := page.Catalog{{Where: "the card", Words: cardWords{
		Title: types.Text{EN: "Flower"},
		Kinds: map[string]types.Text{"leaf": {EN: "Leaf"}, "flower": {EN: "Flower"}},
		Quote: types.Text{EN: "Words", ES: "Palabras"},
		hide:  types.Text{EN: "unexported"},
	}}}

	got, err := cat.Originals(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var lines []string
	for _, s := range got {
		lines = append(lines, s.Where+" = "+s.Text.EN+"/"+s.Text.ES)
	}

	want := []string{
		"the card (Title) = Flower/",
		"the card (Kinds[flower]) = Flower/",
		"the card (Kinds[leaf]) = Leaf/",
		"the card (Quote) = Words/Palabras",
	}

	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
