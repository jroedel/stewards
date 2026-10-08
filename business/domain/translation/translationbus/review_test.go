package translationbus_test

import (
	"errors"
	"testing"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
)

// What a steward does on the screen, and what Claude sees of it: a
// translation sent back is waiting again, first, with the note and the
// translation there now, and stays on the screens until a better one comes.
func TestAStewardChecksCorrectsAndSendsBack(t *testing.T) {
	m := setup(t)
	b := m.open(t)
	b.ReadFrom(garden)

	key := translationbus.Key
	translate(t, b,
		translationbus.Upload{Key: key("Rain garden"), From: types.English, Translated: "Jardín pluvial"},
		translationbus.Upload{Key: key("Winecup"), From: types.English, Translated: "Copa de vino"},
		translationbus.Upload{Key: key("Full sun, dry by July"), From: types.English, Translated: "Sol pleno, seco para julio"},
	)

	if c, err := b.Counts(t.Context()); err != nil || c != (translationbus.Counts{ToCheck: 3, Waiting: 1}) {
		t.Errorf("Counts = %+v, %v", c, err)
	}

	if n, err := b.Check(t.Context(), key("Winecup"), key("Winecup"), "0123456789abcdef"); err != nil || n != 1 {
		t.Errorf("Check = %d, %v", n, err)
	}

	got, err := b.Correct(t.Context(), key("Rain garden"), " Jardín de lluvia ")
	if err != nil || got.Translated != "Jardín de lluvia" || got.By != translationbus.BySteward || !got.Checked {
		t.Errorf("Correct = %+v, %v", got, err)
	}

	sent, err := b.SendBack(t.Context(), key("Full sun, dry by July"), "Is it dry by July, or dry from July on?")
	if err != nil || sent.Checked || sent.Note == "" {
		t.Errorf("SendBack = %+v, %v", sent, err)
	}

	// Still shown while it waits.
	if got := b.Fill(types.Text{EN: "Full sun, dry by July"}); got.ES != "Sol pleno, seco para julio" {
		t.Errorf("Fill = %+v", got)
	}

	w := waiting(t, b, 10)
	if w.Remaining != 2 || w.Pending[0].Source != "Full sun, dry by July" || w.Pending[0].Current != "Sol pleno, seco para julio" || w.Pending[0].Note != sent.Note {
		t.Errorf("waiting %+v", w)
	}

	if c, _ := b.Counts(t.Context()); c != (translationbus.Counts{ToCheck: 1, Waiting: 2}) {
		t.Errorf("Counts = %+v", c)
	}

	// Claude's answer, even the same words again, answers the note.
	if r := translate(t, b, translationbus.Upload{Key: key("Full sun, dry by July"), From: types.English, Translated: "Sol pleno, seco para julio"}); r[0].Outcome != translationbus.Updated {
		t.Errorf("answering the note: %+v", r)
	}

	if tr, _ := b.ByKey(key("Full sun, dry by July")); tr.Note != "" {
		t.Errorf("the note is still there: %+v", tr)
	}

	// Sent back and then agreed with after all: no longer waiting.
	if _, err := b.SendBack(t.Context(), key("Winecup"), "Is there a Spanish name?"); err != nil {
		t.Fatal(err)
	}

	if n, err := b.Check(t.Context(), key("Winecup")); err != nil || n != 1 {
		t.Errorf("Check = %d, %v", n, err)
	}

	if tr, _ := b.ByKey(key("Winecup")); tr.Note != "" || !tr.Checked {
		t.Errorf("checked after sending back: %+v", tr)
	}

	// And a fresh start reads all of it back.
	again := m.open(t)
	if tr, _ := again.ByKey(key("Rain garden")); tr.Translated != "Jardín de lluvia" || !tr.Checked || tr.By != translationbus.BySteward {
		t.Errorf("after a restart: %+v", tr)
	}
}

func TestAStewardsChangeIsRefusedWhenItCannotBeRight(t *testing.T) {
	m := setup(t)
	b := m.open(t)
	b.ReadFrom(append(garden, translationbus.Source{Text: types.Text{EN: "Plant {count} here"}, Where: "a sentence with a blank"}))

	key := translationbus.Key("Plant {count} here")
	translate(t, b, translationbus.Upload{Key: key, From: types.English, Translated: "Planta {count} aquí"})

	for _, tc := range []struct {
		err   error
		field string
	}{
		{func() error { _, err := b.Correct(t.Context(), key, "  "); return err }(), "text"},
		{func() error { _, err := b.Correct(t.Context(), key, "Planta tres aquí"); return err }(), "text"},
		{func() error { _, err := b.SendBack(t.Context(), key, ""); return err }(), "note"},
	} {
		if invalid, ok := errors.AsType[translationbus.Invalid](tc.err); !ok || invalid.Field != tc.field {
			t.Errorf("%v, want Invalid on %s", tc.err, tc.field)
		}
	}

	if _, err := b.Correct(t.Context(), "0123456789abcdef", "Algo"); !errors.Is(err, translationbus.ErrNotFound) {
		t.Errorf("an unknown key: %v", err)
	}
}
