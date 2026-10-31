package revive

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const restoredChecksSource = `// Package sample ...
package sample

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

var timeoutSecs time.Duration

var errFailed error = errors.New("failed")

func report(t *testing.T) {
	t.Error(fmt.Sprintf("failed after %s", timeoutSecs))
}

func TestReport(t *testing.T) {
	report(t)
}
`

const cgoSource = `// Package cg ...
package cg

/*
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Free releases.
func Free(s *C.char) error {
	C.free(unsafe.Pointer(s))
	return errors.New(fmt.Sprintf("bad %d", 1))
}

// Range ...
func Range(m map[string]int) {
	for k, _ := range m {
		_ = k
	}
}
`

const cgoLinesSource = `// Package cl ...
package cl

/*
#include <stdlib.h>
*/
import "C"

import "unsafe"

// Free releases a C string and this comment line is deliberately much longer than the limit.
func Free(s *C.char) {
	C.free(unsafe.Pointer(s))
}
`

const cgoLinesConfig = `version: "2"
linters:
  default: none
  enable: [revive]
  settings:
    revive:
      rules:
        - name: line-length-limit
          arguments: [60]
        - name: file-length-limit
          arguments:
            - max: 15
`

const lineDirectiveSource = `//line gen.tmpl:10
// Package ln ...
package ln

import "time"

var timeoutSecs time.Duration
`

// TestRestoredChecksFire builds the binary with and without -trimpath:
// the revive rules that need type information must report the same findings in both.
func TestRestoredChecksFire(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary twice")
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/sample\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "sample.go"), []byte(restoredChecksSource), 0o600))

	cgoProject := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cgoProject, "go.mod"), []byte("module example.com/cg\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cgoProject, "c.go"), []byte(cgoSource), 0o600))

	cgoLinesProject := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cgoLinesProject, "go.mod"), []byte("module example.com/cl\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cgoLinesProject, "c.go"), []byte(cgoLinesSource), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cgoLinesProject, ".golangci.yml"), []byte(cgoLinesConfig), 0o600))

	lineProject := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(lineProject, "go.mod"), []byte("module example.com/ln\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(lineProject, "l.go"), []byte(lineDirectiveSource), 0o600))

	for name, buildFlags := range map[string][]string{
		"untrimmed": nil,
		"trimmed":   {"-trimpath"},
	} {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "golangci-lint")

			args := append([]string{"build"}, buildFlags...)
			args = append(args, "-o", binary, "./cmd/golangci-lint")

			build := exec.CommandContext(t.Context(), "go", args...)
			build.Dir = root

			output, err := build.CombinedOutput()
			require.NoError(t, err, string(output))

			lintProject := func(dir string) string {
				run := exec.CommandContext(t.Context(), binary, "run", "--allow-parallel-runners", "--default=none", "-Erevive",
					"--output.text.print-issued-lines=false", "--output.text.path=stdout", "./...")
				run.Dir = dir

				// The exit code is 1 when there are issues.
				out, _ := run.CombinedOutput()

				return string(out)
			}

			output = []byte(lintProject(project))

			assert.Contains(t, string(output), `sample.go:11:5: time-naming: var timeoutSecs is of type time.Duration; don't use unit-specific suffix "Secs" (revive)`)
			assert.Contains(t, string(output), "sample.go:13:15: var-declaration: should omit type error from declaration of var errFailed; it will be inferred from the right-hand side (revive)")
			assert.Contains(t, string(output), "sample.go:16:2: errorf: should replace t.Error(fmt.Sprintf(...)) with t.Errorf(...) (revive)")

			// The rules read the content the cgo-processed file was parsed from: no panic, same issues as a file without cgo.
			cgoOutput := lintProject(cgoProject)
			assert.NotContains(t, cgoOutput, "panic")
			assert.Contains(t, cgoOutput, "c.go:18:9: errorf: should replace errors.New(fmt.Sprintf(...)) with fmt.Errorf(...) (revive)")
			assert.Contains(t, cgoOutput, "c.go:23:9: range: should omit 2nd value from range; this loop is equivalent to `for k := range ...` (revive)")

			// The rules that read lines see the reported file, not the cgo-generated one.
			linesOutput := lintProject(cgoLinesProject)
			assert.Contains(t, linesOutput, "c.go:11: line-length-limit: line is 93 characters, out of limit 60 (revive)")
			assert.Contains(t, linesOutput, "1 issues:")
			assert.NotContains(t, linesOutput, "file-length-limit")

			// A //line directive before the package clause must not silence the package.
			assert.Contains(t, lintProject(lineProject), `l.go:7:5: time-naming: var timeoutSecs is of type time.Duration; don't use unit-specific suffix "Secs" (revive)`)
		})
	}
}
