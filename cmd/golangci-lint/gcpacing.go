package main

import (
	"os"
	"runtime/debug"
)

const (
	gib = 1 << 30

	// A run allocates tens of GiB while keeping only a few GiB alive, so GC at the default pace runs about every 2x the live heap.
	// A higher GC percent, capped by a soft memory limit, runs far fewer cycles without growing the peak heap.
	tunedGCPercent = 400

	// The limit is a third of the memory, capped at maxMemoryLimit (a bit under the peak heap of the default pacing on a 3 GiB live heap).
	// Below minMemoryLimit the live heap of a large repository would exceed the limit and the GC would run continuously,
	// so small machines keep the Go defaults.
	memoryLimitDivisor = 3
	minMemoryLimit     = 4 * gib
	maxMemoryLimit     = 5 * gib
)

// gcPacing returns the GC percent and soft memory limit to use for a machine with totalMemory bytes of memory available.
// It reports false when the Go defaults should be kept.
func gcPacing(totalMemory uint64) (percent int, limit int64, ok bool) {
	limit = int64(min(totalMemory/memoryLimitDivisor, maxMemoryLimit))
	if limit < minMemoryLimit {
		return 0, 0, false
	}

	return tunedGCPercent, limit, true
}

// configureGC sets the GC pacing unless the environment already configures the GC.
func configureGC() {
	_, hasGOGC := os.LookupEnv("GOGC")
	_, hasGOMEMLIMIT := os.LookupEnv("GOMEMLIMIT")

	if hasGOGC || hasGOMEMLIMIT {
		return
	}

	percent, limit, ok := gcPacing(availableMemory())
	if !ok {
		return
	}

	debug.SetGCPercent(percent)
	debug.SetMemoryLimit(limit)
}
