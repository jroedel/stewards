package apiapp

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jroedel/stewards/app/sdk/mid"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Translations is what the API needs from the translation memory.
//
// Claude does all the translating, both ways between English and Spanish,
// and what it sends is on every screen at once: the stewards decided on
// 2026-10-07 to trust Claude's translations and check them when they choose,
// as they decided a day earlier for listings. What keeps it honest is the
// same: the steward's own key, the log line each batch writes, and the rule
// in translationbus that a translation a steward has checked is not changed
// from here.
type Translations interface {
	Waiting(ctx context.Context, limit int) (translationbus.Waiting, error)
	Translate(ctx context.Context, by translationbus.Origin, uploads []translationbus.Upload) ([]translationbus.Result, error)
	List(ctx context.Context, f translationbus.Filter) ([]translationbus.Listed, error)
}

// How many a pending list gives by default, and at most: a batch Claude can
// translate in one answer, with room to think about the names.
const (
	pendingDefault = 50
	pendingMost    = 200
	uploadMost     = 200
)

func (a app) translationEndpoints() []Endpoint {
	return []Endpoint{
		{
			Method: http.MethodGet, Path: Prefix + "/translations/pending", Tool: "list_pending_translations", NeedsKey: true,
			Query:   []Field{{Name: "limit", Type: "integer", Description: fmt.Sprintf("How many to give, %d by default and %d at most.", pendingDefault, pendingMost)}},
			Summary: "The words people have written in the app that have no translation yet: place names and notes, plant names, flower colours and notes, the notes on plants listed at places, and work days. Names come first. Each is in the language it was written in, which written_in guesses from the page it was typed on; say which it really is when you translate it. The glossary is every name already translated, to use the same words again.",
			Returns: `{"pending": [{key, text, written_in: "en" or "es", where: [string], name}], "remaining": n, "glossary": [{en, es}]}. remaining counts the ones given too; ask again until it is 0.`,
			handler: a.pendingTranslations,
		},
		{
			Method: http.MethodPut, Path: Prefix + "/translations", Tool: "put_translations", NeedsKey: true,
			Summary: "Translate: send each original's translation into the other language. Each is on every screen at once, for a steward to check when they choose. One a steward has checked is refused unless sent unchanged; tell the steward instead. One refused does not stop the rest. Sending the same translation again changes nothing.",
			Body: &Body{Encoding: "json", Fields: []Field{
				{Name: "translations", Type: "array of object {key, from, text}", Required: true, Description: fmt.Sprintf(`At most %d. key: the original's, from the pending list. from: the language the original is in, "en" or "es" -- not always written_in. text: the translation, in the other language, with every {placeholder} kept as it is.`, uploadMost)},
			}},
			Returns: `200 {"results": [{key, outcome: "created", "updated", "unchanged" or "refused", field, problem}]}, in the order sent.`,
			handler: a.putTranslations,
		},
		{
			Method: http.MethodGet, Path: Prefix + "/translations", Tool: "list_translations", NeedsKey: true,
			Query: []Field{
				{Name: "checked", Type: "string", Values: []string{"yes", "no"}, Description: "yes for those a steward has checked, no for those not yet; leave it out for all."},
				{Name: "limit", Type: "integer", Description: "How many to give, 100 by default and 500 at most."},
				{Name: "offset", Type: "integer", Description: "How many to skip, for the next page."},
			},
			Summary: "The translations already made, the most recently changed first, with where each original appears. For revising some when the steward asks, not for translating what is waiting: list_pending_translations is that.",
			Returns: `{"translations": [{key, text, from, translation, by: "claude", "steward" or "given", checked, where: [string], name, in_use, updated_at}], "total": n}`,
			handler: a.listTranslations,
		},
	}
}

// PendingJSON is an original waiting for its translation.
type PendingJSON struct {
	Key       string   `json:"key"`
	Text      string   `json:"text"`
	WrittenIn string   `json:"written_in"`
	Where     []string `json:"where"`
	Name      bool     `json:"name,omitempty"`
}

func (a app) pendingTranslations(w http.ResponseWriter, r *http.Request) {
	limit, ok := a.number(w, r, "limit", pendingDefault, 1, pendingMost)
	if !ok {
		return
	}

	got, err := a.translations.Waiting(r.Context(), limit)
	if err != nil {
		a.fail(w, r, "listing what is waiting for a translation", err)

		return
	}

	pending := make([]PendingJSON, 0, len(got.Pending))
	for _, p := range got.Pending {
		pending = append(pending, PendingJSON{Key: p.Key, Text: p.Source, WrittenIn: string(p.Guess), Where: p.Where, Name: p.Name})
	}

	glossary := make([]TextJSON, 0, len(got.Glossary))
	for _, t := range got.Glossary {
		glossary = append(glossary, textOf(t))
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"pending": pending, "remaining": got.Remaining, "glossary": glossary})
}

