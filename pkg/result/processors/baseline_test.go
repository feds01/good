package processors

import (
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/fsutils"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

const baselineFileName = "baseline.toml"

type baselineFixture struct {
	t   *testing.T
	dir string
}

func newBaselineFixture(t *testing.T) *baselineFixture {
	t.Helper()

	return &baselineFixture{t: t, dir: t.TempDir()}
}

func (f *baselineFixture) write(relativePath string, lines ...string) {
	f.t.Helper()

	absolutePath := filepath.Join(f.dir, filepath.FromSlash(relativePath))
	require.NoError(f.t, os.MkdirAll(filepath.Dir(absolutePath), 0o750))
	require.NoError(f.t, os.WriteFile(absolutePath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

func (f *baselineFixture) remove(relativePath string) {
	f.t.Helper()

	require.NoError(f.t, os.Remove(filepath.Join(f.dir, filepath.FromSlash(relativePath))))
}

func (f *baselineFixture) issue(relativePath string, line int, linter, text string) *result.Issue {
	return &result.Issue{
		FromLinter:   linter,
		Text:         text,
		Pos:          token.Position{Filename: filepath.Join(f.dir, filepath.FromSlash(relativePath)), Line: line, Column: 1},
		RelativePath: filepath.FromSlash(relativePath),
	}
}

func (f *baselineFixture) options(isRecord bool, analyzed ...string) *BaselineOptions {
	absolute := make([]string, 0, len(analyzed))
	for _, relativePath := range analyzed {
		absolute = append(absolute, filepath.Join(f.dir, filepath.FromSlash(relativePath)))
	}

	return &BaselineOptions{
		FilePath: filepath.Join(f.dir, baselineFileName),
		BasePath: f.dir,
		Record:   isRecord,
		Analyzed: absolute,
	}
}

func (f *baselineFixture) newProcessor(opts *BaselineOptions) *Baseline {
	f.t.Helper()

	logger := logutils.NewStderrLog(logutils.DebugKeyEmpty)

	processor, err := NewBaseline(logger, fsutils.NewLineCache(fsutils.NewFileCache()), opts)
	require.NoError(f.t, err)

	return processor
}

// record writes the baseline file for the issues, as `good baseline` does.
func (f *baselineFixture) record(issues []*result.Issue, mode BaselineMode) BaselineSummary {
	f.t.Helper()

	opts := f.options(true)
	for _, issue := range issues {
		opts.Analyzed = append(opts.Analyzed, issue.FilePath())
	}

	processor := f.newProcessor(opts)

	_, err := processor.Process(issues)
	require.NoError(f.t, err)

	data, summary := processor.Plan(mode)
	require.NoError(f.t, os.WriteFile(opts.FilePath, data, 0o600))

	return summary
}

// run applies the baseline file to the issues and returns the issues that are still reported.
func (f *baselineFixture) run(issues []*result.Issue, analyzed ...string) ([]*result.Issue, BaselineStats) {
	f.t.Helper()

	opts := f.options(false, analyzed...)

	processor := f.newProcessor(opts)

	reported, err := processor.Process(issues)
	require.NoError(f.t, err)

	return reported, processor.Stats()
}

func (f *baselineFixture) readBaseline() string {
	f.t.Helper()

	data, err := os.ReadFile(filepath.Join(f.dir, baselineFileName))
	require.NoError(f.t, err)

	return string(data)
}

func lines(texts ...string) []string {
	return texts
}

func reportedLines(issues []*result.Issue) []int {
	reported := make([]int, 0, len(issues))
	for _, issue := range issues {
		reported = append(reported, issue.Line())
	}

	slices.Sort(reported)

	return reported
}

var addMode = BaselineMode{Add: true}

func TestBaseline_survivesEditsElsewhere(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "", "func f() {", "\tdefer rows.Close()", "}")...)
	issues := []*result.Issue{f.issue("a.go", 4, "errcheck", "Error return value of `rows.Close` is not checked")}

	f.record(issues, addMode)

	reported, stats := f.run(issues, "a.go")
	assert.Empty(t, reported)
	assert.Equal(t, BaselineStats{Hidden: 1}, stats)

	f.write("a.go", lines("package a", "", "// new comment", "// another", "func f() {", "\tdefer rows.Close()", "}")...)
	moved := []*result.Issue{f.issue("a.go", 6, "errcheck", "Error return value of `rows.Close` is not checked")}

	reported, stats = f.run(moved, "a.go")
	assert.Empty(t, reported)
	assert.Equal(t, BaselineStats{Hidden: 1}, stats)
}

func TestBaseline_editedFlaggedLineIsReportedAgain(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tdefer rows.Close()", "}")...)
	f.record([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}, addMode)

	f.write("a.go", lines("package a", "func f() {", "\tdefer other.Close()", "}")...)

	reported, stats := f.run([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}, "a.go")

	assert.Equal(t, []int{3}, reportedLines(reported))
	assert.Equal(t, 1, stats.Stale)
}

func TestBaseline_ignoresFormatting(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tdefer   rows.Close()", "}")...)
	f.record([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}, addMode)

	testCases := []struct {
		desc    string
		content string
	}{
		{desc: "CRLF", content: "package a\r\nfunc f() {\r\n\tdefer   rows.Close()\r\n}\r\n"},
		{desc: "spaces instead of tabs", content: "package a\nfunc f() {\n    defer rows.Close()\n}\n"},
		{desc: "byte order mark", content: "\xef\xbb\xbfpackage a\nfunc f() {\n\tdefer rows.Close()\n}\n"},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "a.go"), []byte(test.content), 0o600))

			reported, stats := f.run([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}, "a.go")

			assert.Empty(t, reported)
			assert.Equal(t, 0, stats.Stale)
		})
	}
}

