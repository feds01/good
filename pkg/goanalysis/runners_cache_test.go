package goanalysis

import (
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/internal/cache"
	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
	"github.com/golangci/golangci-lint/v2/pkg/timeutils"
)

const (
	goodPkgCount = 6

	cacheTestFailPkg = "fail"
)

// cacheTestAnalyzer reports one issue per package, and fails on the package named "fail".
var cacheTestAnalyzer = &analysis.Analyzer{
	Name: "cachetest",
	Doc:  "reports the package clause of every file",
	Run: func(pass *analysis.Pass) (any, error) {
		if pass.Pkg.Name() == cacheTestFailPkg {
			return nil, errors.New("analyzer failure")
		}

		for _, file := range pass.Files {
			pass.Reportf(file.Package, "package %s", pass.Pkg.Name())
		}

		return nil, nil
	},
}

type cacheTestConfig struct{}

func (cacheTestConfig) getName() string { return "cachetest" }

func (cacheTestConfig) getLinterNameForDiagnostic(*Diagnostic) string { return "cachetest" }

func (cacheTestConfig) getAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{cacheTestAnalyzer}
}

func (cacheTestConfig) useOriginalPackages() bool { return false }

func (cacheTestConfig) reportIssues(*linter.Context) []*Issue { return nil }

func (cacheTestConfig) getLoadMode() LoadMode { return LoadModeTypesInfo }

// writeCacheTestModule writes goodPkgCount packages g0..gN that type-check,
// a package that doesn't type-check, one that imports it, and one whose analysis fails.
func writeCacheTestModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	// The cache keys don't depend on the directory: each module gets its own path so tests don't share entries.
	module := fmt.Sprintf("example.com/m%d", time.Now().UnixNano())

	files := map[string]string{
		"go.mod":                     fmt.Sprintf("module %s\n\ngo 1.21\n", module),
		"bad/bad.go":                 "package bad\n\nvar _ int = \"not an int\"\n",
		"importsbad/importsbad.go":   fmt.Sprintf("package importsbad\n\nimport \"%s/bad\"\n\nvar _ = bad.X\n", module),
		"importsbad2/importsbad2.go": fmt.Sprintf("package importsbad2\n\nimport \"%s/bad\"\n\nvar _ = bad.X\n", module),
		"otherbad/otherbad.go":       "package otherbad\n\nvar _ = undefinedName\n",
		"fail/fail.go":               "package fail\n",
	}

	for i := range goodPkgCount {
		files[fmt.Sprintf("g%d/g%d.go", i, i)] = fmt.Sprintf("package g%d\n", i)
	}

	for name, content := range files {
		filename := filepath.Join(dir, filepath.FromSlash(name))

		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		require.NoError(t, os.WriteFile(filename, []byte(content), 0o600))
	}

	return dir
}

func loadCacheTestPackages(t *testing.T, dir string) []*packages.Package {
	t.Helper()

	conf := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedModule,
		Dir: dir,
	}

	pkgs, err := packages.Load(conf, "./...")
	require.NoError(t, err)

	guard := load.NewGuard()
	fset := token.NewFileSet()

	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		pkg.Fset = fset

		guard.AddMutexForPkg(pkg)
	})

	return pkgs
}

func newCacheTestContext(t *testing.T, pkgs []*packages.Package) *linter.Context {
	t.Helper()

	log := logutils.NewStderrLog("skip")

	pkgCache, err := cache.NewCache(timeutils.NewStopwatch("pkgcache", log), log)
	require.NoError(t, err)

	return &linter.Context{
		Packages:         pkgs,
		OriginalPackages: pkgs,
		Cfg:              config.NewDefault(),
		Log:              log,
		PkgCache:         pkgCache,
		LoadGuard:        load.NewGuard(),
	}
}

func pkgsByName(pkgs []*packages.Package) map[string]*packages.Package {
	byName := map[string]*packages.Package{}

	for _, pkg := range pkgs {
		byName[pkg.Name] = pkg
	}

	return byName
}

// An analyzer failure cancels the analysis of the packages that are not analyzed yet, which ones depends on the scheduling.
func Test_runAnalyzers_analyzerFailureCachesOnlyFinishedPackages(t *testing.T) {
	dir := writeCacheTestModule(t)
	pkgs := loadCacheTestPackages(t, dir)
	lintCtx := newCacheTestContext(t, pkgs)

	issues, err := runAnalyzers(cacheTestConfig{}, lintCtx)

	cachedIssues, cached := loadIssuesFromCache(pkgs, lintCtx, cacheTestConfig{}.getAnalyzers())

	byName := pkgsByName(pkgs)

	assert.False(t, cached[byName["bad"]], "a package with a type error is not cached")
	assert.False(t, cached[byName["importsbad"]], "a package that imports a package with a type error is not cached")
	assert.False(t, cached[byName[cacheTestFailPkg]], "a package whose analysis failed is not cached")

	if err != nil {
		assert.Empty(t, cached, "nothing is cached when the run fails")

		return
	}

	for pkg := range cached {
		got := 0

		for _, issue := range cachedIssues {
			if issue.Pkg == pkg {
				got++
			}
		}

		assert.Equal(t, 1, got, "package %s", pkg.Name)
	}

	for _, issue := range issues {
		if issue.Pkg != nil && cached[issue.Pkg] {
			assert.Contains(t, cachedIssues, issue)
		}
	}
}

