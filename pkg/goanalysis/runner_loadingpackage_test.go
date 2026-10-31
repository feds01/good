package goanalysis

import (
	"errors"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/internal/cache"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

type cacheFact struct{}

func (*cacheFact) AFact() {}

var cacheFactAnalyzer = &analysis.Analyzer{
	Name:      "testfact",
	Doc:       "test analyzer with facts",
	FactTypes: []analysis.Fact{(*cacheFact)(nil)},
	Run: func(*analysis.Pass) (any, error) {
		return nil, nil
	},
}

var cacheNoFactAnalyzer = &analysis.Analyzer{
	Name: "testnofact",
	Doc:  "test analyzer without facts",
	Run: func(*analysis.Pass) (any, error) {
		return nil, nil
	},
}

type fakeDependencyLoader struct {
	processedFile string

	cgoFilesCalls int
}

func (l *fakeDependencyLoader) LoadCompiledGoFiles(pkg *packages.Package) error {
	l.cgoFilesCalls++

	pkg.CompiledGoFiles = []string{l.processedFile}

	return nil
}

func (*fakeDependencyLoader) LoadSupportPackage(string) (*types.Package, error) {
	return nil, errors.New("no support package in the fake loader")
}

func writeGoFile(t *testing.T, name, content string) string {
	t.Helper()

	filename := filepath.Join(t.TempDir(), name)

	require.NoError(t, os.WriteFile(filename, []byte(content), 0o600))

	return filename
}

func newDependency(filename string) *packages.Package {
	return &packages.Package{
		ID:              "example.com/dep",
		PkgPath:         "example.com/dep",
		Name:            "dep",
		CompiledGoFiles: []string{filename},
		Fset:            token.NewFileSet(),
	}
}

func newDependencyLoadingPackage(pkg *packages.Package, pkgCache *cache.Cache, depLoader *fakeDependencyLoader,
	analyzers ...*analysis.Analyzer,
) *loadingPackage {
	log := logutils.NewStderrLog("skip")

	run := &runner{log: log, pkgCache: pkgCache}

	lp := &loadingPackage{
		pkg:       pkg,
		log:       log,
		imports:   map[string]*loadingPackage{},
		depLoader: depLoader,
	}

	for _, analyzer := range analyzers {
		lp.actions = append(lp.actions, &action{
			Analyzer:     analyzer,
			Package:      pkg,
			runner:       run,
			objectFacts:  map[objectFactKey]analysis.Fact{},
			packageFacts: map[packageFactKey]analysis.Fact{},
		})
	}

	return lp
}

func Test_loadingPackage_loadImportedPackageWithFacts(t *testing.T) {
	// The low-level cache is shared by the whole process, so it is set up once for all the sub-tests.
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())

	log := logutils.NewStderrLog("skip")

	pkgCache, err := cache.NewCache(timeutils.NewStopwatch("pkgcache", log), log)
	require.NoError(t, err)

	t.Run("analyzes from source when facts are not cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Uncached() {}\n")

		pkg := newDependency(filename)
		lp := newDependencyLoadingPackage(pkg, pkgCache, &fakeDependencyLoader{}, cacheFactAnalyzer)

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.NotNil(t, pkg.Types.Scope().Lookup("Uncached"))
		assert.True(t, lp.actions[0].needAnalyzeSource)
	})

	t.Run("type-checks from source and reuses the facts when they are cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Cached() {}\n")

		pkg := newDependency(filename)
		lp := newDependencyLoadingPackage(pkg, pkgCache, &fakeDependencyLoader{}, cacheFactAnalyzer)

		require.NoError(t, pkgCache.Put(pkg, cache.HashModeNeedAllDeps, factCacheKey(cacheFactAnalyzer), []Fact{}))

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.Empty(t, pkg.ExportFile)
		assert.Empty(t, pkg.Errors)
		assert.NotNil(t, pkg.Types.Scope().Lookup("Cached"))
		assert.False(t, lp.actions[0].needAnalyzeSource, "cached facts are reused")
	})

	t.Run("type-checks from source when no analyzer has facts", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc NoFacts() {}\n")

		pkg := newDependency(filename)
		lp := newDependencyLoadingPackage(pkg, pkgCache, &fakeDependencyLoader{}, cacheNoFactAnalyzer)

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.NotNil(t, pkg.Types.Scope().Lookup("NoFacts"))
		assert.False(t, lp.actions[0].needAnalyzeSource)
	})

	t.Run("type-checks from source when there are no actions", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc NoActions() {}\n")

		pkg := newDependency(filename)
		lp := newDependencyLoadingPackage(pkg, pkgCache, &fakeDependencyLoader{})

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.NotNil(t, pkg.Types.Scope().Lookup("NoActions"))
	})

	t.Run("analyzes only the actions whose facts are not cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Partial() {}\n")

		pkg := newDependency(filename)
		lp := newDependencyLoadingPackage(pkg, pkgCache, &fakeDependencyLoader{}, cacheFactAnalyzer, cacheNoFactAnalyzer)

		require.NoError(t, pkgCache.Put(pkg, cache.HashModeNeedAllDeps, factCacheKey(cacheFactAnalyzer), []Fact{}))

		other := &analysis.Analyzer{Name: "testother", Doc: "other", FactTypes: []analysis.Fact{(*cacheFact)(nil)}, Run: cacheFactAnalyzer.Run}
		lp.actions = append(lp.actions, &action{
			Analyzer:     other,
			Package:      pkg,
			runner:       lp.actions[0].runner,
			objectFacts:  map[objectFactKey]analysis.Fact{},
			packageFacts: map[packageFactKey]analysis.Fact{},
		})

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.NotNil(t, pkg.Types.Scope().Lookup("Partial"))
		assert.False(t, lp.actions[0].needAnalyzeSource, "cached facts are reused")
		assert.True(t, lp.actions[2].needAnalyzeSource)
	})
}

