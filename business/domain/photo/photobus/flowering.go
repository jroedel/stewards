package photobus

import (
	"cmp"
	"slices"

	"github.com/jroedel/stewards/business/types"
)

// The flowering record is each plant's first and last flowering day every
// year, its first and last day in fruit (fruit or seed, added with the fruit
// kind), and the first day it was seen at all, read from its photos. The
// stewards asked for it on 2026-10-06: they photograph the plants all season
// and want to know, year on year, when each one starts and stops flowering,
// and which plants turn up new.
//
// It is read, not kept. Each date is a photo -- one of ours, with the day it
// was taken -- and the record is worked out from them each time it is shown,
// so correcting a photo's day or its "in flower" corrects the record, and
// there is no second copy of the truth to drift from the first. A borrowed
// photo is somebody else's observation, from anywhere and any year, and is
// never counted.
//
// Photos from everywhere count, the steward decided: a plant in flower at
// Pedernales Falls is a date worth having. Each date carries its photo, so
// whoever shows it can say where it was seen. Unchecked photos count too,
// for the stewards; a reader that shows the record to volunteers asks for
// checked photos alone, as a volunteer sees no photo until it is checked.

// Season is one year of a plant's flowering record.
type Season struct {
	Year int

	// FirstSeen is the year's earliest photo of the plant, in flower or
	// not; FirstFlower and LastFlower the earliest and latest in flower,
	// and FirstFruit and LastFruit in fruit. A zero Photo for none: a plant
	// seen only in leaf has no flowering.
	FirstSeen, FirstFlower, LastFlower, FirstFruit, LastFruit Photo

	// Seen is how many of the year's photos are dated, and InFlower and
	// InFruit how many of those show it in flower and in fruit.
	Seen, InFlower, InFruit int
}

// Flowering is the record from photos, newest year first. checkedOnly counts
// only photos a steward has checked.
func Flowering(photos []Photo, checkedOnly bool) []Season {
	dated := slices.DeleteFunc(slices.Clone(photos), func(p Photo) bool {
		return p.Source != Ours || p.TakenAt.IsZero() || (checkedOnly && !p.Checked)
	})

	slices.SortFunc(dated, func(x, y Photo) int { return x.TakenAt.Compare(y.TakenAt) })

	byYear := map[int]*Season{}
	for _, p := range dated {
		year := p.TakenAt.In(types.Garden).Year()

		s, ok := byYear[year]
		if !ok {
			s = &Season{Year: year, FirstSeen: p}
			byYear[year] = s
		}

		s.Seen++

		if p.InFlower {
			s.InFlower++
			if s.FirstFlower.ID.Zero() {
				s.FirstFlower = p
			}
			s.LastFlower = p
		}

		if p.InFruit {
			s.InFruit++
			if s.FirstFruit.ID.Zero() {
				s.FirstFruit = p
			}
			s.LastFruit = p
		}
	}

	out := make([]Season, 0, len(byYear))
	for _, s := range byYear {
		out = append(out, *s)
	}

	slices.SortFunc(out, func(x, y Season) int { return cmp.Compare(y.Year, x.Year) })

	return out
}
