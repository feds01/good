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
// The paths of the baseline are relative to the directory of the configuration file, so that they do not depend on
// the directory the linter is run from.
func newBaseline(
	log logutils.Log, cfg *config.Config, lineCache *fsutils.LineCache, lintCtx *linter.Context,
	enabledLinters map[string]*linter.Config, diff *processors.Diff,
) (*processors.Baseline, error) {
	if cfg.Issues.Baseline == "" {
		return nil, nil
	}

	rootPath := cfg.GetConfigDir()
	if rootPath == "" {
		rootPath = cfg.GetBasePath()
	}

	filePath := cfg.Issues.Baseline
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(rootPath, filePath)
	}

	ranLinters := make(map[string]bool, len(enabledLinters))
	for name := range enabledLinters {
		ranLinters[name] = true
	}

	opts := &processors.BaselineOptions{
		FilePath: filePath,
		BasePath: rootPath,
		Record:   cfg.Issues.BaselineRecord,
		Linters:  ranLinters,
		Analyzed: analyzedFiles(rootPath, lintCtx),
	}

	if diff.IsActive() && !opts.Record {
		opts.IsChanged = diff.IsChanged
	}

	return processors.NewBaseline(log.Child(logutils.DebugKeyBaseline), lineCache, opts)
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
