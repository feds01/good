package goanalysis

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/pkgerrors"
	"github.com/golangci/golangci-lint/v2/pkg/goutil"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
)

const unsafePkgName = "unsafe"

const (
	cgoRuntimePath = "runtime/cgo"
	cgoSyscallPath = "syscall"
)

// https://github.com/golang/go/blob/go1.23.8/src/internal/types/errors/codes.go#L1484
const tooNew = 151

type loadingPackage struct {
	pkg         *packages.Package
	imports     map[string]*loadingPackage
	isInitial   bool
	log         logutils.Log
	actions     []*action // all actions with this package
	loadGuard   *load.Guard
	dependents  int32 // number of depending on it packages
	analyzeOnce sync.Once
	decUseMutex sync.Mutex

	// isCgoProcessed is set when the files of the package were replaced by the cgo-processed ones.
	isCgoProcessed bool

	// depLoader loads the data that is not loaded together with the packages, it's nil in tests.
	depLoader linter.DependencyLoader
}

func (lp *loadingPackage) analyzeRecursive(ctx context.Context, cancel context.CancelFunc, loadMode LoadMode, loadSem chan struct{}) {
	lp.analyzeOnce.Do(func() {
		// Load the direct dependencies, in parallel.
		var wg sync.WaitGroup

		for _, imp := range lp.imports {
			wg.Go(func() {
				imp.analyzeRecursive(ctx, cancel, loadMode, loadSem)
			})
		}

		wg.Wait()

		lp.analyze(ctx, cancel, loadMode, loadSem)
	})
}

func (lp *loadingPackage) analyze(ctx context.Context, cancel context.CancelFunc, loadMode LoadMode, loadSem chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case loadSem <- struct{}{}:
		defer func() {
			<-loadSem
		}()
	}

	// Save memory on unused more fields.
	defer lp.decUse(loadMode < LoadModeWholeProgram)

	if err := lp.loadWithFacts(loadMode); err != nil {
		// Note: this error is ignored when there is no facts loading (e.g. with 98% of linters).
		// But this is not a problem because the errors are added to the package.Errors.
		// You through an error, try to add it to actions, but there is no action annnddd it's gone!
		werr := fmt.Errorf("failed to load package %s: %w", lp.pkg.Name, err)

		// Don't need to write error to errCh, it will be extracted and reported on another layer.
		// Unblock depending on actions and propagate error.
		for _, act := range lp.actions {
			close(act.analysisDoneCh)

			act.Err = werr
		}

		if len(lp.actions) == 0 {
			lp.log.Warnf("no action but there is an error: %v", err)
		}

		return
	}

	actsWg, ctxGroup := errgroup.WithContext(ctx)

	for _, act := range lp.actions {
		actsWg.Go(func() error {
			act.waitUntilDependingAnalyzersWorked(ctxGroup)

			select {
			case <-ctxGroup.Done():
				return nil
			default:
			}

			act.analyzeSafe()

			return act.Err
		})
	}

	err := actsWg.Wait()

	// A package that doesn't type-check is reported as an issue, it has no effect on the analysis of the others.
	var illTypedErr *pkgerrors.IllTypedError
	if err != nil && !errors.As(err, &illTypedErr) {
		cancel()
	}
}

func (lp *loadingPackage) parseSyntax() {
	pkg := lp.pkg

	// Many packages have few files, much fewer than there
	// are CPU cores. Additionally, parsing each individual file is
	// very fast. A naive parallel implementation of this loop won't
	// be faster, and tends to be slower due to extra scheduling,
	// bookkeeping and potentially false sharing of cache lines.
	pkg.Syntax = make([]*ast.File, 0, len(pkg.CompiledGoFiles))
	for _, file := range pkg.CompiledGoFiles {
		f, err := parser.ParseFile(pkg.Fset, file, nil, parser.ParseComments)
		if err != nil {
			pkg.Errors = append(pkg.Errors, lp.convertError(err)...)
			continue
		}
		pkg.Syntax = append(pkg.Syntax, f)
	}
}

// hasUnprocessedCgo reports whether the parsed files of a dependency still import "C":
// the loader leaves the cgo processing of dependencies to the first linter that type-checks them.
func (lp *loadingPackage) hasUnprocessedCgo() bool {
	if lp.isInitial || lp.depLoader == nil {
		return false
	}

	for _, file := range lp.pkg.Syntax {
		for _, imp := range file.Imports {
			if imp.Path.Value == `"C"` {
				return true
			}
		}
	}

	return false
}

