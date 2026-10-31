package main

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_gcPacing(t *testing.T) {
	testCases := []struct {
		desc          string
		totalMemory   uint64
		expectedLimit int64
		expectedOK    bool
	}{
		{desc: "unknown memory keeps the defaults", totalMemory: 0},
		{desc: "8 GiB keeps the defaults", totalMemory: 8 * gib},
		{desc: "just below the minimum keeps the defaults", totalMemory: 12*gib - 3, expectedOK: false},
		{desc: "12 GiB", totalMemory: 12 * gib, expectedLimit: 4 * gib, expectedOK: true},
		{desc: "15 GiB", totalMemory: 15 * gib, expectedLimit: 5 * gib, expectedOK: true},
		{desc: "16 GiB", totalMemory: 16 * gib, expectedLimit: 5 * gib, expectedOK: true},
		{desc: "capped on large machines", totalMemory: 64 * gib, expectedLimit: 5 * gib, expectedOK: true},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			percent, limit, ok := gcPacing(test.totalMemory)

			require.Equal(t, test.expectedOK, ok)

			if !ok {
				return
			}

			assert.Equal(t, tunedGCPercent, percent)
			assert.Equal(t, test.expectedLimit, limit)
		})
	}
}

func Test_configureGC_environmentWins(t *testing.T) {
	for _, name := range []string{"GOGC", "GOMEMLIMIT"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "123")

			percentBefore := debug.SetGCPercent(100)
			debug.SetGCPercent(percentBefore)

			limitBefore := debug.SetMemoryLimit(-1)

			configureGC()

			assert.Equal(t, percentBefore, debug.SetGCPercent(percentBefore))
			assert.Equal(t, limitBefore, debug.SetMemoryLimit(limitBefore))
		})
	}
}
