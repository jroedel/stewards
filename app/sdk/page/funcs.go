package page

import (
	"html/template"

	"github.com/jroedel/stewards/business/types"
)

// Funcs are the template helpers every page has.
var Funcs = template.FuncMap{
	"say": Say,
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
