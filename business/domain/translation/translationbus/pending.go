package translationbus

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// ------------------------------------------------------------------ where the words are

// Source is one piece of text a person wrote, as a record holds it.
type Source struct {
	// Text is as stored: the original alone, in the half of the language
	// the writer's page was in.
	Text types.Text

	// Where says where a reader meets it, for whoever translates it: "Rain
	// garden: its conditions, for a planter". The words around a sentence
	// are what decide many a translation -- whether "it" is a plant or a
	// bed, and which.
	Where string

	// Name is set for the name of a plant or a place. A plant's Spanish
	// name is an established one from a source or none at all, never a
	// translation of the English; and names are the words the glossary
	// keeps the same from one batch to the next.
	Name bool
}

// Origins is a domain whose records hold words to translate: places,
// plants, listings and work days.
type Origins interface {
	Originals(ctx context.Context) ([]Source, error)
}

// ReadFrom adds to where the originals are: main, for the domains -- which
// are made with this memory, so they cannot be handed to NewBusiness -- and
// the muxer, for the screens' own words. Called before serving, never after.
func (b *Business) ReadFrom(origins ...Origins) {
	b.origins = append(b.origins, origins...)
}

// in is one original in use: its words, the language they are stored as,
// and every place they appear.
type in struct {
	source string
	guess  types.Lang
	where  []string
	name   bool
}

// inUse is every original the domains hold, once each by key, in the order
// they were first met. A text stored with both halves -- a record from
// before the memory that MoveTranslations has not reached -- is translated
// already and is not an original waiting for anything.
func (b *Business) inUse(ctx context.Context) (map[string]*in, []string, error) {
	byKey := map[string]*in{}
	var order []string

	for _, o := range b.origins {
		sources, err := o.Originals(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("reading the words to translate: %w", err)
		}

		for _, s := range sources {
			var (
				source string
				guess  types.Lang
			)

			switch {
			case s.Text.EN != "" && s.Text.ES != "":
				continue
			case s.Text.EN != "":
				source, guess = s.Text.EN, types.English
			case s.Text.ES != "":
				source, guess = s.Text.ES, types.Spanish
			default:
				continue
			}

			key := Key(source)

			got, ok := byKey[key]
			if !ok {
				got = &in{source: source, guess: guess}
				byKey[key] = got
				order = append(order, key)
			}

			got.name = got.name || s.Name
			if len(got.where) < maxWhere && !slices.Contains(got.where, s.Where) {
				got.where = append(got.where, s.Where)
			}
		}
	}

	return byKey, order, nil
}

// maxWhere is how many places a text is said to appear: enough to see what
// it is about, and a sentence used on fifty cards does not need fifty.
const maxWhere = 5

// ------------------------------------------------------------------ what is waiting

// Pending is an original with no translation.
type Pending struct {
	Key    string
	Source string

	// Guess is the language it is stored as, which is the language the
	// writer's page was in. The translator says which it really is.
	Guess types.Lang

	Where []string
	Name  bool

	// Current and Note are set for a translation a steward sent back: the
	// translation there now, and what the steward said is wrong with it.
	Current string
	Note    string
}

// Waiting is what Claude is asked to translate.
type Waiting struct {
	// Pending is the first of what is waiting: what a steward sent back,
	// since they asked; then names, so a batch's sentences can use the names
	// the batch before settled; then in the order the domains list them.
	Pending []Pending

	// Remaining is how many are waiting, Pending included.
	Remaining int

	// Glossary is every name in use that has its translation, so a batch
	// says "Jardín de lluvia" where the last one did.
	Glossary []types.Text
}

// Waiting is the first limit originals that have no translation, and the
// glossary to translate them with.
func (b *Business) Waiting(ctx context.Context, limit int) (Waiting, error) {
	byKey, order, err := b.inUse(ctx)
	if err != nil {
		return Waiting{}, err
	}

	var (
		pending  []Pending
		glossary []types.Text
	)

	for _, key := range order {
		u := byKey[key]

		p := Pending{Key: key, Source: u.source, Guess: u.guess, Where: u.where, Name: u.name}

		if tr, ok := b.Lookup(u.source); ok {
			if tr.Note == "" {
				if u.name {
					glossary = append(glossary, tr.Pair())
				}

				continue
			}

			p.Guess, p.Current, p.Note = tr.From, tr.Translated, tr.Note
		}

		pending = append(pending, p)
	}

	slices.SortStableFunc(pending, func(x, y Pending) int {
		rank := func(p Pending) int {
			switch {
			case p.Note != "":
				return 0
			case p.Name:
				return 1
			}

			return 2
		}

		return cmp.Compare(rank(x), rank(y))
	})

	slices.SortFunc(glossary, func(x, y types.Text) int {
		return cmp.Compare(strings.ToLower(x.EN), strings.ToLower(y.EN))
	})

	w := Waiting{Remaining: len(pending), Glossary: glossary}
	w.Pending = pending[:min(limit, len(pending))]

	return w, nil
}

// ------------------------------------------------------------------ translating

// Upload is one translation, as Claude sends it.
type Upload struct {
	// Key is the original's, from the pending list.
	Key string

	// From is the language the original is in -- which may not be the
	// language it was stored as. Translated is in the other one.
	From       types.Lang
	Translated string
}

