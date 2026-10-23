package dayapp

import (
	"context"
	"net/http"

	"github.com/jroedel/stewards/app/sdk/notices"
	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/user/userbus"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/web"
)

// The note to the stewards who asked to be told of each sign-up and each
// cancellation, as it happens (userbus.SignUpsEach; decided 2026-10-22).
//
// It is here, beside the form, because the form is where it happens; the
// once-a-day summary and the list the evening before are workdayapp's, which
// has the loop that sends at a time of day.
//
// # What it says, and what it leaves out
//
// The name the person chose to show, the day, and how many are coming now,
// with a link to the day's stewards screen. Not their email or phone: mail is
// kept forever, in an inbox this app does not look after, and the screen
// behind the link shows both to anyone signed in.
//
// # Why a stranger cannot flood a steward with it
//
// A note goes only for a sign-up that was Joined, and nothing is joined
// without first passing noticebus's caps on what a stranger's form may cause
// (rsvpbus.Request claims before it writes). A cancellation needs a sign-up
// to cancel. So the most a bot can send a steward is what it could already
// make the app send to the people it typed in; a steward who finds that too
// many has the once-a-day summary.
//
// # Sent in the background, once
//
// Through noticebus.SendBatch on the app's context, like the reminder, so
// that the person who pressed the button is not kept waiting on the relay,
// and with a key per sign-up, so that a restart or a second press never
// tells a steward twice.

var joinedNote = notices.Words{
	Subject: types.Text{EN: "{name} is coming on {date}"},
	Email: types.Text{EN: "{name} signed up for the stewardship day on {date}, {hours}: {title}.\n\n" +
		"Coming now: {count}. Who they are, and how to reach them:\n{day}\n\n" +
		"{why}" +
		"-- The garden stewards\n"},
}

var leftNote = notices.Words{
	Subject: types.Text{EN: "{name} can't come on {date}"},
	Email: types.Text{EN: "{name} took their name off the stewardship day on {date}, {hours}: {title}.\n\n" +
		"Coming now: {count}. Who is still coming:\n{day}\n\n" +
		"{why}" +
		"-- The garden stewards\n"},
}

// whyEach is the line that says why a steward is getting the note, and where
// to stop it: a note nobody remembers asking for is one that gets marked as
// spam, on the relay that also carries the sign-in links.
var whyEach = types.Text{EN: "You get a note for each sign-up and each cancellation because you asked for them. To change that:\n{settings}\n\n"}

var (
	joinedName = types.Text{EN: "To the stewards: someone signed up"}
	joinedWhen = types.Text{EN: "Sent at once to each steward who chose, on the stewards screen, a note for each sign-up. Only for a new name on the day; someone adding a phone to their sign-up is not one more person coming."}
	leftName   = types.Text{EN: "To the stewards: someone cancelled"}
	leftWhen   = types.Text{EN: "Sent at once to each steward who chose a note for each sign-up, when someone takes their name off a day with the link in their confirmation. Not when a steward takes a name off."}
)

// Stewards is who has asked to be told about sign-ups: userbus.
type Stewards interface {
	TellSignUps(ctx context.Context, s userbus.SignUps) ([]userbus.User, error)
}

// tellStewards sends w about r to every steward who asked for a note for
// each sign-up. Anything that stops it is logged and goes no further: the
// person on the form has done what they came to do whatever happens here.
func (a app) tellStewards(r *http.Request, kind string, w notices.Words, rsvp rsvpbus.RSVP) {
	if a.cfg.Stewards == nil || a.cfg.Background == nil {
		return
	}

	ctx := r.Context()
	fail := func(what string, err error) {
		a.cfg.Log.ErrorContext(ctx, what, "request_id", web.RequestIDFrom(ctx), "rsvp_id", rsvp.ID.String(), "error", err)
	}

	to, err := a.cfg.Stewards.TellSignUps(ctx, userbus.SignUpsEach)
	if err != nil {
		fail("reading which stewards to tell", err)

		return
	}

	if len(to) == 0 {
		return
	}

	d, err := a.cfg.Days.ByID(ctx, rsvp.DayID)
	if err != nil {
		fail("reading the day to tell the stewards", err)

		return
	}

	coming, err := a.cfg.RSVPs.Coming(ctx, rsvp.DayID)
	if err != nil {
		fail("counting who is coming, to tell the stewards", err)

		return
	}

	recipients := make([]noticebus.Recipient, 0, len(to))
	for _, u := range to {
		recipients = append(recipients, noticebus.Recipient{
			Key: "steward:" + u.ID.String(), Contact: types.Contact{Email: u.Email}, Lang: u.DayNotices.Lang,
		})
	}

	a.cfg.Notices.SendBatch(a.cfg.Background, kind+":"+rsvp.ID.String(), recipients, func(to noticebus.Recipient) noticebus.Notice {
		return a.stewardNote(to.Lang, w, d, rsvp.Name, len(coming))
	})

	a.cfg.Log.InfoContext(ctx, "the stewards are being told", "request_id", web.RequestIDFrom(ctx),
		"rsvp_id", rsvp.ID.String(), "kind", kind, "stewards", len(recipients))
}

// stewardNote is w in l, about name and d, with count coming now.
func (a app) stewardNote(l types.Lang, w notices.Words, d workdaybus.Day, name string, count int) noticebus.Notice {
	return notices.Build(a.cfg.Render, l, w,
		"name", name, "date", page.Date(d.Starts), "hours", page.Hours(d.Starts, d.Ends), "title", d.Title,
		"count", count, "day", a.cfg.StewardDay(d.ID),
		"why", page.Put(whyEach, "settings", a.cfg.Settings))
}

// stewardKinds is the two notes, for the stewards' messages screen, or none
// when they are not being sent.
func (a app) stewardKinds() []notices.Kind {
	if a.cfg.Stewards == nil || a.cfg.Background == nil {
		return nil
	}

	sample := func(w notices.Words) func(types.Lang) noticebus.Notice {
		return func(l types.Lang) noticebus.Notice { return a.stewardNote(l, w, notices.SampleDay(), "Ana", 4) }
	}

	return []notices.Kind{
		{Name: joinedName, When: joinedWhen, Words: joinedNote, Sample: sample(joinedNote)},
		{Name: leftName, When: leftWhen, Words: leftNote, Sample: sample(leftNote)},
	}
}
