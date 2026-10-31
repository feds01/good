package test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
	"github.com/golangci/golangci-lint/v2/test/testshared"
)

const baselineConfig = `
version: "2"
linters:
  default: none
  enable:
    - errcheck
    - misspell
issues:
  baseline: baseline.toml
`

type baselineProject struct {
	t       *testing.T
	binPath string
	dir     string
}

type baselineResult struct {
	stdout   string
	stderr   string
	exitCode int
}

func newBaselineProject(t *testing.T, source string) *baselineProject {
	t.Helper()

	project := &baselineProject{
		t:       t,
		binPath: testshared.InstallGolangciLint(t),
		dir:     t.TempDir(),
	}

	project.write("go.mod", "module example.com/m\n\ngo 1.24\n")
	project.write(".golangci.yml", baselineConfig)
	project.write("main.go", source)

	return project
}

func (p *baselineProject) write(name, content string) {
	p.t.Helper()

	require.NoError(p.t, os.WriteFile(filepath.Join(p.dir, name), []byte(content), 0o600))
}

func (p *baselineProject) read(name string) string {
	p.t.Helper()

	data, err := os.ReadFile(filepath.Join(p.dir, name))
	require.NoError(p.t, err)

	return string(data)
}

func (p *baselineProject) command(args ...string) baselineResult {
	p.t.Helper()

	return p.commandIn(p.dir, args...)
}

func (p *baselineProject) commandIn(dir string, args ...string) baselineResult {
	p.t.Helper()

	//nolint:gosec // the binary is the one built for the tests
	cmd := exec.CommandContext(p.t.Context(), p.binPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOLANGCI_LINT_CACHE="+p.t.TempDir())

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0

	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		require.True(p.t, ok, "can't run %s: %v", p.binPath, err)

		exitCode = exitError.Sys().(syscall.WaitStatus).ExitStatus()
	}

	return baselineResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode}
}

func (p *baselineProject) run(args ...string) baselineResult {
	p.t.Helper()

	return p.command(append([]string{"run", "--allow-parallel-runners", "--show-stats=false"}, args...)...)
}

func (p *baselineProject) baseline(args ...string) baselineResult {
	p.t.Helper()

	return p.command(append([]string{"baseline", "--allow-parallel-runners"}, args...)...)
}

func countIssues(output string) int {
	return strings.Count(output, "(errcheck)") + strings.Count(output, "(misspell)")
}

const baselineSource = `package m

import "os"

func f() {
	os.Remove("a")
	println()
	os.Remove("b")
}
`

func TestBaseline_lifecycle(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	missing := project.run()
	assert.Equal(t, exitcodes.Failure, missing.exitCode, "a configured baseline must exist")

	created := project.baseline()
	require.Equal(t, exitcodes.Success, created.exitCode, created.stderr)
	assert.Contains(t, created.stdout, "baseline: 2 added, 0 pruned, 0 kept, 0 re-keyed")
	assert.Contains(t, project.read("baseline.toml"), `["main.go"]`)

	clean := project.run()
	assert.Equal(t, exitcodes.Success, clean.exitCode, clean.stdout+clean.stderr)
	assert.Empty(t, clean.stdout)
	assert.Contains(t, clean.stderr, "baseline: 2 issues hidden")

	project.write("main.go", strings.Replace(baselineSource, "func f() {", "// added above\n// and above\nfunc f() {", 1))

	moved := project.run()
	assert.Equal(t, exitcodes.Success, moved.exitCode, "edits above the flagged lines keep them hidden: "+moved.stdout)

	project.write("main.go", baselineSource+"\nfunc g() {\n\tos.Remove(\"c\")\n}\n")

	added := project.run()
	assert.Equal(t, exitcodes.IssuesFound, added.exitCode)
	assert.Equal(t, 1, countIssues(added.stdout), added.stdout)
	assert.Contains(t, added.stdout, "main.go:12:")

	everything := project.run("--no-baseline")
	assert.Equal(t, 3, countIssues(everything.stdout), everything.stdout)

	project.write("main.go", strings.Replace(baselineSource, "\tos.Remove(\"b\")\n", "", 1))

	stale := project.run()
	assert.Equal(t, exitcodes.Success, stale.exitCode, stale.stdout)
	assert.Contains(t, stale.stderr, "1 stale entries")

	failing := project.run("--fail-on-stale-baseline")
	assert.Equal(t, exitcodes.IssuesFound, failing.exitCode)

	before := project.read("baseline.toml")

	dryRun := project.baseline("--prune", "--dry-run")
	assert.Contains(t, dryRun.stdout, "1 pruned")
	assert.Contains(t, dryRun.stdout, "dry run")
	assert.Equal(t, before, project.read("baseline.toml"))

	pruned := project.baseline("--prune")
	assert.Contains(t, pruned.stdout, "baseline: 0 added, 1 pruned, 1 kept")

	project.write("main.go", baselineSource+"\nfunc g() {\n\tos.Remove(\"z\")\n}\n")

	prunedOnly := project.baseline("--prune")
	assert.Contains(t, prunedOnly.stdout, "0 added", "--prune never adds")
	assert.Equal(t, 2, countIssues(project.run().stdout), "b and z are not in the baseline")

	absorbed := project.baseline()
	assert.Contains(t, absorbed.stdout, "added")
	assert.Equal(t, exitcodes.Success, project.run().exitCode)
}

