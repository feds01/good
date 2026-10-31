package processors

import (
	"fmt"
	"go/token"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

func newOrderIssue(linter, text string, line, column int) *result.Issue {
	return &result.Issue{
		FromLinter: linter,
		Text:       text,
		Pos: token.Position{
			Filename: "f.go",
			Line:     line,
			Column:   column,
		},
	}
}

// orderIssues has several issues on the same position, and some without one.
func orderIssues() []*result.Issue {
	return []*result.Issue{
		newOrderIssue("revive", "unused-parameter", 3, 5),
		newOrderIssue("revive", "unexported-return", 3, 5),
		newOrderIssue("govet", "shadow", 3, 5),
		newOrderIssue("govet", "shadow", 3, 1),
		newOrderIssue("typecheck", "undefined", 0, 0),
		newOrderIssue("gosec", "G101", 0, 0),
		newOrderIssue("gosec", "G102", 7, 0),
		newOrderIssue("lll", "line too long", 7, 0),
	}
}

func shuffled(issues []*result.Issue, seed uint64) []*result.Issue {
	reordered := make([]*result.Issue, len(issues))
	copy(reordered, issues)

	random := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // a seeded shuffle keeps the test reproducible
	random.Shuffle(len(reordered), func(i, j int) {
		reordered[i], reordered[j] = reordered[j], reordered[i]
	})

	return reordered
}

func TestCanonicalOrder_Process(t *testing.T) {
	expected, err := NewCanonicalOrder().Process(orderIssues())
	require.NoError(t, err)

	for seed := range uint64(50) {
		actual, err := NewCanonicalOrder().Process(shuffled(orderIssues(), seed))
		require.NoError(t, err)

		assert.Equal(t, expected, actual, "seed %d", seed)
	}
}

func TestCanonicalOrder_uniqByLineKeepsTheSameIssue(t *testing.T) {
	for seed := range uint64(50) {
		issues, err := NewCanonicalOrder().Process(shuffled(orderIssues(), seed))
		require.NoError(t, err)

		issues, err = NewUniqByLine(true).Process(issues)
		require.NoError(t, err)

		var kept []string
		for _, issue := range issues {
			kept = append(kept, issue.FromLinter+": "+issue.Text)
		}

		assert.Equal(t, []string{"gosec: G101", "govet: shadow", "gosec: G102"}, kept, "seed %d", seed)
	}
}

func TestSortResults_Process_independentOfInputOrder(t *testing.T) {
	cfg := &config.Output{SortOrder: []string{orderNameFile, orderNameLinter}}

	expected, err := NewSortResults(cfg).Process(orderIssues())
	require.NoError(t, err)

	for seed := range uint64(50) {
		actual, err := NewSortResults(cfg).Process(shuffled(orderIssues(), seed))
		require.NoError(t, err)

		assert.Equal(t, expected, actual, "seed %d", seed)
	}
}

func TestExclusionRules_Finish_ordersByRule(t *testing.T) {
	log := logutils.NewMockLog()
	log.On("Infof", mock.Anything, mock.Anything, mock.Anything).Return()

	var rules []config.ExcludeRule
	for _, text := range []string{"g", "e", "a", "f", "b", "d", "c"} {
		rules = append(rules, config.ExcludeRule{BaseRule: config.BaseRule{Text: text}})
	}

	NewExclusionRules(log, nil, &config.LinterExclusions{Rules: rules}).Finish()

	var reported []string
	for _, call := range log.Calls {
		reported = append(reported, fmt.Sprint(call.Arguments.Get(2)))
	}

	assert.IsIncreasing(t, reported)
}

func TestExclusionPaths_Finish_keepsConfigOrder(t *testing.T) {
	log := logutils.NewMockLog()
	log.On("Child", mock.Anything).Return()
	log.On("Infof", mock.Anything, mock.Anything, mock.Anything).Return()

	p, err := NewExclusionPaths(log, &config.LinterExclusions{Paths: []string{"g", "e", "a", "f", "b", "d", "c"}})
	require.NoError(t, err)

	p.Finish()

	var reported []string
	for _, call := range log.Calls {
		if call.Method != "Infof" {
			continue
		}

		reported = append(reported, fmt.Sprint(call.Arguments.Get(2)))
	}

	assert.Equal(t, []string{"g", "e", "a", "f", "b", "d", "c"}, reported)
}

func TestWalkStringToIntMapSortedByValue_breaksTiesByKey(t *testing.T) {
	counts := map[string]int{"d": 1, "b": 2, "c": 2, "a": 2, "e": 1, "f": 3}

	var walked []string
	walkStringToIntMapSortedByValue(counts, func(key string, _ int) {
		walked = append(walked, key)
	})

	assert.Equal(t, []string{"f", "a", "b", "c", "d", "e"}, walked)
}

func TestCanonicalOrder_formatterDoesNotHideARealIssue(t *testing.T) {
	cfg := &config.Config{}

	for seed := range uint64(50) {
		issues := shuffled([]*result.Issue{
			newOrderIssue("golines", "File is not properly formatted", 5, 1),
			newOrderIssue("revive", "var-naming: AudienceIds should be AudienceIDs", 5, 9),
			newOrderIssue("nolintlint", "unused directive", 5, 20),
			newOrderIssue("gci", "File is not properly formatted", 1, 1),
		}, seed)

		issues, err := NewCanonicalOrder().Process(issues)
		require.NoError(t, err)

		issues, err = NewUniqByLine(true).Process(issues)
		require.NoError(t, err)

		issues, err = NewMaxPerFileFromLinter(cfg).Process(issues)
		require.NoError(t, err)

		var kept []string
		for _, issue := range issues {
			kept = append(kept, issue.FromLinter)
		}

		assert.Equal(t, []string{"gci", "revive"}, kept, "seed %d", seed)
	}
}

func TestCanonicalOrder_ranksLintersFormattersThenNolintlint(t *testing.T) {
	issues, err := NewCanonicalOrder().Process(shuffled([]*result.Issue{
		newOrderIssue("zzz", "text", 2, 1),
		newOrderIssue("nolintlint", "text", 2, 1),
		newOrderIssue("gofmt", "text", 2, 1),
		newOrderIssue("aaa", "text", 2, 1),
		newOrderIssue("swaggo", "text", 2, 1),
	}, 3))
	require.NoError(t, err)

	var linters []string
	for _, issue := range issues {
		linters = append(linters, issue.FromLinter)
	}

	assert.Equal(t, []string{"aaa", "zzz", "gofmt", "swaggo", "nolintlint"}, linters)
}

func TestCanonicalOrder_ordersByFixesWhenNothingElseDiffers(t *testing.T) {
	newFixIssue := func(newText string) *result.Issue {
		issue := newOrderIssue("revive", "text", 2, 1)
		issue.SuggestedFixes = []analysis.SuggestedFix{{TextEdits: []analysis.TextEdit{{Pos: 1, End: 2, NewText: []byte(newText)}}}}

		return issue
	}

	for _, input := range [][]*result.Issue{
		{newFixIssue("b"), newFixIssue("a")},
		{newFixIssue("a"), newFixIssue("b")},
	} {
		issues, err := NewCanonicalOrder().Process(input)
		require.NoError(t, err)

		assert.Equal(t, "a", string(issues[0].SuggestedFixes[0].TextEdits[0].NewText))
	}
}