func TestBaseline_countsIdenticalLines(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tdefer x.Close()", "\tdefer x.Close()", "\tdefer x.Close()", "}")...)

	issues := func(lineNumbers ...int) []*result.Issue {
		var out []*result.Issue
		for _, line := range lineNumbers {
			out = append(out, f.issue("a.go", line, "errcheck", "unchecked"))
		}

		return out
	}

	f.record(issues(3, 4, 5), addMode)

	reported, stats := f.run(issues(3, 4, 5), "a.go")
	assert.Empty(t, reported)
	assert.Equal(t, 0, stats.Stale)

	f.write("a.go", lines("package a", "func f() {", "\tdefer x.Close()", "\tdefer x.Close()", "\tdefer x.Close()", "\tdefer x.Close()", "}")...)

	reported, _ = f.run(issues(3, 4, 5, 6), "a.go")
	assert.Len(t, reported, 1, "a new identical line is exactly one new issue")

	f.write("a.go", lines("package a", "func f() {", "\tdefer x.Close()", "\tdefer x.Close()", "}")...)

	reported, stats = f.run(issues(3, 4), "a.go")
	assert.Empty(t, reported)
	assert.Equal(t, 1, stats.Stale)
}

func TestBaseline_issuesWithoutSourceLine(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("go.mod", lines("module m", "", "go 1.24")...)

	modIssue := func() *result.Issue {
		return f.issue("go.mod", 0, "gomoddirectives", "replacement are not allowed: github.com/x/y")
	}

	f.record([]*result.Issue{modIssue()}, addMode)

	reported, stats := f.run([]*result.Issue{modIssue()}, "go.mod")
	assert.Empty(t, reported)
	assert.Equal(t, BaselineStats{Hidden: 1}, stats)

	reported, _ = f.run([]*result.Issue{modIssue(), f.issue("go.mod", 0, "gomoddirectives", "another problem")}, "go.mod")
	assert.Len(t, reported, 1)
}

func TestBaseline_typeCheckIsNeverHidden(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "x := 1")...)
	f.record([]*result.Issue{f.issue("a.go", 2, "errcheck", "unchecked")}, addMode)

	typeCheck := f.issue("a.go", 2, "typecheck", "declared and not used")

	processor := f.newProcessor(f.options(true))
	_, err := processor.Process([]*result.Issue{typeCheck})
	require.NoError(t, err)
	assert.True(t, processor.HasTypeCheckIssues())

	data, _ := processor.Plan(addMode)
	assert.NotContains(t, string(data), "typecheck")
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, baselineFileName), data, 0o600))

	reported, stats := f.run([]*result.Issue{typeCheck}, "a.go")
	assert.Equal(t, []*result.Issue{typeCheck}, reported)
	assert.Equal(t, BaselineStats{}, stats)
}

