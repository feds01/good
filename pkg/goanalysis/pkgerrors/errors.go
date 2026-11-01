package pkgerrors

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

type IllTypedError struct {
	Pkg *packages.Package
}

func (e *IllTypedError) Error() string {
	return fmt.Sprintf("IllTypedError: errors in package: %v", e.Pkg.Errors)
}

func BuildIssuesFromIllTypedError(errs []error, lintCtx *linter.Context) ([]*result.Issue, error) {
	var issues []*result.Issue

	uniqReportedIssues := map[string]bool{}

	var other error

	reportedPaths := illTypedPaths(lintCtx)

	for _, err := range errs {
		var ill *IllTypedError
		if !errors.As(err, &ill) {
			if other == nil {
				other = err
			}
			continue
		}

		for _, err := range dropReportedImportErrors(ill.Pkg, extractErrors(ill.Pkg), reportedPaths) {
			issue, perr := parseError(err)
			if perr != nil { // failed to parse
				if uniqReportedIssues[err.Msg] {
					continue
				}

				uniqReportedIssues[err.Msg] = true
				lintCtx.Log.Errorf("typechecking error: %s", err.Msg)
			} else {
				key := fmt.Sprintf("%s.%d.%d.%s", issue.FilePath(), issue.Line(), issue.Column(), issue.Text)
				if uniqReportedIssues[key] {
					continue
				}

				uniqReportedIssues[key] = true

				issue.Pkg = ill.Pkg // to save to cache later
				issues = append(issues, issue)
			}
		}
	}

	if len(issues) == 0 && other != nil {
		return nil, other
	}

	return issues, nil
}

// illTypedPaths returns the paths of the analyzed packages that don't type-check: their errors are reported on their own.
// The package that an error is attributed to depends on which analyzer fails first, so the errors are not used for this.
func illTypedPaths(lintCtx *linter.Context) map[string]bool {
	paths := map[string]bool{}

	for _, pkgs := range [][]*packages.Package{lintCtx.Packages, lintCtx.OriginalPackages} {
		for _, pkg := range pkgs {
			if pkg.IllTyped {
				paths[pkg.PkgPath] = true
			}
		}
	}

	return paths
}

// dropReportedImportErrors drops all the errors of a package that fails to import a package that is reported on its own
// (matched by path, as the analyzed variant of a package with tests is not the one the dependants import):
// the dependants of a package with a type error would otherwise each repeat it,
// and the other errors they get are follow-ups of the invalid import.
func dropReportedImportErrors(pkg *packages.Package, pkgErrs []packages.Error, reportedPaths map[string]bool) []packages.Error {
	for path, imp := range pkg.Imports {
		if !reportedPaths[imp.PkgPath] {
			continue
		}

		prefix := "could not import " + path + " ("

		if slices.ContainsFunc(pkgErrs, func(pkgErr packages.Error) bool { return strings.HasPrefix(pkgErr.Msg, prefix) }) {
			return nil
		}
	}

	return pkgErrs
}
