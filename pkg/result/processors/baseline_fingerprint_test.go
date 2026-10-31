package processors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBaselineContextHash(t *testing.T) {
	hash := baselineContextHash("if err != nil {", "return err", "}")

	assert.Equal(t, hash, baselineContextHash("\t if   err != nil {\r", "\t\treturn err\r", "}"))
	assert.Equal(t, baselineContextHash("a", "x := 1", "b"), baselineContextHash("a", "\xef\xbb\xbfx := 1", "b"))
	assert.NotEqual(t, hash, baselineContextHash("if err != nil {", "return err", "// done"))
	assert.NotEqual(t, hash, baselineContextHash("if ok {", "return err", "}"))
	assert.NotEqual(t, hash, baselineContextHash("if err != nil {", "return nil", "}"))
	assert.NotEqual(t, baselineContextHash("", "return err", "}"), baselineContextHash("}", "return err", ""))
	assert.Len(t, hash, baselineHashLength)
	assert.Empty(t, baselineContextHash("a", "   \t", "b"))
}

func TestBaselineNormalizeMessage(t *testing.T) {
	testCases := []struct {
		desc     string
		message  string
		basePath string
		expected string
	}{
		{
			desc:     "unchanged",
			message:  "Error return value of `rows.Close` is not checked",
			expected: "Error return value of `rows.Close` is not checked",
		},
		{
			desc:     "dupl",
			message:  "14-23 lines are duplicate of `/repo/pkg/a.go:25-34`",
			basePath: "/repo",
			expected: "N lines are duplicate of `pkg/a.go:N`",
		},
		{
			desc:     "file position",
			message:  "see x/b.go:120:7 for the other occurrence",
			expected: "see x/b.go:N for the other occurrence",
		},
		{
			desc:     "go.mod line",
			message:  "go.mod:12: replacement not allowed",
			expected: "go.mod:N: replacement not allowed",
		},
		{
			desc:     "line numbers in words",
			message:  "same as lines 10-20 and line 5",
			expected: "same as line N and line N",
		},
		{
			desc:     "numbers that carry meaning stay",
			message:  "cyclomatic complexity 35 of func `f` is high (> 30)",
			expected: "cyclomatic complexity 35 of func `f` is high (> 30)",
		},
		{
			desc:     "host and port stay",
			message:  "dial example.com:8080",
			expected: "dial example.com:8080",
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			assert.Equal(t, test.expected, baselineNormalizeMessage(test.message, test.basePath))
		})
	}
}
