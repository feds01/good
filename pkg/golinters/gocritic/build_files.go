package gocritic

import (
	"bytes"
	"go/build"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// readFilesOnce makes go/build read each file once while load runs.
//
// The embedded rules are type-checked against the standard library from source,
// and go/build opens the files of a package again for every import of it.
func readFilesOnce(load func() error) error {
	previous := build.Default.OpenFile

	var (
		isActive atomic.Bool
		files    sync.Map
	)

	isActive.Store(true)

	// The build context is copied while load runs and kept by the checkers, so the hook outlives this call.
	build.Default.OpenFile = func(path string) (io.ReadCloser, error) {
		if !isActive.Load() {
			return openFile(previous, path)
		}

		if content, ok := files.Load(path); ok {
			return io.NopCloser(bytes.NewReader(content.([]byte))), nil
		}

		file, err := openFile(previous, path)
		if err != nil {
			return nil, err
		}

		defer func() { _ = file.Close() }()

		content, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}

		files.Store(path, content)

		return io.NopCloser(bytes.NewReader(content)), nil
	}

	defer func() {
		build.Default.OpenFile = previous

		isActive.Store(false)
		files.Clear()
	}()

	return load()
}

func openFile(open func(string) (io.ReadCloser, error), path string) (io.ReadCloser, error) {
	if open != nil {
		return open(path)
	}

	return os.Open(path)
}
