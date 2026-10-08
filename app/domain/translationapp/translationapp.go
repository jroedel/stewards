// Package translationapp is the stewards' screen for translations: where a
// steward reads what Claude has translated, and agrees, writes a better
// translation, or sends one back to Claude with a note.
//
// Claude does all the translating, both ways between English and Spanish,
// and a translation is on every screen from the moment it is made (the
// stewards' decision of 2026-10-07; design.md, principle 6). So nothing here
// lets a translation through. Checking is a steward's word that it is right,
// and a list of what nobody has read yet; the screen is built for reading
// many quickly, one tap each, with the rarer change and send-back folded
// away under each.
//
// It works without a script: each button is a form, and after a press the
// list comes back at the next translation, by its anchor.
package translationapp

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// Templates are this app's pages, for the renderer.
//
//go:embed templates
var Templates embed.FS

// Path is the list.
const Path = "/steward/translations"

// shownFirst is how many translations a page shows before "Show more": a
// sitting's worth, and a page a phone opens quickly.
const shownFirst = 50

// Translations is what this app needs from the translation memory.
type Translations interface {
	List(ctx context.Context, f translationbus.Filter) ([]translationbus.Listed, error)
	Counts(ctx context.Context) (translationbus.Counts, error)
	Check(ctx context.Context, keys ...string) (int, error)
	Correct(ctx context.Context, key, text string) (translationbus.Translation, error)
	SendBack(ctx context.Context, key, note string) (translationbus.Translation, error)
}

// Config is what this app needs.
type Config struct {
	Log          *slog.Logger
	Render       *page.Renderer
	Translations Translations
}

type app struct{ cfg Config }

// Routes mounts the app, every route behind guard.
func Routes(mux *http.ServeMux, cfg Config, guard web.Middleware) {
	a := app{cfg: cfg}

	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + Path:                   a.list,
		"POST " + Path + "/check":       a.checkAll,
		"POST " + Path + "/{key}/check": a.check,
		"POST " + Path + "/{key}":       a.correct,
		"POST " + Path + "/{key}/back":  a.sendBack,
	} {
		mux.Handle(pattern, guard(h))
	}
}

// ------------------------------------------------------------------ the list

// row is one translation as the screen shows it.
type row struct {
	Key   string
	Where []string

	// Source is the original and Translated its translation, each with the
	// language it is in, for lang= and for the label above it.
	Source, Translated    string
	SourceLang, TransLang types.Lang
	SourceName, TransName types.Text
	By                    types.Text
	Checked               bool
	Note                  string

	// Show is the list the row is on, and Next the key of the row after
	// it: where the list comes back after a press.
	Show, Next string

	// N is how many the page was asked to show, when more than at first,
	// so the list comes back as long as it was.
	N int

	// After a refusal: which form to open, what was typed in it, and what
	// is wrong.
	Open, Typed string
	Problem     types.Text
}

type listView struct {
	Show    string // "check" or "all"
	Rows    []row
	More    int // how many more there are than are shown
	Next    int // how many to ask for to see them
	Counts  translationbus.Counts
	Done    types.Text
	Pending []string // the keys of the shown rows still to check
}

// The words this app says from Go, in English; Claude translates them
// through the translation memory, and say looks them up (page/words.go).
// The rest are in the template, through t.
type wording struct {
	Checked, AllChecked, Saved, SentBack types.Text

	// Who made a translation, after its language: "Spanish, by Claude".
	ByClaude, BySteward, Given types.Text
}

var words = wording{
	Checked:    types.Text{EN: "Checked."},
	AllChecked: types.Text{EN: "All of those are checked."},
	Saved:      types.Text{EN: "Your translation is saved, and on the screens now."},
	SentBack:   types.Text{EN: "Sent back. Claude sees your note the next time it translates what is waiting; the translation stays until then."},

	ByClaude:  types.Text{EN: "by Claude"},
	BySteward: types.Text{EN: "by a steward"},
	Given:     types.Text{EN: "written with the original"},
}

// Words is this app's copy held in Go, for the catalog the translation
// memory lists.
var Words = page.Catalog{{Where: "the stewards' screen for checking translations", Words: words}}

func (a app) list(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, http.StatusOK, nil)
}

// render is the list as the query asks for it; failed, if not nil, puts a
// refusal back on its row, with what the steward typed.
func (a app) render(w http.ResponseWriter, r *http.Request, status int, failed *row) {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = url.Values{"show": {r.PostFormValue("show")}, "n": {r.PostFormValue("n")}}
	}

	v := listView{Show: "check"}
	if q.Get("show") == "all" {
		v.Show = "all"
	}

	f := translationbus.Filter{Checked: "no"}
	if v.Show == "all" {
		f.Checked = ""
	}

	all, err := a.cfg.Translations.List(r.Context(), f)
	if err != nil {
		a.fail(w, r, "listing translations", err)

		return
	}

	// Only words still in use: a translation of words no record says any
	// more is kept for the day one does again, and is nothing to read now.
	var listed []translationbus.Listed
	for _, t := range all {
		if t.InUse {
			listed = append(listed, t)
		}
	}

	n := shownFirst
	if asked, err := strconv.Atoi(q.Get("n")); err == nil && asked > n {
		n = asked
	}

	shown := listed[:min(n, len(listed))]
	if n == shownFirst {
		n = 0
	}
	v.More, v.Next = len(listed)-len(shown), max(n, shownFirst)+shownFirst

	for i, t := range shown {
		rw := rowOf(t)
		rw.Show, rw.N = v.Show, n
		if i+1 < len(shown) {
			rw.Next = shown[i+1].Key
		}

		if failed != nil && failed.Key == rw.Key {
			rw.Open, rw.Typed, rw.Problem = failed.Open, failed.Typed, failed.Problem
		}

		if !rw.Checked {
			v.Pending = append(v.Pending, rw.Key)
		}

		v.Rows = append(v.Rows, rw)
	}

	if v.Counts, err = a.cfg.Translations.Counts(r.Context()); err != nil {
		a.cfg.Log.WarnContext(r.Context(), "counting translations", "request_id", web.RequestIDFrom(r.Context()), "error", err)
	}

	// A fixed sentence chosen by a word, never the query echoed.
	switch q.Get("done") {
	case "checked":
		v.Done = words.Checked
	case "all":
		v.Done = words.AllChecked
	case "saved":
		v.Done = words.Saved
	case "back":
		v.Done = words.SentBack
	}

	a.cfg.Render.Render(w, r, status, "steward-translations", v)
}

