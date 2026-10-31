package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/goutil"
	"github.com/golangci/golangci-lint/v2/pkg/lint/linter"
	"github.com/golangci/golangci-lint/v2/pkg/lint/lintersdb"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result/processors"
)

func TestNewRunner_ordersIssuesBeforeTheProcessorsThatKeepTheFirst(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = config.GroupNone
	cfg.Linters.Enable = []string{"gofmt", "revive"}

	log := logutils.NewStderrLog(logutils.DebugKeyEmpty)

	dbManager, err := lintersdb.NewManager(log, cfg, lintersdb.NewLinterBuilder())
	require.NoError(t, err)

	lineCache := fsutils.NewLineCache(fsutils.NewFileCache())

	runner, err := NewRunner(log, cfg, goutil.NewEnv(log), lineCache, fsutils.NewFileCache(), dbManager, &linter.Context{})
	require.NoError(t, err)

	position := func(name string) int {
		for index, processor := range runner.Processors {
			if processor.Name() == name {
				return index
			}
		}

		require.FailNow(t, "processor not found", name)

		return -1
	}

	canonicalOrder := position(processors.NewCanonicalOrder().Name())

	for _, name := range []string{
		processors.NewNolintFilter(log, nil, dbManager, nil).Name(),
		processors.NewFixer(cfg, log, nil, nil).Name(),
		processors.NewUniqByLine(true).Name(),
		processors.NewMaxPerFileFromLinter(cfg).Name(),
		processors.NewMaxSameIssues(0, log, cfg).Name(),
		processors.NewMaxFromLinter(0, log, cfg).Name(),
		processors.NewSortResults(&cfg.Output).Name(),
	} {
		assert.Less(t, canonicalOrder, position(name), name)
	}
}
