package goanalysis

import (
	"fmt"
	"os"
	"testing"

	"github.com/golangci/golangci-lint/v2/internal/cache"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

// The low-level cache is shared by the whole process, so it is set up once, in one directory, for all the tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "goanalysis-cache")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := os.Setenv("GOLANGCI_LINT_CACHE", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	log := logutils.NewStderrLog("skip")

	if _, err := cache.NewCache(timeutils.NewStopwatch("pkgcache", log), log); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	code := m.Run()

	_ = os.RemoveAll(dir)

	os.Exit(code)
}
