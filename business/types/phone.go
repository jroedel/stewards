package types

import (
	"errors"
	"fmt"
	"strings"
)

// Phone is a mobile number a text can be sent to, held in E.164 form
// ("+15125550123").
//
// One spelling, decided at the door. A volunteer types "(512) 555-0123", a
// second sign-up from the same person says "512.555.0123", and left as typed
// they are two people: two confirmations, two reminders, and -- the one that
// matters -- a STOP that reaches one row and not the other. So a number is
// normalised before anything compares or stores it, and Twilio is addressed
// in E.164 and nothing else anyway.
//
// The rules are eumaeus's (business/types/phone), which has been sending to
// real numbers since 2026-09: no libphonenumber, whose tables are a large
// dependency to answer a question this app asks of one country.
//
// # The one assumption
//
// A number with no country code is North American. Ten digits, or eleven
// beginning with 1, become +1; anything else must arrive with its "+" and
// country code, and is refused with a sentence asking for it rather than
// turned into a +1 number belonging to a stranger.
//
// What this cannot catch: a Mexican mobile is also ten digits, so one typed
// without its +52 reads as a North American number, and only the shape of a
// North American area code and exchange (neither begins with 0 or 1) stands
// between it and a stranger. The form's help says to write the +52; there is
// no way to know from the digits alone.
//
// # A number is the person
//
// It does not go in a log. [Phone.Redacted] is what goes there; [Phone.String]
// is for Twilio, the database, and the steward screen behind sign-in.
type Phone struct {
	e164 string
}

// ErrNotAPhone is what every parse failure wraps.
var ErrNotAPhone = errors.New("not a phone number we can text")

// E.164 allows fifteen digits at most including the country code, and no
// number is shorter than seven. Both bounds are the standard's: they are what
// tells a typo from a number.
const (
	phoneMinDigits = 7
	phoneMaxDigits = 15

	// nanpDigits is a North American number without its country code.
	nanpDigits = 10
)

// ParsePhone normalises a number the way a person wrote it.
//
// Everything that is not a digit or a leading "+" goes first: brackets,
// spaces, hyphens, dots, and the "tel:" a copied link brings with it. A
// leading "00" is the rest of the world's way of writing "+", and is read as
// one.
func ParsePhone(s string) (Phone, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Phone{}, fmt.Errorf("%w: it is blank", ErrNotAPhone)
	}

	// Noted before the digits are pulled out, because pulling them out is
	// what loses the difference between "+15125550123" and "15125550123" --
	// and that difference is the whole of whether a country code is there.
	plus := strings.HasPrefix(s, "+")
	digits := asciiDigits(s)

	if !plus && strings.HasPrefix(digits, "00") {
		plus, digits = true, digits[2:]
	}

	switch {
	case digits == "":
		return Phone{}, fmt.Errorf("%w: %q has no digits in it", ErrNotAPhone, s)
	case len(digits) > phoneMaxDigits:
		return Phone{}, fmt.Errorf("%w: %q has %d digits, and no number has more than %d", ErrNotAPhone, s, len(digits), phoneMaxDigits)
	}

	if !plus {
		switch {
		case len(digits) == nanpDigits:
			digits = "1" + digits
		case len(digits) == nanpDigits+1 && digits[0] == '1':
		default:
			return Phone{}, fmt.Errorf("%w: %q has no country code; write it with a + and the country code, like +52 for Mexico", ErrNotAPhone, s)
		}
	}

	// A North American area code and exchange each begin with 2-9. A 0 or
	// 1 there is a slipped digit, and the number it makes is not anyone's.
	if len(digits) == nanpDigits+1 && digits[0] == '1' && (digits[1] < '2' || digits[4] < '2') {
		return Phone{}, fmt.Errorf("%w: %q is not a North American number; check the digits, or write it with a + and its country code", ErrNotAPhone, s)
	}

	switch {
	case len(digits) < phoneMinDigits:
		return Phone{}, fmt.Errorf("%w: %q is too short to be a phone number", ErrNotAPhone, s)
	case digits[0] == '0':
		// No country code begins with a zero, so "+0..." is a number that
		// has lost its country code rather than one with an unusual one.
		return Phone{}, fmt.Errorf("%w: %q begins with a zero, so its country code is missing", ErrNotAPhone, s)
	}

	return Phone{e164: "+" + digits}, nil
}

// String is the E.164 number: what Twilio is sent and what is stored. Not
// for a log line; see Redacted.
func (p Phone) String() string { return p.e164 }

// Zero reports whether this is the unset Phone.
func (p Phone) Zero() bool { return p.e164 == "" }

// Redacted is the number for a log line: the last four digits, the rest as
// dots. Four, because that is what lets a steward reading the log tell which
// of two volunteers is meant without the line being enough to dial -- the
// same trade a bank statement makes with a card number.
func (p Phone) Redacted() string {
	if p.Zero() {
		return "(none)"
	}

	digits := p.e164[1:]

	return "+" + strings.Repeat("•", len(digits)-4) + digits[len(digits)-4:]
}

// National is the number the way someone here reads it aloud, for the
// steward screen: "(512) 555-0123". A number from elsewhere stays in E.164,
// since inventing a grouping for a country this does not know would make it
// harder, not easier, to check against a contacts app.
func (p Phone) National() string {
	d := strings.TrimPrefix(p.e164, "+1")
	if len(d) != nanpDigits || len(p.e164) != nanpDigits+2 {
		return p.e164
	}

	return "(" + d[:3] + ") " + d[3:6] + "-" + d[6:]
}

// asciiDigits keeps 0-9 and drops everything else. Not unicode.IsDigit,
// which would quietly turn fullwidth or Arabic-Indic digits into a number
// nobody typed; those are dropped, and too few digits are left to pass.
func asciiDigits(s string) string {
	var b strings.Builder

	for i := range len(s) {
		if c := s[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}

	return b.String()
}
