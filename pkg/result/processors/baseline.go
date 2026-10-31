package processors

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

var _ Processor = (*Baseline)(nil)

type baselineOutcome int

// baselineMaxNeighborDistance bounds the search for a non-blank line next to the flagged one.
const baselineMaxNeighborDistance = 50

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

	// BasePath is the directory the paths of the baseline file are relative to: the one of the configuration file.
	BasePath string

	// Linters lists the linters that ran. Entries of the other linters are never stale and never pruned.
	// Nil means that all of them ran.
	Linters map[string]bool

	// IsChanged tells if an issue is on a changed line, when the issues are also filtered by what changed.
	// Issues on unchanged lines use the entries first, so that a new copy of a baselined line is the one reported.
	IsChanged func(issue *result.Issue) bool

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
	isNew    bool
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

	resolvedBasePath string
	relativePaths    map[string]string

	typeCheckSeen bool
	matched       bool
	observed      []*baselineObserved
	moved         baselineContent
	stats         BaselineStats

	sortedByPath map[string][]*baselineEntry
}

func NewBaseline(log logutils.Log, lineCache *fsutils.LineCache, opts *BaselineOptions) (*Baseline, error) {
	content, err := readBaselineFile(opts.FilePath)
	if err != nil {
		if !opts.Record || !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}

		content = baselineContent{}
	}

	resolvedBasePath, err := filepath.EvalSymlinks(opts.BasePath)
	if err != nil {
		resolvedBasePath = opts.BasePath
	}

	b := &Baseline{
		log:              log,
		lineCache:        lineCache,
		opts:             *opts,
		content:          content,
		analyzed:         map[string]bool{},
		exists:           map[string]bool{},
		resolvedBasePath: resolvedBasePath,
		relativePaths:    map[string]string{},
		moved:            baselineContent{},
		sortedByPath:     map[string][]*baselineEntry{},
	}

	for _, file := range opts.Analyzed {
		b.analyzed[b.relativePath(file)] = true
	}

	return b, nil
}

// relativePath returns the path of a file relative to the base path, with forward slashes.
// A file that is reached through a symbolic link is looked up by its real path.
// A file outside the base path keeps a `..` path, which matches nothing.
func (b *Baseline) relativePath(file string) string {
	cached, ok := b.relativePaths[file]
	if ok {
		return cached
	}

	relative, err := filepath.Rel(b.opts.BasePath, file)
	if err != nil || escapesBase(relative) {
		resolved, resolveErr := filepath.EvalSymlinks(file)
		if resolveErr == nil {
			resolvedRelative, relErr := filepath.Rel(b.resolvedBasePath, resolved)
			if relErr == nil && !escapesBase(resolvedRelative) {
				relative, err = resolvedRelative, nil
			}
		}
	}

	if err != nil {
		relative = file
	}

	relative = filepath.ToSlash(relative)
	b.relativePaths[file] = relative

	return relative
}

func escapesBase(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
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

		line, hash := b.sourceContext(issue)

		observed := &baselineObserved{
			issue:    issue,
			position: position,
			path:     b.relativePath(issue.FilePath()),
			key: baselineKey{
				Linter:  issue.FromLinter,
				Hash:    hash,
				Message: b.normalizeMessage(issue.Text),
			},
			source: baselineNormalizeLine(line),
		}

		if b.opts.IsChanged != nil {
			observed.isNew = b.opts.IsChanged(issue)
		}

		b.observed = append(b.observed, observed)
	}

	slices.SortFunc(b.observed, func(a, other *baselineObserved) int {
		return cmp.Or(
			compareBools(other.isNew, a.isNew),
			cmp.Compare(a.path, other.path),
			cmp.Compare(a.issue.Line(), other.issue.Line()),
			cmp.Compare(a.issue.Column(), other.issue.Column()),
			cmp.Compare(a.key.Linter, other.key.Linter),
			cmp.Compare(a.issue.Text, other.issue.Text),
			cmp.Compare(a.position, other.position),
		)
	})
}

func (b *Baseline) normalizeMessage(message string) string {
	message = baselineNormalizeMessage(message, b.opts.BasePath)

	if b.resolvedBasePath != b.opts.BasePath {
		message = baselineNormalizeMessage(message, b.resolvedBasePath)
	}

	return message
}

// sourceContext returns the flagged line and the fingerprint of the line with its nearest non-blank neighbors.
func (b *Baseline) sourceContext(issue *result.Issue) (line, hash string) {
	if issue.Line() == 0 {
		return "", ""
	}

	line, err := b.lineCache.GetLine(issue.FilePath(), issue.Line())
	if err != nil {
		b.log.Infof("Baseline: no source line for %s: %v", issue.FilePath(), err)

		return "", ""
	}

	above := b.neighbor(issue.FilePath(), issue.Line(), -1)
	below := b.neighbor(issue.FilePath(), issue.Line(), 1)

	return line, baselineContextHash(above, line, below)
}

// neighbor returns the nearest line in a direction that is not blank, or "" at the edge of the file.
func (b *Baseline) neighbor(file string, lineNumber, step int) string {
	for number := lineNumber + step; number >= 1 && abs(number-lineNumber) <= baselineMaxNeighborDistance; number += step {
		text, err := b.lineCache.GetLine(file, number)
		if err != nil {
			return ""
		}

		if baselineNormalizeLine(text) != "" {
			return text
		}
	}

	return ""
}

func abs(value int) int {
	if value < 0 {
		return -value
	}

	return value
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

// isInScope tells if the run saw what the entry describes: a file that was not analyzed (partial run, build tags,
// tests excluded) and a linter that did not run never make an entry stale or pruned, a file that is gone does.
func (b *Baseline) isInScope(path string, entry *baselineEntry) bool {
	if b.opts.Linters != nil && !b.opts.Linters[entry.Linter] {
		return false
	}

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
		for _, entry := range b.entriesOf(path) {
			if entry.remaining() == 0 || !b.isInScope(path, entry) {
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
		for _, entry := range entries {
			keep := entry.Count - entry.moved

			if mode.Prune && b.isInScope(path, entry) {
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
