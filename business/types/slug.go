package types

import (
	"fmt"
	"strings"
)

// MaxSlug is the longest address a place or a species may have.
const MaxSlug = 48

// SlugProblem says what is wrong with s as an address, or "" when nothing is.
//
// An address is the last part of a link that gets printed -- on a stake, a
// guide, a QR code -- so it is held to the shape a person can read aloud and
// type on a phone: lower-case letters, digits and single hyphens. Shared by
// places and species, which both have one, so the two cannot drift into
// accepting different shapes.
//
// A sentence rather than an error, because the caller wraps it in its own
// Invalid with the field it belongs to; the empty case is the caller's too,
// since what to suggest instead ("rain-garden", "winecup") is theirs.
func SlugProblem(s string) string {
	switch {
	case len(s) > MaxSlug:
		return fmt.Sprintf("keep the address under %d characters", MaxSlug)
	case strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") || strings.Contains(s, "--"):
		return "use single hyphens between words, and none at the ends"
	}

	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "use only lower-case letters, digits and hyphens"
		}
	}

	return ""
}
