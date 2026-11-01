package pkgerrors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
)

func newTestContext(pkgs ...*packages.Package) *linter.Context {
	return &linter.Context{Log: logutils.NewStderrLog("skip"), Packages: pkgs}
}

func illTypedErr(pkg *packages.Package) error {
	return fmt.Errorf("analysis skipped: %w", &IllTypedError{Pkg: pkg})
}

func importError(path string) packages.Error {
	return packages.Error{
		Pos:  "/src/dependant.go:3:8",
		Msg:  "could not import " + path + " (/src/bad/bad.go:3:15: cannot use 1 as string value)",
		Kind: packages.TypeError,
	}
}

func TestBuildIssuesFromIllTypedError_reportsRootCauseOnce(t *testing.T) {
	bad := &packages.Package{
		PkgPath:  "m/bad",
		IllTyped: true,
		Errors:   []packages.Error{{Pos: "/src/bad/bad.go:3:15", Msg: "cannot use 1 as string value", Kind: packages.TypeError}},
	}

	dependants := make([]*packages.Package, 0, 3)
	for i := range 3 {
		dependants = append(dependants, &packages.Package{
			PkgPath:  fmt.Sprintf("m/dependant%d", i),
			IllTyped: true,
			Imports:  map[string]*packages.Package{"m/bad": bad},
			Errors: []packages.Error{
				importError("m/bad"),
				{Pos: "/src/dependant.go:9:3", Msg: "bad.X undefined (follow-up of the failed import)", Kind: packages.TypeError},
			},
		})
	}

	other := &packages.Package{
		PkgPath:  "m/other",
		IllTyped: true,
		Errors:   []packages.Error{{Pos: "/src/other/other.go:5:2", Msg: "undefined: x", Kind: packages.TypeError}},
	}

	// Which package an error is attributed to depends on the analyzer that fails first: some dependants have no error of their own.
	errs := []error{illTypedErr(dependants[0]), illTypedErr(bad), illTypedErr(other)}

	lintCtx := newTestContext(append([]*packages.Package{bad, other}, dependants...)...)

	issues, err := BuildIssuesFromIllTypedError(errs, lintCtx)
	require.NoError(t, err)

	var texts []string
	for _, issue := range issues {
		texts = append(texts, issue.Text)
	}

	assert.ElementsMatch(t, []string{"cannot use 1 as string value", "undefined: x"}, texts)
}

func TestBuildIssuesFromIllTypedError_keepsImportErrorOfUnreportedImport(t *testing.T) {
	// The broken import is not analyzed itself, so nothing else reports it.
	bad := &packages.Package{
		PkgPath:  "m/bad",
		IllTyped: true,
		Errors:   []packages.Error{{Pos: "/src/bad/bad.go:3:15", Msg: "cannot use 1 as string value", Kind: packages.TypeError}},
	}

	dependant := &packages.Package{
		PkgPath:  "m/dependant",
		IllTyped: true,
		Imports:  map[string]*packages.Package{"m/bad": bad},
		Errors:   []packages.Error{importError("m/bad")},
	}

	issues, err := BuildIssuesFromIllTypedError([]error{illTypedErr(dependant)}, newTestContext(dependant))
	require.NoError(t, err)
	require.Len(t, issues, 1)

	assert.Contains(t, issues[0].Text, "could not import m/bad")
}

func TestBuildIssuesFromIllTypedError_chain(t *testing.T) {
	bad := &packages.Package{
		PkgPath:  "m/bad",
		IllTyped: true,
		Errors:   []packages.Error{{Pos: "/src/bad/bad.go:3:15", Msg: "cannot use 1 as string value", Kind: packages.TypeError}},
	}

	middle := &packages.Package{
		PkgPath:  "m/middle",
		IllTyped: true,
		Imports:  map[string]*packages.Package{"m/bad": bad},
		Errors:   []packages.Error{importError("m/bad")},
	}

	top := &packages.Package{
		PkgPath:  "m/top",
		IllTyped: true,
		Imports:  map[string]*packages.Package{"m/middle": middle},
		Errors:   []packages.Error{importError("m/middle")},
	}

	issues, err := BuildIssuesFromIllTypedError([]error{illTypedErr(top), illTypedErr(middle), illTypedErr(bad)}, newTestContext(top, middle, bad))
	require.NoError(t, err)
	require.Len(t, issues, 1)

	assert.Equal(t, "cannot use 1 as string value", issues[0].Text)
}

func TestBuildIssuesFromIllTypedError_matchesTestVariantByPath(t *testing.T) {
	rootError := packages.Error{Pos: "/src/bad/bad.go:3:15", Msg: "cannot use 1 as string value", Kind: packages.TypeError}

	// Only the variant with tests is analyzed, the dependant imports the plain package.
	plain := &packages.Package{ID: "m/bad", PkgPath: "m/bad", IllTyped: true, Errors: []packages.Error{rootError}}
	variant := &packages.Package{ID: "m/bad [m/bad.test]", PkgPath: "m/bad", IllTyped: true, Errors: []packages.Error{rootError}}

	dependant := &packages.Package{
		PkgPath:  "m/dependant",
		IllTyped: true,
		Imports:  map[string]*packages.Package{"m/bad": plain},
		Errors:   []packages.Error{importError("m/bad")},
	}

	issues, err := BuildIssuesFromIllTypedError([]error{illTypedErr(variant), illTypedErr(dependant)}, newTestContext(variant, dependant))
	require.NoError(t, err)
	require.Len(t, issues, 1)

	assert.Equal(t, "cannot use 1 as string value", issues[0].Text)
}
