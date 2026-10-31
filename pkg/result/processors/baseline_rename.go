package processors

import (
	"cmp"
	"maps"
	pathpkg "path"
	"slices"
)

// renameMinShare is the part of a deleted file's entries that must reappear in one new file, as a fraction of 5.
const renameMinShareNumerator, renameMinShareDenominator = 4, 5

// renameMinEntries is the size under which a deleted file is never taken over: with so few entries a new file that
// holds one similar line would be taken for a rename.
const renameMinEntries = 3

type baselineRenameCandidate struct {
	oldPath    string
	newPath    string
	overlap    int
	isSameBase bool
}

type baselineRenamePosting struct {
	path  string
	count int
}

// matchRenamed lets a new file take over the entries of a file that is gone, when the new file reproduces
// at least 80% of that file's entries. The decision is made per file pair, never per entry: single lines such as
// `return err` repeat across files, and an entry-wise match would let a new file absorb real new issues.
func (b *Baseline) matchRenamed() {
	newFiles := b.unmatchedNewFiles()
	if len(newFiles) == 0 {
		return
	}

	orphans := b.orphanFiles()
	if len(orphans) == 0 {
		return
	}

	candidates := b.renameCandidates(orphans, newFiles)

	slices.SortFunc(candidates, func(a, other baselineRenameCandidate) int {
		return cmp.Or(
			-cmp.Compare(a.overlap, other.overlap),
			compareBools(other.isSameBase, a.isSameBase),
			cmp.Compare(a.oldPath, other.oldPath),
			cmp.Compare(a.newPath, other.newPath),
		)
	})

	usedOld := map[string]bool{}
	usedNew := map[string]bool{}

	for _, candidate := range candidates {
		if usedOld[candidate.oldPath] || usedNew[candidate.newPath] {
			continue
		}

		usedOld[candidate.oldPath] = true
		usedNew[candidate.newPath] = true

		b.adoptRenamed(candidate.oldPath, newFiles[candidate.newPath])
	}
}

func compareBools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}

// unmatchedNewFiles groups the issues that no entry covers by file, for the files the baseline does not know.
func (b *Baseline) unmatchedNewFiles() map[string][]*baselineObserved {
	files := map[string][]*baselineObserved{}

	for _, observed := range b.observed {
		if observed.outcome != baselineReported || len(b.content[observed.path]) != 0 {
			continue
		}

		files[observed.path] = append(files[observed.path], observed)
	}

	return files
}

// orphanFiles lists the files of the baseline that are gone and still have entries nothing used.
func (b *Baseline) orphanFiles() []string {
	var orphans []string

	for _, path := range slices.Sorted(maps.Keys(b.content)) {
		if !b.fileExists(path) {
			orphans = append(orphans, path)
		}
	}

	return orphans
}

func (b *Baseline) renameCandidates(orphans []string, newFiles map[string][]*baselineObserved) []baselineRenameCandidate {
	postings := map[string][]baselineRenamePosting{}

	for path, observedIssues := range newFiles {
		for key, count := range countRenameKeys(observedIssues) {
			postings[key] = append(postings[key], baselineRenamePosting{path: path, count: count})
		}
	}

	var candidates []baselineRenameCandidate

	for _, oldPath := range orphans {
		total := 0
		oldKeys := map[string]int{}

		for _, entry := range b.content[oldPath] {
			if b.opts.Linters != nil && !b.opts.Linters[entry.Linter] {
				continue
			}

			total += entry.Count
			oldKeys[renameKey(entry.baselineKey)] += entry.Count
		}

		if total < renameMinEntries {
			continue
		}

		overlaps := map[string]int{}

		for key, oldCount := range oldKeys {
			for _, posting := range postings[key] {
				overlaps[posting.path] += min(oldCount, posting.count)
			}
		}

		needed := (renameMinShareNumerator*total + renameMinShareDenominator - 1) / renameMinShareDenominator

		for newPath, overlap := range overlaps {
			if overlap < needed {
				continue
			}

			candidates = append(candidates, baselineRenameCandidate{
				oldPath:    oldPath,
				newPath:    newPath,
				overlap:    overlap,
				isSameBase: pathpkg.Base(oldPath) == pathpkg.Base(newPath),
			})
		}
	}

	return candidates
}

func countRenameKeys(observedIssues []*baselineObserved) map[string]int {
	counts := map[string]int{}

	for _, observed := range observedIssues {
		counts[renameKey(observed.key)]++
	}

	return counts
}

// renameKey ignores the message when there is a line text to compare: it can change with the linter version.
func renameKey(key baselineKey) string {
	if key.Hash == "" {
		return key.Linter + "\x00\x00" + key.Message
	}

	return key.Linter + "\x00" + key.Hash
}

// adoptRenamed matches the issues of a new file with the entries of the file it replaces.
func (b *Baseline) adoptRenamed(oldPath string, observedIssues []*baselineObserved) {
	for _, observed := range observedIssues {
		entry := b.content[oldPath][observed.key]
		if entry != nil && entry.remaining() > 0 {
			b.consumeMoved(entry, observed, observed.path, baselineRenamed)

			continue
		}

		if observed.key.Hash == "" {
			continue
		}

		for _, candidate := range b.entriesOf(oldPath) {
			if candidate.Linter != observed.key.Linter || candidate.Hash != observed.key.Hash || candidate.remaining() == 0 {
				continue
			}

			b.consumeMoved(candidate, observed, observed.path, baselineRenamed)

			break
		}
	}
}
