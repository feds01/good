package lint

import (
	"context"
	"errors"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ldez/grignotin/goenv"
	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/goutil"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
)

// PackageLoader loads packages based on [golang.org/x/tools/go/packages.Load].
type PackageLoader struct {
	log    logutils.Log
	debugf logutils.DebugFunc

	cfg *config.Config

	args []string

	pkgTestIDRe *regexp.Regexp

	goenv *goutil.Env

	loadGuard *load.Guard

	fileCache *fsutils.FileCache

	supportMu   sync.Mutex
	supportFset *token.FileSet
	supportView map[string]*types.Package

	exportOnce  sync.Once
	exportFiles map[string]string
	exportErr   error
}

// NewPackageLoader creates a new PackageLoader.
func NewPackageLoader(log logutils.Log, cfg *config.Config, args []string, env *goutil.Env, loadGuard *load.Guard,
	fileCache *fsutils.FileCache,
) *PackageLoader {
	return &PackageLoader{
		cfg:         cfg,
		args:        args,
		log:         log,
		debugf:      logutils.Debug(logutils.DebugKeyLoader),
		goenv:       env,
		pkgTestIDRe: regexp.MustCompile(`^(.*) \[(.*)\.test\]`),
		loadGuard:   loadGuard,
		fileCache:   fileCache,
	}
}

// Load loads packages.
func (l *PackageLoader) Load(ctx context.Context, linters []*linter.Config) (pkgs, deduplicatedPkgs []*packages.Package, err error) {
	loadMode := findLoadMode(linters)

	pkgs, err = l.loadPackages(ctx, loadMode)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load packages: %w", err)
	}

	return pkgs, l.filterDuplicatePackages(pkgs), nil
}

func (l *PackageLoader) loadPackages(ctx context.Context, loadMode packages.LoadMode) ([]*packages.Package, error) {
	l.prepareBuildContext()

	// Export data makes `go list` compile every dependency, and compiled files (also requested by the types sizes)
	// make it run the C compiler for every cgo dependency.
	// Both are skipped here: the export data is loaded on demand by LoadExportData,
	// and dependencies are type-checked from their original files.
	isLazyExport := loadMode&packages.NeedExportFile != 0
	if isLazyExport {
		loadMode &^= packages.NeedExportFile | packages.NeedCompiledGoFiles | packages.NeedTypesSizes
	}

	pkgs, err := l.listPackages(ctx, loadMode)
	if err != nil {
		return nil, err
	}

	if isLazyExport {
		pkgs, err = l.completeLazyPackages(ctx, loadMode, pkgs)
		if err != nil {
			return nil, err
		}
	}

	if loadMode&packages.NeedSyntax == 0 {
		// Needed e.g. for go/analysis loading.
		fset := token.NewFileSet()
		packages.Visit(pkgs, nil, func(pkg *packages.Package) {
			pkg.Fset = fset
			l.loadGuard.AddMutexForPkg(pkg)
		})
	}

	l.debugPrintLoadedPackages(pkgs)

	if err := l.parseLoadedPackagesErrors(pkgs); err != nil {
		return nil, err
	}

	return l.filterTestMainPackages(pkgs), nil
}

// completeLazyPackages sets the data that the lazy listing skips: the compiled files and the types sizes.
func (l *PackageLoader) completeLazyPackages(ctx context.Context, loadMode packages.LoadMode,
	pkgs []*packages.Package,
) ([]*packages.Package, error) {
	if hasCgoFiles(pkgs, l.fileCache) {
		// The analyzed packages need the cgo-processed files.
		var err error

		pkgs, err = l.listPackages(ctx, loadMode|packages.NeedCompiledGoFiles)
		if err != nil {
			return nil, err
		}
	} else {
		packages.Visit(pkgs, nil, func(pkg *packages.Package) {
			pkg.CompiledGoFiles = pkg.GoFiles
		})
	}

	sizes, err := l.typesSizes(ctx)
	if err != nil {
		return nil, err
	}

	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		pkg.TypesSizes = sizes
	})

	return pkgs, nil
}

