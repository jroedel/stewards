package page

import "github.com/jroedel/stewards/business/types"

// Box is one text field of a form that has one box per field, in the page's
// language: what the box shows, and the language that is in, for its lang=.
//
// One box rather than one per language, because a person writes in the
// language they think in and Claude translates it into the other (design.md,
// principle 6). The box shows the text as the page would -- in the page's
// language, or the original while its translation waits -- and what comes
// back is the half in the page's language; translationbus.Keep tells words
// left as they were from words written anew.
type Box struct {
	Value string
	Lang  types.Lang
}

// BoxOf is t in a box on a page in l.
func BoxOf(t types.Text, l types.Lang) Box {
	return Box{Value: t.In(l), Lang: t.Shown(l)}
}

// Typed is what was typed in a box on a page in l, to show again after a
// refusal.
func Typed(s string, l types.Lang) Box {
	return Box{Value: s, Lang: l}
}

// Text is the box's words as the half in l, the page's language, for the
// rules.
func (b Box) Text(l types.Lang) types.Text {
	return types.Only(l, b.Value)
}
