package translationbus_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
)

// origins is a domain, as far as the memory can tell: a list of words and
// where they are read.
type origins []translationbus.Source

func (o origins) Originals(context.Context) ([]translationbus.Source, error) { return o, nil }

var garden = origins{
	{Text: types.Text{EN: "Full sun, dry by July"}, Where: "Rain garden: its conditions"},
	{Text: types.Text{EN: "Rain garden"}, Where: "the name of a place", Name: true},
	{Text: types.Text{ES: "Dos al fondo, contra la pared"}, Where: "Rain garden: a note on Sedge"},
	{Text: types.Text{EN: "Full sun, dry by July"}, Where: "Skinny bed: its conditions"},
	{Text: types.Text{EN: "Winecup"}, Where: "the common name of Callirhoe involucrata", Name: true},
	{Text: types.Text{EN: "Old", ES: "Viejo"}, Where: "a record the startup has not moved"},
	{Text: types.Text{}, Where: "nothing written"},
}

func waiting(t *testing.T, b *translationbus.Business, limit int) translationbus.Waiting {
	t.Helper()

	w, err := b.Waiting(t.Context(), limit)
	if err != nil {
		t.Fatal(err)
	}

	return w
}

func sources(w translationbus.Waiting) []string {
	var out []string
	for _, p := range w.Pending {
		out = append(out, p.Source)
	}

	return out
}

func translate(t *testing.T, b *translationbus.Business, uploads ...translationbus.Upload) []translationbus.Result {
	t.Helper()

	results, err := b.Translate(t.Context(), translationbus.ByClaude, uploads)
	if err != nil {
		t.Fatal(err)
	}

	return results
}

// Each original once, names first, every place it is read; the glossary is
// the names already translated; and remaining counts what a limit leaves.
func TestWaitingIsEveryOriginalWithNoTranslation(t *testing.T) {
	b := setup(t).open(t)
	b.ReadFrom(garden)

	w := waiting(t, b, 10)

	want := []string{"Rain garden", "Winecup", "Full sun, dry by July", "Dos al fondo, contra la pared"}
	if got := sources(w); !slices.Equal(got, want) {
		t.Fatalf("pending %q, want %q", got, want)
	}

	sun := w.Pending[2]
	switch {
	case w.Remaining != 4:
		t.Errorf("remaining %d", w.Remaining)
	case len(sun.Where) != 2 || sun.Guess != types.English || sun.Key != translationbus.Key(sun.Source):
		t.Errorf("the sun note: %+v", sun)
	case w.Pending[3].Guess != types.Spanish || !w.Pending[0].Name || w.Pending[2].Name:
		t.Errorf("guesses and names: %+v", w.Pending)
	case len(w.Glossary) != 0:
		t.Errorf("glossary %+v before any name is translated", w.Glossary)
	}

	translate(t, b, translationbus.Upload{Key: w.Pending[0].Key, From: types.English, Translated: "Jardín de lluvia"})

	w = waiting(t, b, 1)
	switch {
	case w.Remaining != 3 || len(w.Pending) != 1 || w.Pending[0].Source != "Winecup":
		t.Errorf("after one: %d remaining, %+v", w.Remaining, w.Pending)
	case len(w.Glossary) != 1 || w.Glossary[0] != (types.Text{EN: "Rain garden", ES: "Jardín de lluvia"}):
		t.Errorf("glossary %+v", w.Glossary)
	}
}

// A translation is on the screens as soon as it is kept, the right way
// round for the language Claude says the original is in -- which may not
// be the half it was stored in.
func TestATranslationIsShownAtOnce(t *testing.T) {
	m := setup(t)
	b := m.open(t)
	b.ReadFrom(garden)

	note := types.Text{ES: "Dos al fondo, contra la pared"}
	results := translate(t, b,
		translationbus.Upload{Key: translationbus.Key(note.ES), From: types.Spanish, Translated: "  Two at the back, against the wall "},
		translationbus.Upload{Key: translationbus.Key("Winecup"), From: types.English, Translated: "Winecup"},
	)

	for _, r := range results {
		if r.Outcome != translationbus.Created {
			t.Errorf("%+v", r)
		}
	}

	if got, want := b.Fill(note), (types.Text{EN: "Two at the back, against the wall", ES: note.ES}); got != want {
		t.Errorf("Fill = %+v, want %+v", got, want)
	}

	// A name with no Spanish of its own is translated as itself, and that
	// is a translation: it is no longer waiting.
	if got := b.Fill(types.Text{EN: "Winecup"}); got != (types.Text{EN: "Winecup", ES: "Winecup"}) {
		t.Errorf("Fill = %+v", got)
	}

	tr, _ := m.open(t).Lookup(note.ES)
	if tr.By != translationbus.ByClaude || tr.Checked || tr.From != types.Spanish {
		t.Errorf("kept as %+v", tr)
	}
}