// typesSizes returns the sizes go/packages would set with NeedTypesSizes, without listing the packages again.
func (l *PackageLoader) typesSizes(ctx context.Context) (types.Sizes, error) {
	conf := &packages.Config{
		Mode:       packages.NeedName | packages.NeedImports | packages.NeedTypesSizes,
		Context:    ctx,
		BuildFlags: l.makeBuildFlags(),
		Logf:       l.debugf,
	}

	pkgs, err := packages.Load(conf, "unsafe")
	if err != nil {
		return nil, fmt.Errorf("failed to load the types sizes: %w", err)
	}

	if len(pkgs) != 1 || pkgs[0].TypesSizes == nil {
		return nil, errors.New("failed to load the types sizes")
	}

	return pkgs[0].TypesSizes, nil
}

func (l *PackageLoader) listPackages(ctx context.Context, loadMode packages.LoadMode) ([]*packages.Package, error) {
	defer func(startedAt time.Time) {
		l.log.Infof("Go packages loading at mode %s took %s", stringifyLoadMode(loadMode), time.Since(startedAt))
	}(time.Now())

	conf := &packages.Config{
		Mode:       loadMode,
		Tests:      l.cfg.Run.AnalyzeTests,
		Context:    ctx,
		BuildFlags: l.makeBuildFlags(),
		Logf:       l.debugf,
		// TODO: use fset, parsefile, overlay
	}

	args := buildArgs(l.args)

	l.debugf("Built loader args are %s", args)

	pkgs, err := packages.Load(conf, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to load with go/packages: %w", err)
	}

	return pkgs, nil
}

// LoadExportData sets pkg.ExportFile.
// The first call builds the export data of all packages, the next calls reuse it.
func (l *PackageLoader) LoadExportData(ctx context.Context, pkg *packages.Package) error {
	l.exportOnce.Do(func() {
		l.exportFiles, l.exportErr = l.listExportFiles(ctx)
	})

	if l.exportErr != nil {
		return l.exportErr
	}

	pkg.ExportFile = l.exportFiles[pkg.ID]

	return nil
}

// LoadCompiledGoFiles sets pkg.CompiledGoFiles to the files with cgo processed.
func (l *PackageLoader) LoadCompiledGoFiles(ctx context.Context, pkg *packages.Package) error {
	if pkg.ID != pkg.PkgPath {
		return fmt.Errorf("package variant %s can't be loaded on its own", pkg.ID)
	}

	// The C objects are only linked to find the dynamic imports, so they don't need to be optimized.
	err := l.loadCompiledGoFiles(ctx, pkg, append(os.Environ(), cgoFlagsEnv(os.Getenv("CGO_CFLAGS"))))
	if err != nil {
		l.debugf("Retry cgo processing of %s with the default flags: %v", pkg.PkgPath, err)

		return l.loadCompiledGoFiles(ctx, pkg, nil)
	}

	return nil
}

func (l *PackageLoader) loadCompiledGoFiles(ctx context.Context, pkg *packages.Package, env []string) error {
	conf := &packages.Config{
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles,
		Context:    ctx,
		BuildFlags: l.makeBuildFlags(),
		Env:        env,
		Logf:       l.debugf,
	}

	pkgs, err := packages.Load(conf, pkg.PkgPath)
	if err != nil {
		return fmt.Errorf("failed to load with go/packages: %w", err)
	}

	if len(pkgs) != 1 || len(pkgs[0].Errors) != 0 {
		return fmt.Errorf("failed to process cgo files of %s: %v", pkg.PkgPath, pkgs)
	}

	pkg.CompiledGoFiles = pkgs[0].CompiledGoFiles

	return nil
}

// cgoFlagsEnv returns the CGO_CFLAGS setting that disables optimization on top of the current flags.
func cgoFlagsEnv(cflags string) string {
	return "CGO_CFLAGS=" + strings.TrimSpace(cflags+" -O0 -g0")
}

