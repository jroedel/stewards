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
	"short":    Short,
}

// ShortLen is how much of an ID a person is shown: as much as anybody needs
// to say which one, in a conversation about the photos or in a message.
// Eight hex characters, as git shortens a commit -- and as Claude, sorting the
// inbox through the API, names a photo -- so the photo it names is the one the
// screen shows. Two of 2^32 never meet among a garden's photos.
const ShortLen = 8

// Short is an ID as a person is shown it: its first ShortLen characters.
func Short(id string) string {
	if len(id) <= ShortLen {
		return id
	}

	return id[:ShortLen]
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
