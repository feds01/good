package goanalysis

import (
	"go/token"
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
	exportFile string

	exportDataCalls int
}

func (l *fakeDependencyLoader) LoadExportData(pkg *packages.Package) error {
	l.exportDataCalls++

	pkg.ExportFile = l.exportFile

	return nil
}

func writeGoFile(t *testing.T, name, content string) string {
	t.Helper()

	filename := filepath.Join(t.TempDir(), name)

	require.NoError(t, os.WriteFile(filename, []byte(content), 0o600))

	return filename
}

// writeExportData builds the export data of a package, like the loader does for dependencies.
func writeExportData(t *testing.T, filename string) string {
	t.Helper()

	dir := filepath.Dir(filename)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/dep\n\ngo 1.21\n"), 0o600))

	conf := &packages.Config{
		Mode: packages.NeedName | packages.NeedExportFile,
		Dir:  dir,
		Env:  append(os.Environ(), "GOFLAGS=-mod=mod"),
	}

	pkgs, err := packages.Load(conf, ".")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	require.Empty(t, pkgs[0].Errors)

	return pkgs[0].ExportFile
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

	t.Run("loads from source without export data when facts are not cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Uncached() {}\n")

		pkg := newDependency(filename)
		depLoader := &fakeDependencyLoader{}
		lp := newDependencyLoadingPackage(pkg, pkgCache, depLoader, cacheFactAnalyzer)

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.Zero(t, depLoader.exportDataCalls)
		assert.NotNil(t, pkg.Types.Scope().Lookup("Uncached"))
		assert.True(t, lp.actions[0].needAnalyzeSource)
	})

	t.Run("loads export data when facts are cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Cached() {}\n")

		pkg := newDependency(filename)
		depLoader := &fakeDependencyLoader{exportFile: writeExportData(t, filename)}
		lp := newDependencyLoadingPackage(pkg, pkgCache, depLoader, cacheFactAnalyzer)

		require.NoError(t, pkgCache.Put(pkg, cache.HashModeNeedAllDeps, factCacheKey(cacheFactAnalyzer), []Fact{}))

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.Equal(t, 1, depLoader.exportDataCalls)
		assert.Equal(t, depLoader.exportFile, pkg.ExportFile)
		assert.Empty(t, pkg.Errors)
		assert.Empty(t, pkg.Syntax)
		assert.NotNil(t, pkg.Types.Scope().Lookup("Cached"))
		assert.False(t, lp.actions[0].needAnalyzeSource)
	})

	t.Run("loads export data when there are no facts", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc NoFacts() {}\n")

		pkg := newDependency(filename)
		depLoader := &fakeDependencyLoader{exportFile: writeExportData(t, filename)}
		lp := newDependencyLoadingPackage(pkg, pkgCache, depLoader, cacheNoFactAnalyzer)

		require.NoError(t, lp.loadImportedPackageWithFacts(LoadModeTypesInfo))

		assert.Equal(t, 1, depLoader.exportDataCalls)
		assert.NotNil(t, pkg.Types.Scope().Lookup("NoFacts"))
	})

	t.Run("loads source when only some facts are cached", func(t *testing.T) {
		filename := writeGoFile(t, "dep.go", "package dep\n\nfunc Partial() {}\n")

		pkg := newDependency(filename)
		depLoader := &fakeDependencyLoader{}
		lp := newDependencyLoadingPackage(pkg, pkgCache, depLoader, cacheFactAnalyzer, cacheNoFactAnalyzer)

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

		assert.Zero(t, depLoader.exportDataCalls)
		assert.NotNil(t, pkg.Types.Scope().Lookup("Partial"))
		assert.False(t, lp.actions[0].needAnalyzeSource, "cached facts are reused")
		assert.True(t, lp.actions[2].needAnalyzeSource)
	})
}