func TestBaseline_rewordedMessages(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "const key = \"k\"", "func f() { use(key) }")...)

	revive := func(text string, line int) *result.Issue {
		return f.issue("a.go", line, "revive", text)
	}

	f.record([]*result.Issue{revive("should not use basic type string as key in context.WithValue", 2)}, addMode)

	reworded := func() []*result.Issue {
		return []*result.Issue{revive("should not use basic type untyped string as key in context.WithValue", 2)}
	}

	t.Run("run reports a reworded issue", func(t *testing.T) {
		reported, stats := f.run(reworded(), "a.go")

		assert.Equal(t, []int{2}, reportedLines(reported))
		assert.Equal(t, BaselineStats{Stale: 1}, stats)
	})

	t.Run("baseline rewrites the entry in place, then run reports nothing", func(t *testing.T) {
		summary := f.record(reworded(), addMode)

		assert.Equal(t, BaselineSummary{Rekeyed: 1}, summary)
		assert.Contains(t, f.readBaseline(), "untyped string")
		assert.NotContains(t, f.readBaseline(), "basic type string as key")

		reported, stats := f.run(reworded(), "a.go")

		assert.Empty(t, reported)
		assert.Equal(t, BaselineStats{Hidden: 1}, stats)
	})
}

func TestBaseline_secondIssueOnTheLineIsReported(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "const key = \"k\"", "func f() { use(key) }")...)
	f.record([]*result.Issue{f.issue("a.go", 2, "revive", "message")}, addMode)

	reported, _ := f.run([]*result.Issue{
		f.issue("a.go", 2, "revive", "message"),
		f.issue("a.go", 2, "revive", "another message"),
	}, "a.go")

	require.Len(t, reported, 1)
	assert.Equal(t, "another message", reported[0].Text)

	other, _ := f.run([]*result.Issue{f.issue("a.go", 2, "gocritic", "message")}, "a.go")
	assert.Len(t, other, 1, "another linter is not hidden")
}

func TestBaseline_positionsInMessagesDoNotMatter(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func First() {", "\tcode()", "}", "func Second() {", "\tcode()", "}")...)

	first := f.issue("a.go", 2, "dupl", "2-4 lines are duplicate of `"+f.dir+"/a.go:5-7`")
	f.record([]*result.Issue{first}, addMode)

	f.write("a.go", lines("// Package a.", "package a", "func First() {", "\tcode()", "}", "func Second() {", "\tcode()", "}")...)

	moved := f.issue("a.go", 3, "dupl", "3-5 lines are duplicate of `"+f.dir+"/a.go:6-8`")

	reported, stats := f.run([]*result.Issue{moved}, "a.go")

	assert.Empty(t, reported)
	assert.Equal(t, 0, stats.Drifted)
}

func TestBaseline_stale(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tbad()", "}")...)
	f.write("b.go", lines("package a", "func g() {", "\tbad()", "}")...)

	f.record([]*result.Issue{
		f.issue("a.go", 3, "errcheck", "unchecked"),
		f.issue("b.go", 3, "errcheck", "unchecked"),
	}, addMode)

	t.Run("analyzed file without the issue is stale", func(t *testing.T) {
		_, stats := f.run([]*result.Issue{f.issue("b.go", 3, "errcheck", "unchecked")}, "a.go", "b.go")

		assert.Equal(t, BaselineStats{Hidden: 1, Stale: 1}, stats)
	})

	t.Run("file that was not analyzed is not stale", func(t *testing.T) {
		_, stats := f.run([]*result.Issue{f.issue("b.go", 3, "errcheck", "unchecked")}, "b.go")

		assert.Equal(t, BaselineStats{Hidden: 1}, stats)
	})

	t.Run("deleted file is stale", func(t *testing.T) {
		f.remove("a.go")

		_, stats := f.run([]*result.Issue{f.issue("b.go", 3, "errcheck", "unchecked")}, "b.go")

		assert.Equal(t, BaselineStats{Hidden: 1, Stale: 1}, stats)
	})
}

