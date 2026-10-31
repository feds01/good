package cache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settledClock returns a clock that is far enough ahead of the files written by a test for them to be memoized.
func settledClock() func() time.Time {
	return func() time.Time {
		return time.Now().Add(time.Hour)
	}
}

type countingHasher struct {
	calls atomic.Int32
}

func (h *countingHasher) hash(file string) (fileHash, error) {
	h.calls.Add(1)

	content, err := os.ReadFile(file)
	if err != nil {
		return fileHash{}, err
	}

	return sha256.Sum256(content), nil
}

func skipWithoutStatIdentity(t *testing.T) {
	t.Helper()

	if !isStatIdentitySupported {
		t.Skip("no stat identity on this platform")
	}
}

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	file := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))

	return file
}

func TestFileHashMemo_unchangedFileIsNotRead(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	file := writeTestFile(t, workDir, "a.go", "package a\n")

	hasher := &countingHasher{}

	first := newFileHashMemo(memoDir, workDir, settledClock())

	expected, err := first.hash(file, hasher.hash)
	require.NoError(t, err)
	require.EqualValues(t, 1, hasher.calls.Load())

	// A second request in the same run is answered from memory.
	_, err = first.hash(file, hasher.hash)
	require.NoError(t, err)
	require.EqualValues(t, 1, hasher.calls.Load())

	first.save()

	second := newFileHashMemo(memoDir, workDir, settledClock())

	actual, err := second.hash(file, hasher.hash)
	require.NoError(t, err)

	assert.Equal(t, expected, actual)
	assert.EqualValues(t, 1, hasher.calls.Load(), "the file must not be read again")
}

func TestFileHashMemo_editedFileIsRead(t *testing.T) {
	skipWithoutStatIdentity(t)

	testCases := []struct {
		desc string
		edit func(t *testing.T, file string, modTime time.Time)
	}{
		{
			desc: "other size",
			edit: func(t *testing.T, file string, _ time.Time) {
				t.Helper()

				require.NoError(t, os.WriteFile(file, []byte("package a // longer\n"), 0o600))
			},
		},
		{
			desc: "same size and same modification time",
			edit: func(t *testing.T, file string, modTime time.Time) {
				t.Helper()

				// Let the change time tick: some file systems keep a coarse clock.
				time.Sleep(50 * time.Millisecond)

				require.NoError(t, os.WriteFile(file, []byte("package b\n"), 0o600))
				require.NoError(t, os.Chtimes(file, modTime, modTime))
			},
		},
		{
			desc: "replaced by a file with the same size and modification time",
			edit: func(t *testing.T, file string, modTime time.Time) {
				t.Helper()

				time.Sleep(50 * time.Millisecond)

				replacement := writeTestFile(t, filepath.Dir(file), "replacement.go", "package b\n")
				require.NoError(t, os.Chtimes(replacement, modTime, modTime))
				require.NoError(t, os.Rename(replacement, file))
			},
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			memoDir := t.TempDir()
			workDir := t.TempDir()
			file := writeTestFile(t, workDir, "a.go", "package a\n")

			modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
			require.NoError(t, os.Chtimes(file, modTime, modTime))

			hasher := &countingHasher{}

			first := newFileHashMemo(memoDir, workDir, settledClock())

			before, err := first.hash(file, hasher.hash)
			require.NoError(t, err)

			first.save()

			test.edit(t, file, modTime)

			second := newFileHashMemo(memoDir, workDir, settledClock())

			after, err := second.hash(file, hasher.hash)
			require.NoError(t, err)

			assert.NotEqual(t, before, after)
			assert.EqualValues(t, 2, hasher.calls.Load())
		})
	}
}

func TestFileHashMemo_recentFileIsNotKept(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	file := writeTestFile(t, workDir, "a.go", "package a\n")

	hasher := &countingHasher{}

	first := newFileHashMemo(memoDir, workDir, time.Now)

	_, err := first.hash(file, hasher.hash)
	require.NoError(t, err)

	first.save()

	second := newFileHashMemo(memoDir, workDir, time.Now)

	_, err = second.hash(file, hasher.hash)
	require.NoError(t, err)

	assert.EqualValues(t, 2, hasher.calls.Load(), "a file changed a moment ago can still change within one timestamp")
}

