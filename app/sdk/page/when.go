package page

import (
	"time"

	"github.com/jroedel/stewards/business/types"
)

// Date is a day as the home page and the steward screens write it, in the
// garden's time zone: "Saturday, October 10". No year, because what these
// pages date is weeks away, not years.
//
// English only, as a Text with no Spanish, so that Say marks it lang="en" on
// a Spanish page. A Spanish date is a closed set of words rather than prose,
// but design.md's rule is that Spanish is written or checked by a native
// speaker, and "sábado 10 de octubre" is no exception until one has.
func Date(t time.Time) types.Text {
	return types.Text{EN: t.In(types.Garden).Format("Monday, January 2")}
}

// Hours is when something runs on its day: "8:00–11:30 am", or
// "11:00 am – 1:00 pm" when it crosses noon. The am or pm is written once
// when both ends share it, as people write it on a flyer, and twelve o'clock
// is "noon", since half of everyone reads "12:00 pm" as midnight.
func Hours(starts, ends time.Time) types.Text {
	s, e := starts.In(types.Garden), ends.In(types.Garden)

	if s.Format("pm") == e.Format("pm") && !noon(s) && !noon(e) {
		return types.Text{EN: s.Format("3:04") + "–" + e.Format("3:04 pm")}
	}

	return types.Text{EN: clock(s) + " – " + clock(e)}
}

func clock(t time.Time) string {
	if noon(t) {
		return "noon"
	}

	return t.Format("3:04 pm")
}

func noon(t time.Time) bool { return t.Hour() == 12 && t.Minute() == 0 }
