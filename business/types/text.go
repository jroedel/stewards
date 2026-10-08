package types

import (
	"fmt"
	"strings"
)

// Lang is a language the app speaks. English and Spanish, and nothing else:
// the paid gardeners work in Spanish and some volunteers may too (see
// phase-1-plan.md, "Who").
type Lang string

const (
	English Lang = "en"
	Spanish Lang = "es"
)

// ParseLang reads a language from outside -- a query parameter, a cookie, a
// form field -- and refuses anything it does not speak.
func ParseLang(s string) (Lang, error) {
	switch l := Lang(strings.ToLower(strings.TrimSpace(s))); l {
	case English, Spanish:
		return l, nil
	}

	return "", fmt.Errorf("%q is not a language this app speaks; use en or es", s)
}

// Text is one piece of copy in both languages.
//
// A person writes one half and Claude translates it into the other, through
// the translation memory (translationbus), which fills the second half as a
// record is read (design.md, "Field-first principles" 6). Until the
// translation is made, that half is honestly absent rather than invented.
type Text struct {
	EN string
	ES string
}

// In is the text in the language asked for, falling back to the other half
// when that one has not been written or translated yet.
//
// Falling back rather than showing nothing, because a volunteer reading
// Spanish still needs to know which bed they are standing in -- and so does
// a steward reading English, now that a person may write in Spanish; and
// rather than translating on the fly, for the reason Text gives.
func (t Text) In(l Lang) string {
	if (l == Spanish && t.ES != "") || t.EN == "" {
		return t.ES
	}

	return t.EN
}

// Shown is the language In(l) gives the text in: l, or the other one when it
// falls back -- so a page can mark it with lang= for a screen reader.
func (t Text) Shown(l Lang) Lang {
	switch {
	case l == Spanish && t.ES == "" && t.EN != "":
		return English
	case l == English && t.EN == "" && t.ES != "":
		return Spanish
	}

	return l
}

// Written reports whether either half has been written: whether there is
// anything to show at all.
func (t Text) Written() bool { return t.EN != "" || t.ES != "" }

// Half is the half in l alone, with no falling back: what a form with one
// box per field shows in its box.
func (t Text) Half(l Lang) string {
	if l == Spanish {
		return t.ES
	}

	return t.EN
}

// Only is s alone, as the half in l.
func Only(l Lang, s string) Text {
	if l == Spanish {
		return Text{ES: s}
	}

	return Text{EN: s}
}

// Trimmed is the text with surrounding space removed from both halves, which
// is how everything typed into a form arrives at a rule.
func (t Text) Trimmed() Text {
	return Text{EN: strings.TrimSpace(t.EN), ES: strings.TrimSpace(t.ES)}
}
