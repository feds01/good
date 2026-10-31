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

			run := exec.CommandContext(t.Context(), binary, "run", "--default=none", "-Erevive",
				"--output.text.print-issued-lines=false", "--output.text.path=stdout", "./...")
			run.Dir = project

			// The exit code is 1 when there are issues.
			output, _ = run.CombinedOutput()

			assert.Contains(t, string(output), `sample.go:11:5: time-naming: var timeoutSecs is of type time.Duration; don't use unit-specific suffix "Secs" (revive)`)
			assert.Contains(t, string(output), "sample.go:13:15: var-declaration: should omit type error from declaration of var errFailed; it will be inferred from the right-hand side (revive)")
			assert.Contains(t, string(output), "sample.go:16:2: errorf: should replace t.Error(fmt.Sprintf(...)) with t.Errorf(...) (revive)")
		})
	}
}
