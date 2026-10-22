package rsvpbus_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/notice/noticebus"
	"github.com/jroedel/stewards/business/domain/notice/stores/noticedb"
	"github.com/jroedel/stewards/business/domain/rsvp/rsvpbus"
	"github.com/jroedel/stewards/business/domain/rsvp/stores/rsvpdb"
	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/mail"
	"github.com/jroedel/stewards/foundation/sms"
	"github.com/jroedel/stewards/foundation/sqldb"
)

type fixture struct {
	rsvps *rsvpbus.Business
	days  *workdaybus.Business
	clock *time.Time
	day   workdaybus.Day
}

// The real stores, because the uniqueness and the cascade are theirs.
func setup(t *testing.T) fixture {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for range 2 { // at every startup
		for _, init := range []func() error{
			func() error { return sqldb.Init(t.Context(), db) },
			func() error { return workdaydb.Init(t.Context(), db) },
			func() error { return rsvpdb.Init(t.Context(), db) },
			func() error { return noticedb.Init(t.Context(), db) },
		} {
			if err := init(); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := sqldb.CheckSchema(t.Context(), db, rsvpdb.Expected); err != nil {
		t.Fatalf("CheckSchema: %v", err)
	}

	clock := time.Date(2026, 10, 21, 9, 0, 0, 0, types.Garden)
	now := func() time.Time { return clock }

	days := workdaybus.NewBusiness(workdaydb.NewStore(db), nil, now)
	notices := noticebus.NewBusiness(noticedb.NewStore(db), &mail.Recorder{}, &sms.Recorder{}, now)

	d, err := days.Create(t.Context(), workdaybus.Fields{
		Starts: time.Date(2026, 10, 24, 8, 0, 0, 0, types.Garden),
		Ends:   time.Date(2026, 10, 24, 11, 0, 0, 0, types.Garden),
		Title:  types.Text{EN: "Planting the rain garden"},
	})
	if err != nil {
		t.Fatal(err)
	}

	return fixture{rsvps: rsvpbus.NewBusiness(rsvpdb.NewStore(db), days, notices, now), days: days, clock: &clock, day: d}
}

func contact(t *testing.T, email, phone string) types.Contact {
	t.Helper()

	c, err := types.ParseContact(email, phone)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestSigningUpForADay(t *testing.T) {
	f := setup(t)

	req, err := f.rsvps.Request(t.Context(), f.day.ID, "  Ana   María ", contact(t, "ana@example.invalid", ""), types.Spanish)
	if err != nil || req.Outcome != rsvpbus.Added || !req.Joined {
		t.Fatalf("Request: %+v %v", req, err)
	}

	if r := req.RSVP; r.Name != "Ana María" || r.Lang != types.Spanish || r.Cancel == "" || r.DayID != f.day.ID {
		t.Errorf("signed up %+v", r)
	}

	// Again with the same email and a new name: the name changes, and
	// nothing is sent.
	again, err := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", ""), types.Spanish)
	if err != nil || again.Outcome != rsvpbus.AlreadyOn || again.RSVP.ID != req.RSVP.ID || again.RSVP.Name != "Ana" {
		t.Errorf("again: %+v %v", again, err)
	}

	// Her phone added: one row, the confirmation to the phone alone, and
	// nobody new joined.
	phone, err := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", "512 555 0123"), types.Spanish)
	if err != nil || phone.Outcome != rsvpbus.Added || phone.Joined || phone.RSVP.ID != req.RSVP.ID || !phone.New.Email.Zero() || phone.New.Phone.Zero() {
		t.Errorf("adding a phone: %+v %v", phone, err)
	}

	// A minute later, so that the order is the order they signed up in
	// rather than the order of two random IDs.
	*f.clock = f.clock.Add(time.Minute)

	if _, err := f.rsvps.Request(t.Context(), f.day.ID, "Luis", contact(t, "", "512 555 0124"), types.English); err != nil {
		t.Fatal(err)
	}

	names, _ := f.rsvps.Coming(t.Context(), f.day.ID)
	if !slices.Equal(names, []string{"Ana", "Luis"}) {
		t.Errorf("coming: %q", names)
	}

	counts, _ := f.rsvps.Counts(t.Context(), []types.ID{f.day.ID, types.NewID()})
	if len(counts) != 1 || counts[f.day.ID] != 2 {
		t.Errorf("counts: %v", counts)
	}

	// Cancelling by the link, twice and with nonsense, is never an error,
	// and only the first press gives back who it took off.
	for i, token := range []string{req.RSVP.Cancel, req.RSVP.Cancel, "nonsense", ""} {
		off, err := f.rsvps.Cancel(t.Context(), token)
		if err != nil {
			t.Errorf("Cancel(%q): %v", token, err)
		}

		if want := i == 0; off.ID.Zero() == want || want && off.Name != "Ana" {
			t.Errorf("Cancel(%q) took off %+v", token, off)
		}
	}

	if names, _ := f.rsvps.Coming(t.Context(), f.day.ID); !slices.Equal(names, []string{"Luis"}) {
		t.Errorf("after cancelling: %q", names)
	}
}

func TestNames(t *testing.T) {
	for _, ok := range []string{"Ana", "José Luis", "Mary-Kate O'Neill", "Ana & the kids", "D. Nguyễn", "Familia Pérez (4)"} {
		if _, err := rsvpbus.Name(ok); err != nil {
			t.Errorf("Name(%q): %v", ok, err)
		}
	}

	for _, bad := range []string{"", "   ", "www.example.com", "visit http://spam", "ana@example.invalid", "<b>Ana</b>", "Ana\x00", "a very long name that goes on and on past forty"} {
		if _, err := rsvpbus.Name(bad); !isInvalid(err) {
			t.Errorf("Name(%q) = %v, want Invalid", bad, err)
		}
	}
}

func TestADayThatIsGoneOrOver(t *testing.T) {
	f := setup(t)

	if _, err := f.rsvps.Request(t.Context(), types.NewID(), "Ana", contact(t, "ana@example.invalid", ""), types.English); !errors.Is(err, rsvpbus.ErrNotFound) {
		t.Errorf("no such day: %v", err)
	}

	// Under way is fine; ended is not.
	*f.clock = time.Date(2026, 10, 24, 10, 0, 0, 0, types.Garden)
	if _, err := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", ""), types.English); err != nil {
		t.Errorf("under way: %v", err)
	}

	*f.clock = time.Date(2026, 10, 24, 11, 0, 0, 0, types.Garden)
	if _, err := f.rsvps.Request(t.Context(), f.day.ID, "Luis", contact(t, "luis@example.invalid", ""), types.English); !errors.Is(err, rsvpbus.ErrOver) {
		t.Errorf("over: %v", err)
	}
}

func TestRemovingADayTakesItsSignUps(t *testing.T) {
	f := setup(t)

	req, _ := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", ""), types.English)

	if err := f.days.Delete(t.Context(), f.day.ID); err != nil {
		t.Fatalf("a day with a sign-up could not be removed: %v", err)
	}

	if _, err := f.rsvps.ByCancel(t.Context(), req.RSVP.Cancel); !errors.Is(err, rsvpbus.ErrNotFound) {
		t.Errorf("the sign-up outlived its day: %v", err)
	}
}

func TestStopAndAStewardsRemove(t *testing.T) {
	f := setup(t)

	both, _ := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", "512 555 0123"), types.English)
	f.rsvps.Request(t.Context(), f.day.ID, "Luis", contact(t, "", "512 555 0124"), types.English)

	for _, p := range []string{"512 555 0123", "512 555 0124"} {
		phone, _ := types.ParsePhone(p)
		if err := f.rsvps.Stopped(t.Context(), phone); err != nil {
			t.Fatal(err)
		}
	}

	rows, _ := f.rsvps.ForDay(t.Context(), f.day.ID)
	if len(rows) != 1 || rows[0].Name != "Ana" || !rows[0].Contact.Phone.Zero() {
		t.Errorf("after STOP: %+v", rows)
	}

	if err := f.rsvps.Remove(t.Context(), both.RSVP.ID); err != nil {
		t.Fatal(err)
	}

	if err := f.rsvps.Remove(t.Context(), both.RSVP.ID); !errors.Is(err, rsvpbus.ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}

func TestTheCapStopsConfirmations(t *testing.T) {
	f := setup(t)

	// Signing up, cancelling and signing up again: the confirmations stop
	// at the cap, and so do the rows.
	for i := range noticebus.PerAddress {
		req, _ := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", ""), types.English)
		if req.Outcome != rsvpbus.Added {
			t.Fatalf("sign-up %d: %s", i+1, req.Outcome)
		}

		_, _ = f.rsvps.Cancel(t.Context(), req.RSVP.Cancel)
	}

	if req, _ := f.rsvps.Request(t.Context(), f.day.ID, "Ana", contact(t, "ana@example.invalid", ""), types.English); req.Outcome != rsvpbus.Capped {
		t.Errorf("past the cap: %s", req.Outcome)
	}

	if names, _ := f.rsvps.Coming(t.Context(), f.day.ID); len(names) != 0 {
		t.Errorf("a capped sign-up is on the day: %q", names)
	}
}

func isInvalid(err error) bool {
	_, ok := errors.AsType[rsvpbus.Invalid](err)
	return ok
}
