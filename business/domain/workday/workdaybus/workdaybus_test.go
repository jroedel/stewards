package workdaybus_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jroedel/stewards/business/domain/workday/stores/workdaydb"
	"github.com/jroedel/stewards/business/domain/workday/workdaybus"
	"github.com/jroedel/stewards/business/types"
	"github.com/jroedel/stewards/foundation/sqldb"
)

// The real store, over a fresh database, with a clock the test moves.
func setup(t *testing.T) (*workdaybus.Business, *time.Time) {
	t.Helper()

	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, init := range []func() error{
		func() error { return sqldb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) },
		func() error { return workdaydb.Init(t.Context(), db) }, // at every startup
	} {
		if err := init(); err != nil {
			t.Fatal(err)
		}
	}

	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, types.Garden)

	return workdaybus.NewBusiness(workdaydb.NewStore(db), nil, func() time.Time { return clock }), &clock
}

// at is a time on a date in October 2026, in the garden's zone.
func at(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, types.Garden)
}

func morning(day int, title string) workdaybus.Fields {
	return workdaybus.Fields{Starts: at(day, 8, 0), Ends: at(day, 11, 30), Title: types.Text{EN: title}}
}

func TestADayRoundTrips(t *testing.T) {
	days, _ := setup(t)

	f := workdaybus.Fields{
		Starts: at(10, 8, 0), Ends: at(10, 11, 30),
		Title:   types.Text{EN: "  Planting the rain garden ", ES: "Plantar el jardín de lluvia"},
		Details: types.Text{EN: "Meet at the fire pit. Bring gloves if you have them."},
	}

	made, err := days.Create(t.Context(), f)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := days.ByID(t.Context(), made.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}

	if !got.Starts.Equal(f.Starts) || !got.Ends.Equal(f.Ends) {
		t.Errorf("times %v–%v, want %v–%v", got.Starts, got.Ends, f.Starts, f.Ends)
	}

	if got.Title.EN != "Planting the rain garden" || got.Title.ES != "Plantar el jardín de lluvia" {
		t.Errorf("title %+v, want it trimmed and both halves kept", got.Title)
	}

	if got.Details != f.Details {
		t.Errorf("details %+v", got.Details)
	}
}

func TestUpcomingIsWhatIsNotOverSoonestFirst(t *testing.T) {
	days, clock := setup(t)

	for _, f := range []workdaybus.Fields{morning(24, "Third"), morning(10, "Second"), morning(1, "Today, under way")} {
		if _, err := days.Create(t.Context(), f); err != nil {
			t.Fatalf("Create %s: %v", f.Title.EN, err)
		}
	}

	titles := func() []string {
		up, err := days.Upcoming(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		var out []string
		for _, d := range up {
			out = append(out, d.Title.EN)
		}

		return out
	}

	// At nine on the first, the morning of the first is under way and
	// still upcoming: someone who scans the sign then should learn the
	// stewards are out now.
	if got := titles(); len(got) != 3 || got[0] != "Today, under way" || got[1] != "Second" {
		t.Errorf("upcoming %q, want today's first, then the 10th, then the 24th", got)
	}

	*clock = at(1, 11, 30) // the moment it ends

	if got := titles(); len(got) != 2 || got[0] != "Second" {
		t.Errorf("upcoming at the end of the first day %q, want it gone", got)
	}

	recent, err := days.Recent(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}

	if len(recent) != 1 || recent[0].Title.EN != "Today, under way" {
		t.Errorf("recent %v, want the day just over", recent)
	}
}

func TestADayIsRefusedWhenItCannotBeRight(t *testing.T) {
	days, _ := setup(t)

	for name, tc := range map[string]struct {
		f     workdaybus.Fields
		field string
	}{
		"no start":          {workdaybus.Fields{Ends: at(10, 11, 0), Title: types.Text{EN: "x"}}, "date"},
		"no end":            {workdaybus.Fields{Starts: at(10, 8, 0), Title: types.Text{EN: "x"}}, "ends"},
		"ends before":       {workdaybus.Fields{Starts: at(10, 8, 0), Ends: at(10, 7, 0), Title: types.Text{EN: "x"}}, "ends"},
		"ends as it starts": {workdaybus.Fields{Starts: at(10, 8, 0), Ends: at(10, 8, 0), Title: types.Text{EN: "x"}}, "ends"},
		"pm for am":         {workdaybus.Fields{Starts: at(10, 8, 0), Ends: at(10, 23, 0), Title: types.Text{EN: "x"}}, "ends"},
		"no title":          {workdaybus.Fields{Starts: at(10, 8, 0), Ends: at(10, 11, 0), Title: types.Text{EN: "   "}}, "title"},
	} {
		_, err := days.Create(t.Context(), tc.f)

		invalid, ok := errors.AsType[workdaybus.Invalid](err)
		if !ok || invalid.Field != tc.field {
			t.Errorf("%s: %v, want a problem with %s", name, err, tc.field)
		}
	}

	past := workdaybus.Fields{Starts: at(1, 6, 0), Ends: at(1, 8, 0), Title: types.Text{EN: "Dawn watering"}}
	if _, err := days.Create(t.Context(), past); err == nil {
		t.Error("a day that ended an hour ago was added")
	}
}

// Editing a day that is over is how a steward corrects the record, so it is
// allowed where adding one is not.
func TestADayThatIsOverCanStillBeCorrected(t *testing.T) {
	days, clock := setup(t)

	d, err := days.Create(t.Context(), morning(3, "Mulching"))
	if err != nil {
		t.Fatal(err)
	}

	*clock = at(5, 9, 0)

	f := morning(3, "Mulching the skinny bed")
	if _, err := days.Update(t.Context(), d.ID, f); err != nil {
		t.Fatalf("Update of a day just over: %v", err)
	}

	got, _ := days.ByID(t.Context(), d.ID)
	if got.Title.EN != "Mulching the skinny bed" || !got.UpdatedAt.Equal(*clock) || !got.CreatedAt.Equal(at(1, 9, 0)) {
		t.Errorf("after the update: %+v", got)
	}
}

func TestAMissingDayIsNotFound(t *testing.T) {
	days, _ := setup(t)

	if _, err := days.Update(t.Context(), types.NewID(), morning(10, "x")); !errors.Is(err, workdaybus.ErrNotFound) {
		t.Errorf("Update: %v, want ErrNotFound", err)
	}

	if err := days.Delete(t.Context(), types.NewID()); !errors.Is(err, workdaybus.ErrNotFound) {
		t.Errorf("Delete: %v, want ErrNotFound", err)
	}

	d, err := days.Create(t.Context(), morning(10, "Weeding"))
	if err != nil {
		t.Fatal(err)
	}

	if err := days.Delete(t.Context(), d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := days.ByID(t.Context(), d.ID); !errors.Is(err, workdaybus.ErrNotFound) {
		t.Errorf("ByID after Delete: %v, want ErrNotFound", err)
	}
}
