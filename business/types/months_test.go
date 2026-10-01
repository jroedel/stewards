package types_test

import (
	"testing"
	"time"

	"github.com/jroedel/stewards/business/types"
)

func TestASeasonIsASetOfMonths(t *testing.T) {
	penstemon := types.MonthsOf(time.March, time.April, time.May)

	if !penstemon.Has(time.April) || penstemon.Has(time.June) {
		t.Error("Has is wrong")
	}

	for set, want := range map[types.Months]string{
		penstemon: "Mar–May",
		types.MonthsOf(time.May, time.June, time.July, time.August, time.September, time.October, time.November): "May–Nov",
		types.MonthsOf(time.March, time.April, time.September, time.October):                                     "Mar–Apr, Sep–Oct",
		types.MonthsOf(time.January, time.February, time.December):                                               "Jan–Feb, Dec",
		types.MonthsOf(time.February): "Feb",
		types.AllMonths:               "Jan–Dec",
		0:                             "",
	} {
		if got := set.String(); got != want {
			t.Errorf("%012b: %q, want %q", set, got, want)
		}
	}

	if (types.AllMonths + 1).Valid() || !types.AllMonths.Valid() {
		t.Error("Valid is wrong")
	}

	if got := types.MonthsOf(time.Month(13), time.Month(0)); !got.Zero() {
		t.Errorf("months that do not exist were added: %012b", got)
	}

	if got := penstemon.Each(); len(got) != 3 || got[0] != time.March {
		t.Errorf("Each = %v", got)
	}
}
