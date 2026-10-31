package processors

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

var _ Processor = (*Baseline)(nil)

type baselineOutcome int

const (
	baselineReported baselineOutcome = iota
	baselineExact
	baselineDrifted
	baselineRenamed
)

// BaselineOptions configures [Baseline].
type BaselineOptions struct {
	// FilePath is the absolute path of the baseline file.
	FilePath string

	// BasePath is the directory the paths of the baseline file are relative to.
	BasePath string

	// Record keeps every issue: the processor only observes them, for `good baseline`.
	// A baseline file that does not exist is then an empty baseline.
	Record bool

	// Analyzed lists the absolute paths of the files that were analyzed in this run.
	Analyzed []string

	// FileExists replaces the file system check, for tests.
	FileExists func(path string) bool
}

// BaselineStats summarizes what the baseline did in a run.
type BaselineStats struct {
	Hidden  int
	Stale   int
	Drifted int
	Renamed int
}

// BaselineMode selects how [Baseline.Plan] updates the baseline file.
type BaselineMode struct {
	// Add records the issues the baseline does not cover yet.
	Add bool

	// Prune drops the entries of fixed issues.
	Prune bool

	// WithSource writes the flagged line next to the entries.
	WithSource bool
}

// BaselineSummary counts the changes of [Baseline.Plan].
type BaselineSummary struct {
	Added   int
	Pruned  int
	Kept    int
	Rekeyed int
}

type baselineObserved struct {
	issue    *result.Issue
	position int
	path     string
	key      baselineKey
	source   string
	outcome  baselineOutcome
}

// Baseline hides the issues recorded in a baseline file.
//
// An issue is recorded by its file, linter, message and the text of the flagged line, not by its line number,
// so edits elsewhere in the file keep it hidden. The baseline is a multiset: a file with two identical findings
// has an entry with a count of two.
type Baseline struct {
	log       logutils.Log
	lineCache *fsutils.LineCache
	opts      BaselineOptions

	content  baselineContent
	analyzed map[string]bool
	exists   map[string]bool

	typeCheckSeen bool
	matched       bool
	observed      []*baselineObserved
	moved         baselineContent
	stats         BaselineStats

	sortedByPath map[string][]*baselineEntry
}

func NewBaseline(log logutils.Log, lineCache *fsutils.LineCache, opts BaselineOptions) (*Baseline, error) {
	content, err := readBaselineFile(opts.FilePath)
	if err != nil {
		if !opts.Record || !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}

		content = baselineContent{}
	}

	analyzed := map[string]bool{}
	for _, file := range opts.Analyzed {
		analyzed[baselineRelativePath(opts.BasePath, file)] = true
	}

	return &Baseline{
		log:          log,
		lineCache:    lineCache,
		opts:         opts,
		content:      content,
		analyzed:     analyzed,
		exists:       map[string]bool{},
		moved:        baselineContent{},
		sortedByPath: map[string][]*baselineEntry{},
	}, nil
}

func (*Baseline) Name() string {
	return "baseline"
}

func (b *Baseline) Process(issues []*result.Issue) ([]*result.Issue, error) {
	b.match(issues)

	if b.opts.Record {
		return issues, nil
	}

	hidden := make(map[*result.Issue]bool, b.stats.Hidden)

	for _, observed := range b.observed {
		if observed.outcome != baselineReported {
			hidden[observed.issue] = true
		}
	}

	return filterIssuesUnsafe(issues, func(issue *result.Issue) bool {
		return !hidden[issue]
	}), nil
}

func (*Baseline) Finish() {}

// FilePath returns the absolute path of the baseline file.
func (b *Baseline) FilePath() string {
	return b.opts.FilePath
}

// Stats returns the counters of the run. It is complete once the issues have been processed.
func (b *Baseline) Stats() BaselineStats {
	b.match(nil)

	return b.stats
}

// HasTypeCheckIssues reports whether the code does not compile, in which case the other linters did not run.
func (b *Baseline) HasTypeCheckIssues() bool {
	return b.typeCheckSeen
}

// match runs once, on the first call: the runner skips the processors when there are no issues.
func (b *Baseline) match(issues []*result.Issue) {
	if b.matched {
		return
	}

	b.matched = true

	b.observe(issues)

	b.matchExact()
	b.matchDrifted()
	b.matchRenamed()

	b.computeStats()
}

func (b *Baseline) observe(issues []*result.Issue) {
	for position, issue := range issues {
		if issue.FromLinter == typeCheckName {
			b.typeCheckSeen = true

			continue
		}

		path := filepath.ToSlash(issue.RelativePath)
		source := b.sourceLine(issue)

		b.observed = append(b.observed, &baselineObserved{
			issue:    issue,
			position: position,
			path:     path,
			key: baselineKey{
				Linter:  issue.FromLinter,
				Hash:    baselineLineHash(source),
				Message: baselineNormalizeMessage(issue.Text, b.opts.BasePath),
			},
			source: baselineNormalizeLine(source),
		})
	}

	slices.SortFunc(b.observed, func(a, other *baselineObserved) int {
		return cmp.Or(
			cmp.Compare(a.path, other.path),
			cmp.Compare(a.issue.Line(), other.issue.Line()),
			cmp.Compare(a.issue.Column(), other.issue.Column()),
			cmp.Compare(a.key.Linter, other.key.Linter),
			cmp.Compare(a.issue.Text, other.issue.Text),
			cmp.Compare(a.position, other.position),
		)
	})
}