// processCgo replaces the files of a dependency by the cgo-processed ones, if it has cgo files.
func (lp *loadingPackage) processCgo() {
	pkg := lp.pkg

	if len(pkg.Errors) != 0 || !lp.hasUnprocessedCgo() {
		return
	}

	err := lp.depLoader.LoadCompiledGoFiles(pkg)
	if err != nil {
		pkg.Errors = append(pkg.Errors, packages.Error{
			Pos:  "-",
			Msg:  fmt.Sprintf("could not process cgo files: %s", err),
			Kind: packages.ListError,
		})

		return
	}

	lp.isCgoProcessed = true

	lp.parseSyntax()
}

// cgoGeneratedImport returns the packages that the code generated by cgo imports, which the listing of the
// dependency doesn't contain because it was listed without cgo processing.
func (lp *loadingPackage) cgoGeneratedImport(path string) (*types.Package, error) {
	if !lp.isCgoProcessed {
		return nil, nil
	}

	switch path {
	case cgoRuntimePath, cgoSyscallPath:
	default:
		return nil, nil
	}

	if tpkg := lp.findTransitiveImport(path); tpkg != nil {
		return tpkg, nil
	}

	if path == cgoRuntimePath {
		return newCgoRuntimePackage(), nil
	}

	tpkg, err := lp.depLoader.LoadSupportPackage(path)
	if err != nil {
		return nil, fmt.Errorf("could not load %s for the cgo files: %w", path, err)
	}

	return tpkg, nil
}

func (lp *loadingPackage) findTransitiveImport(path string) *types.Package {
	seen := map[*packages.Package]bool{}

	var visit func(pkgs map[string]*packages.Package) *types.Package
	visit = func(pkgs map[string]*packages.Package) *types.Package {
		for _, imp := range pkgs {
			if seen[imp] {
				continue
			}

			seen[imp] = true

			if imp.PkgPath == path && imp.Types != nil && len(imp.Errors) == 0 {
				return imp.Types
			}

			if found := visit(imp.Imports); found != nil {
				return found
			}
		}

		return nil
	}

	return visit(lp.pkg.Imports)
}

// newCgoRuntimePackage returns a stand-in for runtime/cgo, which cgo processing imports after the imports are listed.
// Only the Incomplete type is referenced by the generated code.
func newCgoRuntimePackage() *types.Package {
	pkg := types.NewPackage(cgoRuntimePath, "cgo")

	typeName := types.NewTypeName(token.NoPos, pkg, "Incomplete", nil)
	types.NewNamed(typeName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(typeName)

	pkg.MarkComplete()

	return pkg
}

func (lp *loadingPackage) loadFromSource(loadMode LoadMode) error {
	pkg := lp.pkg

	lp.parseSyntax()
	lp.processCgo()

	if len(pkg.Errors) != 0 {
		pkg.IllTyped = true
		return nil
	}

	if loadMode == LoadModeSyntax {
		return nil
	}

	// Call NewPackage directly with explicit name.
	// This avoids skew between golist and go/types when the files'
	// package declarations are inconsistent.
	pkg.Types = types.NewPackage(pkg.PkgPath, pkg.Name)

	pkg.IllTyped = true

	pkg.TypesInfo = &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Instances:    make(map[*ast.Ident]types.Instance),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:       make(map[ast.Node]*types.Scope),
		FileVersions: make(map[*ast.File]string),
	}

	importer := func(path string) (*types.Package, error) {
		if path == unsafePkgName {
			return types.Unsafe, nil
		}
		if path == "C" {
			// go/packages doesn't tell us that cgo preprocessing
			// failed. When we subsequently try to parse the package,
			// we'll encounter the raw C import.
			return nil, errors.New("cgo preprocessing failed")
		}
		imp := pkg.Imports[path]
		if imp == nil {
			return lp.cgoGeneratedImport(path)
		}
		if len(imp.Errors) > 0 {
			return nil, imp.Errors[0]
		}
		return imp.Types, nil
	}

	var goVersion string
	if pkg.Module != nil && pkg.Module.GoVersion != "" {
		goVersion = "go" + strings.TrimPrefix(pkg.Module.GoVersion, "go")
	} else {
		var err error
		goVersion, err = goutil.CleanRuntimeVersion()
		if err != nil {
			return err
		}
	}

	tc := &types.Config{
		Importer: importerFunc(importer),
		Error: func(err error) {
			pkg.Errors = append(pkg.Errors, lp.convertError(err)...)
		},
		GoVersion: goVersion,
		Sizes:     types.SizesFor(build.Default.Compiler, build.Default.GOARCH),
	}

	_ = types.NewChecker(tc, pkg.Fset, pkg.Types, pkg.TypesInfo).Files(pkg.Syntax)
	// Don't handle error here: errors are adding by tc.Error function.

	illTyped := len(pkg.Errors) != 0
	if !illTyped {
		for _, imp := range lp.imports {
			if imp.pkg.IllTyped {
				illTyped = true
				break
			}
		}
	}
	pkg.IllTyped = illTyped
	return nil
}