// LoadSupportPackage returns the types of a package that is not in the loaded packages, from its export data.
func (l *PackageLoader) LoadSupportPackage(ctx context.Context, path string) (*types.Package, error) {
	l.supportMu.Lock()
	defer l.supportMu.Unlock()

	if tpkg := l.supportView[path]; tpkg != nil && tpkg.Complete() {
		return tpkg, nil
	}

	conf := &packages.Config{
		Mode:       packages.NeedName | packages.NeedExportFile,
		Context:    ctx,
		BuildFlags: l.makeBuildFlags(),
		Logf:       l.debugf,
	}

	pkgs, err := packages.Load(conf, path)
	if err != nil {
		return nil, fmt.Errorf("failed to load with go/packages: %w", err)
	}

	if len(pkgs) != 1 || pkgs[0].ExportFile == "" {
		return nil, fmt.Errorf("no export data for %s", path)
	}

	file, err := os.Open(pkgs[0].ExportFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open the export data: %w", err)
	}

	defer func() { _ = file.Close() }()

	reader, err := gcexportdata.NewReader(file) //nolint:staticcheck // the export data comes from `go list -export`, like in pkg/goanalysis
	if err != nil {
		return nil, fmt.Errorf("failed to read the export data: %w", err)
	}

	if l.supportView == nil {
		l.supportView = map[string]*types.Package{}
		l.supportFset = token.NewFileSet()
	}

	return gcexportdata.Read(reader, l.supportFset, l.supportView, path)
}

func (l *PackageLoader) listExportFiles(ctx context.Context) (map[string]string, error) {
	// Only with cgo processed, go list adds the runtime/cgo import to the cgo packages.
	loadMode := packages.NeedName | packages.NeedImports | packages.NeedDeps | packages.NeedExportFile | packages.NeedCompiledGoFiles

	pkgs, err := l.listPackages(ctx, loadMode)
	if err != nil {
		return nil, err
	}

	exportFiles := map[string]string{}

	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		exportFiles[pkg.ID] = pkg.ExportFile
	})

	return exportFiles, nil
}

// hasCgoFiles reports whether one of the packages imports "C".
func hasCgoFiles(pkgs []*packages.Package, fileCache *fsutils.FileCache) bool {
	fset := token.NewFileSet()

	for _, pkg := range pkgs {
		for _, filename := range pkg.GoFiles {
			src, err := fileCache.GetFileBytes(filename)
			if err != nil {
				continue
			}

			file, err := parser.ParseFile(fset, filename, src, parser.ImportsOnly)
			if err != nil {
				continue
			}

			for _, imp := range file.Imports {
				if imp.Path.Value == `"C"` {
					return true
				}
			}
		}
	}

	return false
}

func (*PackageLoader) parseLoadedPackagesErrors(pkgs []*packages.Package) error {
	for _, pkg := range pkgs {
		var errs []packages.Error
		for _, err := range pkg.Errors {
			// quick fix: skip error related to `go list` invocation by packages.Load()
			// The behavior has been changed between go1.19 and go1.20, the error is now inside the JSON content.
			// https://github.com/golangci/golangci-lint/pull/3414#issuecomment-1364756303
			if strings.Contains(err.Msg, "# command-line-arguments") {
				continue
			}

			errs = append(errs, err)

			if strings.Contains(err.Msg, "no Go files") {
				return fmt.Errorf("package %s: %w", pkg.PkgPath, exitcodes.ErrNoGoFiles)
			}
			if strings.Contains(err.Msg, "cannot find package") {
				// when analyzing not existing directory
				return fmt.Errorf("%v: %w", err.Msg, exitcodes.ErrFailure)
			}
		}

		pkg.Errors = errs
	}

	return nil
}

func (l *PackageLoader) tryParseTestPackage(pkg *packages.Package) (name string, isTest bool) {
	matches := l.pkgTestIDRe.FindStringSubmatch(pkg.ID)
	if matches == nil {
		return "", false
	}

	return matches[1], true
}

func (l *PackageLoader) filterDuplicatePackages(pkgs []*packages.Package) []*packages.Package {
	packagesWithTests := map[string]bool{}
	for _, pkg := range pkgs {
		name, isTest := l.tryParseTestPackage(pkg)
		if !isTest {
			continue
		}
		packagesWithTests[name] = true
	}

	l.debugf("package with tests: %#v", packagesWithTests)

	var retPkgs []*packages.Package
	for _, pkg := range pkgs {
		_, isTest := l.tryParseTestPackage(pkg)
		if !isTest && packagesWithTests[pkg.PkgPath] {
			// If tests loading is enabled,
			// for package with files a.go and a_test.go go/packages loads two packages:
			// 1. ID=".../a" GoFiles=[a.go]
			// 2. ID=".../a [.../a.test]" GoFiles=[a.go a_test.go]
			// We need only the second package, otherwise we can get warnings about unused variables/fields/functions
			// in a.go if they are used only in a_test.go.
			l.debugf("skip pkg ID=%s because we load it with test package", pkg.ID)
			continue
		}

		retPkgs = append(retPkgs, pkg)
	}

	return retPkgs
}

