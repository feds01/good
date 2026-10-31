package cache

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/golangci/golangci-lint/v2/internal/go/cache"
)

const (
	memoFilePrefix = "filehash-"

	// memoRacyWindow is how recent a file change must be for the file to stay out of the memo.
	// A second change inside one timestamp tick would keep the same stat identity, so a file is
	// memoized only when its timestamps are older than the clock granularity of any file system.
	memoRacyWindow = 2 * time.Second

	// memoMaxAge is how many days an unused entry stays in the memo.
	// A shard that was not written for as long is deleted.
	memoMaxAge = 14

	// memoTempMaxAge is how old a temporary file must be to be taken for the leftover of a crashed save,
	// not the file of a save that is running.
	memoTempMaxAge = time.Hour

	// memoDisableEnv switches the memo off, so that every file is read.
	memoDisableEnv = "GOLANGCI_LINT_NO_HASH_MEMO"

	secondsPerDay = 24 * 60 * 60
)

type fileHash = [cache.HashSize]byte

// memoEntry is a file hash with the stat identity of the file when it was hashed.
type memoEntry struct {
	Stat    fileStat
	Hash    fileHash
	UsedDay int64 // Unix day of the last run that used the entry
}

// memoShard is the part of the memo kept in one file.
type memoShard struct {
	path string

	once    sync.Once
	mu      sync.Mutex
	entries map[string]memoEntry
	isDirty bool
}

// fileHashMemo remembers file hashes between runs, so that a file is read only when it changed.
//
// An entry is trusted only if the file still has the same size, modification time, change time,
// inode and device. The change time is set by the kernel on every write, rename and timestamp change,
// so a file edited to the same size with its modification time restored still gets a new identity.
//
// The files below the working directory go to one shard per directory,
// so that the many worktrees of a repository do not grow one shared file.
// All other files (the module cache, GOROOT, replaced modules) share one shard.
type fileHashMemo struct {
	local  *memoShard
	shared *memoShard

	localPrefix string

	now func() time.Time

	// runHashes holds the hash used for each file during this run, like the cache of the go command.
	runHashes sync.Map
}

// newFileHashMemo returns a memo stored in dir for runs started in workDir.
// It returns nil if the platform cannot provide a reliable stat identity.
func newFileHashMemo(dir, workDir string, now func() time.Time) *fileHashMemo {
	if !isStatIdentitySupported || dir == "" || dir == "off" {
		return nil
	}

	workDirSum := sha256.Sum256([]byte(workDir))

	return &fileHashMemo{
		local:       &memoShard{path: filepath.Join(dir, memoFilePrefix+hex.EncodeToString(workDirSum[:8]))},
		shared:      &memoShard{path: filepath.Join(dir, memoFilePrefix+"shared")},
		localPrefix: strings.TrimSuffix(workDir, string(filepath.Separator)) + string(filepath.Separator),
		now:         now,
	}
}

// hash returns the hash of the file, calling compute only when the memo cannot vouch for the file.
func (m *fileHashMemo) hash(file string, compute func(string) (fileHash, error)) (fileHash, error) {
	// A relative path names different files in different working directories.
	if m == nil || !filepath.IsAbs(file) {
		return compute(file)
	}

	if hash, ok := m.runHashes.Load(file); ok {
		return hash.(fileHash), nil
	}

	// The file is described before it is read: a change made after this point
	// leaves the entry with an identity that no longer matches the file.
	statTime := m.now()

	info, err := os.Stat(file)
	if err != nil {
		return compute(file)
	}

	identity, ok := statIdentity(info)
	if !ok {
		return compute(file)
	}

	shard := m.shardFor(file)

	if hash, isFound := shard.lookup(file, identity, m.now().Unix()/secondsPerDay); isFound {
		m.runHashes.Store(file, hash)

		return hash, nil
	}

	hash, err := compute(file)
	if err != nil {
		return hash, err
	}

	m.runHashes.Store(file, hash)

	if statTime.Sub(identity.latestChange()) >= memoRacyWindow {
		shard.store(file, &memoEntry{Stat: identity, Hash: hash, UsedDay: statTime.Unix() / secondsPerDay})
	}

	return hash, nil
}

func (m *fileHashMemo) shardFor(file string) *memoShard {
	if strings.HasPrefix(file, m.localPrefix) {
		return m.local
	}

	return m.shared
}

// save writes the shards that changed, without the entries unused for too long.
// Failing to save only costs the next run a rehash, so errors are ignored.
func (m *fileHashMemo) save() {
	if m == nil {
		return
	}

	today := m.now().Unix() / secondsPerDay

	m.local.save(today)
	m.shared.save(today)

	m.sweep()
}

// sweep deletes the shards of working directories that have not been used for long,
// and the temporary files left by a save that crashed. It is best effort.
func (m *fileHashMemo) sweep() {
	dir := filepath.Dir(m.local.path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	now := m.now()

	for _, entry := range entries {
		name := entry.Name()

		if entry.IsDir() || !strings.HasPrefix(name, memoFilePrefix) {
			continue
		}

		path := filepath.Join(dir, name)
		if path == m.local.path || path == m.shared.path {
			continue
		}

		maxAge := time.Duration(memoMaxAge) * 24 * time.Hour
		if strings.Contains(name, ".tmp") {
			maxAge = memoTempMaxAge
		}

		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}

		_ = os.Remove(path)
	}
}

func (s *memoShard) load() {
	s.once.Do(func() {
		s.entries = map[string]memoEntry{}

		f, err := os.Open(s.path)
		if err != nil {
			return
		}

		defer func() { _ = f.Close() }()

		var entries map[string]memoEntry

		err = gob.NewDecoder(f).Decode(&entries)
		if err != nil {
			// A damaged or older file is not trusted at all.
			return
		}

		s.entries = entries
	})
}

func (s *memoShard) lookup(file string, identity fileStat, today int64) (fileHash, bool) {
	s.load()

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[file]
	if !ok || entry.Stat != identity {
		return fileHash{}, false
	}

	if entry.UsedDay != today {
		entry.UsedDay = today
		s.entries[file] = entry
		s.isDirty = true
	}

	return entry.Hash, true
}

func (s *memoShard) store(file string, entry *memoEntry) {
	s.load()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[file] = *entry
	s.isDirty = true
}

func (s *memoShard) save(today int64) {
	s.load()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isDirty {
		return
	}

	for file, entry := range s.entries {
		if today-entry.UsedDay > memoMaxAge {
			delete(s.entries, file)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp*")
	if err != nil {
		return
	}

	err = gob.NewEncoder(tmp).Encode(s.entries)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return
	}

	err = tmp.Close()
	if err != nil {
		_ = os.Remove(tmp.Name())

		return
	}

	err = os.Rename(tmp.Name(), s.path)
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
}
