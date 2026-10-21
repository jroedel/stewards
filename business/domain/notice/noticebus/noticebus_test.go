package noticebus_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/notice/stores/noticedb"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sms"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The real store, because the caps are its statements.
func setup(t *testing.T, texts sms.Sender) (*noticebus.Business, *mail.Recorder, *time.Time) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for range 2 { // at every startup
		if err := noticedb.Init(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, noticedb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	clock := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	mails := &mail.Recorder{}

	return noticebus.NewBusiness(noticedb.NewStore(db), mails, texts, func() time.Time { return clock }), mails, &clock
}

// Invented contacts only: example.invalid, and the 555-01xx block.
func contact(t *testing.T, email, phone string) types.Contact {
	t.Helper()

	c, err := types.ParseContact(email, phone)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestANoticeGoesEachWayItCan(t *testing.T) {
	texts := &sms.Recorder{}
	b, mails, _ := setup(t, texts)

	n := noticebus.Notice{Subject: "See you Saturday", Email: "Line one.\nLine two.", Text: "Short."}

	r := b.Send(t.Context(), contact(t, "ana@example.invalid", "512 555 0123"), n)
	if !r.Mailed || !r.Texted || r.MailErr != nil || r.TextErr != nil {
		t.Fatalf("both: %+v", r)
	}

	m, _ := mails.Last()
	if m.To != "ana@example.invalid" || m.Subject != "See you Saturday" || m.Text != "Line one.\r\nLine two." {
		t.Errorf("the email: %+v", m)
	}

	if tx, _ := texts.Last(); tx.To != "+15125550123" || tx.Body != "Short." {
		t.Errorf("the text: %+v", tx)
	}

	// Email only, and phone only: the other half is not attempted.
	if r := b.Send(t.Context(), contact(t, "luis@example.invalid", ""), n); !r.Mailed || r.Texted {
		t.Errorf("email only: %+v", r)
	}

	if r := b.Send(t.Context(), contact(t, "", "512 555 0124"), n); r.Mailed || !r.Texted {
		t.Errorf("phone only: %+v", r)
	}

	if len(mails.Sent) != 2 || len(texts.Sent()) != 2 {
		t.Errorf("sent %d emails and %d texts, want 2 and 2", len(mails.Sent), len(texts.Sent()))
	}
}

func TestAShortEmailIsTheText(t *testing.T) {
	texts := &sms.Recorder{}
	b, _, _ := setup(t, texts)

	n := noticebus.Notice{Subject: "s", Email: "We'll decide at 9 am whether we're rained out.\n"}
	if !n.Same() || !noticebus.ShortEnough(n.Email) {
		t.Errorf("a short notice with no text of its own is not the same both ways")
	}

	b.Send(t.Context(), contact(t, "", "512 555 0123"), n)

	if tx, _ := texts.Last(); tx.Body != "We'll decide at 9 am whether we're rained out." {
		t.Errorf("the text = %q", tx.Body)
	}

	if noticebus.ShortEnough(strings.Repeat("a", noticebus.TextLimit+1)) {
		t.Error("a body past the limit is short enough")
	}
}

func TestWithTextsOffAPhoneIsLeftAlone(t *testing.T) {
	b, mails, _ := setup(t, nil)

	if b.Texts() {
		t.Error("Texts with no sender")
	}

	r := b.Send(t.Context(), contact(t, "ana@example.invalid", "512 555 0123"), noticebus.Notice{Subject: "s", Email: "e"})
	if !r.Mailed || r.Texted || r.TextErr != nil || len(mails.Sent) != 1 {
		t.Errorf("%+v", r)
	}

	// And a phone-only contact is nobody the app can reach, so no claim.
	if ok, err := b.Claim(t.Context(), contact(t, "", "512 555 0123")); ok || err != nil {
		t.Errorf("Claim for an unreachable phone = %v, %v", ok, err)
	}
}

func TestAStopTakesTheNumberOffEveryList(t *testing.T) {
	texts := &sms.Recorder{Refuse: func(m sms.Message) error {
		if m.To == "+15125550123" {
			return sms.Refused{Code: 21610, Message: "unsubscribed recipient"}
		}
		return nil
	}}
	b, _, _ := setup(t, texts)

	var heard []string
	for _, list := range []string{"the email list", "a day"} {
		b.OnStopped(func(_ context.Context, p types.Phone) error {
			heard = append(heard, list+" "+p.String())
			return nil
		})
	}

	r := b.Send(t.Context(), contact(t, "ana@example.invalid", "512 555 0123"), noticebus.Notice{Subject: "s", Email: "e"})
	if !r.Stopped || r.Texted || !r.Mailed || !errors.Is(r.TextErr, sms.ErrOptedOut) {
		t.Errorf("%+v", r)
	}

	if strings.Join(heard, ", ") != "the email list +15125550123, a day +15125550123" {
		t.Errorf("heard %q", heard)
	}

	// Another refusal is a failure, not a STOP.
	heard = nil
	texts.Refuse = func(sms.Message) error { return sms.Refused{Code: 21614, Message: "a landline"} }

	if r := b.Send(t.Context(), contact(t, "", "512 555 0124"), noticebus.Notice{Email: "e"}); r.Stopped || r.TextErr == nil || len(heard) != 0 {
		t.Errorf("a landline: %+v, heard %q", r, heard)
	}
}

func TestTheCaps(t *testing.T) {
	b, _, clock := setup(t, &sms.Recorder{})

	ana := contact(t, "ana@example.invalid", "512 555 0123")

	for i := range noticebus.PerAddress {
		if ok, err := b.Claim(t.Context(), ana); !ok || err != nil {
			t.Fatalf("claim %d: %v %v", i+1, ok, err)
		}
	}

	if ok, _ := b.Claim(t.Context(), ana); ok {
		t.Error("a fourth claim in a day was allowed")
	}

	// The phone alone has been claimed three times too: a stranger cannot
	// reach it by pairing it with a new address.
	if ok, _ := b.Claim(t.Context(), contact(t, "other@example.invalid", "512 555 0123")); ok {
		t.Error("a capped phone was reached beside a new address")
	}

	// And that refusal claimed nothing for the new address: all or none.
	for i := range noticebus.PerAddress {
		if ok, _ := b.Claim(t.Context(), contact(t, "other@example.invalid", "")); !ok {
			t.Fatalf("the new address alone, claim %d, was refused", i+1)
		}
	}

	// A day later, all of it again.
	*clock = clock.Add(25 * time.Hour)
	if err := b.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}

	if ok, _ := b.Claim(t.Context(), ana); !ok {
		t.Error("the cap did not lift after a day")
	}
}

func TestTheHourlyCap(t *testing.T) {
	b, _, clock := setup(t, &sms.Recorder{})

	// Each contact is an email and a phone, so each claim is two messages.
	for i := range noticebus.PerHour / 2 {
		c := contact(t, fmt.Sprintf("v%d@example.invalid", i), fmt.Sprintf("512 555 01%02d", i))

		if ok, err := b.Claim(t.Context(), c); !ok || err != nil {
			t.Fatalf("claim %d: %v %v", i+1, ok, err)
		}
	}

	if ok, _ := b.Claim(t.Context(), contact(t, "late@example.invalid", "")); ok {
		t.Error("a claim past the hourly cap was allowed")
	}

	*clock = clock.Add(61 * time.Minute)

	if ok, _ := b.Claim(t.Context(), contact(t, "late@example.invalid", "")); !ok {
		t.Error("the hourly cap did not lift after an hour")
	}
}