func (lp *loadingPackage) loadWithFacts(loadMode LoadMode) error {
	pkg := lp.pkg

	if pkg.PkgPath == unsafePkgName {
		// Fill in the blanks to avoid surprises.
		pkg.Syntax = []*ast.File{}
		if loadMode >= LoadModeTypesInfo {
			pkg.Types = types.Unsafe
			pkg.TypesInfo = new(types.Info)
		}
		return nil
	}

	if pkg.TypesInfo != nil {
		// Already loaded package, e.g. because another not go/analysis linter required types for deps.
		// Try load cached facts for it.

		for _, act := range lp.actions {
			if !act.loadActionCachedFacts(false) {
				// Cached facts loading failed: analyze later the action from source.
				act.needAnalyzeSource = true
				factsCacheDebugf("Loading of facts for already loaded %s failed, analyze it from source later", act)
				act.markDepsForAnalyzingSource()
			}
		}
		return nil
	}

	if lp.isInitial {
		// No need to load cached facts: the package will be analyzed from source
		// because it's the initial.
		return lp.loadFromSource(loadMode)
	}

	return lp.loadImportedPackageWithFacts(loadMode)
}

func (lp *loadingPackage) loadImportedPackageWithFacts(loadMode LoadMode) error {
	if loadMode >= LoadModeTypesInfo {
		// Reading export data would make `go list` compile the whole dependency graph.
		return lp.loadFromSourceWithCachedFacts(loadMode)
	}

	needLoadFromSource := false

	for _, act := range lp.actions {
		if act.loadActionCachedFacts(false) {
			continue
		}

		// Cached facts loading failed: analyze later the action from source.
		factsCacheDebugf("Loading of facts for %s failed, analyze it from source later", act)
		act.needAnalyzeSource = true // can't be set in parallel
		needLoadFromSource = true

		act.markDepsForAnalyzingSource()
	}

	if needLoadFromSource {
		return lp.loadFromSource(loadMode)
	}

	return nil
}

// loadFromSourceWithCachedFacts type-checks the package from source, then reuses the facts of the actions that are cached.
func (lp *loadingPackage) loadFromSourceWithCachedFacts(loadMode LoadMode) error {
	lp.pkg.Types = types.NewPackage(lp.pkg.PkgPath, lp.pkg.Name)

	if err := lp.loadFromSource(loadMode); err != nil {
		return err
	}

	for _, act := range lp.actions {
		if act.loadActionCachedFacts(false) {
			continue
		}

		factsCacheDebugf("Loading of facts for %s failed, analyze it from source later", act)
		act.needAnalyzeSource = true // can't be set in parallel

		act.markDepsForAnalyzingSource()
	}

	return nil
}

func (lp *loadingPackage) decUse(canClearTypes bool) {
	lp.decUseMutex.Lock()
	defer lp.decUseMutex.Unlock()

	for _, act := range lp.actions {
		pass := act.pass
		if pass == nil {
			continue
		}

		pass.Files = nil
		pass.TypesInfo = nil
		pass.TypesSizes = nil
		pass.ResultOf = nil
		pass.Pkg = nil
		pass.OtherFiles = nil
		pass.AllObjectFacts = nil
		pass.AllPackageFacts = nil
		pass.ImportObjectFact = nil
		pass.ExportObjectFact = nil
		pass.ImportPackageFact = nil
		pass.ExportPackageFact = nil
		act.pass = nil
		act.Deps = nil
		if act.Result != nil {
			if isMemoryDebug {
				debugf("%s: decUse: nilling act result of size %d bytes", act, sizeOfValueTreeBytes(act.Result))
			}
			act.Result = nil
		}
	}

	lp.pkg.Syntax = nil
	lp.pkg.TypesInfo = nil
	lp.pkg.TypesSizes = nil

	dependents := atomic.AddInt32(&lp.dependents, -1)
	if dependents != 0 {
		return
	}

	if canClearTypes {
		// canClearTypes is set to true if we can discard type
		// information after the package and its dependents have been
		// processed. This is the case when no whole program checkers (unused) are
		// being run.
		lp.pkg.Types = nil
	}
	lp.pkg = nil

	for _, imp := range lp.imports {
		imp.decUse(canClearTypes)
	}
	lp.imports = nil

	for _, act := range lp.actions {
		if !lp.isInitial {
			act.Package = nil
		}
		act.releaseFacts()
	}
	lp.actions = nil
}

