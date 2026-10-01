package types

import (
	"time"

	// The zone database, built into the binary. Without it, LoadLocation
	// reads the host's /usr/share/zoneinfo, and a host without one (a slim
	// container, a shared host that trimmed it) would leave Garden at UTC:
	// a work day typed as 8 am shown to volunteers as 1 pm. About 450 kB
	// buys a time that is right everywhere the binary runs.
	_ "time/tzdata"
)

// Garden is the garden's time zone, Austin's. A work day starts at 8 am here,
// and "today" on a steward's screen is today in Texas, wherever the server
// keeps its clock.
//
// Here rather than in the app that first needed it, because two apps now do:
// the steward screens date an API key's last use by it, and the work-day
// screens read and show every start and end in it.
var Garden = mustLoad("America/Chicago")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// Unreachable with time/tzdata imported above; a panic at startup,
		// if it ever is reached, beats a day shown at the wrong hour.
		panic("loading the time zone " + name + ": " + err.Error())
	}

	return loc
}
