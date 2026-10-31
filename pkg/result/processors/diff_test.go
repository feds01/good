package processors

import (
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

const diffTestPatch = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 package a
+var added = 1
 var old = 2
`

func TestDiff_isChanged(t *testing.T) {
	t.Setenv(envGolangciDiffProcessorPatch, diffTestPatch)

	diff := NewDiff(&config.Issues{})

	assert.True(t, diff.IsActive())

	issueAt := func(line int) *result.Issue {
		return &result.Issue{
			FromLinter:                   "errcheck",
			Pos:                          token.Position{Filename: "a.go", Line: line},
			WorkingDirectoryRelativePath: "a.go",
		}
	}

	assert.True(t, diff.IsChanged(issueAt(2)))
	assert.False(t, diff.IsChanged(issueAt(1)))
	assert.False(t, diff.IsChanged(issueAt(3)))

	// the changes are read once: the filter sees the same ones
	kept, err := diff.Process([]*result.Issue{issueAt(1), issueAt(2), issueAt(3)})
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, 2, kept[0].Line())
}

func TestDiff_inactive(t *testing.T) {
	diff := NewDiff(&config.Issues{})

	assert.False(t, diff.IsActive())
	assert.False(t, diff.IsChanged(&result.Issue{}))
}
