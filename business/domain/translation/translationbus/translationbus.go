// Package translationbus is the rules about translations: each piece of text
// a person wrote, in the other of the app's two languages.
//
// # One memory, keyed by what was written
//
// A translation belongs to the text, not to the record the text sits in. The
// memory holds one row per original -- "Full sun, dry by July" -- and every
// place, plant, listing and work day that says those words is shown the same
// translation. That is what makes the three things the stewards asked for
// cheap:
//
//   - Changed words are noticed by themselves. When a steward edits a note,
//     the new words have no row yet, so the note is shown in the language it
//     was written in until Claude has translated it. A volunteer never reads
//     a translation of what the note used to say.
//   - The same sentence is translated once, wherever it appears.
//   - What is waiting for Claude is every original in use that has no row.
//
// The alternative was a Spanish column beside each English one, which is
// what there was before. It needs a second column per field to know which
// English its Spanish was made from, and a review screen that edits every
// domain's rows; and it cannot see that two places say the same thing.
//
// # Either language can be the original
//
// A person writes in whichever language they think in. Where it is stored
// -- the English half of a types.Text or the Spanish one -- is the language
// the writer's page was in, which is a guess; Claude is to say which
// language it really is when it translates, and From records that. Fill reads the memory
// in both directions, so a note typed in English on the Spanish page comes
// out the right way round once Claude has seen it.
//
// # Kept in memory
//
// Every row is held in a map as well as in the store, loaded once by
// NewBusiness, because Fill runs on every place, plant, listing and work day
// read -- three fields of every plant on the plant list -- and a query for
// each would be thousands of them for one page. The memory is a few hundred
// rows of short text; the host's limit (300 MB) is not near. This process is
// the only writer, so the map cannot go stale behind it.
package translationbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/jroedel/stewards/business/types"
)

// Origin is who wrote a translation.
type Origin string

const (
	// Given is a translation that arrived beside its original: typed in the
	// other language's box of a steward's form, sent as the other half of a
	// record through the API, or there before this memory was. Who wrote it
	// is not known, which is why it has a name of its own.
	Given Origin = "given"

	// ByClaude is one Claude made through the translations API.
	ByClaude Origin = "claude"

	// BySteward is one a steward wrote or changed on the review screen.
	BySteward Origin = "steward"
)

// Translation is one original and its translation.
type Translation struct {
	// Key is Source's address: the API names a translation by it, so that a
	// paragraph does not have to travel in a URL.
	Key string

	// Source is the text as a person wrote it.
	Source string

	// From is the language Source is in.
	From types.Lang

	// Translated is Source in the other language.
	Translated string

	By Origin

	// Checked says a steward has read it and agreed. A translation is shown
	// to volunteers whether or not it is checked: the stewards decided on
	// 2026-10-07 to trust Claude's, and to check them when they choose.
	Checked bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Pair is the translation as both halves of a types.Text, the right way
// round for the language the original is in.
func (t Translation) Pair() types.Text {
	if t.From == types.Spanish {
		return types.Text{EN: t.Translated, ES: t.Source}
	}

	return types.Text{EN: t.Source, ES: t.Translated}
}

// Key is the address of an original.
//
// Sixteen hex characters of its SHA-256: short enough for a URL, and at a few
// thousand originals a collision is not a thing that happens.
func Key(source string) string {
	sum := sha256.Sum256([]byte(source))

	return hex.EncodeToString(sum[:8])
}

// Storer is what the rules need from storage.
type Storer interface {
	// All is every translation, for NewBusiness to hold.
	All(ctx context.Context) ([]Translation, error)

	// Put inserts or replaces the translation with t's key.
	Put(ctx context.Context, t Translation) error

	// Add inserts t unless its key is already there, and reports whether it
	// did -- one statement, so a translation already there is never
	// replaced by an older one.
	Add(ctx context.Context, t Translation) (bool, error)
}

// Business applies the rules and then asks the store.
type Business struct {
	store Storer
	now   func() time.Time

	mu    sync.RWMutex
	byKey map[string]Translation
}

// NewBusiness constructs one and loads every translation into memory. nil now
// means the wall clock.
func NewBusiness(ctx context.Context, store Storer, now func() time.Time) (*Business, error) {
	if now == nil {
		now = time.Now
	}

	all, err := store.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the translations: %w", err)
	}

	b := &Business{store: store, now: now, byKey: make(map[string]Translation, len(all))}
	for _, t := range all {
		b.byKey[t.Key] = t
	}

	return b, nil
}

// Lookup is the translation of source, if there is one.
func (b *Business) Lookup(source string) (Translation, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	t, ok := b.byKey[Key(source)]

	return t, ok
}

// Fill is t with its translation from the memory, as a page shows it.
//
// The original is whichever half is written. When both are -- a record saved
// before this memory existed, which MoveTranslations has not reached -- it
// is the English, as it always was then. With no translation in the memory,
// t comes back as it is: the original alone, which a page shows marked with
// its language, or both halves of an old record.
func (b *Business) Fill(t types.Text) types.Text {
	source := t.EN
	if source == "" {
		source = t.ES
	}

	if source == "" {
		return t
	}

	if tr, ok := b.Lookup(source); ok {
		return tr.Pair()
	}

	return t
}