func rowOf(t translationbus.Listed) row {
	other := types.Spanish
	if t.From == types.Spanish {
		other = types.English
	}

	return row{
		Key: t.Key, Where: t.Where,
		Source: t.Source, Translated: t.Translated,
		SourceLang: t.From, TransLang: other,
		SourceName: page.Language(t.From), TransName: page.Language(other),
		By: byWords(t.By), Checked: t.Checked, Note: t.Note,
	}
}

func byWords(o translationbus.Origin) types.Text {
	switch o {
	case translationbus.ByClaude:
		return words.ByClaude
	case translationbus.BySteward:
		return words.BySteward
	}

	return words.Given
}

// ------------------------------------------------------------------ the buttons

// key is the translation a route names. Sixteen hex characters, as
// translationbus.Key makes them; anything else is no translation.
var key = regexp.MustCompile(`^[0-9a-f]{16}$`)

// back is where a press returns to: the same list, at the next row.
func back(r *http.Request, done string) string {
	q := url.Values{"done": {done}}
	if show := r.PostFormValue("show"); show == "all" {
		q.Set("show", show)
	}

	if n := r.PostFormValue("n"); n != "" {
		if _, err := strconv.Atoi(n); err == nil {
			q.Set("n", n)
		}
	}

	to := Path + "?" + q.Encode()
	if next := r.PostFormValue("next"); key.MatchString(next) {
		to += "#t-" + next
	}

	return to
}

func (a app) check(w http.ResponseWriter, r *http.Request) {
	k, ok := a.readKey(w, r)
	if !ok {
		return
	}

	if _, err := a.cfg.Translations.Check(r.Context(), k); err != nil {
		a.fail(w, r, "checking a translation", err)

		return
	}

	http.Redirect(w, r, back(r, "checked"), http.StatusSeeOther)
}

// checkAll checks every translation the page showed unchecked. The keys
// come from the page rather than from the memory, so a translation that
// arrived after the page was opened is not checked unread.
func (a app) checkAll(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return
	}

	var keys []string
	for _, k := range r.PostForm["key"] {
		if key.MatchString(k) {
			keys = append(keys, k)
		}
	}

	if _, err := a.cfg.Translations.Check(r.Context(), keys...); err != nil {
		a.fail(w, r, "checking translations", err)

		return
	}

	http.Redirect(w, r, back(r, "all"), http.StatusSeeOther)
}

func (a app) correct(w http.ResponseWriter, r *http.Request) {
	k, ok := a.readKey(w, r)
	if !ok {
		return
	}

	text := r.PostFormValue("text")

	_, err := a.cfg.Translations.Correct(r.Context(), k, text)
	if a.refused(w, r, err, row{Key: k, Open: "change", Typed: text}) {
		return
	}

	http.Redirect(w, r, back(r, "saved"), http.StatusSeeOther)
}

func (a app) sendBack(w http.ResponseWriter, r *http.Request) {
	k, ok := a.readKey(w, r)
	if !ok {
		return
	}

	note := r.PostFormValue("note")

	_, err := a.cfg.Translations.SendBack(r.Context(), k, note)
	if a.refused(w, r, err, row{Key: k, Open: "back", Typed: note}) {
		return
	}

	http.Redirect(w, r, back(r, "back"), http.StatusSeeOther)
}

// refused answers a press that did not work, and reports whether it did: a
// rule's refusal goes back on the translation's row with what was typed; a
// translation gone is the list again, without it.
func (a app) refused(w http.ResponseWriter, r *http.Request, err error, failed row) bool {
	invalid, isInvalid := errors.AsType[translationbus.Invalid](err)

	switch {
	case err == nil:
		return false
	case isInvalid:
		failed.Problem = types.Text{EN: page.Sentence(invalid.Problem)}
		a.render(w, r, http.StatusUnprocessableEntity, &failed)
	case errors.Is(err, translationbus.ErrNotFound):
		http.Redirect(w, r, back(r, ""), http.StatusSeeOther)
	default:
		a.fail(w, r, "changing a translation", err)
	}

	return true
}

func (a app) readKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "We could not read that. Open the page again and try once more.", http.StatusBadRequest)

		return "", false
	}

	k := r.PathValue("key")
	if !key.MatchString(k) {
		http.NotFound(w, r)

		return "", false
	}

	return k, true
}

func (a app) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.cfg.Log.ErrorContext(r.Context(), what, "request_id", web.RequestIDFrom(r.Context()), "error", err)
	http.Error(w, "Something went wrong on our end. Try again in a few minutes.", http.StatusInternalServerError)
}
