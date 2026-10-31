package goanalysis

import (
	"bytes"
	"encoding/gob"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/analysis"

	"github.com/golangci/golangci-lint/v2/pkg/result"
)

func TestClearEmptyReplacements_matchesCacheRoundTrip(t *testing.T) {
	issue := &result.Issue{
		SuggestedFixes: []analysis.SuggestedFix{
			{
				TextEdits: []analysis.TextEdit{
					{Pos: 1, End: 4, NewText: []byte{}},
					{Pos: 5, End: 5, NewText: []byte("x")},
					{Pos: 6, End: 9},
				},
			},
		},
	}

	clearEmptyReplacements([]*result.Issue{issue})

	var buf bytes.Buffer

	require.NoError(t, gob.NewEncoder(&buf).Encode([]EncodingIssue{{SuggestedFixes: issue.SuggestedFixes}}))

	var decoded []EncodingIssue

	require.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))

	assert.Equal(t, issue.SuggestedFixes, decoded[0].SuggestedFixes)
}
