package main

import (
	"runtime/debug"
	"testing"
)

func TestTheMemoryLimitIsSetUnlessTheEnvironmentSaysOtherwise(t *testing.T) {
	before := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(before) })

	if got := setMemoryLimit(""); got != memoryLimit || debug.SetMemoryLimit(-1) != memoryLimit {
		t.Errorf("with no GOMEMLIMIT: reported %d, in force %d, want %d", got, debug.SetMemoryLimit(-1), memoryLimit)
	}

	// As the runtime would have, from GOMEMLIMIT=200MiB.
	debug.SetMemoryLimit(200 << 20)

	if got := setMemoryLimit("200MiB"); got != 200<<20 || debug.SetMemoryLimit(-1) != 200<<20 {
		t.Errorf("with GOMEMLIMIT set: reported %d, in force %d, want it left alone", got, debug.SetMemoryLimit(-1))
	}
}
