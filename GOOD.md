# good

`good` is golangci-lint v2.14.0 plus a few fixes, built as a binary named `good`.
The module path is unchanged (`github.com/golangci/golangci-lint/v2`) so each patch stays upstreamable.

## What is patched

- Cache keys: each package hash is computed once per run instead of once per concurrent caller
  (branch `good-v2.14.0`, `internal/cache`). The key bytes of that change are unchanged. The cold-start change below re-keys the
  packages that have cgo or other non-Go files, and everything that imports them, once: the lint cache is rebuilt on the first run.
- `--new-from-*`: renames from outside the working directory are detected, so a moved directory is not
  reported as all-new code (`go.mod` replace of `github.com/golangci/revgrep` with `feds01/revgrep`).
- Cold start: export data is built only for packages whose cached facts will be read, and the C code of cgo dependencies
  is processed only when a dependency is type-checked from source (branch `perf-cold-load`, `pkg/lint`, `pkg/goanalysis`).
  Cache keys now hash the original cgo files, the non-Go files of a package (headers, C and assembly files) and the cgo
  environment (`CGO_CFLAGS`, `CGO_CPPFLAGS`, `CGO_CXXFLAGS`, `CC`, `CXX`), so a header or flag change invalidates the package
  and its dependants. Headers outside the package directory, and flags set with `go env -w`, are not hashed.
  A package that has no export data (it does not compile) is analyzed from source, so the output does not depend on the
  state of the lint cache. Errors that only the compiler reports, such as `missing function body`, are no longer
  reported for analyzed packages; type errors still are. `go build` and the tests catch the others.
- forbidigo: no `go/printer` for identifiers and selectors (`go.mod` replace of
  `github.com/ashanbrown/forbidigo/v2` with `feds01/forbidigo`).

## Build

Use a Go toolchain at least as new as the one your code targets (golangci-lint refuses older ones):

```sh
go build -trimpath -o good ./cmd/golangci-lint
```

Build with `-trimpath`, as stock release builds are, as an interim. Revive type-checks with `go/importer`, which fails in a trimmed
binary, so that work is skipped: on a cold core/backend run this halves the wall time (about 10s against 19s) and removes about
90 CPU seconds of system time. The cost is quiet: a trimmed binary does not report revive's `time-naming`, `var-declaration` and the
`t.Error(fmt.Sprintf(...))` branch of `errorf`, and rewords `context-keys-type` (`untyped string` instead of `string`). Stock release
builds have the same gap. The real fix is to make revive use the type information of the analysis pass.
The Go patch version (1.27.1 against 1.27.3) makes no measurable difference.

## Verify

```sh
go test ./internal/... ./pkg/...
# same issues as upstream v2.14.0 on a repo:
good run --output.json.path=good.json --max-issues-per-linter=0 --max-same-issues=0
golangci-lint run --output.json.path=stock.json --max-issues-per-linter=0 --max-same-issues=0
# rename fix: from a subdirectory of a branch that moved files into it
good run --new-from-merge-base=origin/main
```
