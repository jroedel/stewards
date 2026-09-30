package page

import (
	"html/template"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
)

// Funcs are the template helpers every page has.
var Funcs = template.FuncMap{
	"say":      Say,
	"sentence": Sentence,
}

// Say writes a piece of copy in the page's language.
//
// When the page is in Spanish and the Spanish has not been written yet, the
// English is shown in its place wrapped in lang="en", so a screen reader
// switches voice rather than reading English with Spanish pronunciation
// (design.md, "Accessibility"). The alternative, a machine translation, is
// the one thing design.md rules out.
//
// The text is escaped here, since the result is trusted HTML.
func Say(l types.Lang, t types.Text) template.HTML {
	s := template.HTMLEscapeString(t.In(l))

	if l == types.Spanish && !t.HasSpanish() && t.EN != "" {
		return template.HTML(`<span lang="en">` + s + `</span>`)
	}

	return template.HTML(s)
}

// Sentence makes a rule's problem into something a page can show on its own:
// a capital at the start and a full stop at the end.
//
// The rules write their problems as the continuation of a sentence -- "give
// the place a name in English" -- because that is also how they read inside
// an error chain in a log. Written as sentences there, they would read
// "saving: Give the place a name." in every log line instead.
func Sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]

	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, "!") {
		s += "."
	}

	return s
}