func (b *Baseline) sourceLine(issue *result.Issue) string {
	if issue.Line() == 0 {
		return ""
	}

	line, err := b.lineCache.GetLine(issue.FilePath(), issue.Line())
	if err != nil {
		b.log.Infof("Baseline: no source line for %s: %v", issue.FilePath(), err)

		return ""
	}

	return line
}

// matchExact hides the issues that have an entry with the same file, linter, message and line text.
// The issues on the lowest lines use the entries first.
func (b *Baseline) matchExact() {
	for _, observed := range b.observed {
		entry := b.content[observed.path][observed.key]
		if entry == nil || entry.remaining() == 0 {
			continue
		}

		entry.exact++
		observed.outcome = baselineExact

		if entry.liveSource == "" {
			entry.liveSource = observed.source
		}
	}
}

// matchDrifted hides the issues whose message changed, for instance after a linter upgrade, using the entries of the
// same file and linter and the same line text that the exact pass left over.
func (b *Baseline) matchDrifted() {
	for _, observed := range b.observed {
		if observed.outcome != baselineReported || observed.key.Hash == "" {
			continue
		}

		for _, entry := range b.entriesOf(observed.path) {
			if entry.Linter != observed.key.Linter || entry.Hash != observed.key.Hash || entry.remaining() == 0 {
				continue
			}

			b.consumeMoved(entry, observed, observed.path, baselineDrifted)

			break
		}
	}
}

// consumeMoved lets an issue use an entry of the baseline, and records it under the issue's own key and file,
// where it is written to when the baseline is updated.
func (b *Baseline) consumeMoved(entry *baselineEntry, observed *baselineObserved, toPath string, outcome baselineOutcome) {
	entry.moved++
	observed.outcome = outcome

	b.moved.add(toPath, &baselineEntry{baselineKey: observed.key, Count: 1, Source: observed.source})
}

func (b *Baseline) entriesOf(path string) []*baselineEntry {
	entries, ok := b.sortedByPath[path]
	if !ok {
		entries = b.content.sortedEntries(path)
		b.sortedByPath[path] = entries
	}

	return entries
}

func (b *Baseline) fileExists(path string) bool {
	exists, ok := b.exists[path]
	if ok {
		return exists
	}

	if b.opts.FileExists != nil {
		exists = b.opts.FileExists(path)
	} else {
		_, err := os.Stat(filepath.Join(b.opts.BasePath, filepath.FromSlash(path)))
		exists = err == nil
	}

	b.exists[path] = exists

	return exists
}

// isInScope tells if the run saw the whole file: a file that was not analyzed (partial run, build tags, tests
// excluded) is never reported as stale and never pruned, a file that is gone is.
func (b *Baseline) isInScope(path string) bool {
	return b.analyzed[path] || !b.fileExists(path)
}

func (b *Baseline) computeStats() {
	for _, observed := range b.observed {
		switch observed.outcome {
		case baselineReported:
		case baselineExact:
			b.stats.Hidden++
		case baselineDrifted:
			b.stats.Hidden++
			b.stats.Drifted++
		case baselineRenamed:
			b.stats.Hidden++
			b.stats.Renamed++
		}
	}

	if b.typeCheckSeen {
		// The other linters did not run: nothing can be said about the entries.
		return
	}

	for path := range b.content {
		if !b.isInScope(path) {
			continue
		}

		for _, entry := range b.entriesOf(path) {
			if entry.remaining() == 0 {
				continue
			}

			b.stats.Stale += entry.remaining()

			b.log.Infof("Baseline: stale entry in %s: %s: %s", path, entry.Linter, entry.Message)
		}
	}
}

// Plan returns the new content of the baseline file for `good baseline`.
// Entries that were reworded or moved to a renamed file are always rewritten, as a move, never as a removal.
func (b *Baseline) Plan(mode BaselineMode) ([]byte, BaselineSummary) {
	b.match(nil)

	var summary BaselineSummary

	out := baselineContent{}

	for path, entries := range b.content {
		inScope := b.isInScope(path)

		for _, entry := range entries {
			keep := entry.Count - entry.moved

			if inScope && mode.Prune {
				summary.Pruned += entry.remaining()

				keep = entry.exact
			}

			source := entry.Source
			if mode.WithSource && entry.liveSource != "" {
				source = entry.liveSource
			}

			out.add(path, &baselineEntry{baselineKey: entry.baselineKey, Count: keep, Source: source})

			summary.Kept += keep
		}
	}

	for path, entries := range b.moved {
		for _, entry := range entries {
			entry.Source = sourceIf(mode.WithSource, entry.Source)
			out.add(path, entry)

			summary.Rekeyed += entry.Count
		}
	}

	if mode.Add {
		for _, observed := range b.observed {
			if observed.outcome != baselineReported {
				continue
			}

			out.add(observed.path, &baselineEntry{
				baselineKey: observed.key,
				Count:       1,
				Source:      sourceIf(mode.WithSource, observed.source),
			})

			summary.Added++
		}
	}

	return encodeBaseline(out), summary
}

func sourceIf(isEnabled bool, source string) string {
	if isEnabled {
		return source
	}

	return ""
}

func baselineRelativePath(basePath, file string) string {
	relative, err := filepath.Rel(basePath, file)
	if err != nil {
		return filepath.ToSlash(file)
	}

	return filepath.ToSlash(relative)
}