// TranslationIn is one translation in a PUT.
type TranslationIn struct {
	Key  string `json:"key"`
	From string `json:"from"`
	Text string `json:"text"`
}

// ResultJSON is what became of one.
type ResultJSON struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
	Field   string `json:"field,omitempty"`
	Problem string `json:"problem,omitempty"`
}

func (a app) putTranslations(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Translations []TranslationIn `json:"translations"`
	}

	if err := web.ReadJSON(r, &in); err != nil {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("", page.Sentence(err.Error())))

		return
	}

	switch {
	case len(in.Translations) == 0:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("translations", "Send at least one translation, as {key, from, text}."))

		return
	case len(in.Translations) > uploadMost:
		web.WriteJSON(w, http.StatusUnprocessableEntity, web.Problem("translations", fmt.Sprintf("Send at most %d at a time.", uploadMost)))

		return
	}

	// A language that is not one is the rules' to refuse, one by one, with
	// the rest of the batch kept; so it is passed through as it came.
	uploads := make([]translationbus.Upload, 0, len(in.Translations))
	for _, t := range in.Translations {
		from, err := types.ParseLang(t.From)
		if err != nil {
			from = types.Lang(t.From)
		}

		uploads = append(uploads, translationbus.Upload{Key: t.Key, From: from, Translated: t.Text})
	}

	results, err := a.translations.Translate(r.Context(), translationbus.ByClaude, uploads)
	if err != nil {
		a.fail(w, r, "keeping translations", err)

		return
	}

	out := make([]ResultJSON, 0, len(results))
	counts := map[translationbus.Outcome]int{}

	for _, res := range results {
		counts[res.Outcome]++

		problem := ""
		if res.Problem != "" {
			problem = page.Sentence(res.Problem)
		}

		out = append(out, ResultJSON{Key: res.Key, Outcome: string(res.Outcome), Field: res.Field, Problem: problem})
	}

	steward, _ := mid.StewardFrom(r.Context())
	a.log.InfoContext(r.Context(), "translations kept",
		"created", counts[translationbus.Created], "updated", counts[translationbus.Updated],
		"unchanged", counts[translationbus.Unchanged], "refused", counts[translationbus.Refused],
		"user_id", steward.ID.String())

	web.WriteJSON(w, http.StatusOK, map[string]any{"results": out})
}

// TranslationJSON is a translation as the API shows it.
type TranslationJSON struct {
	Key         string    `json:"key"`
	Text        string    `json:"text"`
	From        string    `json:"from"`
	Translation string    `json:"translation"`
	By          string    `json:"by"`
	Checked     bool      `json:"checked"`
	Where       []string  `json:"where"`
	Name        bool      `json:"name,omitempty"`
	InUse       bool      `json:"in_use"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (a app) listTranslations(w http.ResponseWriter, r *http.Request) {
	checked := r.URL.Query().Get("checked")
	if checked != "" && checked != "yes" && checked != "no" {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem("checked", "Use checked=yes or checked=no, or leave it out for all."))

		return
	}

	limit, ok := a.number(w, r, "limit", 100, 1, 500)
	if !ok {
		return
	}

	offset, ok := a.number(w, r, "offset", 0, 0, 1<<30)
	if !ok {
		return
	}

	all, err := a.translations.List(r.Context(), translationbus.Filter{Checked: checked})
	if err != nil {
		a.fail(w, r, "listing translations", err)

		return
	}

	shown := all[min(offset, len(all)):]
	shown = shown[:min(limit, len(shown))]

	out := make([]TranslationJSON, 0, len(shown))
	for _, t := range shown {
		where := t.Where
		if where == nil {
			where = []string{}
		}

		out = append(out, TranslationJSON{
			Key: t.Key, Text: t.Source, From: string(t.From), Translation: t.Translated, By: string(t.By),
			Checked: t.Checked, Where: where, Name: t.Name, InUse: t.InUse, UpdatedAt: t.UpdatedAt,
		})
	}

	web.WriteJSON(w, http.StatusOK, map[string]any{"translations": out, "total": len(all)})
}

// number reads a whole number from the query, def when it is absent, and
// answers 400 itself when it is not one or is out of range.
func (a app) number(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int) (int, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, true
	}

	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		web.WriteJSON(w, http.StatusBadRequest, web.Problem(name, fmt.Sprintf("Give %s as a whole number from %d to %d.", name, lo, hi)))

		return 0, false
	}

	return n, true
}
