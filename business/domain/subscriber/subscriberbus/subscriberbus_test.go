package subscriberbus_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/notice/stores/noticedb"
	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sms"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The real store, because the caps and the single use of a link are its
// statements.
func setup(t *testing.T) (*subscriberbus.Business, *time.Time) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return subscriberdb.Init(t.Context(), db) },
		func() error { return subscriberdb.Init(t.Context(), db) }, // at every startup
		func() error { return noticedb.Init(t.Context(), db) },
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, subscriberdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	clock := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

	now := func() time.Time { return clock }
	notices := noticebus.NewBusiness(noticedb.NewStore(db), &mail.Recorder{}, &sms.Recorder{}, now)

	return subscriberbus.NewBusiness(subscriberdb.NewStore(db), notices, now), &clock
}

// addr is an email-only contact; contact is either or both.
func addr(t *testing.T, s string) types.Contact { return contact(t, s, "") }

func contact(t *testing.T, email, phone string) types.Contact {
	t.Helper()

	c, err := types.ParseContact(email, phone)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestSigningUpPutsTheAddressOnTheListAtOnce(t *testing.T) {
	list, _ := setup(t)

	req, err := list.Request(t.Context(), addr(t, "Walker@Example.org"), types.Spanish)
	if err != nil || req.Outcome != subscriberbus.Added {
		t.Fatalf("Request: %+v %v", req, err)
	}

	s := req.Subscriber
	if s.Contact.Email.String() != "walker@example.org" || s.Lang != types.Spanish || s.Unsubscribe == "" || s.ID.Zero() {
		t.Errorf("added %+v", s)
	}

	on, _ := list.All(t.Context())
	if len(on) != 1 || on[0].ID != s.ID || on[0].Unsubscribe != s.Unsubscribe {
		t.Errorf("the list %+v", on)
	}

	// Signing up again sends nothing and changes nothing.
	again, err := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
	if err != nil || again.Outcome != subscriberbus.AlreadyOn || again.Subscriber.ID != s.ID {
		t.Errorf("again: %+v %v", again, err)
	}
}

func TestTheCapsStopTheMail(t *testing.T) {
	list, clock := setup(t)

	// One address, leaving and coming back: PerAddress welcomes in a day,
	// then nothing -- and not on the list -- until a day has passed.
	for i := range noticebus.PerAddress {
		req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
		if req.Outcome != subscriberbus.Added {
			t.Fatalf("welcome %d: %s", i+1, req.Outcome)
		}

		if err := list.Unsubscribe(t.Context(), req.Subscriber.Unsubscribe); err != nil {
			t.Fatal(err)
		}
	}

	if req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); req.Outcome != subscriberbus.Capped {
		t.Errorf("one past the per-address cap: %+v", req)
	}

	if on, _ := list.All(t.Context()); len(on) != 0 {
		t.Errorf("a capped request added somebody: %+v", on)
	}

	*clock = clock.Add(24*time.Hour + time.Minute)

	if req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); req.Outcome != subscriberbus.Added {
		t.Errorf("a day later: %s", req.Outcome)
	}

	// Every address together: PerHour, then nothing, and no new rows.
	*clock = clock.Add(2 * time.Hour)

	for i := range noticebus.PerHour {
		e := addr(t, fmt.Sprintf("visitor%d@example.org", i))
		if req, _ := list.Request(t.Context(), e, types.English); req.Outcome != subscriberbus.Added {
			t.Fatalf("welcome %d of the hour: %s", i+1, req.Outcome)
		}
	}

	before, _ := list.All(t.Context())

	if req, _ := list.Request(t.Context(), addr(t, "one-more@example.org"), types.English); req.Outcome != subscriberbus.Capped {
		t.Errorf("one past the hourly cap: %s", req.Outcome)
	}

	if after, _ := list.All(t.Context()); len(after) != len(before) {
		t.Errorf("a capped request added a row: %d then %d", len(before), len(after))
	}
}