func TestFileHashMemo_damagedFileIsIgnored(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	file := writeTestFile(t, workDir, "a.go", "package a\n")

	hasher := &countingHasher{}

	first := newFileHashMemo(memoDir, workDir, settledClock())

	expected, err := first.hash(file, hasher.hash)
	require.NoError(t, err)

	first.save()

	shards, err := filepath.Glob(filepath.Join(memoDir, memoFilePrefix+"*"))
	require.NoError(t, err)
	require.NotEmpty(t, shards)

	for _, shard := range shards {
		require.NoError(t, os.WriteFile(shard, []byte("not a memo"), 0o600))
	}

	second := newFileHashMemo(memoDir, workDir, settledClock())

	actual, err := second.hash(file, hasher.hash)
	require.NoError(t, err)

	assert.Equal(t, expected, actual)
	assert.EqualValues(t, 2, hasher.calls.Load())
}

func TestFileHashMemo_truncatedFileIsIgnored(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	file := writeTestFile(t, workDir, "a.go", "package a\n")

	hasher := &countingHasher{}

	first := newFileHashMemo(memoDir, workDir, settledClock())

	expected, err := first.hash(file, hasher.hash)
	require.NoError(t, err)

	first.save()

	shards, err := filepath.Glob(filepath.Join(memoDir, memoFilePrefix+"*"))
	require.NoError(t, err)
	require.NotEmpty(t, shards)

	for _, shard := range shards {
		content, errRead := os.ReadFile(shard)
		require.NoError(t, errRead)
		require.Greater(t, len(content), 1)

		for _, size := range []int{0, 1, len(content) / 2, len(content) - 1} {
			require.NoError(t, os.WriteFile(shard, content[:size], 0o600))

			memo := newFileHashMemo(memoDir, workDir, settledClock())

			actual, errHash := memo.hash(file, hasher.hash)
			require.NoError(t, errHash)

			assert.Equal(t, expected, actual)
		}
	}

	assert.EqualValues(t, 1+len(shards)*4, hasher.calls.Load(), "a cut-off memo is never used")
}

