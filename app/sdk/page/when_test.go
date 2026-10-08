package page_test

import (
	"testing"
	"time"

	"github.com/jroedel/stewards/app/sdk/page"
	"github.com/jroedel/stewards/business/types"
)

// In the garden's zone whatever zone the time arrives in: the store hands
// back UTC, and 13:00 UTC on a day in October is 8 am in Austin.
func TestADayIsWrittenInTheGardensTime(t *testing.T) {
	utc := func(h, m int) time.Time { return time.Date(2026, 10, 10, h, m, 0, 0, time.UTC) }

	if got := page.Date(utc(13, 0)).In(types.English); got != "Saturday, October 10" {
		t.Errorf("Date %q", got)
	}

	for _, tc := range []struct {
		starts, ends time.Time
		want         string
	}{
		{utc(13, 0), utc(16, 30), "8:00–11:30 am"},
		{utc(16, 0), utc(18, 0), "11:00 am – 1:00 pm"},
		{utc(19, 0), utc(22, 0), "2:00–5:00 pm"},
		{utc(14, 0), utc(17, 0), "9:00 am – noon"},
		{utc(17, 0), utc(20, 0), "noon – 3:00 pm"},
	} {
		if got := page.Hours(tc.starts, tc.ends).In(types.English); got != tc.want {
			t.Errorf("Hours %q, want %q", got, tc.want)
		}
	}

	// After the clocks change: 8 am on the first Saturday of November is
	// 14:00 UTC, not 13:00.
	nov := time.Date(2026, 11, 7, 14, 0, 0, 0, time.UTC)
	if got := page.Hours(nov, nov.Add(2*time.Hour)).In(types.English); got != "8:00–10:00 am" {
		t.Errorf("after the change to standard time: %q", got)
	}
}