func TestBaseline_renamedFile(t *testing.T) {
	oldLines := lines("package a", "func f() {",
		"\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "\tskip()", "\tfour()", "\tskip()", "\tfive()", "}")

	oldIssues := func(f *baselineFixture, path string) []*result.Issue {
		return []*result.Issue{
			f.issue(path, 3, "errcheck", "unchecked"),
			f.issue(path, 5, "errcheck", "unchecked"),
			f.issue(path, 7, "errcheck", "unchecked"),
			f.issue(path, 9, "errcheck", "unchecked"),
			f.issue(path, 11, "errcheck", "unchecked"),
		}
	}

	t.Run("moved with an edited line is absorbed, the edited line is reported", func(t *testing.T) {
		f := newBaselineFixture(t)

		f.write("old/a.go", oldLines...)
		f.record(oldIssues(f, "old/a.go"), addMode)
		f.remove("old/a.go")

		f.write("new/a.go", lines("package a", "func f() {",
			"\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "\tskip()", "\tfour()", "\tskip()", "\tfive(1)", "}")...)

		reported, stats := f.run(oldIssues(f, "new/a.go"), "new/a.go")

		assert.Equal(t, []int{11}, reportedLines(reported))
		assert.Equal(t, 4, stats.Renamed)
		assert.Equal(t, 1, stats.Stale)
	})

	t.Run("a new file that reuses a few lines is not absorbed", func(t *testing.T) {
		f := newBaselineFixture(t)

		f.write("old/a.go", oldLines...)
		f.record(oldIssues(f, "old/a.go"), addMode)
		f.remove("old/a.go")

		f.write("new/b.go", lines("package a", "func f() {",
			"\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "\tskip()", "\tother()", "\tskip()", "\tmore()", "}")...)

		reported, stats := f.run(oldIssues(f, "new/b.go"), "new/b.go")

		assert.Equal(t, []int{3, 5, 7, 9, 11}, reportedLines(reported))
		assert.Equal(t, 0, stats.Renamed)
	})

	t.Run("a deleted file with fewer than 3 entries is never taken over", func(t *testing.T) {
		f := newBaselineFixture(t)

		f.write("old/a.go", lines("package a", "func f() {", "\treturn err", "\tskip()", "\treturn err", "}")...)
		f.record([]*result.Issue{
			f.issue("old/a.go", 3, "errcheck", "unchecked"),
			f.issue("old/a.go", 5, "errcheck", "unchecked"),
		}, addMode)
		f.remove("old/a.go")

		f.write("new/a.go", lines("package a", "func f() {", "\treturn err", "\tskip()", "\treturn err", "}")...)

		reported, stats := f.run([]*result.Issue{
			f.issue("new/a.go", 3, "errcheck", "unchecked"),
			f.issue("new/a.go", 5, "errcheck", "unchecked"),
		}, "new/a.go")

		assert.Len(t, reported, 2)
		assert.Equal(t, 0, stats.Renamed)
	})

	t.Run("a deleted file with 3 entries is taken over by an identical file", func(t *testing.T) {
		f := newBaselineFixture(t)

		content := lines("package a", "func f() {", "\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "}")
		issues := func(path string) []*result.Issue {
			return []*result.Issue{
				f.issue(path, 3, "errcheck", "unchecked"),
				f.issue(path, 5, "errcheck", "unchecked"),
				f.issue(path, 7, "errcheck", "unchecked"),
			}
		}

		f.write("old/a.go", content...)
		f.record(issues("old/a.go"), addMode)
		f.remove("old/a.go")
		f.write("new/a.go", content...)

		reported, stats := f.run(issues("new/a.go"), "new/a.go")

		assert.Empty(t, reported)
		assert.Equal(t, 3, stats.Renamed)
	})

	t.Run("common lines of a larger deleted file are not absorbed", func(t *testing.T) {
		f := newBaselineFixture(t)

		f.write("old/a.go", lines("package a", "func f() {",
			"\treturn err", "\tskip()", "\tdefer rows.Close()", "\tskip()", "\tx()", "\tskip()", "\ty()", "\tskip()", "\tz()", "}")...)
		f.record([]*result.Issue{
			f.issue("old/a.go", 3, "errcheck", "unchecked"),
			f.issue("old/a.go", 5, "errcheck", "unchecked"),
			f.issue("old/a.go", 7, "errcheck", "unchecked"),
			f.issue("old/a.go", 9, "errcheck", "unchecked"),
			f.issue("old/a.go", 11, "errcheck", "unchecked"),
		}, addMode)
		f.remove("old/a.go")

		f.write("new/b.go", lines("package a", "func f() {",
			"\treturn err", "\tskip()", "\tdefer rows.Close()", "\tskip()", "\tbrandNew()", "}")...)

		reported, stats := f.run([]*result.Issue{
			f.issue("new/b.go", 3, "errcheck", "unchecked"),
			f.issue("new/b.go", 5, "errcheck", "unchecked"),
			f.issue("new/b.go", 7, "errcheck", "unchecked"),
		}, "new/b.go")

		assert.Len(t, reported, 3)
		assert.Equal(t, 0, stats.Renamed)
	})

	t.Run("a file that still exists is never a rename source", func(t *testing.T) {
		f := newBaselineFixture(t)

		f.write("old/a.go", oldLines...)
		f.record(oldIssues(f, "old/a.go"), addMode)

		f.write("new/a.go", oldLines...)

		reported, stats := f.run(oldIssues(f, "new/a.go"), "new/a.go")

		assert.Len(t, reported, 5)
		assert.Equal(t, 0, stats.Renamed)
	})
}

func TestBaseline_isDeterministic(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tx()", "\tx()", "\tx()", "}")...)
	f.write("b.go", lines("package b", "func f() {", "\ty()", "}")...)

	baselined := []*result.Issue{
		f.issue("a.go", 3, "errcheck", "unchecked"),
		f.issue("a.go", 4, "errcheck", "unchecked"),
		f.issue("b.go", 3, "errcheck", "unchecked"),
	}
	f.record(baselined, addMode)

	current := append(slices.Clone(baselined),
		f.issue("a.go", 5, "errcheck", "unchecked"),
		f.issue("b.go", 3, "revive", "reworded"))

	expected, _ := f.run(slices.Clone(current), "a.go", "b.go")

	for range 5 {
		shuffled := slices.Clone(current)
		slices.Reverse(shuffled)
		shuffled = append(shuffled[3:], shuffled[:3]...)

		got, _ := f.run(shuffled, "a.go", "b.go")

		assert.ElementsMatch(t, expected, got)
	}
}

func TestBaseline_recordKeepsAllIssues(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tx()", "}")...)
	issues := []*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}

	processor := f.newProcessor(f.options(true))

	got, err := processor.Process(issues)
	require.NoError(t, err)
	assert.Equal(t, issues, got)
}