func Test_runAnalyzers_typeErrorDoesNotStopOtherPackages(t *testing.T) {
	dir := writeCacheTestModule(t)

	// Without the failing package nothing cancels the analysis, so every other package is analyzed and cached.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, cacheTestFailPkg)))

	pkgs := loadCacheTestPackages(t, dir)
	lintCtx := newCacheTestContext(t, pkgs)

	issues, err := runAnalyzers(cacheTestConfig{}, lintCtx)
	require.NoError(t, err)

	byName := pkgsByName(pkgs)

	var typeErrors []string

	perPkg := map[*packages.Package]int{}

	for _, issue := range issues {
		perPkg[issue.Pkg]++

		if issue.FromLinter == "typecheck" {
			typeErrors = append(typeErrors, filepath.Base(issue.Pos.Filename)+": "+issue.Text)
		}
	}

	// The importers of the broken package don't repeat its error, an unrelated type error is still reported.
	require.Len(t, typeErrors, 2)
	assert.Contains(t, typeErrors, "otherbad.go: undefined: undefinedName")
	assert.Contains(t, typeErrors, "bad.go: cannot use \"not an int\" (untyped string constant) as int value in variable declaration")

	for i := range goodPkgCount {
		pkg := byName[fmt.Sprintf("g%d", i)]

		assert.Equal(t, 1, perPkg[pkg], "package %s is analyzed", pkg.Name)
	}

	_, cached := loadIssuesFromCache(pkgs, lintCtx, cacheTestConfig{}.getAnalyzers())

	for i := range goodPkgCount {
		pkg := byName[fmt.Sprintf("g%d", i)]

		assert.True(t, cached[pkg], "package %s is cached", pkg.Name)
	}

	assert.False(t, cached[byName["bad"]])
	assert.False(t, cached[byName["importsbad"]])
}

func Test_saveIssuesToCache_skipsUnfinishedDependencies(t *testing.T) {
	dir := writeCacheTestModule(t)
	pkgs := loadCacheTestPackages(t, dir)
	lintCtx := newCacheTestContext(t, pkgs)

	byName := pkgsByName(pkgs)

	unfinished := map[*packages.Package]bool{byName["bad"]: true}

	var issues []*result.Issue

	saveIssuesToCache(pkgs, map[*packages.Package]bool{}, unfinished, issues, lintCtx, cacheTestConfig{}.getAnalyzers())

	_, cached := loadIssuesFromCache(pkgs, lintCtx, cacheTestConfig{}.getAnalyzers())

	assert.False(t, cached[byName["bad"]])
	assert.False(t, cached[byName["importsbad"]])
	assert.True(t, cached[byName["g0"]])
	assert.True(t, cached[byName["fail"]])
}

func Test_newUnfinishedDependencyChecker(t *testing.T) {
	leaf := &packages.Package{PkgPath: "leaf"}
	middle := &packages.Package{PkgPath: "middle", Imports: map[string]*packages.Package{"leaf": leaf}}
	top := &packages.Package{PkgPath: "top", Imports: map[string]*packages.Package{"middle": middle}}
	other := &packages.Package{PkgPath: "other"}

	t.Run("nothing is unfinished", func(t *testing.T) {
		hasUnfinished := newUnfinishedDependencyChecker(nil)

		assert.False(t, hasUnfinished(top))
	})

	t.Run("reports the package and its dependants", func(t *testing.T) {
		hasUnfinished := newUnfinishedDependencyChecker(map[*packages.Package]bool{leaf: true})

		assert.True(t, hasUnfinished(leaf))
		assert.True(t, hasUnfinished(middle))
		assert.True(t, hasUnfinished(top))
		assert.False(t, hasUnfinished(other))
	})

	t.Run("does not report the dependencies", func(t *testing.T) {
		hasUnfinished := newUnfinishedDependencyChecker(map[*packages.Package]bool{top: true})

		assert.True(t, hasUnfinished(top))
		assert.False(t, hasUnfinished(middle))
		assert.False(t, hasUnfinished(leaf))
	})
}
