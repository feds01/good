package lint

import (
	"path/filepath"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result/processors"
)

// newBaseline returns nil when the configuration has no baseline.
func newBaseline(
	log logutils.Log, cfg *config.Config, lineCache *fsutils.LineCache, lintCtx *linter.Context,
) (*processors.Baseline, error) {
	if cfg.Issues.Baseline == "" {
		return nil, nil
	}

	basePath := cfg.GetBasePath()

	filePath := cfg.Issues.Baseline
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(basePath, filePath)
	}

	return processors.NewBaseline(log.Child(logutils.DebugKeyBaseline), lineCache, processors.BaselineOptions{
		FilePath: filePath,
		BasePath: basePath,
		Record:   cfg.Issues.BaselineRecord,
		Analyzed: analyzedFiles(basePath, lintCtx),
	})
}

// analyzedFiles lists the files whose issues the run reported: the Go files of the loaded packages and their go.mod.
func analyzedFiles(basePath string, lintCtx *linter.Context) []string {
	files := []string{filepath.Join(basePath, "go.mod")}

	for _, pkg := range lintCtx.OriginalPackages {
		files = append(files, pkg.GoFiles...)

		if pkg.Module != nil && pkg.Module.GoMod != "" {
			files = append(files, pkg.Module.GoMod)
		}
	}

	return files
}