func TestNewBaseline_missingFile(t *testing.T) {
	f := newBaselineFixture(t)

	logger := logutils.NewStderrLog(logutils.DebugKeyEmpty)
	lineCache := fsutils.NewLineCache(fsutils.NewFileCache())

	_, err := NewBaseline(logger, lineCache, f.options(false))
	require.Error(t, err)

	_, err = NewBaseline(logger, lineCache, f.options(true))
	require.NoError(t, err)
}

func TestBaseline_plan(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tfixed()", "\tfiller()", "\tkept()", "}")...)
	f.write("b.go", lines("package b", "func f() {", "\tuntouched()", "}")...)

	f.record([]*result.Issue{
		f.issue("a.go", 3, "errcheck", "unchecked"),
		f.issue("a.go", 5, "errcheck", "unchecked"),
		f.issue("b.go", 3, "errcheck", "unchecked"),
	}, addMode)

	before := f.readBaseline()

	// fixed() is gone, a new linter reports on kept(), b.go was not analyzed
	f.write("a.go", lines("package a", "func f() {", "\tfiller()", "\tkept()", "}")...)

	current := []*result.Issue{
		f.issue("a.go", 4, "errcheck", "unchecked"),
		f.issue("a.go", 4, "newlinter", "new finding"),
	}

	plan := func(mode BaselineMode) (string, BaselineSummary) {
		processor := f.newProcessor(f.options(true, "a.go"))

		_, err := processor.Process(slices.Clone(current))
		require.NoError(t, err)

		data, summary := processor.Plan(mode)

		return string(data), summary
	}

	t.Run("add keeps stale entries and records the new finding", func(t *testing.T) {
		data, summary := plan(BaselineMode{Add: true})

		assert.Equal(t, BaselineSummary{Added: 1, Kept: 3}, summary)
		assert.Contains(t, data, `linter = "newlinter"`)
		assert.Contains(t, data, `["a.go"]`)
		assert.Contains(t, data, `["b.go"]`)
	})

	t.Run("prune drops stale entries and never adds", func(t *testing.T) {
		data, summary := plan(BaselineMode{Prune: true})

		assert.Equal(t, BaselineSummary{Pruned: 1, Kept: 2}, summary)
		assert.NotContains(t, data, "newlinter")
		assert.Contains(t, data, `["b.go"]`, "b.go was not analyzed, so it is untouched")
	})

	t.Run("add and prune together make a full sync", func(t *testing.T) {
		data, summary := plan(BaselineMode{Add: true, Prune: true})

		assert.Equal(t, BaselineSummary{Added: 1, Pruned: 1, Kept: 2}, summary)
		assert.Contains(t, data, "newlinter")
	})

	t.Run("an unchanged baseline is written byte for byte", func(t *testing.T) {
		f.write("a.go", lines("package a", "func f() {", "\tfixed()", "\tfiller()", "\tkept()", "}")...)

		processor := f.newProcessor(f.options(true, "a.go", "b.go"))

		_, err := processor.Process([]*result.Issue{
			f.issue("a.go", 3, "errcheck", "unchecked"),
			f.issue("a.go", 5, "errcheck", "unchecked"),
			f.issue("b.go", 3, "errcheck", "unchecked"),
		})
		require.NoError(t, err)

		data, _ := processor.Plan(BaselineMode{Add: true, Prune: true})

		assert.Equal(t, before, string(data))
	})
}

