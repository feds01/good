package gocritic

import (
	"go/build"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadFilesOnce(t *testing.T) {
	var opened atomic.Int32

	original := build.Default.OpenFile

	t.Cleanup(func() {
		build.Default.OpenFile = original
	})

	build.Default.OpenFile = func(path string) (io.ReadCloser, error) {
		opened.Add(1)

		return openFile(nil, path)
	}

	var duringLoad build.Context

	err := readFilesOnce(func() error {
		duringLoad = build.Default

		_, err := build.Default.Import("errors", "", 0)
		if err != nil {
			return err
		}

		afterFirstImport := opened.Load()

		_, err = build.Default.Import("errors", "", 0)
		if err != nil {
			return err
		}

		assert.NotZero(t, afterFirstImport)
		assert.Equal(t, afterFirstImport, opened.Load(), "the second import must not open files again")

		return nil
	})
	require.NoError(t, err)

	opened.Store(0)

	// The hook is gone once load returns, and a copy of the context made meanwhile reads the files again.
	_, err = build.Default.Import("errors", "", 0)
	require.NoError(t, err)

	afterDefault := opened.Load()
	assert.NotZero(t, afterDefault)

	_, err = duringLoad.Import("errors", "", 0)
	require.NoError(t, err)

	assert.Greater(t, opened.Load(), afterDefault)
}

func TestReadFilesOnce_error(t *testing.T) {
	err := readFilesOnce(func() error {
		_, openErr := build.Default.OpenFile("testdata/missing.go")

		return openErr
	})

	require.Error(t, err)
}
