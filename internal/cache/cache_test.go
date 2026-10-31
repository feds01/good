package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

func setupCache(t *testing.T) *Cache {
	t.Helper()

	log := logutils.NewStderrLog("skip")
	sw := timeutils.NewStopwatch("pkgcache", log)

	pkgCache, err := NewCache(sw, log)
	require.NoError(t, err)

	return pkgCache
}

func fakePackage() *packages.Package {
	return &packages.Package{
		PkgPath: "github.com/golangci/example",
		CompiledGoFiles: []string{
			"./testdata/hello.go",
		},
		Imports: map[string]*packages.Package{
			"a": {
				PkgPath: "github.com/golangci/example/a",
			},
			"b": {
				PkgPath: "github.com/golangci/example/b",
			},
			"unsafe": {
				PkgPath: "unsafe",
			},
		},
	}
}

type Foo struct {
	Value string
}

func TestCache_Put(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())

	pkgCache := setupCache(t)

	pkg := fakePackage()

	in := &Foo{Value: "hello"}

	err := pkgCache.Put(pkg, HashModeNeedAllDeps, "key", in)
	require.NoError(t, err)

	out := &Foo{}
	err = pkgCache.Get(pkg, HashModeNeedAllDeps, "key", out)
	require.NoError(t, err)

	assert.Equal(t, in, out)

	pkgCache.Close()
}

func TestCache_Get_missing_data(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())

	pkgCache := setupCache(t)

	pkg := fakePackage()

	out := &Foo{}
	err := pkgCache.Get(pkg, HashModeNeedAllDeps, "key", out)
	require.Error(t, err)

	require.ErrorIs(t, err, ErrMissing)

	pkgCache.Close()
}

func TestCache_buildKey(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	actionID, err := pkgCache.buildKey(pkg, HashModeNeedAllDeps, "")
	require.NoError(t, err)

	assert.Equal(t, "f32bf1bf010aa9b570e081c64ec9e22e17aafa1e822990ba952905ec5fdf8d9d", fmt.Sprintf("%x", actionID))
}

func TestCache_pkgActionID(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	actionID, err := pkgCache.pkgActionID(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "f690f05acd1024386ae912d9ad9c04080523b9a899f6afe56ab3108d88215c1d", fmt.Sprintf("%x", actionID))
}

func TestCache_packageHash_load(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	entry := &pkgHashEntry{results: hashResults{HashModeNeedAllDeps: "fake"}}
	entry.once.Do(func() {})

	pkgCache.pkgHashes.Store(pkg, entry)

	hash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "fake", hash)
}

func TestCache_packageHash_store(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	hash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
	require.NoError(t, err)

	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", hash)

	value, ok := pkgCache.pkgHashes.Load(pkg)
	require.True(t, ok)

	hashRes := value.(*pkgHashEntry).results

	require.Len(t, hashRes, 3)

	assert.Equal(t, "8978e3d76c6f99e9663558d7147a7790f229a676804d1fde706a611898547b74", hashRes[HashModeNeedOnlySelf])
	assert.Equal(t, "b1aef902a0619b5cbfc2d6e2e91a73dd58dd448e58274b2d7a5ff8efd97aefa4", hashRes[HashModeNeedDirectDeps])
	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", hashRes[HashModeNeedAllDeps])
}

func TestCache_computeHash(t *testing.T) {
	pkgCache := setupCache(t)

	pkg := fakePackage()

	results, err := pkgCache.computePkgHash(pkg)
	require.NoError(t, err)

	require.Len(t, results, 3)

	assert.Equal(t, "8978e3d76c6f99e9663558d7147a7790f229a676804d1fde706a611898547b74", results[HashModeNeedOnlySelf])
	assert.Equal(t, "b1aef902a0619b5cbfc2d6e2e91a73dd58dd448e58274b2d7a5ff8efd97aefa4", results[HashModeNeedDirectDeps])
	assert.Equal(t, "9c602ef861197b6807e82c99caa7c4042eb03c1a92886303fb02893744355131", results[HashModeNeedAllDeps])
}

