package page

import (
	"cmp"
	"context"
	"fmt"
	"html/template"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/domain/translation/translationbus"
	"github.com/jroedel/stewards/business/types"
)

// This file is how the screens' own words reach the page in Spanish: the
// headings, the buttons, the help under a field -- everything the app says,
// as against what a steward wrote.
//
// They are written in English, in the code, as a types.Text beside the
// screen that says them. Claude translates them through the same memory as
// a steward's words (translationbus): the Catalog below is how they are
// listed as waiting, and say looks each one up as the page is written. So a
// heading is translated once, the day Claude next translates what is
// waiting, and is on every screen from then on, with no deploy.
//
// Looked up at render time rather than when the copy is declared, because
// the copy is a package variable made before the memory is read, and a
// translation made while the app runs has to reach the next page anyway.

// Translations is what the renderer needs from the translation memory.
type Translations interface {
	Fill(t types.Text) types.Text
}

// Translate has the pages look up the screens' own words in the memory.
// Without it, a page says each in English, marked as English on a Spanish
// page; a renderer in a test of a page's shape needs nothing more.
//
// Called while the routes are built, before anything is served.
func (rn *Renderer) Translate(words Translations) { rn.words = words }

func (rn *Renderer) fill(t types.Text) types.Text {
	if rn == nil || rn.words == nil {
		return t
	}

	return rn.words.Fill(t)
}

// ------------------------------------------------------------------ phrases

// Phrase is copy with something put into it: "{count} to plant", "{month}
// {year}". The words are translated as written, with their {placeholders},
// and what goes into them is put in afterwards, in the page's language.
//
// The alternative, gluing translated pieces together in the code -- count +
// " " + "to plant" -- fixes the English word order on every language, and
// Spanish dates are where that shows first: "October 10" is "10 de octubre".
// The memory refuses a translation that drops or renames a placeholder, so
// what is put in always has somewhere to go.
type Phrase struct {
	Text types.Text
	args []arg
}

type arg struct {
	name  string
	value any
}

// Put is t with each {name} replaced by the value after it: Put(t, "count",
// 4, "place", name). A value is a string, a number, a types.Text -- itself
// translated -- a Phrase or a List. A placeholder with nothing given for it
// is left showing, which a test catches sooner than a blank would.
func Put(t types.Text, pairs ...any) Phrase {
	p := Phrase{Text: t}

	for i := 0; i+1 < len(pairs); i += 2 {
		name, _ := pairs[i].(string)
		p.args = append(p.args, arg{name: name, value: pairs[i+1]})
	}

	return p
}

// In is the phrase in l without the memory, for words that never reach a
// page through say: a steward screen's English, a log line.
func (p Phrase) In(l types.Lang) string {
	s, _, _ := compose(nil, l, p, false)
	return s
}

// List is several pieces said one after another, with Sep between them:
// "4 to plant · 1 to pull", "March to May, September".
type List struct {
	Items []any
	Sep   string
}

// ------------------------------------------------------------------ saying it

// say is the template's say: v in the page's language, escaped, and marked
// with its own lang= where it is in the other one.
func (rn *Renderer) say(l types.Lang, v any) (template.HTML, error) {
	s, _, err := compose(rn, l, v, true)
	return template.HTML(s), err
}

// plain is v as plain text, for where markup cannot go: an alt=, a title.
// html/template escapes it there.
func (rn *Renderer) plain(l types.Lang, v any) (string, error) {
	s, _, err := compose(rn, l, v, false)
	return s, err
}

// initial is the first letter of a word, as a capital: a month's on the
// bloom strip, J for January and E for enero. From the translation, so a
// language's letters come with its words.
func (rn *Renderer) initial(l types.Lang, t types.Text) string {
	r, _ := utf8.DecodeRuneInString(rn.fill(t).In(l))
	if r == utf8.RuneError {
		return ""
	}

	return string(unicode.ToUpper(r))
}