// Outcome is what an upload did.
type Outcome string

const (
	Created   Outcome = "created"
	Updated   Outcome = "updated"
	Unchanged Outcome = "unchanged"
	Refused   Outcome = "refused"
)

// Result is what became of one upload. Field and Problem say why, when it
// was refused, in a sentence that says what to do about it.
type Result struct {
	Key     string
	Outcome Outcome
	Field   string
	Problem string
}

// maxTranslation is the longest translation taken: twice the longest note
// any domain allows, since a translation can run longer than its original.
const maxTranslation = 4000

// placeholder is a {name} in text that the app fills in as it shows it. It
// must come through a translation exactly as it went in.
var placeholder = regexp.MustCompile(`\{[a-z_]+\}`)

// Translate keeps each upload, as by, and says what became of each. A
// refusal of one does not stop the rest: a batch of fifty with one bad key
// keeps forty-nine. The error is for the store failing, not for an upload.
//
// A translation is shown on every screen at once, unchecked; a steward
// checks it when they choose. One a steward has checked is refused, unless
// it is sent unchanged: a correction made by a person is not undone by the
// next batch, and is changed on the translations screen if it needs to be.
func (b *Business) Translate(ctx context.Context, by Origin, uploads []Upload) ([]Result, error) {
	byKey, _, err := b.inUse(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(uploads))

	for _, u := range uploads {
		r, err := b.translate(ctx, by, u, byKey)
		if err != nil {
			return nil, err
		}

		results = append(results, r)
	}

	return results, nil
}

func (b *Business) translate(ctx context.Context, by Origin, u Upload, inUse map[string]*in) (Result, error) {
	u.Translated = strings.TrimSpace(u.Translated)

	refuse := func(field, problem string) (Result, error) {
		return Result{Key: u.Key, Outcome: Refused, Field: field, Problem: problem}, nil
	}

	old, known := b.ByKey(u.Key)

	source := old.Source
	if !known {
		got, ok := inUse[u.Key]
		if !ok {
			return refuse("key", "no text in the app has that key. Ask for the pending list again: the words may have been changed since")
		}

		source = got.source
	}

	switch {
	case u.From != types.English && u.From != types.Spanish:
		return refuse("from", "say which language the original is in: en or es")
	case u.Translated == "":
		return refuse("text", "the translation is empty")
	case utf8.RuneCountInString(u.Translated) > maxTranslation:
		return refuse("text", fmt.Sprintf("the translation is longer than %d characters", maxTranslation))
	}

	if want, got := placeholders(source), placeholders(u.Translated); !slices.Equal(want, got) {
		return refuse("text", fmt.Sprintf("keep each of %s exactly as it is, untranslated: the app fills it in", strings.Join(want, " ")))
	}

	// The same again is unchanged, unless a steward sent it back: then it
	// is Claude's answer that it was right, and the note is answered.
	if known && old.From == u.From && old.Translated == u.Translated && old.Note == "" {
		return Result{Key: u.Key, Outcome: Unchanged}, nil
	}

	if known && old.Checked {
		return refuse("key", "a steward has checked this translation, so it is not changed through the API. Tell the steward what you would change; they change it on the translations screen")
	}

	now := b.now()
	t := Translation{
		Key: u.Key, Source: source, From: u.From, Translated: u.Translated, By: by,
		CreatedAt: now, UpdatedAt: now,
	}

	outcome := Created
	if known {
		t.CreatedAt, outcome = old.CreatedAt, Updated
	}

	if err := b.store.Put(ctx, t); err != nil {
		return Result{}, fmt.Errorf("keeping a translation: %w", err)
	}

	b.remember(t)

	return Result{Key: u.Key, Outcome: outcome}, nil
}

// placeholders is every {name} in s, sorted, once each.
func placeholders(s string) []string {
	found := placeholder.FindAllString(s, -1)
	slices.Sort(found)

	return slices.Compact(found)
}

// ------------------------------------------------------------------ what there is

// Listed is a translation in the memory, with where its original appears.
type Listed struct {
	Translation

	// Where is as for Pending; empty for words no record says any more,
	// which are kept in case one says them again.
	Where []string
	Name  bool
	InUse bool
}

// Filter is which translations List gives.
type Filter struct {
	// Checked: "" for all, "yes" or "no".
	Checked string
}

// List is every translation the filter allows, the most recently changed
// first, so that a batch just made is at the top.
func (b *Business) List(ctx context.Context, f Filter) ([]Listed, error) {
	byKey, _, err := b.inUse(ctx)
	if err != nil {
		return nil, err
	}

	b.mu.RLock()
	all := make([]Listed, 0, len(b.byKey))
	for _, t := range b.byKey {
		if (f.Checked == "yes" && !t.Checked) || (f.Checked == "no" && t.Checked) {
			continue
		}

		l := Listed{Translation: t}
		if u, ok := byKey[t.Key]; ok {
			l.Where, l.Name, l.InUse = u.where, u.name, true
		}

		all = append(all, l)
	}
	b.mu.RUnlock()

	slices.SortFunc(all, func(x, y Listed) int {
		return cmp.Or(y.UpdatedAt.Compare(x.UpdatedAt), cmp.Compare(x.Key, y.Key))
	})

	return all, nil
}
