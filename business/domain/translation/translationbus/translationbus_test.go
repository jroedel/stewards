package translationbus_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/translation/stores/translationdb"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The rules run against the real store, and a second Business is opened on
// the same database where it matters that the memory was saved and not only
// held. The words are invented garden notes.
type memory struct {
	db    *sql.DB
	clock *time.Time
}

func setup(t *testing.T) *memory {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := translationdb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	if err := translationdb.Init(t.Context(), db); err != nil { // at every startup
		t.Fatal(err)
	}

	if err := sqldb.CheckSchema(t.Context(), db, translationdb.Expected); err != nil {
		t.Fatal(err)
	}

	clock := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

	return &memory{db: db, clock: &clock}
}

// open is a Business on m's database, as a fresh start of the app would make.
func (m *memory) open(t *testing.T) *translationbus.Business {
	t.Helper()

	b, err := translationbus.NewBusiness(t.Context(), translationdb.NewStore(m.db), func() time.Time { return *m.clock })
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func keep(t *testing.T, b *translationbus.Business, text, before types.Text) types.Text {
	t.Helper()

	stored, err := b.Keep(t.Context(), text, before)
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}

	return stored
}

var (
	sun    = types.Text{EN: "Full sun, dry by July", ES: "Pleno sol, seco en julio"}
	moved  = types.Text{EN: "Full sun, dry by August"}
	shaded = types.Text{EN: "Shade after noon", ES: "Sombra por la tarde"}
)

func TestKeepStoresTheOriginalAndRemembersItsTranslation(t *testing.T) {
	m := setup(t)
	b := m.open(t)

	if got := keep(t, b, sun, types.Text{}); got != (types.Text{EN: sun.EN}) {
		t.Errorf("stored %+v, want the English alone", got)
	}

	// Saved, not only held: a fresh start reads it back.
	again := m.open(t)

	if got := again.Fill(types.Text{EN: sun.EN}); got != sun {
		t.Errorf("Fill = %+v, want %+v", got, sun)
	}

	tr, ok := again.Lookup(sun.EN)
	switch {
	case !ok:
		t.Fatal("the translation is not in the memory")
	case tr.By != translationbus.Given || tr.Checked || tr.From != types.English:
		t.Errorf("kept as %+v, want given, unchecked, from English", tr)
	case tr.Key != translationbus.Key(sun.EN) || len(tr.Key) != 16:
		t.Errorf("key %q", tr.Key)
	}
}

func TestFillLeavesWordsWithNoTranslationAsTheyAre(t *testing.T) {
	b := setup(t).open(t)

	for _, text := range []types.Text{{}, {EN: "Pull before it seeds"}, {ES: "Arrancar antes de semillar"}, shaded} {
		if got := b.Fill(text); got != text {
			t.Errorf("Fill(%+v) = %+v", text, got)
		}
	}
}

// The words carry the translation, not the record: a second place saying
// the same thing is shown the same Spanish, and leaving its Spanish box empty
// takes nothing away from the first.
func TestATranslationBelongsToTheWords(t *testing.T) {
	b := setup(t).open(t)

	keep(t, b, sun, types.Text{})
	keep(t, b, types.Text{EN: sun.EN}, types.Text{})

	if got := b.Fill(types.Text{EN: sun.EN}); got != sun {
		t.Errorf("Fill = %+v, want %+v", got, sun)
	}
}

// The steward corrects the English and leaves the Spanish box as it was
// shown: the old Spanish is a translation of the old words, so it is not
// kept for the new ones, which wait for Claude. The old words keep theirs.
func TestSpanishLeftAsItWasBesideChangedEnglishIsNotKept(t *testing.T) {
	b := setup(t).open(t)

	keep(t, b, sun, types.Text{})

	edited := types.Text{EN: moved.EN, ES: sun.ES}
	if got := keep(t, b, edited, sun); got != moved {
		t.Errorf("stored %+v, want %+v", got, moved)
	}

	if got := b.Fill(moved); got != moved {
		t.Errorf("Fill = %+v; the new English has a translation it was never given", got)
	}

	if got := b.Fill(types.Text{EN: sun.EN}); got != sun {
		t.Errorf("the old words lost their translation: %+v", got)
	}

	// Both changed together is a translation of the new words.
	both := types.Text{EN: moved.EN, ES: "Pleno sol, seco en agosto"}
	keep(t, b, both, sun)

	if got := b.Fill(moved); got != both {
		t.Errorf("Fill = %+v, want %+v", got, both)
	}
}