// compose writes v in l: as escaped HTML, with a span round anything shown
// in the other language, or as plain text. It reports the language the
// outermost words were in.
//
// What is put into a phrase is in the page's language whatever the phrase's
// own words are in: a place's Spanish name stays Spanish inside a sentence
// that waits for its translation, each part marked as what it is.
func compose(rn *Renderer, l types.Lang, v any, html bool) (string, types.Lang, error) {
	esc := func(s string) string {
		if html {
			return template.HTMLEscapeString(s)
		}

		return s
	}

	mark := func(s string, shown types.Lang) string {
		if html && shown != l {
			return `<span lang="` + string(shown) + `">` + s + `</span>`
		}

		return s
	}

	switch v := v.(type) {
	case types.Text:
		t := rn.fill(v)

		return mark(esc(t.In(l)), t.Shown(l)), t.Shown(l), nil

	case Phrase:
		t := rn.fill(v.Text)
		s := esc(t.In(l))

		for _, a := range v.args {
			put, _, err := compose(rn, l, a.value, html)
			if err != nil {
				return "", l, err
			}

			s = strings.ReplaceAll(s, "{"+a.name+"}", put)
		}

		return mark(s, t.Shown(l)), t.Shown(l), nil

	case List:
		parts := make([]string, 0, len(v.Items))

		for _, item := range v.Items {
			s, _, err := compose(rn, l, item, html)
			if err != nil {
				return "", l, err
			}

			parts = append(parts, s)
		}

		return strings.Join(parts, esc(v.Sep)), l, nil

	case string:
		return esc(v), l, nil
	case int:
		return strconv.Itoa(v), l, nil
	case nil:
		return "", l, nil
	}

	return "", l, fmt.Errorf("say was given a %T, which is not words", v)
}

// ------------------------------------------------------------------ the catalog

// Copy is one screen's own words, for the catalog: Where says where a reader
// meets them, for whoever translates them, and Words holds them -- a struct
// whose fields are types.Text, or maps, slices and structs of them.
type Copy struct {
	Where string
	Words any
}

// Catalog is the screens' own words, as the translation memory lists what is
// waiting. Each app says what is its own; the muxer gathers them.
type Catalog []Copy

// Originals is every piece of copy in the catalog, each with where it is
// said and the field that holds it -- "the plant card (Kinds[flower])" --
// which is what tells a translator whether "Flower" is a heading or a kind
// of photo.
func (c Catalog) Originals(context.Context) ([]translationbus.Source, error) {
	var out []translationbus.Source

	for _, cp := range c {
		walk(reflect.ValueOf(cp.Words), "", func(path string, t types.Text) {
			where := cp.Where
			if path != "" {
				where += " (" + path + ")"
			}

			out = append(out, translationbus.Source{Text: t, Where: where})
		})
	}

	return out, nil
}

var textType = reflect.TypeFor[types.Text]()

// walk calls found for every types.Text in v, with the path to it. A map is
// walked in the order of its keys, so the list is the same from one call to
// the next.
func walk(v reflect.Value, path string, found func(string, types.Text)) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem(), path, found)
		}

	case reflect.Struct:
		if v.Type() == textType {
			if t := v.Interface().(types.Text); t.Written() {
				found(path, t)
			}

			return
		}

		for i := range v.NumField() {
			if f := v.Type().Field(i); f.IsExported() {
				walk(v.Field(i), join(path, f.Name), found)
			}
		}

	case reflect.Map:
		keys := v.MapKeys()
		slices.SortFunc(keys, func(x, y reflect.Value) int { return cmp.Compare(fmt.Sprint(x), fmt.Sprint(y)) })

		for _, k := range keys {
			walk(v.MapIndex(k), path+"["+fmt.Sprint(k)+"]", found)
		}

	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walk(v.Index(i), path+"["+strconv.Itoa(i)+"]", found)
		}
	}
}

func join(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}
