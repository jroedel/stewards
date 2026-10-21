// Package noticebus is how a message reaches a volunteer: by email, by text,
// or both, according to what they gave.
//
// Every message the app sends to the public goes through here -- the welcome
// to the list, the confirmation of a stewardship day, and the messages the
// stewards send to many -- so that three rules hold everywhere at once:
//
//   - A message has an email version and a text version, and a person who
//     gave both gets both (decided 2026-10-21: "they will receive
//     communications through both means if provided"). Where the email is
//     short the text is the same words; see [Notice].
//   - A stranger's form cannot make the app flood anybody. [Business.Claim]
//     is the cap, counted per address -- an email or a phone number alike --
//     and across the whole app per hour.
//   - A number that has texted STOP is taken off whatever it is on. Replies
//     to the shared number go to eumaeus, not here, so the only way this app
//     learns of a STOP is Twilio refusing the next text (21610); Send hears
//     it and tells every list that registered with [Business.OnStopped].
//
// Rendering the words is the app layer's: a Notice arrives already in the
// recipient's language.
package noticebus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sms"
)

const (
	// PerAddress is how many messages a stranger's form may cause to one
	// address, email or phone, in a day: signing up, leaving and signing up
	// again, three times over, and then nothing until tomorrow.
	PerAddress = 3

	// PerHour is how many such messages the whole app may send in an hour,
	// counting an email and a text as two. Far above what a sign on a trail
	// produces, and far below what would burn the relay's reputation -- the
	// relay that also carries the stewards' sign-in links -- or run up the
	// Twilio bill.
	PerHour = 60

	// TextLimit is how long an email body may be and still go as the text,
	// word for word: two text segments of plain characters. Longer, and the
	// notice carries a text of its own.
	TextLimit = 320
)

// Notice is one message, in one language, in both its forms.
type Notice struct {
	// Subject is the email's subject line.
	Subject string

	// Email is the email's body, in plain text.
	Email string

	// Text is the text message. Empty means the email is short enough to
	// be the text as it stands -- the stewards asked that a message be the
	// same both ways unless it is long.
	Text string
}

// TextBody is what goes by text: Text if the notice has one, or else the
// email's body.
func (n Notice) TextBody() string {
	if n.Text != "" {
		return n.Text
	}

	return n.Email
}

// Same reports whether the text is the email word for word, which is what a
// preview shows once rather than twice.
func (n Notice) Same() bool { return strings.TrimSpace(n.TextBody()) == strings.TrimSpace(n.Email) }

// ShortEnough reports whether an email body may go as the text unchanged.
func ShortEnough(body string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(body)) <= TextLimit
}

// Result is what came of one Send, half by half. A half that was not
// attempted -- no address, or that channel is off -- is neither sent nor
// failed.
type Result struct {
	Mailed, Texted   bool
	MailErr, TextErr error

	// Stopped is a text refused because the number replied STOP.
	Stopped bool
}

// Reached reports whether either half arrived at its provider.
func (r Result) Reached() bool { return r.Mailed || r.Texted }

// Storer is what the caps need from storage.
type Storer interface {
	// Claim records one message to each address at now, all or none, and
	// only if fewer than perAddress went to each since since and fewer than
	// perHour went to anybody since hourSince. It reports whether they may
	// be sent.
	Claim(ctx context.Context, addresses []string, now, since time.Time, perAddress int, hourSince time.Time, perHour int) (bool, error)

	// Prune deletes claims made before before.
	Prune(ctx context.Context, before time.Time) error
}

// Business sends notices and keeps the caps.
type Business struct {
	store Storer
	mail  mail.Sender
	texts sms.Sender
	now   func() time.Time

	mu      sync.Mutex
	stopped []func(context.Context, types.Phone) error
}

// NewBusiness constructs one. Either sender may be nil, and that channel is
// then off: a contact's half for it is skipped, and the forms do not ask
// for it (see Texts). nil now means the wall clock.
func NewBusiness(store Storer, m mail.Sender, t sms.Sender, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, mail: m, texts: t, now: now}
}

// Texts reports whether texts can be sent, which is whether a form should
// offer a phone field at all: nobody is asked for a number nothing can text.
func (b *Business) Texts() bool { return b.texts != nil }

// OnStopped registers f to hear of every number that has replied STOP, so
// that each list holding numbers can take it off. Registered once, at
// startup, by main.
func (b *Business) OnStopped(f func(context.Context, types.Phone) error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.stopped = append(b.stopped, f)
}

// Claim takes the cap for one message to each half of c that this app can
// reach. It is for the messages a stranger's form causes; the ones a steward
// sends are a steward's decision and are not capped.
func (b *Business) Claim(ctx context.Context, c types.Contact) (bool, error) {
	addresses := b.reachable(c)
	if len(addresses) == 0 {
		return false, nil
	}

	now := b.now()

	ok, err := b.store.Claim(ctx, addresses, now, now.Add(-24*time.Hour), PerAddress, now.Add(-time.Hour), PerHour)
	if err != nil {
		return false, fmt.Errorf("counting the messages sent: %w", err)
	}

	return ok, nil
}

// reachable is the halves of c a channel is on for, as the caps key them.
func (b *Business) reachable(c types.Contact) []string {
	var out []string

	if !c.Email.Zero() && b.mail != nil {
		out = append(out, c.Email.String())
	}

	if !c.Phone.Zero() && b.texts != nil {
		out = append(out, c.Phone.String())
	}

	return out
}

// Send sends n to each half of c this app can reach: the email to the
// address, the text to the phone. One half failing does not stop the other.
func (b *Business) Send(ctx context.Context, c types.Contact, n Notice) Result {
	var r Result

	if !c.Email.Zero() && b.mail != nil {
		// CRLF, which is what a mail body's lines end with on the wire.
		err := b.mail.Send(ctx, mail.Message{
			To: c.Email.String(), Subject: n.Subject,
			Text: strings.ReplaceAll(strings.ReplaceAll(n.Email, "\r\n", "\n"), "\n", "\r\n"),
		})
		r.Mailed, r.MailErr = err == nil, err
	}

	if !c.Phone.Zero() && b.texts != nil {
		_, err := b.texts.Send(ctx, sms.Message{To: c.Phone.String(), Body: strings.TrimSpace(n.TextBody())})
		r.Texted, r.TextErr = err == nil, err

		if errors.Is(err, sms.ErrOptedOut) {
			r.Stopped = true
			r.TextErr = errors.Join(err, b.stop(ctx, c.Phone))
		}
	}

	return r
}

// stop tells every list that p has replied STOP.
func (b *Business) stop(ctx context.Context, p types.Phone) error {
	b.mu.Lock()
	hooks := append([]func(context.Context, types.Phone) error(nil), b.stopped...)
	b.mu.Unlock()

	var errs []error

	for _, f := range hooks {
		if err := f(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("taking a stopped number off a list: %w", err))
		}
	}

	return errors.Join(errs...)
}

// Prune forgets claims older than the per-address cap looks back.
func (b *Business) Prune(ctx context.Context) error {
	return b.store.Prune(ctx, b.now().Add(-24*time.Hour))
}
