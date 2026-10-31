package commands

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result/processors"
)

const (
	baselineFilePerm = 0o644
	baselineDirPerm  = 0o750
)

type baselineOptions struct {
	add        bool
	prune      bool
	dryRun     bool
	withSource bool
}

func newBaselineCommand(logger logutils.Log, info BuildInfo) *runCommand {
	c := newLintCommand(logger, info, "baseline", "Update the baseline file of issues to ignore.")

	c.baselineOpts = &baselineOptions{}

	c.cmd.Long = `Run the linters and update the baseline file (` + "`issues.baseline`" + `), which lists the issues ` +
		`` + "`run`" + ` does not report.

Without a mode, the issues that are not in the baseline yet are added and nothing is removed:
use it after upgrading, enabling a linter or changing settings.
` + "`--prune`" + ` removes the entries of issues that no longer exist and never adds.
Both flags together synchronize the file with the code.`

	fs := c.cmd.Flags()
	fs.BoolVar(&c.baselineOpts.add, "add", false,
		color.GreenString("Add the issues that are not in the baseline (default when no mode is given)"))
	fs.BoolVar(&c.baselineOpts.prune, "prune", false,
		color.GreenString("Remove the entries of issues that no longer exist"))
	fs.BoolVar(&c.baselineOpts.dryRun, "dry-run", false,
		color.GreenString("Print what would change and write nothing"))
	fs.BoolVar(&c.baselineOpts.withSource, "with-source", false,
		color.GreenString("Write the flagged line next to each entry, to make the file easier to review"))

	c.cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		err := c.prepareBaseline()
		if err != nil {
			return err
		}

		return c.preRunE(cmd, args)
	}

	return c
}

// prepareBaseline makes the run report every issue that is left after the exclusions.
func (c *runCommand) prepareBaseline() error {
	if c.cfg.Issues.Baseline == "" {
		return errors.New("no baseline file configured: add `issues: baseline: .golangci.baseline.toml` " +
			"to the configuration file, or use --baseline PATH")
	}

	issues := &c.cfg.Issues

	issues.BaselineRecord = true
	issues.NeedFix = false
	issues.Diff = false
	issues.DiffFromRevision = ""
	issues.DiffFromMergeBase = ""
	issues.DiffPatchFilePath = ""
	issues.MaxIssuesPerLinter = 0
	issues.MaxSameIssues = 0
	issues.UniqByLine = false

	return nil
}

func (c *runCommand) writeBaseline() error {
	if c.baseline.HasTypeCheckIssues() {
		return errors.New("the code does not compile: fix the type-check errors before updating the baseline")
	}

	opts := c.baselineOpts

	data, summary := c.baseline.Plan(processors.BaselineMode{
		Add:        opts.add || !opts.prune,
		Prune:      opts.prune,
		WithSource: opts.withSource,
	})

	filePath := c.baseline.FilePath()

	existing, err := os.ReadFile(filePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("can't read the baseline: %w", err)
	}

	state := "unchanged"

	switch {
	case opts.dryRun:
		state = "dry run, nothing written"

	case !bytes.Equal(existing, data):
		err = os.MkdirAll(filepath.Dir(filePath), baselineDirPerm)
		if err != nil {
			return fmt.Errorf("can't write the baseline: %w", err)
		}

		err = writeFileAtomic(filePath, data)
		if err != nil {
			return fmt.Errorf("can't write the baseline: %w", err)
		}

		state = "written to " + filePath
	}

	c.cmd.Printf("baseline: %d added, %d pruned, %d kept, %d re-keyed (%s)\n",
		summary.Added, summary.Pruned, summary.Kept, summary.Rekeyed, state)

	c.exitCode = exitcodes.Success

	return nil
}

// writeFileAtomic writes next to the file and renames, so that a reader never sees a truncated baseline.
func writeFileAtomic(filePath string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(filePath), ".baseline-*.tmp")
	if err != nil {
		return err
	}

	tempPath := temp.Name()

	_, err = temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}

	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Chmod(tempPath, baselineFilePerm)
	}

	if err == nil {
		err = os.Rename(tempPath, filePath)
	}

	if err != nil {
		_ = os.Remove(tempPath)
	}

	return err
}

// reportBaseline prints what the baseline did, on stderr to leave the output formats alone.
func (c *runCommand) reportBaseline() {
	if c.baseline == nil {
		return
	}

	stats := c.baseline.Stats()
	binary := filepath.Base(os.Args[0])

	c.cmd.PrintErrf("baseline: %d issues hidden", stats.Hidden)

	if stats.Stale > 0 {
		c.cmd.PrintErrf(", %d stale entries (run `%s baseline --prune`)", stats.Stale, binary)
	}

	if stats.Drifted+stats.Renamed > 0 {
		c.cmd.PrintErrf(", %d reworded or renamed (run `%s baseline` to update)", stats.Drifted+stats.Renamed, binary)
	}

	c.cmd.PrintErrln()

	if stats.Stale > 0 && c.cfg.Issues.FailOnStaleBaseline && c.exitCode == exitcodes.Success {
		c.exitCode = c.cfg.Run.ExitCodeIfIssuesFound
	}
}