// Saving a record without changing its words writes nothing, so a check a
// steward has given a translation survives every save of the place it is
// on. A different Spanish replaces it, and is unchecked again.
func TestSavingTheSameWordsKeepsTheCheck(t *testing.T) {
	m := setup(t)
	store := translationdb.NewStore(m.db)

	checked := translationbus.Translation{
		Key: translationbus.Key(sun.EN), Source: sun.EN, From: types.English, Translated: sun.ES,
		By: translationbus.BySteward, Checked: true, CreatedAt: *m.clock, UpdatedAt: *m.clock,
	}
	if err := store.Put(t.Context(), checked); err != nil {
		t.Fatal(err)
	}

	b := m.open(t)
	*m.clock = m.clock.Add(time.Hour)

	keep(t, b, sun, sun)

	if got, _ := m.open(t).Lookup(sun.EN); got != checked {
		t.Errorf("after an unchanged save: %+v, want %+v", got, checked)
	}

	other := types.Text{EN: sun.EN, ES: "Sol todo el día, seco en julio"}
	keep(t, b, other, sun)

	got, _ := m.open(t).Lookup(sun.EN)
	switch {
	case got.Translated != other.ES || got.Checked || got.By != translationbus.Given:
		t.Errorf("after a change: %+v, want the new Spanish, given and unchecked", got)
	case !got.CreatedAt.Equal(checked.CreatedAt) || !got.UpdatedAt.Equal(*m.clock):
		t.Errorf("times %v, %v: want created kept and updated now", got.CreatedAt, got.UpdatedAt)
	}
}

// Words first written in Spanish are stored in Spanish, and a translation
// from Spanish fills the English half. So does a translation that says words
// stored in the English half were really Spanish -- typed on the English
// page -- and the pair comes out the right way round.
func TestEitherLanguageCanBeTheOriginal(t *testing.T) {
	m := setup(t)
	store := translationdb.NewStore(m.db)

	written := types.Text{ES: "Regar dos veces por semana hasta que agarre"}
	misplaced := types.Text{EN: "Cuidado con las espinas"}

	for _, tr := range []translationbus.Translation{
		{Source: written.ES, Translated: "Water twice a week until it takes"},
		{Source: misplaced.EN, Translated: "Watch out for the thorns"},
	} {
		tr.Key, tr.From, tr.By = translationbus.Key(tr.Source), types.Spanish, translationbus.ByClaude
		tr.CreatedAt, tr.UpdatedAt = *m.clock, *m.clock

		if err := store.Put(t.Context(), tr); err != nil {
			t.Fatal(err)
		}
	}

	b := m.open(t)

	if got := keep(t, b, written, types.Text{}); got != written {
		t.Errorf("stored %+v, want the Spanish alone", got)
	}

	if got, want := b.Fill(written), (types.Text{EN: "Water twice a week until it takes", ES: written.ES}); got != want {
		t.Errorf("Fill = %+v, want %+v", got, want)
	}

	if got, want := b.Fill(misplaced), (types.Text{EN: "Watch out for the thorns", ES: misplaced.EN}); got != want {
		t.Errorf("Fill = %+v, want %+v", got, want)
	}
}

// Move is the startup's: Spanish stored beside English before the memory
// goes in, unless the memory already has a translation of those words --
// which may be a steward's correction -- and the record keeps its English.
func TestMoveNeverReplacesWhatTheMemoryHas(t *testing.T) {
	b := setup(t).open(t)

	got, err := b.Move(t.Context(), sun)
	if err != nil {
		t.Fatal(err)
	}

	if got != (types.Text{EN: sun.EN}) {
		t.Errorf("Move = %+v, want the English alone", got)
	}

	if tr, _ := b.Lookup(sun.EN); tr.Translated != sun.ES || tr.By != translationbus.Given {
		t.Errorf("moved as %+v", tr)
	}

	stale := types.Text{EN: sun.EN, ES: "Sol"}
	if _, err := b.Move(t.Context(), stale); err != nil {
		t.Fatal(err)
	}

	if got := b.Fill(types.Text{EN: sun.EN}); got != sun {
		t.Errorf("Fill = %+v; an old column replaced the memory", got)
	}

	for _, text := range []types.Text{{}, {EN: "Pull it"}, {ES: "Arráncala"}} {
		if got, _ := b.Move(t.Context(), text); got != text {
			t.Errorf("Move(%+v) = %+v; with one half there is nothing to move", text, got)
		}
	}
}