func TestFileHashMemo_concurrentUse(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()

	files := make([]string, 20)
	expected := make(map[string]fileHash)

	for i := range files {
		files[i] = writeTestFile(t, workDir, fmt.Sprintf("f%d.go", i), fmt.Sprintf("package f%d\n", i))

		var err error

		expected[files[i]], err = (&countingHasher{}).hash(files[i])
		require.NoError(t, err)
	}

	// Several runs share the memo files at once, as when two linters run in different terminals.
	// Each run also hashes the same files from many goroutines.
	var wg sync.WaitGroup

	var wrongHashes atomic.Int32

	for range 4 {
		wg.Go(func() {
			for range 5 {
				memo := newFileHashMemo(memoDir, workDir, settledClock())

				var inner sync.WaitGroup

				for range 8 {
					inner.Go(func() {
						for _, file := range files {
							actual, err := memo.hash(file, (&countingHasher{}).hash)
							if err != nil || actual != expected[file] {
								wrongHashes.Add(1)
							}
						}
					})
				}

				inner.Wait()
				memo.save()
			}
		})
	}

	wg.Wait()

	assert.Zero(t, wrongHashes.Load())

	leftovers, err := filepath.Glob(filepath.Join(memoDir, "*.tmp*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers)

	hasher := &countingHasher{}

	memo := newFileHashMemo(memoDir, workDir, settledClock())

	for _, file := range files {
		actual, errHash := memo.hash(file, hasher.hash)
		require.NoError(t, errHash)
		assert.Equal(t, expected[file], actual)
	}

	assert.Zero(t, hasher.calls.Load(), "the memo left by the concurrent runs is complete and readable")
}

func TestFileHashMemo_shards(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	otherDir := t.TempDir()

	inside := writeTestFile(t, workDir, "a.go", "package a\n")
	outside := writeTestFile(t, otherDir, "b.go", "package b\n")

	hasher := &countingHasher{}

	first := newFileHashMemo(memoDir, workDir, settledClock())

	for _, file := range []string{inside, outside} {
		_, err := first.hash(file, hasher.hash)
		require.NoError(t, err)
	}

	first.save()

	shards, err := filepath.Glob(filepath.Join(memoDir, memoFilePrefix+"*"))
	require.NoError(t, err)
	assert.Len(t, shards, 2)

	// Another working directory keeps its own files apart but shares the rest.
	anotherWorkDir := t.TempDir()

	second := newFileHashMemo(memoDir, anotherWorkDir, settledClock())

	for _, file := range []string{inside, outside} {
		_, err = second.hash(file, hasher.hash)
		require.NoError(t, err)
	}

	assert.EqualValues(t, 3, hasher.calls.Load(), "only the file outside the working directory is shared")
}

func TestFileHashMemo_dropsEntriesUnusedForLong(t *testing.T) {
	skipWithoutStatIdentity(t)

	memoDir := t.TempDir()
	workDir := t.TempDir()
	forgotten := writeTestFile(t, workDir, "a.go", "package a\n")
	kept := writeTestFile(t, workDir, "b.go", "package b\n")

	hasher := &countingHasher{}

	clockStart := time.Now().Add(time.Hour)
	clockAt := func(days int) func() time.Time {
		return func() time.Time {
			return clockStart.AddDate(0, 0, days)
		}
	}

	first := newFileHashMemo(memoDir, workDir, clockAt(0))

	for _, file := range []string{forgotten, kept} {
		_, err := first.hash(file, hasher.hash)
		require.NoError(t, err)
	}

	first.save()

	// A later run that uses only one of the files.
	later := newFileHashMemo(memoDir, workDir, clockAt(memoMaxAge+1))

	_, err := later.hash(kept, hasher.hash)
	require.NoError(t, err)

	later.save()

	require.EqualValues(t, 2, hasher.calls.Load())

	last := newFileHashMemo(memoDir, workDir, clockAt(memoMaxAge+1))

	_, err = last.hash(kept, hasher.hash)
	require.NoError(t, err)
	assert.EqualValues(t, 2, hasher.calls.Load(), "the used entry is kept")

	_, err = last.hash(forgotten, hasher.hash)
	require.NoError(t, err)
	assert.EqualValues(t, 3, hasher.calls.Load(), "the unused entry is dropped")
}

func TestFileHashMemo_missingFile(t *testing.T) {
	skipWithoutStatIdentity(t)

	workDir := t.TempDir()

	memo := newFileHashMemo(t.TempDir(), workDir, settledClock())

	_, err := memo.hash(filepath.Join(workDir, "missing.go"), (&countingHasher{}).hash)
	require.Error(t, err)
}

func TestFileHashMemo_nil(t *testing.T) {
	file := writeTestFile(t, t.TempDir(), "a.go", "package a\n")

	hasher := &countingHasher{}

	var memo *fileHashMemo

	_, err := memo.hash(file, hasher.hash)
	require.NoError(t, err)

	memo.save()

	assert.EqualValues(t, 1, hasher.calls.Load())
}

func TestCache_packageHash_memoInvalidation(t *testing.T) {
	skipWithoutStatIdentity(t)

	testCases := []struct {
		desc string
		edit func(t *testing.T, file string, modTime time.Time)
	}{
		{
			desc: "no edit",
			edit: func(*testing.T, string, time.Time) {},
		},
		{
			desc: "edit with the same size and modification time",
			edit: func(t *testing.T, file string, modTime time.Time) {
				t.Helper()

				time.Sleep(50 * time.Millisecond)

				require.NoError(t, os.WriteFile(file, []byte("package b\nvar x = 2\n"), 0o600))
				require.NoError(t, os.Chtimes(file, modTime, modTime))
			},
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			root := writeModule(t, map[string]string{"b": "var x = 1\n"})

			workDir := filepath.Dir(filepath.Dir(root.CompiledGoFiles[0]))
			memoDir := t.TempDir()

			// The file of the transitive dependency "b".
			file := root.Imports["example.com/m/a"].Imports["example.com/m/b"].CompiledGoFiles[0]

			modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
			require.NoError(t, os.Chtimes(file, modTime, modTime))

			hashWithMemo := func() string {
				pkgCache := setupCache(t)
				pkgCache.hashMemo = newFileHashMemo(memoDir, workDir, settledClock())
				// The hashes of cache.FileHash are also memoized by path for the whole process.
				pkgCache.readFileHash = (&countingHasher{}).hash

				hash, err := pkgCache.packageHash(root, HashModeNeedAllDeps)
				require.NoError(t, err)

				pkgCache.hashMemo.save()

				return hash
			}

			before := hashWithMemo()

			test.edit(t, file, modTime)

			after := hashWithMemo()

			if test.desc == "no edit" {
				assert.Equal(t, before, after)

				return
			}

			assert.NotEqual(t, before, after)
		})
	}
}

func TestFileHashMemo_relativePath(t *testing.T) {
	skipWithoutStatIdentity(t)

	workDir := t.TempDir()
	writeTestFile(t, workDir, "a.go", "package a\n")

	t.Chdir(workDir)

	memoDir := t.TempDir()
	hasher := &countingHasher{}

	for range 2 {
		memo := newFileHashMemo(memoDir, workDir, settledClock())

		_, err := memo.hash("a.go", hasher.hash)
		require.NoError(t, err)

		memo.save()
	}

	assert.EqualValues(t, 2, hasher.calls.Load())
}