func TestTranslateSaysWhatBecameOfEach(t *testing.T) {
	m := setup(t)
	store := translationdb.NewStore(m.db)

	// One a steward has checked, and one of words no record says any more.
	for _, tr := range []translationbus.Translation{
		{Source: "Rain garden", Translated: "Jardín de lluvia", By: translationbus.BySteward, Checked: true},
		{Source: "Shade bed", Translated: "Cama de sombra", By: translationbus.ByClaude},
	} {
		tr.Key, tr.From, tr.CreatedAt, tr.UpdatedAt = translationbus.Key(tr.Source), types.English, *m.clock, *m.clock

		if err := store.Put(t.Context(), tr); err != nil {
			t.Fatal(err)
		}
	}

	b := m.open(t)
	b.ReadFrom(append(garden, translationbus.Source{Text: types.Text{EN: "Plant {count} at {place}"}, Where: "a sentence with blanks"}))

	key := translationbus.Key
	results := translate(t, b,
		translationbus.Upload{Key: key("Rain garden"), From: types.English, Translated: "Jardín de lluvia"},
		translationbus.Upload{Key: key("Rain garden"), From: types.English, Translated: "Jardín pluvial"},
		translationbus.Upload{Key: key("Shade bed"), From: types.English, Translated: "Arriate de sombra"},
		translationbus.Upload{Key: key("Shade bed"), From: types.English, Translated: "Arriate de sombra"},
		translationbus.Upload{Key: "0123456789abcdef", From: types.English, Translated: "Algo"},
		translationbus.Upload{Key: key("Winecup"), From: "fr", Translated: "Copa de vino"},
		translationbus.Upload{Key: key("Winecup"), From: types.English, Translated: "   "},
		translationbus.Upload{Key: key("Plant {count} at {place}"), From: types.English, Translated: "Planta {count} en el lugar"},
		translationbus.Upload{Key: key("Plant {count} at {place}"), From: types.English, Translated: "Planta {count} en {place}"},
	)

	want := []struct {
		outcome translationbus.Outcome
		field   string
	}{
		{translationbus.Unchanged, ""},
		{translationbus.Refused, "key"},
		{translationbus.Updated, ""},
		{translationbus.Unchanged, ""},
		{translationbus.Refused, "key"},
		{translationbus.Refused, "from"},
		{translationbus.Refused, "text"},
		{translationbus.Refused, "text"},
		{translationbus.Created, ""},
	}

	for i, r := range results {
		if r.Outcome != want[i].outcome || r.Field != want[i].field || (r.Outcome == translationbus.Refused) != (r.Problem != "") {
			t.Errorf("upload %d: %+v, want %s %q", i, r, want[i].outcome, want[i].field)
		}
	}

	if got := b.Fill(types.Text{EN: "Rain garden"}); got.ES != "Jardín de lluvia" {
		t.Errorf("the checked translation changed: %+v", got)
	}

	tr, _ := b.Lookup("Shade bed")
	if !tr.CreatedAt.Equal(*m.clock) || tr.By != translationbus.ByClaude {
		t.Errorf("updated as %+v", tr)
	}
}

func TestListGivesTheNewestFirstWithWhereEachIsRead(t *testing.T) {
	m := setup(t)
	b := m.open(t)
	b.ReadFrom(garden)

	translate(t, b, translationbus.Upload{Key: translationbus.Key("Rain garden"), From: types.English, Translated: "Jardín de lluvia"})

	*m.clock = m.clock.Add(time.Hour)
	translate(t, b, translationbus.Upload{Key: translationbus.Key("Winecup"), From: types.English, Translated: "Copa de vino"})

	all, err := b.List(t.Context(), translationbus.Filter{})
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 2 || all[0].Source != "Winecup" || !all[0].InUse || !all[0].Name || len(all[0].Where) != 1 {
		t.Errorf("List = %+v", all)
	}

	if checked, _ := b.List(t.Context(), translationbus.Filter{Checked: "yes"}); len(checked) != 0 {
		t.Errorf("checked: %+v", checked)
	}

	if unchecked, _ := b.List(t.Context(), translationbus.Filter{Checked: "no"}); len(unchecked) != 2 {
		t.Errorf("unchecked: %+v", unchecked)
	}
}
