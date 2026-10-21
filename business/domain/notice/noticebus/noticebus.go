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
//
// # Sending to many
//
// A steward's message to the list, or to the people coming to a day, and the
// reminder the evening before, go through [Business.SendBatch]. It runs in
// the background, since forty texts at Twilio's pace for one number is most
// of a minute, and it claims each recipient and channel in notice_sends
// before sending, with one INSERT … ON CONFLICT DO NOTHING. So a button
// pressed twice, a reminder tick that runs again, or a restart in the
// middle never sends anybody the same message twice: what is claimed is
// never claimed again, and a send cut off by a restart is left as "sending"
// rather than tried a second time, since whether it went is not known.
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
// email's body, with typographic punctuation made plain.
func (n Notice) TextBody() string {
	body := n.Email
	if n.Text != "" {
		body = n.Text
	}

	return plain.Replace(strings.TrimSpace(body))
}

// plain swaps the punctuation the pages use -- the en dash in "8:00–11:00
// am", curly quotes -- for its plain-character twin in a text. Any one
// character outside the plain text set sends the whole text in UCS-2, at 70
// characters a segment rather than 160 (sms.Segments), and the dash in the
// hours would otherwise do that to every text about a day. Accented letters
// are left alone: they are the words.
var plain = strings.NewReplacer(
	"–", "-", "—", "-", "‘", "'", "’", "'", "“", `"`, "”", `"`, "…", "...", "\u00a0", " ", "\u202f", " ",
)

// Same reports whether the text is the email word for word, which is what a
// preview shows once rather than twice.
func (n Notice) Same() bool {
	return n.Text == "" || strings.TrimSpace(n.Text) == strings.TrimSpace(n.Email)
}

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

// Storer is what the caps and the sends to many need from storage.
type Storer interface {
	// ClaimSend records that batch is sending to recipient by channel, as
	// one statement, and reports false when it has been claimed before.
	ClaimSend(ctx context.Context, batch, recipient, channel string, at time.Time) (bool, error)

	// MarkSend sets what came of a claimed send.
	MarkSend(ctx context.Context, batch, recipient, channel, status string) error

	// Batches is every batch whose key starts with prefix, earliest first.
	Batches(ctx context.Context, prefix string) ([]Batch, error)

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

	// pace is the gap between two texts in a batch; wg is the batches
	// running.
	pace time.Duration
	wg   sync.WaitGroup
}

// TextPace is the gap between texts in a batch. A US long-code number sends
// about one message a second, and Twilio queues anything faster; pacing
// here keeps the queue -- and the moment a STOP comes back -- in step with
// what has been sent.
const TextPace = time.Second

// NewBusiness constructs one. Either sender may be nil, and that channel is
// then off: a contact's half for it is skipped, and the forms do not ask
// for it (see Texts). nil now means the wall clock.
func NewBusiness(store Storer, m mail.Sender, t sms.Sender, now func() time.Time) *Business {
	if now == nil {
		now = time.Now
	}

	return &Business{store: store, mail: m, texts: t, now: now, pace: TextPace}
}

// Pace sets the gap between texts in a batch; a test sets none.
func (b *Business) Pace(d time.Duration) { b.pace = d }

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
		r.MailErr = b.sendEmail(ctx, c.Email, n)
		r.Mailed = r.MailErr == nil
	}

	if !c.Phone.Zero() && b.texts != nil {
		r.Stopped, r.TextErr = b.sendText(ctx, c.Phone, n)
		r.Texted = r.TextErr == nil
	}

	return r
}

func (b *Business) sendEmail(ctx context.Context, to types.Email, n Notice) error {
	// CRLF, which is what a mail body's lines end with on the wire.
	return b.mail.Send(ctx, mail.Message{
		To: to.String(), Subject: n.Subject,
		Text: strings.ReplaceAll(strings.ReplaceAll(n.Email, "\r\n", "\n"), "\n", "\r\n"),
	})
}

