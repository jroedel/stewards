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

func TestSigningUpPutsTheAddressOnTheListAtOnce(t *testing.T) {
	list, _ := setup(t)

	req, err := list.Request(t.Context(), addr(t, "Walker@Example.org"), types.Spanish)
	if err != nil || req.Outcome != subscriberbus.Added {
		t.Fatalf("Request: %+v %v", req, err)
	}

	s := req.Subscriber
	if s.Email.String() != "walker@example.org" || s.Lang != types.Spanish || s.Unsubscribe == "" || s.ID.Zero() {
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
	for i := range subscriberbus.PerAddress {
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

	for i := range subscriberbus.PerHour {
		e := addr(t, "visitor"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.org")
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
