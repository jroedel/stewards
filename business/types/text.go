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

// In is the text in the language asked for, falling back to English when the
// Spanish has not been written yet.
//
// Falling back rather than showing nothing, because a volunteer reading
// Spanish still needs to know which bed they are standing in; and rather than
// translating on the fly, for the reason Text gives.
func (t Text) In(l Lang) string {
	if l == Spanish && t.ES != "" {
		return t.ES
	}

	return t.EN
}

// HasSpanish reports whether the Spanish half has been written, so a page can
// mark English shown in its place with lang="en" for a screen reader.
func (t Text) HasSpanish() bool { return t.ES != "" }

// Trimmed is the text with surrounding space removed from both halves, which
// is how everything typed into a form arrives at a rule.
func (t Text) Trimmed() Text {
	return Text{EN: strings.TrimSpace(t.EN), ES: strings.TrimSpace(t.ES)}
}
