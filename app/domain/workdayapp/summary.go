package workdayapp

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
)

// The once-a-day summary to the stewards who chose it (userbus.SignUpsDaily;
// decided 2026-10-22): who signed up and who cancelled since summaryAt
// yesterday, day by day, with how many are coming to each now.
//
// It goes from RemindLoop, which already wakes every few minutes, and is
// keyed by the date, so that a restart or the next tick never sends it twice
// and a server that was down at 8 sends it when it comes back. A morning with
// nothing to say sends nothing: a summary that says "nobody" every day
// teaches its reader to stop opening it.
//
// What it says and leaves out is dayapp's note's (stewards.go there): the
// names people chose to show, never how to reach them, and the day's
// stewards screen for that. The span is fixed -- summaryAt yesterday to
// summaryAt today -- rather than "since the last one", so that it reads the
// same whoever gets it and whenever it is sent.

// summaryAt is the hour of the morning the summary goes, in the garden's
// zone. The words say "8 am" with it, for the translator to see.
const summaryAt = 8

// Stewards is who chose which notes: userbus.
type Stewards interface {
	TellSignUps(ctx context.Context, s userbus.SignUps) ([]userbus.User, error)
	TellEvening(ctx context.Context) ([]userbus.User, error)
	SetDayNotices(ctx context.Context, id types.ID, n userbus.DayNotices) error
}

var summary = notices.Words{
	Subject: types.Text{EN: "Sign-ups for the stewardship days since yesterday"},
	Email: types.Text{EN: "Since 8 am yesterday:\n\n{days}" +
		"You get this once a day, and only when someone has signed up or cancelled, because you asked for it. To change that:\n{settings}\n\n" +
		"-- The garden stewards\n"},
}

// summaryLines is each day's part of the summary, held apart from the frame
// so that each says one thing in the translation memory.
var summaryLines = struct {
	Day, SignedUp, Cancelled types.Text
}{
	Day:       types.Text{EN: "{date}, {hours}: {title}. Coming now: {count}."},
	SignedUp:  types.Text{EN: "Signed up: {names}"},
	Cancelled: types.Text{EN: "Cancelled: {names}"},
}

var (
	summaryName = types.Text{EN: "To the stewards: the day's sign-ups"}
	summaryWhen = types.Text{EN: "Sent at 8 in the morning to each steward who chose a summary once a day, listing who signed up for a day and who cancelled since 8 the morning before. Not sent on a morning when nobody did."}
)

// summaryDay is one day's part of the summary.
type summaryDay struct {
	Day               workdaybus.Day
	Coming            int
	Joined, Cancelled []string
}

// summarize sends today's summary, once it is summaryAt, to the stewards who
// chose it, if anyone signed up or cancelled since summaryAt yesterday.
func (a app) summarize(ctx context.Context) error {
	if !a.messaging() || a.cfg.Stewards == nil {
		return nil
	}

	now := a.cfg.Days.Now().In(types.Garden)
	morning := time.Date(now.Year(), now.Month(), now.Day(), summaryAt, 0, 0, 0, types.Garden)

	if now.Before(morning) {
		return nil
	}

	to, err := a.cfg.Stewards.TellSignUps(ctx, userbus.SignUpsDaily)
	if err != nil || len(to) == 0 {
		return err
	}

	// The same hour yesterday, by the calendar rather than 24 hours back,
	// so that the morning after a clock change still starts at 8.
	ch, err := a.cfg.RSVPs.Changes(ctx, morning.AddDate(0, 0, -1), morning)
	if err != nil || ch.Zero() {
		return err
	}

	days, err := a.summaryDays(ctx, ch)
	if err != nil || len(days) == 0 {
		return err
	}

	recipients := make([]noticebus.Recipient, 0, len(to))
	for _, u := range to {
		recipients = append(recipients, noticebus.Recipient{
			Key: "steward:" + u.ID.String(), Contact: types.Contact{Email: u.Email}, Lang: u.DayNotices.Lang,
		})
	}

	a.cfg.Notices.SendBatch(ctx, "stewards-daily:"+morning.Format(time.DateOnly), recipients, func(r noticebus.Recipient) noticebus.Notice {
		return a.summaryNote(r.Lang, days)
	})

	return nil
}

// summaryDays is ch by day, soonest day first, with each day's count. A day
// taken off the calendar since takes its sign-ups with it, and is left out.
func (a app) summaryDays(ctx context.Context, ch rsvpbus.Changes) ([]summaryDay, error) {
	byDay := map[types.ID]*summaryDay{}
	var ids []types.ID

	at := func(id types.ID) *summaryDay {
		if d, ok := byDay[id]; ok {
			return d
		}

		d := &summaryDay{}
		byDay[id] = d
		ids = append(ids, id)

		return d
	}

	for _, r := range ch.Joined {
		d := at(r.DayID)
		d.Joined = append(d.Joined, r.Name)
	}

	for _, c := range ch.Cancelled {
		d := at(c.DayID)
		d.Cancelled = append(d.Cancelled, c.Name)
	}

	counts, err := a.cfg.RSVPs.Counts(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]summaryDay, 0, len(ids))

	for _, id := range ids {
		day, err := a.cfg.Days.ByID(ctx, id)
		if err != nil {
			continue
		}

		d := byDay[id]
		d.Day, d.Coming = day, counts[id]
		out = append(out, *d)
	}

	slices.SortFunc(out, func(x, y summaryDay) int { return x.Day.Starts.Compare(y.Day.Starts) })

	return out, nil
}

// summaryNote is the summary of days, in l.
func (a app) summaryNote(l types.Lang, days []summaryDay) noticebus.Notice {
	var b strings.Builder

	line := func(p page.Phrase) {
		b.WriteString(a.cfg.Render.Plain(l, p))
		b.WriteString("\n")
	}

	for _, d := range days {
		line(page.Put(summaryLines.Day, "date", page.Date(d.Day.Starts), "hours", page.Hours(d.Day.Starts, d.Day.Ends),
			"title", d.Day.Title, "count", d.Coming))

		if len(d.Joined) > 0 {
			line(page.Put(summaryLines.SignedUp, "names", strings.Join(d.Joined, ", ")))
		}

		if len(d.Cancelled) > 0 {
			line(page.Put(summaryLines.Cancelled, "names", strings.Join(d.Cancelled, ", ")))
		}

		b.WriteString(a.cfg.Links.StewardDay(d.Day.ID))
		b.WriteString("\n\n")
	}

	return notices.Build(a.cfg.Render, l, summary, "days", b.String(), "settings", a.cfg.Links.Settings)
}

// summaryKind is the summary for the messages screen, or none when it is not
// sent.
func (a app) summaryKind() []notices.Kind {
	if a.cfg.Stewards == nil {
		return nil
	}

	return []notices.Kind{{
		Name: summaryName, When: summaryWhen, Words: summary,
		Sample: func(l types.Lang) noticebus.Notice {
			return a.summaryNote(l, []summaryDay{{Day: notices.SampleDay(), Coming: 4, Joined: []string{"Ana", "Luis"}, Cancelled: []string{"Rosa"}}})
		},
	}}
}
