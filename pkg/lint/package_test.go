package lint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/goanalysis/load"
	"github.com/golangci/golangci-lint/v2/pkg/goutil"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
)

func Test_buildArgs(t *testing.T) {
	testCases := []struct {
		desc     string
		args     []string
		expected []string
	}{
		{
			desc:     "empty",
			args:     nil,
			expected: []string{"./..."},
		},
		{
			desc:     "start with a dot",
			args:     []string{filepath.FromSlash("./foo")},
			expected: []string{filepath.FromSlash("./foo")},
		},
		{
			desc:     "start without a dot",
			args:     []string{"foo"},
			expected: []string{filepath.FromSlash("./foo")},
		},
		{
			desc:     "absolute path",
			args:     []string{mustAbs(t, "/tmp/foo")},
			expected: []string{mustAbs(t, "/tmp/foo")},
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			results := buildArgs(test.args)

			assert.Equal(t, test.expected, results)
		})
	}
}

func Test_hasCgoFiles(t *testing.T) {
	dir := t.TempDir()

	writeFile := func(name, content string) string {
		t.Helper()

		filename := filepath.Join(dir, name)

		require.NoError(t, os.WriteFile(filename, []byte(content), 0o600))

		return filename
	}

	plain := writeFile("plain.go", "package a\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n")
	cgo := writeFile("cgo.go", "package a\n\n// int x;\nimport \"C\"\n")
	groupedCgo := writeFile("grouped.go", "package a\n\nimport (\n\t\"C\"\n\t\"fmt\"\n)\n")
	broken := writeFile("broken.go", "not go")

	testCases := []struct {
		desc     string
		pkgs     []*packages.Package
		expected bool
	}{
		{
			desc:     "no packages",
			expected: false,
		},
		{
			desc:     "no cgo",
			pkgs:     []*packages.Package{{GoFiles: []string{plain}}},
			expected: false,
		},
		{
			desc:     "cgo",
			pkgs:     []*packages.Package{{GoFiles: []string{plain}}, {GoFiles: []string{plain, cgo}}},
			expected: true,
		},
		{
			desc:     "cgo in an import group",
			pkgs:     []*packages.Package{{GoFiles: []string{groupedCgo}}},
			expected: true,
		},
		{
			desc:     "unparsable file",
			pkgs:     []*packages.Package{{GoFiles: []string{broken, plain}}},
			expected: false,
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.expected, hasCgoFiles(test.pkgs))
		})
	}
}

func TestPackageLoader_LoadCompiledGoFiles_variant(t *testing.T) {
	loader := &PackageLoader{}

	pkg := &packages.Package{ID: "example.com/a [example.com/b.test]", PkgPath: "example.com/a"}

	require.Error(t, loader.LoadCompiledGoFiles(context.Background(), pkg))
}

func TestPackageLoader_loadPackages_lazy(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/lazy\n\ngo 1.21\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lazy.go"), []byte("package lazy\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n"), 0o600))

	t.Chdir(dir)

	log := logutils.NewStderrLog("skip")
	loader := NewPackageLoader(log, config.NewDefault(), []string{"./..."}, goutil.NewEnv(log), load.NewGuard())

	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
		packages.NeedDeps | packages.NeedExportFile | packages.NeedTypesSizes

	pkgs, err := loader.loadPackages(t.Context(), mode)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)

	pkg := pkgs[0]

	assert.Empty(t, pkg.ExportFile, "export data is loaded on demand")
	assert.NotNil(t, pkg.TypesSizes)
	assert.Equal(t, pkg.GoFiles, pkg.CompiledGoFiles)
	assert.NotNil(t, pkg.Imports["fmt"])
	assert.Empty(t, pkg.Imports["fmt"].ExportFile)

	require.NoError(t, loader.LoadExportData(t.Context(), pkg.Imports["fmt"]))
	assert.NotEmpty(t, pkg.Imports["fmt"].ExportFile)
}

func Test_cgoFlagsEnv(t *testing.T) {
	assert.Equal(t, "CGO_CFLAGS=-O0 -g0", cgoFlagsEnv(""))
	assert.Equal(t, "CGO_CFLAGS=-I/include -O0 -g0", cgoFlagsEnv("-I/include"))
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()

	abs, err := filepath.Abs(filepath.FromSlash(p))
	require.NoError(t, err)

	return abs
}
