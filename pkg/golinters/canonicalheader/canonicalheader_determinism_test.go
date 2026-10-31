package canonicalheader

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer_reportsDeterministically(t *testing.T) {
	testdata, err := filepath.Abs(filepath.Join("testdata", "analysistest"))
	if err != nil {
		t.Fatal(err)
	}

	// The unfiltered analyzer misses the reports in most runs.
	for range 5 {
		analysistest.Run(t, testdata, newAnalyzer(), "header")
	}
}