// sendText sends the text, and on a STOP tells every list.
func (b *Business) sendText(ctx context.Context, to types.Phone, n Notice) (stopped bool, err error) {
	_, err = b.texts.Send(ctx, sms.Message{To: to.String(), Body: n.TextBody()})
	if errors.Is(err, sms.ErrOptedOut) {
		return true, errors.Join(err, b.stop(ctx, to))
	}

	return false, err
}

// Recipient is one person a batch goes to. Key is what the batch remembers
// them by -- a subscriber's or a sign-up's ID -- so that the same person is
// never sent the same batch twice, whatever their contact has become since.
type Recipient struct {
	Key     string
	Contact types.Contact
	Lang    types.Lang
}

// The channels and statuses notice_sends holds.
const (
	Email = "email"
	Text  = "text"

	Sending = "sending"
	Sent    = "sent"
	Failed  = "failed"
	Stopped = "stopped"
)

// Batch is how a batch went, for the steward who sent it.
type Batch struct {
	Key     string
	Started time.Time

	Sent, Failed, Stopped, Sending int
}

// SendBatch sends to each of to, in the background on ctx, the notice build
// makes for them; see the package comment. Key names the batch: the same key
// is the same batch, and reaches nobody twice. It returns at once; Batches
// says how it is going, and Wait waits for it.
//
// ctx is the app's, not a request's: the request that pressed the button is
// over long before the last text goes.
func (b *Business) SendBatch(ctx context.Context, key string, to []Recipient, build func(Recipient) Notice) {
	b.wg.Go(func() {
		for _, r := range to {
			if ctx.Err() != nil {
				return
			}

			b.sendOne(ctx, key, r, build)
		}
	})
}

// sendOne claims and sends each half of r's message, and records each.
func (b *Business) sendOne(ctx context.Context, key string, r Recipient, build func(Recipient) Notice) {
	var n *Notice

	notice := func() Notice {
		if n == nil {
			built := build(r)
			n = &built
		}

		return *n
	}

	if !r.Contact.Email.Zero() && b.mail != nil && b.claim(ctx, key, r.Key, Email) {
		status := Sent
		if err := b.sendEmail(ctx, r.Contact.Email, notice()); err != nil {
			status = Failed
		}

		b.mark(ctx, key, r.Key, Email, status)
	}

	if !r.Contact.Phone.Zero() && b.texts != nil && b.claim(ctx, key, r.Key, Text) {
		status := Sent

		switch stopped, err := b.sendText(ctx, r.Contact.Phone, notice()); {
		case stopped:
			status = Stopped
		case err != nil:
			status = Failed
		}

		b.mark(ctx, key, r.Key, Text, status)

		select {
		case <-ctx.Done():
		case <-time.After(b.pace):
		}
	}
}

// claim reports whether this send is this batch's to make. A store that
// cannot answer is a no: an unclaimed send might be a second copy.
func (b *Business) claim(ctx context.Context, batch, recipient, channel string) bool {
	ok, err := b.store.ClaimSend(ctx, batch, recipient, channel, b.now())
	return err == nil && ok
}

// mark records what came of a send. A failure to record it leaves the row
// at "sending", which the steward's screen shows as not known.
func (b *Business) mark(ctx context.Context, batch, recipient, channel, status string) {
	_ = b.store.MarkSend(context.WithoutCancel(ctx), batch, recipient, channel, status)
}

// Wait waits for every batch running to finish: for a test, and for a
// shutdown that would rather finish a send than leave it unknown.
func (b *Business) Wait() { b.wg.Wait() }

// Batches is how every batch whose key starts with prefix went, earliest
// first: "announce:<day>" is one, "note:<day>:" every note about a day.
func (b *Business) Batches(ctx context.Context, prefix string) ([]Batch, error) {
	return b.store.Batches(ctx, prefix)
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
