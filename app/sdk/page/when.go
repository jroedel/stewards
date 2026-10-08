package page

import (
	"strconv"
	"time"

	"github.com/jroedel/stewards/business/types"
)

// calendarWords are the names a date is written with, and the shapes it is
// written in, as copy like any other: Claude translates them, and "{weekday},
// {month} {day}" becomes "{weekday} {day} de {month}" in the translation
// rather than in the code. Month names are written out in full everywhere,
// since "May" would otherwise be both the month and its abbreviation, with
// one translation for the two.
type calendarWords struct {
	Months   [12]types.Text
	Weekdays [7]types.Text // Sunday first, as time.Weekday counts

	Date, MonthYear, DayMonth, Run types.Text

	SameHalf map[string]types.Text // by "am" or "pm"
	Clock    map[string]types.Text
	Across   types.Text
	Noon     types.Text

	Feet, Inches types.Text
}

var calendar = calendarWords{
	Months: [12]types.Text{
		{EN: "January"}, {EN: "February"}, {EN: "March"}, {EN: "April"}, {EN: "May"}, {EN: "June"},
		{EN: "July"}, {EN: "August"}, {EN: "September"}, {EN: "October"}, {EN: "November"}, {EN: "December"},
	},
	Weekdays: [7]types.Text{
		{EN: "Sunday"}, {EN: "Monday"}, {EN: "Tuesday"}, {EN: "Wednesday"}, {EN: "Thursday"}, {EN: "Friday"}, {EN: "Saturday"},
	},

	Date:      types.Text{EN: "{weekday}, {month} {day}"},
	MonthYear: types.Text{EN: "{month} {year}"},
	DayMonth:  types.Text{EN: "{day} {month}"},
	Run:       types.Text{EN: "{from} to {to}"},

	SameHalf: map[string]types.Text{"am": {EN: "{from}–{to} am"}, "pm": {EN: "{from}–{to} pm"}},
	Clock:    map[string]types.Text{"am": {EN: "{time} am"}, "pm": {EN: "{time} pm"}},
	Across:   types.Text{EN: "{from} – {to}"},
	Noon:     types.Text{EN: "noon"},

	Feet:   types.Text{EN: "{size} ft"},
	Inches: types.Text{EN: "{size} in"},
}

// CalendarWords is the catalog's entry for these, which the muxer lists with
// the apps' own.
var CalendarWords = Copy{
	Where: "dates, times and sizes, put together on the home page and the plant and place cards; " +
		"Months and Weekdays are the names alone, put into the other phrases",
	Words: calendar,
}

// Month is a month's name.
func Month(m time.Month) types.Text {
	if m < time.January || m > time.December {
		return types.Text{}
	}

	return calendar.Months[m-1]
}

// Months is the twelve names, January first, for a strip or a calendar's
// columns headed by their initials.
func Months() []types.Text { return calendar.Months[:] }

// Date is a day as the home page and the steward screens write it, in the
// garden's time zone: "Saturday, October 10", "sábado 10 de octubre". No
// year, because what these pages date is weeks away, not years.
func Date(t time.Time) Phrase {
	t = t.In(types.Garden)

	return Put(calendar.Date, "weekday", calendar.Weekdays[t.Weekday()], "month", Month(t.Month()), "day", t.Day())
}

// MonthYear is "April 2027", "April" or "2027", or nothing, for a photo's
// date as far as it is known.
func MonthYear(year, month int) any {
	m := Month(time.Month(month))

	switch {
	case year != 0 && m.Written():
		return Put(calendar.MonthYear, "month", m, "year", strconv.Itoa(year))
	case year != 0:
		return strconv.Itoa(year)
	case m.Written():
		return m
	}

	return nil
}

// DayMonth is "3 April", a day seen in a plant's record.
func DayMonth(t time.Time) Phrase {
	t = t.In(types.Garden)

	return Put(calendar.DayMonth, "day", t.Day(), "month", Month(t.Month()))
}

// Run is "March to May": the months of a set, a run at a time, as a
// screen reader says a bloom strip. Nothing for an empty set.
func Run(s types.Months) List {
	out := List{Sep: ", "}

	var start time.Month

	flush := func(end time.Month) {
		switch {
		case start == 0:
			return
		case start == end:
			out.Items = append(out.Items, Month(start))
		default:
			out.Items = append(out.Items, Put(calendar.Run, "from", Month(start), "to", Month(end)))
		}

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

	return out
}

// Hours is when something runs on its day: "8:00–11:30 am", or
// "11:00 am – 1:00 pm" when it crosses noon. The am or pm is written once
// when both ends share it, as people write it on a flyer, and twelve o'clock
// is "noon", since half of everyone reads "12:00 pm" as midnight.
func Hours(starts, ends time.Time) Phrase {
	s, e := starts.In(types.Garden), ends.In(types.Garden)

	if half := s.Format("pm"); half == e.Format("pm") && !noon(s) && !noon(e) {
		return Put(calendar.SameHalf[half], "from", s.Format("3:04"), "to", e.Format("3:04"))
	}

	return Put(calendar.Across, "from", clock(s), "to", clock(e))
}

func clock(t time.Time) any {
	if noon(t) {
		return calendar.Noon
	}

	return Put(calendar.Clock[t.Format("pm")], "time", t.Format("3:04"))
}

func noon(t time.Time) bool { return t.Hour() == 12 && t.Minute() == 0 }

// Length is a plant's size, "2–3 ft" or "18 in": the number as the plant's
// record writes it, and the unit as copy.
func Length(size string, feet bool) Phrase {
	if feet {
		return Put(calendar.Feet, "size", size)
	}

	return Put(calendar.Inches, "size", size)
}