func TestBaseline_refusesCodeThatDoesNotCompile(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	project.write("main.go", baselineSource+"\nfunc broken() { undefined() }\n")

	result := project.baseline()

	assert.NotEqual(t, exitcodes.Success, result.exitCode)
	assert.Contains(t, result.stderr, "does not compile")

	_, err := os.Stat(filepath.Join(project.dir, "baseline.toml"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestBaseline_requiresConfiguration(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	project.write(".golangci.yml", strings.Replace(baselineConfig, "issues:\n  baseline: baseline.toml\n", "", 1))

	result := project.baseline()

	assert.NotEqual(t, exitcodes.Success, result.exitCode)
	assert.Contains(t, result.stderr, "issues: baseline: .golangci.baseline.toml")
}

func TestBaseline_fixSkipsBaselinedIssues(t *testing.T) {
	// Built in two parts so that the repository's own spell checker does not flag it.
	const typo = "recie" + "ve"

	misspelled := "package m\n\n// This sentence has a " + typo + " typo.\nfunc f() {}\n"

	project := newBaselineProject(t, misspelled)

	require.Equal(t, exitcodes.Success, project.baseline().exitCode)

	fixed := project.run("--fix")
	assert.Equal(t, exitcodes.Success, fixed.exitCode, fixed.stdout)
	assert.Equal(t, misspelled, project.read("main.go"), "a baselined issue is not fixed")

	project.write("main.go", misspelled+"\n// Another "+typo+" typo.\nfunc g() {}\n")

	project.run("--fix")
	assert.Contains(t, project.read("main.go"), "// Another receive typo.")
	assert.Contains(t, project.read("main.go"), "a "+typo+" typo.", "the baselined comment keeps its typo")
}

func TestBaseline_jsonOutputIsUnchanged(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	require.Equal(t, exitcodes.Success, project.baseline().exitCode)

	result := project.run("--output.json.path=stdout")

	assert.Equal(t, exitcodes.Success, result.exitCode)
	assert.NotContains(t, result.stdout, "baseline")
	assert.Contains(t, result.stdout, `"Issues":[]`)
}

func TestBaseline_onlyLintersThatRanCanBeStale(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	require.Equal(t, exitcodes.Success, project.baseline().exitCode)

	partial := project.run("--enable-only=misspell", "--fail-on-stale-baseline")
	assert.Equal(t, exitcodes.Success, partial.exitCode, partial.stdout+partial.stderr)
	assert.NotContains(t, partial.stderr, "stale")

	before := project.read("baseline.toml")

	pruned := project.baseline("--prune", "--enable-only=misspell")
	assert.Contains(t, pruned.stdout, "0 pruned")
	assert.Equal(t, before, project.read("baseline.toml"), "the entries of the linters that did not run stay")
}

func TestBaseline_newCodeIsNotHiddenByAnIdenticalOldLine(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	require.Equal(t, exitcodes.Success, project.baseline().exitCode)

	// the old issue is fixed, and the same line is added in code that has other neighbors
	project.write("main.go", `package m

import "os"

func f() {
	println()
}

func g() {
	println("other")
	os.Remove("a")
	println("neighbors")
}
`)

	result := project.run()

	assert.Equal(t, exitcodes.IssuesFound, result.exitCode)
	assert.Equal(t, 1, countIssues(result.stdout), result.stdout)
	assert.Contains(t, result.stdout, "main.go:11:")
	assert.Contains(t, result.stderr, "stale")
}

func TestBaseline_newFromRevReportsTheNewCopy(t *testing.T) {
	const block = "\tx = 1\n\tos.Remove(\"a\")\n\ty = 2\n\t_, _ = x, y\n"

	source := "package m\n\nimport \"os\"\n\nfunc f() {\n\tvar x, y int\n" + block + "}\n"

	project := newBaselineProject(t, source)

	require.Equal(t, exitcodes.Success, project.baseline().exitCode)

	git := func(args ...string) {
		t.Helper()

		//nolint:gosec // fixed arguments
		cmd := exec.CommandContext(t.Context(), "git",
			append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = project.dir

		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	project.write("main.go", "package m\n\nimport \"os\"\n\nfunc f() {\n\tvar x, y int\n"+block+block+"}\n")

	everything := project.run("--new-from-rev=HEAD")
	assert.Equal(t, exitcodes.IssuesFound, everything.exitCode, everything.stdout)
	assert.Equal(t, 1, countIssues(everything.stdout), everything.stdout)
}

func TestBaseline_worksFromASubdirectoryAndThroughALink(t *testing.T) {
	project := newBaselineProject(t, baselineSource)

	project.write(".golangci.yml", strings.Replace(baselineConfig, "version: \"2\"\n", "version: \"2\"\nrun:\n  relative-path-mode: wd\n", 1))

	require.NoError(t, os.MkdirAll(filepath.Join(project.dir, "sub"), 0o750))
	project.write("sub/sub.go", "package sub\n\nimport \"os\"\n\nfunc g() {\n\tos.Remove(\"z\")\n}\n")

	created := project.baseline()
	require.Equal(t, exitcodes.Success, created.exitCode, created.stderr)
	assert.Contains(t, project.read("baseline.toml"), `["sub/sub.go"]`)

	fromRoot := project.run()
	assert.Equal(t, exitcodes.Success, fromRoot.exitCode, fromRoot.stdout+fromRoot.stderr)

	fromSub := project.commandIn(filepath.Join(project.dir, "sub"), "run", "--allow-parallel-runners", "--show-stats=false")
	assert.Equal(t, exitcodes.Success, fromSub.exitCode, fromSub.stdout+fromSub.stderr)
	assert.Contains(t, fromSub.stderr, "baseline: 1 issues hidden")

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(project.dir, link))

	throughLink := project.commandIn(link, "run", "--allow-parallel-runners", "--show-stats=false")
	assert.Equal(t, exitcodes.Success, throughLink.exitCode, throughLink.stdout+throughLink.stderr)
	assert.Contains(t, throughLink.stderr, "baseline: 3 issues hidden")
}