func TestUnsubscribingDeletesTheAddress(t *testing.T) {
	list, _ := setup(t)

	req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)

	if err := list.Unsubscribe(t.Context(), req.Subscriber.Unsubscribe); err != nil {
		t.Fatal(err)
	}

	if on, _ := list.All(t.Context()); len(on) != 0 {
		t.Errorf("still on the list: %+v", on)
	}

	// Again, and with nonsense: still not an error.
	for _, token := range []string{req.Subscriber.Unsubscribe, "nonsense", ""} {
		if err := list.Unsubscribe(t.Context(), token); err != nil {
			t.Errorf("Unsubscribe(%q): %v", token, err)
		}
	}
}

func TestAStewardRemovesSomebody(t *testing.T) {
	list, _ := setup(t)

	req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)

	if err := list.Remove(t.Context(), req.Subscriber.ID); err != nil {
		t.Fatal(err)
	}

	if err := list.Remove(t.Context(), req.Subscriber.ID); !errors.Is(err, subscriberbus.ErrNotFound) {
		t.Errorf("removing twice: %v, want ErrNotFound", err)
	}
}

func TestAPhoneJoinsItsPersonOrStandsAlone(t *testing.T) {
	list, _ := setup(t)

	first, _ := list.Request(t.Context(), addr(t, "ana@example.invalid"), types.Spanish)

	// Ana again, with her phone: one row, the phone added, welcomed alone.
	both, err := list.Request(t.Context(), contact(t, "ana@example.invalid", "512 555 0123"), types.English)
	if err != nil || both.Outcome != subscriberbus.Added || both.Subscriber.ID != first.Subscriber.ID {
		t.Fatalf("adding a phone: %+v %v", both, err)
	}

	if !both.New.Email.Zero() || both.New.Phone.String() != "+15125550123" {
		t.Errorf("the welcome goes to %+v, want the phone alone", both.New)
	}

	if both.Subscriber.Contact.Phone.Zero() || both.Subscriber.Lang != types.Spanish {
		t.Errorf("the row is %+v; want the phone on it and its language kept", both.Subscriber)
	}

	// The same again: nothing new.
	if again, _ := list.Request(t.Context(), contact(t, "ana@example.invalid", "512 555 0123"), types.English); again.Outcome != subscriberbus.AlreadyOn {
		t.Errorf("again: %+v", again)
	}

	// Somebody else's address beside Ana's phone: the address is new, but
	// Ana's row has an address already, so it is a row of its own and
	// Ana's is untouched.
	luis, _ := list.Request(t.Context(), contact(t, "luis@example.invalid", "512 555 0123"), types.English)
	if luis.Outcome != subscriberbus.Added || luis.Subscriber.ID == first.Subscriber.ID || !luis.New.Phone.Zero() {
		t.Errorf("luis: %+v", luis)
	}

	if ana, _ := list.All(t.Context()); len(ana) != 2 || !slices.ContainsFunc(ana, func(s subscriberbus.Subscriber) bool {
		return s.ID == first.Subscriber.ID && s.Contact.Phone.String() == "+15125550123"
	}) {
		t.Errorf("the list: %+v", ana)
	}

	// A phone alone.
	solo, _ := list.Request(t.Context(), contact(t, "", "512 555 0124"), types.English)
	if solo.Outcome != subscriberbus.Added || !solo.Subscriber.Contact.Email.Zero() {
		t.Errorf("phone only: %+v", solo)
	}

	// STOP: the phone-only row goes, and Ana keeps her email.
	for _, p := range []string{"512 555 0123", "512 555 0124"} {
		phone, _ := types.ParsePhone(p)
		if err := list.Stopped(t.Context(), phone); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := list.All(t.Context())
	if len(all) != 2 || slices.ContainsFunc(all, func(s subscriberbus.Subscriber) bool { return !s.Contact.Phone.Zero() }) {
		t.Errorf("after STOP: %+v", all)
	}
}
