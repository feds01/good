# good

`good` is golangci-lint v2.14.0 plus a few fixes, built as a binary named `good`.
The module path is unchanged (`github.com/golangci/golangci-lint/v2`) so each patch stays upstreamable.

## What is patched

- Cache keys: each package hash is computed once per run instead of once per concurrent caller
  (branch `good-v2.14.0`, `internal/cache`). The key bytes of that change are unchanged. The cold-start change below re-keys the
  packages that have cgo or other non-Go files, and everything that imports them, once: the lint cache is rebuilt on the first run.
- `--new-from-*`: renames from outside the working directory are detected, so a moved directory is not
  reported as all-new code (`go.mod` replace of `github.com/golangci/revgrep` with `feds01/revgrep`).
- Cold start: export data is never built, dependencies are type-checked from source and reuse their cached facts, and the C code of
  cgo dependencies is processed only when a dependency is type-checked (branches `perf-cold-load` and `perf-no-graph-export`,
  `pkg/lint`, `pkg/goanalysis`). Building export data made `go list` compile the whole dependency graph as soon as one package had
  its facts cached.
  Cache keys now hash the original cgo files, the non-Go files of a package (headers, C and assembly files) and the cgo
  settings (`CGO_CFLAGS`, `CGO_CPPFLAGS`, `CGO_CXXFLAGS`, `CC`, `CXX`, read with `go env`, so `go env -w` counts), so a header
  or flag change invalidates the package and its dependants. Only headers outside the package directory are not hashed.
  The output does not depend on the state of the lint cache. Errors that only the compiler reports, such as `missing function body`, are no longer
  reported for analyzed packages; type errors still are. `go build` and the tests catch the others.
- Baseline: `issues.baseline` points to a committed file of known issues that `run` does not report, so old code
  does not have to be fixed after an upgrade or when a linter is enabled. `good baseline` adds the new findings,
  `good baseline --prune` removes the fixed ones (branch `baseline`, `pkg/result/processors/baseline*.go`,
  docs in `docs/content/docs/configuration/baseline.md`). `run` matches exactly on file, linter, message and a hash of the
  flagged line with its nearest non-blank neighbors, so edits elsewhere in a file do not bring issues back, and a renamed file keeps its entries.
  The baseline exists only in `good`: using it in CI means running `good` there, stock golangci-lint does not know `issues.baseline`.
- Issues cache: a package with a type error or a failed analyzer no longer keeps the issues of the other packages from being
  cached. Only the packages that failed, are canceled, or import one of those are left out. A package that does not type-check
  no longer cancels the analysis of the others, so independent type errors in several packages are all reported (before, which
  ones showed depended on scheduling). The packages that import a broken analyzed package do not repeat its error with a
  `could not import` line; one that imports a broken package that is not analyzed itself still reports it.
- forbidigo: no `go/printer` for identifiers and selectors (`go.mod` replace of
  `github.com/ashanbrown/forbidigo/v2` with `feds01/forbidigo`).
- revive: the rules reuse the type information golangci-lint already computed (`go.mod` replace of
  `github.com/mgechev/revive` with `feds01/revive`, branch `use-pass-types`). revive no longer type-checks
  each package a second time, so `time-naming`, `var-declaration`, the `errorf` check on `t.Error(fmt.Sprintf(...))`
  and `context-keys-type` give the same result in `-trimpath` and regular builds.
  Like the other typed linters, revive does not run on a package that fails to type-check (a `typecheck` issue is
  reported instead), and an import that cannot be resolved is a `typecheck` error.

## Build

Use a Go toolchain at least as new as the one your code targets (golangci-lint refuses older ones):

```sh
go build -o good ./cmd/golangci-lint
```

`-trimpath` is optional: revive uses the type information of the analysis pass, so its rules give the same result in
both builds. The Go patch version (1.27.1 against 1.27.3) makes no measurable difference.

## Verify

```sh
go test ./internal/... ./pkg/...
# same issues as upstream v2.14.0 on a repo:
good run --output.json.path=good.json --max-issues-per-linter=0 --max-same-issues=0
golangci-lint run --output.json.path=stock.json --max-issues-per-linter=0 --max-same-issues=0
# rename fix: from a subdirectory of a branch that moved files into it
good run --new-from-merge-base=origin/main
```
