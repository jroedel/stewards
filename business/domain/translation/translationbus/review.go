package translationbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// What a steward does on the translations screen: agree with a translation,
// write a better one, or send it back to Claude with a note saying what is
// wrong. Checking is the steward's word that it is right; it is not needed
// for a translation to be shown, which it is from the moment it is made.

// ErrNotFound is returned when there is no translation with the key.
var ErrNotFound = errors.New("there is no such translation")

// Invalid is a change a steward could not make as given, shaped like the
// other domains' Invalid: Field names the input, Problem says what to fix.
type Invalid struct {
	Field   string
	Problem string
}

func (e Invalid) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Problem) }

// maxNote is the longest note a steward sends back with.
const maxNote = 500

// Check marks each of keys checked and reports how many that changed. A key
// with no translation is passed over: one sent back and translated again by
// Claude in the meantime is still there, and is checked; one that never was
// is nothing to report.
//
// Checking one that was sent back takes the note away too: the steward has
// looked again and it is right after all, so it is no longer waiting.
func (b *Business) Check(ctx context.Context, keys ...string) (int, error) {
	n := 0

	for _, key := range keys {
		t, ok := b.ByKey(key)
		if !ok || (t.Checked && t.Note == "") {
			continue
		}

		t.Checked, t.Note, t.UpdatedAt = true, "", b.now()

		if err := b.store.Put(ctx, t); err != nil {
			return n, fmt.Errorf("checking a translation: %w", err)
		}

		b.remember(t)
		n++
	}

	return n, nil
}

// Correct is a steward's own translation of key's original, which is
// checked by being theirs. Any note sent back with it is answered by it.
func (b *Business) Correct(ctx context.Context, key, text string) (Translation, error) {
	t, ok := b.ByKey(key)
	if !ok {
		return Translation{}, ErrNotFound
	}

	text = strings.TrimSpace(text)

	switch {
	case text == "":
		return Translation{}, Invalid{Field: "text", Problem: "write the translation, or press Looks right to keep the one there"}
	case utf8.RuneCountInString(text) > maxTranslation:
		return Translation{}, Invalid{Field: "text", Problem: fmt.Sprintf("keep it under %d characters", maxTranslation)}
	}

	if want, got := placeholders(t.Source), placeholders(text); !slices.Equal(want, got) {
		return Translation{}, Invalid{Field: "text", Problem: fmt.Sprintf("keep each of %s exactly as it is: the app fills it in", strings.Join(want, " "))}
	}

	t.Translated, t.By, t.Checked, t.Note, t.UpdatedAt = text, BySteward, true, "", b.now()

	if err := b.store.Put(ctx, t); err != nil {
		return Translation{}, fmt.Errorf("saving a steward's translation: %w", err)
	}

	b.remember(t)

	return t, nil
}

// SendBack asks Claude to translate key's original again, saying what is
// wrong in note. The translation stays on the screens until Claude sends a
// better one -- a doubtful translation is closer than none -- and is no
// longer checked.
func (b *Business) SendBack(ctx context.Context, key, note string) (Translation, error) {
	t, ok := b.ByKey(key)
	if !ok {
		return Translation{}, ErrNotFound
	}

	note = strings.TrimSpace(note)

	switch {
	case note == "":
		return Translation{}, Invalid{Field: "note", Problem: "say what is wrong with it, so Claude knows what to change"}
	case utf8.RuneCountInString(note) > maxNote:
		return Translation{}, Invalid{Field: "note", Problem: fmt.Sprintf("keep the note under %d characters", maxNote)}
	}

	t.Note, t.Checked, t.UpdatedAt = note, false, b.now()

	if err := b.store.Put(ctx, t); err != nil {
		return Translation{}, fmt.Errorf("sending a translation back: %w", err)
	}

	b.remember(t)

	return t, nil
}

// Counts is what the stewards' screens say about translations at a glance.
type Counts struct {
	// ToCheck is the translations of words in use that no steward has
	// checked.
	ToCheck int

	// Waiting is the words waiting for Claude: never translated, or sent
	// back.
	Waiting int
}

// Counts counts them.
func (b *Business) Counts(ctx context.Context) (Counts, error) {
	byKey, _, err := b.inUse(ctx)
	if err != nil {
		return Counts{}, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	var c Counts

	for key := range byKey {
		t, ok := b.byKey[key]

		if !ok || t.Note != "" {
			c.Waiting++
		}

		if ok && !t.Checked {
			c.ToCheck++
		}
	}

	return c, nil
}

// ByKey is one translation.
func (b *Business) ByKey(key string) (Translation, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	t, ok := b.byKey[key]

	return t, ok
}