func (l *PackageLoader) filterTestMainPackages(pkgs []*packages.Package) []*packages.Package {
	var retPkgs []*packages.Package
	for _, pkg := range pkgs {
		if pkg.Name == "main" && strings.HasSuffix(pkg.PkgPath, ".test") {
			// it's an implicit testmain package
			l.debugf("skip pkg ID=%s", pkg.ID)
			continue
		}

		retPkgs = append(retPkgs, pkg)
	}

	return retPkgs
}

func (l *PackageLoader) debugPrintLoadedPackages(pkgs []*packages.Package) {
	l.debugf("loaded %d pkgs", len(pkgs))
	for i, pkg := range pkgs {
		var syntaxFiles []string
		for _, sf := range pkg.Syntax {
			syntaxFiles = append(syntaxFiles, pkg.Fset.Position(sf.Pos()).Filename)
		}
		l.debugf("Loaded pkg #%d: ID=%s GoFiles=%s CompiledGoFiles=%s Syntax=%s",
			i, pkg.ID, pkg.GoFiles, pkg.CompiledGoFiles, syntaxFiles)
	}
}

func (l *PackageLoader) prepareBuildContext() {
	// Set GOROOT to have working cross-compilation: cross-compiled binaries
	// have invalid GOROOT. XXX: can't use runtime.GOROOT().
	goroot := l.goenv.Get(goenv.GOROOT)
	if goroot == "" {
		return
	}

	_ = os.Setenv(goenv.GOROOT, goroot)

	build.Default.GOROOT = goroot
	build.Default.BuildTags = l.cfg.Run.BuildTags
}

func (l *PackageLoader) makeBuildFlags() []string {
	var buildFlags []string

	if len(l.cfg.Run.BuildTags) != 0 {
		// go help build
		buildFlags = append(buildFlags, "-tags", strings.Join(l.cfg.Run.BuildTags, " "))
		l.log.Infof("Using build tags: %v", l.cfg.Run.BuildTags)
	}

	if l.cfg.Run.ModulesDownloadMode != "" {
		// go help modules
		buildFlags = append(buildFlags, fmt.Sprintf("-mod=%s", l.cfg.Run.ModulesDownloadMode))
	}

	if !l.cfg.Run.EnableBuildVCS {
		// disable collecting VCS information
		buildFlags = append(buildFlags, "-buildvcs=false")
	}

	return buildFlags
}

func buildArgs(args []string) []string {
	if len(args) == 0 {
		return []string{"./..."}
	}

	var retArgs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, ".") || filepath.IsAbs(arg) {
			retArgs = append(retArgs, arg)
		} else {
			// go/packages doesn't work well if we don't have the prefix ./ for local packages
			retArgs = append(retArgs, fmt.Sprintf(".%c%s", filepath.Separator, arg))
		}
	}

	return retArgs
}

func findLoadMode(linters []*linter.Config) packages.LoadMode {
	loadMode := packages.LoadMode(0)
	for _, lc := range linters {
		loadMode |= lc.LoadMode
	}

	return loadMode
}

func stringifyLoadMode(mode packages.LoadMode) string {
	m := map[packages.LoadMode]string{
		packages.NeedCompiledGoFiles: "compiled_files",
		packages.NeedDeps:            "deps",
		packages.NeedExportFile:      "exports_file",
		packages.NeedFiles:           "files",
		packages.NeedImports:         "imports",
		packages.NeedName:            "name",
		packages.NeedSyntax:          "syntax",
		packages.NeedTypes:           "types",
		packages.NeedTypesInfo:       "types_info",
		packages.NeedTypesSizes:      "types_sizes",
	}

	var flags []string
	for flag, flagStr := range m {
		if mode&flag != 0 {
			flags = append(flags, flagStr)
		}
	}

	return fmt.Sprintf("%d (%s)", mode, strings.Join(flags, "|"))
}
