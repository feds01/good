package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_computeCgoSalt(t *testing.T) {
	for _, name := range cgoEnvNames {
		t.Setenv(name, "")
	}

	// A scratch GOENV file stands for `go env -w`, which must not touch the real one.
	goEnvFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("GOENV", goEnvFile)

	original, err := computeCgoSalt(t.Context())
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(goEnvFile, []byte("CGO_CFLAGS=-DFROM_GO_ENV=1\n"), 0o600))

	written, err := computeCgoSalt(t.Context())
	require.NoError(t, err)

	assert.NotEqual(t, original, written)
	assert.Contains(t, written, "CGO_CFLAGS=-DFROM_GO_ENV=1;")

	t.Setenv("CGO_CFLAGS", "-DFROM_ENVIRONMENT=1")

	environment, err := computeCgoSalt(t.Context())
	require.NoError(t, err)

	assert.NotEqual(t, written, environment)
	assert.Contains(t, environment, "CGO_CFLAGS=-DFROM_ENVIRONMENT=1;")
}