func TestBaseline_planRekeysWithoutRemoving(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tx()", "}")...)
	f.record([]*result.Issue{f.issue("a.go", 3, "revive", "old wording")}, addMode)

	processor := f.newProcessor(f.options(true, "a.go"))

	_, err := processor.Process([]*result.Issue{f.issue("a.go", 3, "revive", "new wording")})
	require.NoError(t, err)

	data, summary := processor.Plan(BaselineMode{Add: true})

	assert.Equal(t, BaselineSummary{Rekeyed: 1}, summary)
	assert.Contains(t, string(data), "new wording")
	assert.NotContains(t, string(data), "old wording")
}

func TestBaseline_planMovesRenamedFiles(t *testing.T) {
	f := newBaselineFixture(t)

	content := lines("package a", "func f() {", "\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "}")

	f.write("old/a.go", content...)
	f.record([]*result.Issue{
		f.issue("old/a.go", 3, "errcheck", "unchecked"),
		f.issue("old/a.go", 5, "errcheck", "unchecked"),
		f.issue("old/a.go", 7, "errcheck", "unchecked"),
	}, addMode)
	f.remove("old/a.go")

	f.write("new/a.go", content...)

	processor := f.newProcessor(f.options(true, "new/a.go"))

	_, err := processor.Process([]*result.Issue{
		f.issue("new/a.go", 3, "errcheck", "unchecked"),
		f.issue("new/a.go", 5, "errcheck", "unchecked"),
		f.issue("new/a.go", 7, "errcheck", "unchecked"),
	})
	require.NoError(t, err)

	data, summary := processor.Plan(BaselineMode{Add: true})

	assert.Equal(t, BaselineSummary{Rekeyed: 3}, summary)
	assert.Contains(t, string(data), `["new/a.go"]`)
	assert.NotContains(t, string(data), `["old/a.go"]`)
}