func Test_loadingPackage_loadFromSource_cgo(t *testing.T) {
	cgoFile := writeGoFile(t, "cgo.go", "package dep\n\n// int x;\nimport \"C\"\n\nfunc Raw() {}\n")
	processedFile := writeGoFile(t, "processed.go", "package dep\n\nfunc Processed() {}\n")

	t.Run("processes the cgo files of a dependency", func(t *testing.T) {
		pkg := newDependency(cgoFile)
		depLoader := &fakeDependencyLoader{processedFile: processedFile}
		lp := newDependencyLoadingPackage(pkg, nil, depLoader)

		require.NoError(t, lp.loadFromSource(LoadModeTypesInfo))

		assert.Equal(t, 1, depLoader.cgoFilesCalls)
		assert.Equal(t, []string{processedFile}, pkg.CompiledGoFiles)
		assert.Empty(t, pkg.Errors)
		assert.NotNil(t, pkg.Types.Scope().Lookup("Processed"))
	})

	t.Run("leaves the files of an analyzed package alone", func(t *testing.T) {
		pkg := newDependency(cgoFile)
		depLoader := &fakeDependencyLoader{processedFile: processedFile}
		lp := newDependencyLoadingPackage(pkg, nil, depLoader)
		lp.isInitial = true

		require.NoError(t, lp.loadFromSource(LoadModeSyntax))

		assert.Zero(t, depLoader.cgoFilesCalls)
		assert.Equal(t, []string{cgoFile}, pkg.CompiledGoFiles)
	})

	t.Run("ignores packages without cgo", func(t *testing.T) {
		pkg := newDependency(processedFile)
		depLoader := &fakeDependencyLoader{processedFile: processedFile}
		lp := newDependencyLoadingPackage(pkg, nil, depLoader)

		require.NoError(t, lp.loadFromSource(LoadModeSyntax))

		assert.Zero(t, depLoader.cgoFilesCalls)
	})
}

func Test_newCgoRuntimePackage(t *testing.T) {
	pkg := newCgoRuntimePackage()

	assert.True(t, pkg.Complete())
	assert.Equal(t, "cgo", pkg.Name())
	assert.NotNil(t, pkg.Scope().Lookup("Incomplete"))
}
