package types

import (
	"strings"
	"time"
)

// Months is a set of months of the year: when a plant blooms, and later when
// a job is done. Bit 0 is January.
//
// A set rather than a start and an end, because a season is not always one
// run: Brazos penstemon flowers in spring and keeps its red leaves through the
// winter, and a plant that reblooms after a late-summer rain has two runs. The
// bloom strip in design.md is twelve cells, filled or not, which is exactly
// this.
type Months uint16

// AllMonths is every month, for checking a set read from outside.
const AllMonths Months = 1<<12 - 1

// MonthsOf is the set holding each month given.
func MonthsOf(ms ...time.Month) Months {
	var set Months
	for _, m := range ms {
		set = set.With(m)
	}

	return set
}

// With is the set with m added. A month outside January–December adds
// nothing.
func (s Months) With(m time.Month) Months {
	if m < time.January || m > time.December {
		return s
	}

	return s | 1<<(m-1)
}

// Has reports whether m is in the set.
func (s Months) Has(m time.Month) bool {
	return m >= time.January && m <= time.December && s&(1<<(m-1)) != 0
}

// Valid reports whether the set holds only real months, for a value read
// from a database or a form.
func (s Months) Valid() bool { return s&^AllMonths == 0 }

// Zero reports whether the set is empty: no bloom recorded.
func (s Months) Zero() bool { return s == 0 }

// Each is the months in the set, January first.
func (s Months) Each() []time.Month {
	var out []time.Month
	for m := time.January; m <= time.December; m++ {
		if s.Has(m) {
			out = append(out, m)
		}
	}

	return out
}

// String is the set as runs of months, "Mar–May, Sep–Nov", for a log line or
// a steward's list. A volunteer sees the twelve-cell strip instead, which
// needs no words and no translating.
//
// A run that crosses the new year is written as two, "Jan–Feb, Dec", rather
// than wrapping; the strip runs January to December, and the words should
// read the same way as the strip they sit beside.
func (s Months) String() string {
	var (
		runs  []string
		start time.Month
	)

	flush := func(end time.Month) {
		if start == 0 {
			return
		}

		run := start.String()[:3]
		if end != start {
			run += "–" + end.String()[:3]
		}

		runs = append(runs, run)
		start = 0
	}

	for m := time.January; m <= time.December; m++ {
		switch {
		case s.Has(m) && start == 0:
			start = m
		case !s.Has(m):
			flush(m - 1)
		}
	}
	flush(time.December)

	return strings.Join(runs, ", ")
}