// Keep is what to store for t, a text a person has just saved, and it puts
// any translation that came with it into the memory. before is the text as
// it was shown to them, filled; the zero Text for something new.
//
// What is stored is the original alone. It is the English when there is
// English: while the forms still have a box for each language, the English
// box is the one a steward is asked to fill, and the Spanish one is the
// translation.
//
// A translation that arrives unchanged beside a changed original is not
// kept: a steward who corrects the English and leaves the Spanish box as it
// was has not translated the new words, and keeping the old Spanish would
// show volunteers a translation of what the note used to say. The new words
// wait for Claude instead.
//
// Leaving the other box empty takes nothing out of the memory, because the
// translation belongs to the words and not to this record: another place may
// say the same thing.
func (b *Business) Keep(ctx context.Context, t, before types.Text) (types.Text, error) {
	var (
		stored types.Text
		source string
		from   types.Lang
		other  string
	)

	switch {
	case t.EN != "":
		stored, source, from, other = types.Text{EN: t.EN}, t.EN, types.English, t.ES
	case t.ES != "":
		stored, source, from = types.Text{ES: t.ES}, t.ES, types.Spanish
	default:
		return types.Text{}, nil
	}

	if other == "" || (other == before.ES && t.EN != before.EN) {
		return stored, nil
	}

	if tr, ok := b.Lookup(source); ok && tr.Pair() == t {
		return stored, nil
	}

	now := b.now()
	tr := Translation{
		Key: Key(source), Source: source, From: from, Translated: other, By: Given,
		CreatedAt: now, UpdatedAt: now,
	}

	if old, ok := b.Lookup(source); ok {
		tr.CreatedAt = old.CreatedAt
	}

	if err := b.store.Put(ctx, tr); err != nil {
		return types.Text{}, fmt.Errorf("keeping the translation: %w", err)
	}

	b.remember(tr)

	return stored, nil
}

// Move puts the Spanish half of an old record's text into the memory, unless
// the memory already has a translation of its English, and reports what the
// record should now store: the English alone.
//
// This is for MoveTranslations in each domain, which runs at every startup
// over records written before the memory existed (and by a binary rolled
// back to before it). A translation already in the memory wins over the old
// column, because it may be a steward's correction of it.
func (b *Business) Move(ctx context.Context, t types.Text) (types.Text, error) {
	if t.EN == "" || t.ES == "" {
		return t, nil
	}

	now := b.now()
	tr := Translation{
		Key: Key(t.EN), Source: t.EN, From: types.English, Translated: t.ES, By: Given,
		CreatedAt: now, UpdatedAt: now,
	}

	added, err := b.store.Add(ctx, tr)
	if err != nil {
		return types.Text{}, fmt.Errorf("moving a translation: %w", err)
	}

	if added {
		b.remember(tr)
	}

	return types.Text{EN: t.EN}, nil
}

func (b *Business) remember(t Translation) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.byKey[t.Key] = t
}

// ------------------------------------------------------------------ for the domains

// Memory is what a domain's rules need from the translation memory. A
// *Business is one; so is None.
type Memory interface {
	Fill(t types.Text) types.Text
	Keep(ctx context.Context, t, before types.Text) (types.Text, error)
	Move(ctx context.Context, t types.Text) (types.Text, error)
}

var _ Memory = (*Business)(nil)

// None is a memory with nothing in it that keeps nothing: each text is
// stored and shown exactly as it was saved, which is how the app worked
// before the memory. For tests of a domain that are not about translation.
type None struct{}

func (None) Fill(t types.Text) types.Text { return t }

func (None) Keep(_ context.Context, t, _ types.Text) (types.Text, error) { return t, nil }

func (None) Move(_ context.Context, t types.Text) (types.Text, error) { return t, nil }

// FillAll fills each of texts from m, in place.
func FillAll(m Memory, texts ...*types.Text) {
	for _, t := range texts {
		*t = m.Fill(*t)
	}
}

// KeepAll replaces each of texts with what to store for it, through m.Keep.
// before is the same fields as they were shown, filled, in the same order; nil
// for a record that is new.
func KeepAll(ctx context.Context, m Memory, texts []*types.Text, before []types.Text) error {
	for i, t := range texts {
		var was types.Text
		if before != nil {
			was = before[i]
		}

		kept, err := m.Keep(ctx, *t, was)
		if err != nil {
			return err
		}

		*t = kept
	}

	return nil
}

// MoveAll moves each of texts through m.Move, in place, and reports whether
// any of them changed -- whether the record needs writing back.
func MoveAll(ctx context.Context, m Memory, texts ...*types.Text) (bool, error) {
	changed := false

	for _, t := range texts {
		moved, err := m.Move(ctx, *t)
		if err != nil {
			return false, err
		}

		if moved != *t {
			*t, changed = moved, true
		}
	}

	return changed, nil
}

// Complete fills, from m, each of texts that has one half written and not
// the other, in place; a text with both halves, or neither, is left alone.
//
// It is for an import comparing what a program sent with what is there. A
// program that sends the English alone has said nothing about the Spanish,
// which belongs to the words and stays in the memory; completed, what it
// sent compares equal to the record it already matches, and the answer is
// "unchanged" rather than an update that changes nothing.
func Complete(m Memory, texts ...*types.Text) {
	for _, t := range texts {
		if (t.EN == "") != (t.ES == "") {
			*t = m.Fill(*t)
		}
	}
}
