package processors

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/golangci/golangci-lint/v2/pkg/goformatters"
	"github.com/golangci/golangci-lint/v2/pkg/golinters/nolintlint"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

const (
	rankLinter = iota
	rankFormatter
	rankNolintlint
)

var _ Processor = (*CanonicalOrder)(nil)

// CanonicalOrder sorts issues by a total order.
// Linters report in the order their goroutines finish (and cached results come first),
// so every processor that keeps the first of several issues needs a stable input order.
type CanonicalOrder struct{}

func NewCanonicalOrder() *CanonicalOrder {
	return &CanonicalOrder{}
}

func (CanonicalOrder) Name() string { return "canonical_order" }

func (CanonicalOrder) Process(issues []*result.Issue) ([]*result.Issue, error) {
	slices.SortFunc(issues, compareIssues)

	return issues, nil
}

func (CanonicalOrder) Finish() {}

// compareIssues is a total order: it only returns zero for issues that are identical in everything but the cached package.
// The linter comes before the column so that, of several issues on one line, uniq-by-line keeps one from a linter
// ranked first (real linters by name, then formatters, then nolintlint), then the leftmost.
// A formatter must not win over a real issue: max-per-file-from-linter drops all but one issue of a formatter per file,
// after uniq-by-line, and the real issue would be gone with it.
func compareIssues(a, b *result.Issue) int {
	return cmp.Or(
		strings.Compare(a.FilePath(), b.FilePath()),
		cmp.Compare(a.Line(), b.Line()),
		cmp.Compare(linterRank(a.FromLinter), linterRank(b.FromLinter)),
		strings.Compare(a.FromLinter, b.FromLinter),
		cmp.Compare(a.Column(), b.Column()),
		strings.Compare(a.Text, b.Text),
		strings.Compare(a.Severity, b.Severity),
		cmp.Compare(a.Pos.Offset, b.Pos.Offset),
		cmp.Compare(a.GetLineRange().From, b.GetLineRange().From),
		cmp.Compare(a.GetLineRange().To, b.GetLineRange().To),
		strings.Compare(a.ExpectedNoLintLinter, b.ExpectedNoLintLinter),
		slices.Compare(a.SourceLines, b.SourceLines),
		slices.CompareFunc(a.SuggestedFixes, b.SuggestedFixes, compareFixes),
	)
}

func linterRank(name string) int {
	switch {
	case name == nolintlint.LinterName:
		return rankNolintlint
	case goformatters.IsFormatter(name):
		return rankFormatter
	default:
		return rankLinter
	}
}

func compareFixes(a, b analysis.SuggestedFix) int {
	return cmp.Or(
		strings.Compare(a.Message, b.Message),
		slices.CompareFunc(a.TextEdits, b.TextEdits, compareEdits),
	)
}

func compareEdits(a, b analysis.TextEdit) int {
	return cmp.Or(
		cmp.Compare(a.Pos, b.Pos),
		cmp.Compare(a.End, b.End),
		bytes.Compare(a.NewText, b.NewText),
	)
}