func (lp *loadingPackage) convertError(err error) []packages.Error {
	var errs []packages.Error
	// taken from go/packages
	switch err := err.(type) {
	case packages.Error:
		// from driver
		errs = append(errs, err)

	case *os.PathError:
		// from parser
		errs = append(errs, packages.Error{
			Pos:  err.Path + ":1",
			Msg:  err.Err.Error(),
			Kind: packages.ParseError,
		})

	case scanner.ErrorList:
		// from parser
		for _, err := range err {
			errs = append(errs, packages.Error{
				Pos:  err.Pos.String(),
				Msg:  err.Msg,
				Kind: packages.ParseError,
			})
		}

	case types.Error:
		// from type checker

		// https://github.com/golang/go/blob/go1.23.8/src/go/types/api.go#L52-L57
		if int(reflect.ValueOf(err).FieldByName("go116code").Int()) == tooNew {
			// https://github.com/golang/go/blob/go1.23.8/src/go/types/check.go#L380
			// https://github.com/golang/go/blob/go1.23.8/src/go/types/check.go#L349
			panic(err.Msg)
		}

		errs = append(errs, packages.Error{
			Pos:  err.Fset.Position(err.Pos).String(),
			Msg:  err.Msg,
			Kind: packages.TypeError,
		})

	default:
		// unexpected impoverished error from parser?
		errs = append(errs, packages.Error{
			Pos:  "-",
			Msg:  err.Error(),
			Kind: packages.UnknownError,
		})

		// If you see this error message, please file a bug.
		lp.log.Warnf("Internal error: error %q (%T) without position", err, err)
	}

	return errs
}

func (lp *loadingPackage) String() string {
	return fmt.Sprintf("%s@%s", lp.pkg.PkgPath, lp.pkg.Name)
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func sizeOfValueTreeBytes(v any) int {
	return sizeOfReflectValueTreeBytes(reflect.ValueOf(v), map[uintptr]struct{}{})
}

func sizeOfReflectValueTreeBytes(rv reflect.Value, visitedPtrs map[uintptr]struct{}) int {
	switch rv.Kind() {
	case reflect.Pointer:
		ptrSize := int(rv.Type().Size())
		if rv.IsNil() {
			return ptrSize
		}
		ptr := rv.Pointer()
		if _, ok := visitedPtrs[ptr]; ok {
			return 0
		}
		visitedPtrs[ptr] = struct{}{}
		return ptrSize + sizeOfReflectValueTreeBytes(rv.Elem(), visitedPtrs)
	case reflect.Interface:
		if rv.IsNil() {
			return 0
		}
		return sizeOfReflectValueTreeBytes(rv.Elem(), visitedPtrs)
	case reflect.Struct:
		ret := 0
		for _, field := range rv.Fields() {
			ret += sizeOfReflectValueTreeBytes(field, visitedPtrs)
		}
		return ret
	case reflect.Slice, reflect.Array, reflect.Chan:
		return int(rv.Type().Size()) + rv.Cap()*int(rv.Type().Elem().Size())
	case reflect.Map:
		ret := 0
		for _, key := range rv.MapKeys() {
			mv := rv.MapIndex(key)
			ret += sizeOfReflectValueTreeBytes(key, visitedPtrs)
			ret += sizeOfReflectValueTreeBytes(mv, visitedPtrs)
		}
		return ret
	case reflect.String:
		return rv.Len()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr, reflect.Bool, reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128, reflect.Func, reflect.UnsafePointer:
		return int(rv.Type().Size())
	case reflect.Invalid:
		return 0
	default:
		panic("unknown rv of type " + rv.String())
	}
}