func TestCache_packageHash_invalidation(t *testing.T) {
	baseHashes := rootHashes(t, map[string]string{})

	testCases := []struct {
		desc           string
		edited         string
		changedSelf    bool
		changedDirect  bool
		changedAllDeps bool
	}{
		{
			desc:           "root file",
			edited:         "root",
			changedSelf:    true,
			changedDirect:  true,
			changedAllDeps: true,
		},
		{
			desc:           "direct dependency file",
			edited:         "a",
			changedDirect:  true,
			changedAllDeps: true,
		},
		{
			desc:           "transitive dependency file",
			edited:         "b",
			changedAllDeps: true,
		},
		{
			desc:   "unrelated package file",
			edited: "c",
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			hashes := rootHashes(t, map[string]string{test.edited: "var edited = true\n"})

			assert.Equal(t, test.changedSelf, hashes[HashModeNeedOnlySelf] != baseHashes[HashModeNeedOnlySelf])
			assert.Equal(t, test.changedDirect, hashes[HashModeNeedDirectDeps] != baseHashes[HashModeNeedDirectDeps])
			assert.Equal(t, test.changedAllDeps, hashes[HashModeNeedAllDeps] != baseHashes[HashModeNeedAllDeps])
		})
	}
}

func TestCache_packageHash_golden(t *testing.T) {
	hashes := rootHashes(t, map[string]string{})

	assert.Equal(t, "d872820361b0c8d28a9271f0b468263fd2e36a088e001754730f21d221303c4d", hashes[HashModeNeedOnlySelf])
	assert.Equal(t, "458200c7f784f1045d76a0e6ecd7f68c781df5b3f0027ec570b5ceb173bbcb6f", hashes[HashModeNeedDirectDeps])
	assert.Equal(t, "2f0618e85433028288775e9282794a68dae599e3af686e65263c28b7172c947a", hashes[HashModeNeedAllDeps])

	actionID, err := setupCache(t).buildKey(writeModule(t, map[string]string{}), HashModeNeedAllDeps, "key")
	require.NoError(t, err)

	assert.Equal(t, "20b4d81f48e1e1df701806251a597b49930266f6e9e1c02c68a7e6e21578a46c", fmt.Sprintf("%x", actionID))
}

func TestCache_packageHash_concurrent(t *testing.T) {
	root := writeModule(t, map[string]string{})

	var allPkgs []*packages.Package
	packages.Visit([]*packages.Package{root}, nil, func(pkg *packages.Package) {
		allPkgs = append(allPkgs, pkg)
	})

	expected := map[*packages.Package]string{}
	for _, pkg := range allPkgs {
		hash, err := setupCache(t).packageHash(pkg, HashModeNeedAllDeps)
		require.NoError(t, err)

		expected[pkg] = hash
	}

	pkgCache := setupCache(t)

	var wg sync.WaitGroup

	for range 64 {
		wg.Go(func() {
			for _, pkg := range allPkgs {
				hash, err := pkgCache.packageHash(pkg, HashModeNeedAllDeps)
				assert.Equal(t, expected[pkg], hash, err)
			}
		})
	}

	wg.Wait()
}

// rootHashes returns the hashes of the root package of a module written by writeModule.
func rootHashes(t *testing.T, edits map[string]string) hashResults {
	t.Helper()

	root := writeModule(t, edits)

	pkgCache := setupCache(t)

	hashes := hashResults{}

	for _, mode := range []HashMode{HashModeNeedOnlySelf, HashModeNeedDirectDeps, HashModeNeedAllDeps} {
		hash, err := pkgCache.packageHash(root, mode)
		require.NoError(t, err)

		hashes[mode] = hash
	}

	return hashes
}

// writeModule writes the module "root -> a -> b", plus "c" which nothing imports, to a new directory.
// The edits are appended to the file of the named packages.
// Each call uses a new directory because file hashes are memoized by path for the whole process.
func writeModule(t *testing.T, edits map[string]string) *packages.Package {
	t.Helper()

	dir := t.TempDir()

	module := &packages.Module{Path: "example.com/m", Dir: dir}

	newPkg := func(name string, imports ...*packages.Package) *packages.Package {
		pkgDir := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		file := filepath.Join(pkgDir, name+".go")
		require.NoError(t, os.WriteFile(file, []byte("package "+name+"\n"+edits[name]), 0o600))

		pkg := &packages.Package{
			Name:            name,
			PkgPath:         module.Path + "/" + name,
			Module:          module,
			CompiledGoFiles: []string{file},
			Imports:         map[string]*packages.Package{},
		}

		for _, imp := range imports {
			pkg.Imports[imp.PkgPath] = imp
		}

		return pkg
	}

	newPkg("c")

	return newPkg("root", newPkg("a", newPkg("b")))
}
