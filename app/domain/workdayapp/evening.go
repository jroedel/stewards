package workdayapp

import (
	"context"
	"strings"

	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
)

// The list the evening before each day, of everyone coming, to the stewards
// who ticked the box for it (userbus.DayNotices.Evening; decided 2026-10-22).
//
// It goes with the volunteers' reminder, from the same pass of remind, and
// names everyone coming at that moment -- including someone who signed up
// at 6:02 and so had a confirmation rather than a reminder. It goes even
// when nobody has signed up: unlike the morning summary, it answers a
// question the steward is asking ("how many tomorrow?"), and "nobody yet" is
// an answer. Keyed by the day, so it goes once.
//
// Names only, as in the other notes to the stewards; the day's screen behind
// the link has how to reach them.

var eveningList = notices.Words{
	Subject: types.Text{EN: "Tomorrow, {date}: {count} coming"},
	Email: types.Text{EN: "{date}, {hours}: {title}.\n\n" +
		"{coming}\n\n" +
		"The day's screen, with how to reach them:\n{day}\n\n" +
		"You get this the evening before each stewardship day because you asked for it. To change that:\n{settings}\n\n" +
		"-- The garden stewards\n"},
}

// eveningLines is the middle of the list: who is coming, or that nobody is.
var eveningLines = struct {
	Coming, Nobody types.Text
}{
	Coming: types.Text{EN: "Coming ({count}):\n{names}"},
	Nobody: types.Text{EN: "Nobody has signed up yet."},
}

var (
	eveningName = types.Text{EN: "To the stewards: who is coming tomorrow"}
	eveningWhen = types.Text{EN: "Sent at 6 the evening before each stewardship day, with the reminder to the people coming, to each steward who ticked the box for it. Sent even when nobody has signed up."}
)

// tellEvening sends the list for d, with rs coming, to the stewards who
// asked for it.
func (a app) tellEvening(ctx context.Context, d workdaybus.Day, rs []rsvpbus.RSVP) error {
	if a.cfg.Stewards == nil {
		return nil
	}

	to, err := a.cfg.Stewards.TellEvening(ctx)
	if err != nil || len(to) == 0 {
		return err
	}

	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}

	recipients := make([]noticebus.Recipient, 0, len(to))
	for _, u := range to {
		recipients = append(recipients, noticebus.Recipient{
			Key: "steward:" + u.ID.String(), Contact: types.Contact{Email: u.Email}, Lang: u.DayNotices.Lang,
		})
	}

	a.cfg.Notices.SendBatch(ctx, "stewards-evening:"+d.ID.String(), recipients, func(r noticebus.Recipient) noticebus.Notice {
		return a.eveningNote(r.Lang, d, names)
	})

	return nil
}

// eveningNote is the list for d, in l, with names coming, earliest first.
func (a app) eveningNote(l types.Lang, d workdaybus.Day, names []string) noticebus.Notice {
	coming := any(eveningLines.Nobody)
	if len(names) > 0 {
		coming = page.Put(eveningLines.Coming, "count", len(names), "names", "- "+strings.Join(names, "\n- "))
	}

	return notices.Build(a.cfg.Render, l, eveningList,
		"date", page.Date(d.Starts), "hours", page.Hours(d.Starts, d.Ends), "title", d.Title,
		"count", len(names), "coming", coming,
		"day", a.cfg.Links.StewardDay(d.ID), "settings", a.cfg.Links.Settings)
}

// eveningKind is the list for the messages screen, or none when it is not
// sent.
func (a app) eveningKind() []notices.Kind {
	if a.cfg.Stewards == nil {
		return nil
	}

	return []notices.Kind{{
		Name: eveningName, When: eveningWhen, Words: eveningList,
		Sample: func(l types.Lang) noticebus.Notice {
			return a.eveningNote(l, notices.SampleDay(), []string{"Ana", "Luis", "Rosa"})
		},
	}}
}
