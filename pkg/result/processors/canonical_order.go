package processors

import (
	"cmp"
	"slices"
	"strings"

	"github.com/golangci/golangci-lint/v2/pkg/result"
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
// The linter comes before the column so that, of several issues on one line, the first linter by name wins uniq-by-line.
func compareIssues(a, b *result.Issue) int {
	return cmp.Or(
		strings.Compare(a.FilePath(), b.FilePath()),
		cmp.Compare(a.Line(), b.Line()),
		strings.Compare(a.FromLinter, b.FromLinter),
		cmp.Compare(a.Column(), b.Column()),
		strings.Compare(a.Text, b.Text),
		strings.Compare(a.Severity, b.Severity),
		cmp.Compare(a.Pos.Offset, b.Pos.Offset),
		cmp.Compare(a.GetLineRange().From, b.GetLineRange().From),
		cmp.Compare(a.GetLineRange().To, b.GetLineRange().To),
		strings.Compare(a.ExpectedNoLintLinter, b.ExpectedNoLintLinter),
		slices.Compare(a.SourceLines, b.SourceLines),
	)
}
