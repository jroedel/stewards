package subscriberbus_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/subscriber/stores/subscriberdb"
	"github.com/jroedel/stewards/business/domain/subscriber/subscriberbus"
	"github.com/jroedel/stewards/business/types"
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
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, subscriberdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	clock := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

	return subscriberbus.NewBusiness(subscriberdb.NewStore(db), func() time.Time { return clock }), &clock
}

func addr(t *testing.T, s string) types.Email {
	t.Helper()

	e, err := types.ParseEmail(s)
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func TestAnAddressIsOnTheListOnlyOnceConfirmed(t *testing.T) {
	list, _ := setup(t)

	req, err := list.Request(t.Context(), addr(t, "Walker@Example.org"), types.Spanish)
	if err != nil || req.Outcome != subscriberbus.Sent || req.Confirm == "" {
		t.Fatalf("Request: %+v %v", req, err)
	}

	if on, _ := list.Confirmed(t.Context()); len(on) != 0 {
		t.Fatal("a pending address is on the list")
	}

	if n, _ := list.PendingCount(t.Context()); n != 1 {
		t.Errorf("pending %d, want 1", n)
	}

	s, err := list.Confirm(t.Context(), req.Confirm)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if s.Email.String() != "walker@example.org" || s.Lang != types.Spanish || s.Unsubscribe == "" || !s.Confirmed() {
		t.Errorf("confirmed %+v", s)
	}

	on, _ := list.Confirmed(t.Context())
	if len(on) != 1 || on[0].ID != s.ID {
		t.Errorf("the list %+v", on)
	}

	// The link works once.
	if _, err := list.Confirm(t.Context(), req.Confirm); !errors.Is(err, subscriberbus.ErrDenied) {
		t.Errorf("a second use: %v, want ErrDenied", err)
	}

	// Signing up again once on the list sends nothing.
	again, err := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
	if err != nil || again.Outcome != subscriberbus.AlreadyOn || again.Confirm != "" {
		t.Errorf("again: %+v %v", again, err)
	}
}

func TestABadOrOldLinkIsDenied(t *testing.T) {
	list, clock := setup(t)

	req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)

	for _, bad := range []string{"", "nodot", "." + req.Confirm, req.Confirm + "x", types.NewID().String() + ".abc"} {
		if _, err := list.Confirm(t.Context(), bad); !errors.Is(err, subscriberbus.ErrDenied) {
			t.Errorf("Confirm(%q): %v, want ErrDenied", bad, err)
		}
	}

	*clock = clock.Add(subscriberbus.ConfirmLife)

	if _, err := list.Confirm(t.Context(), req.Confirm); !errors.Is(err, subscriberbus.ErrDenied) {
		t.Errorf("an expired link: %v, want ErrDenied", err)
	}

	// And pruning forgets the expired sign-up altogether.
	if err := list.Prune(t.Context()); err != nil {
		t.Fatal(err)
	}

	if n, _ := list.PendingCount(t.Context()); n != 0 {
		t.Errorf("pending after prune %d", n)
	}
}

// A new confirmation replaces the old: only the latest link works, and the
// address keeps its row.
func TestAskingAgainReplacesTheLink(t *testing.T) {
	list, _ := setup(t)

	first, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
	second, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)

	if first.SubscriberID != second.SubscriberID {
		t.Error("asking again made a second row")
	}

	if _, err := list.Confirm(t.Context(), first.Confirm); !errors.Is(err, subscriberbus.ErrDenied) {
		t.Errorf("the replaced link: %v, want ErrDenied", err)
	}

	if _, err := list.Confirm(t.Context(), second.Confirm); err != nil {
		t.Errorf("the latest link: %v", err)
	}
}

func TestTheCapsStopTheMail(t *testing.T) {
	list, clock := setup(t)

	// One address: PerAddress in a day, then nothing until a day has passed.
	for i := range subscriberbus.PerAddress {
		if req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); req.Outcome != subscriberbus.Sent {
			t.Fatalf("mail %d: %s", i+1, req.Outcome)
		}
	}

	if req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); req.Outcome != subscriberbus.Capped || req.Confirm != "" {
		t.Errorf("one past the per-address cap: %+v", req)
	}

	*clock = clock.Add(24*time.Hour + time.Minute)

	if req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); req.Outcome != subscriberbus.Sent {
		t.Errorf("a day later: %s", req.Outcome)
	}

	// Every address together: PerHour, then nothing -- and no new rows.
	*clock = clock.Add(2 * time.Hour)

	for i := range subscriberbus.PerHour {
		e := addr(t, "visitor"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.org")
		if req, _ := list.Request(t.Context(), e, types.English); req.Outcome != subscriberbus.Sent {
			t.Fatalf("mail %d of the hour: %s", i+1, req.Outcome)
		}
	}

	before, _ := list.PendingCount(t.Context())

	if req, _ := list.Request(t.Context(), addr(t, "one-more@example.org"), types.English); req.Outcome != subscriberbus.Capped {
		t.Errorf("one past the hourly cap: %s", req.Outcome)
	}

	if after, _ := list.PendingCount(t.Context()); after != before {
		t.Errorf("a capped request added a row: %d then %d", before, after)
	}
}

func TestUnsubscribingDeletesTheAddress(t *testing.T) {
	list, _ := setup(t)

	req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
	s, _ := list.Confirm(t.Context(), req.Confirm)

	if err := list.Unsubscribe(t.Context(), s.Unsubscribe); err != nil {
		t.Fatal(err)
	}

	if on, _ := list.Confirmed(t.Context()); len(on) != 0 {
		t.Errorf("still on the list: %+v", on)
	}

	// Again, and with nonsense: still not an error.
	for _, token := range []string{s.Unsubscribe, "nonsense", ""} {
		if err := list.Unsubscribe(t.Context(), token); err != nil {
			t.Errorf("Unsubscribe(%q): %v", token, err)
		}
	}

	// Gone means gone: signing up again starts over, pending.
	if again, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English); again.Outcome != subscriberbus.Sent {
		t.Errorf("signing up after leaving: %s", again.Outcome)
	}
}

func TestAStewardRemovesSomebody(t *testing.T) {
	list, _ := setup(t)

	req, _ := list.Request(t.Context(), addr(t, "walker@example.org"), types.English)
	s, _ := list.Confirm(t.Context(), req.Confirm)

	if err := list.Remove(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}

	if err := list.Remove(t.Context(), s.ID); !errors.Is(err, subscriberbus.ErrNotFound) {
		t.Errorf("removing twice: %v, want ErrNotFound", err)
	}
}