func TestBaseline_planWithSource(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\t  defer   rows.Close()", "}")...)

	processor := f.newProcessor(f.options(true, "a.go"))

	_, err := processor.Process([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")})
	require.NoError(t, err)

	data, _ := processor.Plan(BaselineMode{Add: true, WithSource: true})
	assert.Contains(t, string(data), `source = "defer rows.Close()"`)

	data, _ = processor.Plan(BaselineMode{Add: true})
	assert.NotContains(t, string(data), "source")
}

func TestBaseline_onlyLintersThatRanCanBeStale(t *testing.T) {
	f := newBaselineFixture(t)

	f.write("a.go", lines("package a", "func f() {", "\tone()", "\tskip()", "\ttwo()", "}")...)

	f.record([]*result.Issue{
		f.issue("a.go", 3, "errcheck", "unchecked"),
		f.issue("a.go", 3, "godot", "Comment should end in a period"),
		f.issue("a.go", 5, "godot", "Comment should end in a period"),
	}, addMode)

	opts := f.options(false, "a.go")
	opts.Linters = map[string]bool{"godot": true}

	processor := f.newProcessor(opts)

	reported, err := processor.Process([]*result.Issue{
		f.issue("a.go", 3, "godot", "Comment should end in a period"),
	})
	require.NoError(t, err)
	assert.Empty(t, reported)

	// the entry of errcheck is not stale: it did not run, the second godot entry is: it ran and found nothing
	assert.Equal(t, BaselineStats{Hidden: 1, Stale: 1}, processor.Stats())

	recordOpts := f.options(true, "a.go")
	recordOpts.Linters = map[string]bool{"godot": true}

	recorder := f.newProcessor(recordOpts)

	_, err = recorder.Process([]*result.Issue{f.issue("a.go", 3, "godot", "Comment should end in a period")})
	require.NoError(t, err)

	data, summary := recorder.Plan(BaselineMode{Prune: true})

	assert.Equal(t, BaselineSummary{Pruned: 1, Kept: 2}, summary)
	assert.Contains(t, string(data), `linter = "errcheck"`)
}

func TestBaseline_linterThatDidNotRunDoesNotBlockRenames(t *testing.T) {
	f := newBaselineFixture(t)

	content := lines("package a", "func f() {", "\tone()", "\tskip()", "\ttwo()", "\tskip()", "\tthree()", "}")

	f.write("old/a.go", content...)
	f.record([]*result.Issue{
		f.issue("old/a.go", 3, "errcheck", "unchecked"),
		f.issue("old/a.go", 5, "errcheck", "unchecked"),
		f.issue("old/a.go", 7, "errcheck", "unchecked"),
		f.issue("old/a.go", 7, "godot", "period"),
	}, addMode)
	f.remove("old/a.go")
	f.write("new/a.go", content...)

	opts := f.options(false, "new/a.go")
	opts.Linters = map[string]bool{"errcheck": true}

	processor := f.newProcessor(opts)

	reported, err := processor.Process([]*result.Issue{
		f.issue("new/a.go", 3, "errcheck", "unchecked"),
		f.issue("new/a.go", 5, "errcheck", "unchecked"),
		f.issue("new/a.go", 7, "errcheck", "unchecked"),
	})
	require.NoError(t, err)

	assert.Empty(t, reported)
}

func TestBaseline_unchangedLinesUseTheEntriesFirst(t *testing.T) {
	f := newBaselineFixture(t)

	block := lines("\tx := 1", "\tos.Remove(a)", "\ty := 2")

	f.write("a.go", append(lines("package a", "func f() {"), append(block, "}")...)...)
	f.record([]*result.Issue{f.issue("a.go", 4, "errcheck", "unchecked")}, addMode)

	// a copy of the block is added above the old one
	f.write("a.go", append(lines("package a", "func f() {"), append(append(block, block...), "}")...)...)

	current := func() []*result.Issue {
		return []*result.Issue{
			f.issue("a.go", 4, "errcheck", "unchecked"),
			f.issue("a.go", 7, "errcheck", "unchecked"),
		}
	}

	t.Run("without a diff the lowest line is hidden", func(t *testing.T) {
		reported, _ := f.run(current(), "a.go")

		assert.Equal(t, []int{7}, reportedLines(reported))
	})

	t.Run("with a diff the line that did not change is hidden", func(t *testing.T) {
		opts := f.options(false, "a.go")
		opts.IsChanged = func(issue *result.Issue) bool {
			return issue.Line() == 4
		}

		processor := f.newProcessor(opts)

		reported, err := processor.Process(current())
		require.NoError(t, err)

		assert.Equal(t, []int{4}, reportedLines(reported))
	})
}

func TestBaseline_idiomaticErrorReturnIsNotAbsorbed(t *testing.T) {
	f := newBaselineFixture(t)

	// both functions have the same three-line window around the flagged `return err`
	f.write("a.go", lines("package a",
		"func f() error {", "\t_, err := strconv.Atoi(s)", "\tif err != nil {", "\t\treturn err", "\t}", "\treturn nil", "}")...)
	f.record([]*result.Issue{f.issue("a.go", 5, "wrapcheck", "error returned from external package is unwrapped: Atoi")}, addMode)

	// the old issue is fixed, and a new one with another message appears in a second function
	f.write("a.go", lines("package a",
		"func f() error {", "\t_, err := strconv.Atoi(s)", "\tif err != nil {", "\t\treturn fmt.Errorf(\"x: %w\", err)", "\t}", "\treturn nil", "}",
		"func g() error {", "\t_, err := strconv.ParseBool(s)", "\tif err != nil {", "\t\treturn err", "\t}", "\treturn nil", "}")...)

	reported, stats := f.run([]*result.Issue{
		f.issue("a.go", 12, "wrapcheck", "error returned from external package is unwrapped: ParseBool"),
	}, "a.go")

	assert.Equal(t, []int{12}, reportedLines(reported))
	assert.Equal(t, BaselineStats{Stale: 1}, stats)
}

func TestBaseline_identicalMessageAndWindowStaysHidden(t *testing.T) {
	f := newBaselineFixture(t)

	window := lines("\tif err != nil {", "\t\treturn err", "\t}")

	f.write("a.go", append(lines("package a", "func f() error {"), append(window, "\treturn nil", "}")...)...)
	f.record([]*result.Issue{f.issue("a.go", 4, "wrapcheck", "unwrapped: Atoi")}, addMode)

	// fixed in f, the same message and window appear in g: nothing tells them apart
	f.write("a.go", lines("package a", "func f() error {", "\treturn nil", "}",
		"func g() error {", "\tif err != nil {", "\t\treturn err", "\t}", "\treturn nil", "}")...)

	reported, stats := f.run([]*result.Issue{f.issue("a.go", 7, "wrapcheck", "unwrapped: Atoi")}, "a.go")

	assert.Empty(t, reported)
	assert.Equal(t, 0, stats.Stale)
}

func TestBaseline_pathsThroughSymbolicLinks(t *testing.T) {
	f := newBaselineFixture(t)

	linkPath := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(f.dir, linkPath))

	f.write("a.go", lines("package a", "func f() {", "\tx()", "}")...)
	f.record([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")}, addMode)

	t.Run("the base path is a link, the files are not", func(t *testing.T) {
		opts := f.options(false, "a.go")
		opts.BasePath = linkPath

		processor := f.newProcessor(opts)

		reported, err := processor.Process([]*result.Issue{f.issue("a.go", 3, "errcheck", "unchecked")})
		require.NoError(t, err)

		assert.Empty(t, reported)
		assert.Equal(t, BaselineStats{Hidden: 1}, processor.Stats())
	})

	t.Run("the files are reached through a link, the base path is not", func(t *testing.T) {
		opts := f.options(false)
		opts.Analyzed = []string{filepath.Join(linkPath, "a.go")}

		processor := f.newProcessor(opts)

		issue := f.issue("a.go", 3, "errcheck", "unchecked")
		issue.Pos.Filename = filepath.Join(linkPath, "a.go")

		reported, err := processor.Process([]*result.Issue{issue})
		require.NoError(t, err)

		assert.Empty(t, reported)
		assert.Equal(t, BaselineStats{Hidden: 1}, processor.Stats())
	})

	t.Run("a file that cannot be resolved fails open", func(t *testing.T) {
		processor := f.newProcessor(f.options(false))

		issue := f.issue("a.go", 3, "errcheck", "unchecked")
		issue.Pos.Filename = filepath.Join(t.TempDir(), "elsewhere", "a.go")

		assert.NotEqual(t, "a.go", processor.relativePath(issue.FilePath()))
	})
}
