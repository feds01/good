package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "baseline.toml")

	require.NoError(t, writeFileAtomic(filePath, []byte("first")))
	require.NoError(t, writeFileAtomic(filePath, []byte("second")))

	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))

	info, err := os.Stat(filePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(baselineFilePerm), info.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file is left behind")
}

func TestWriteFileAtomic_keepsTheFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "baseline.toml")

	require.NoError(t, os.WriteFile(filePath, []byte("first"), 0o600))

	require.Error(t, writeFileAtomic(filepath.Join(dir, "missing", "baseline.toml"), []byte("second")))

	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "first", string(data))
}
