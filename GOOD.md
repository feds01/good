# good

`good` is golangci-lint v2.14.0 plus a few fixes, built as a binary named `good`.
The module path is unchanged (`github.com/golangci/golangci-lint/v2`) so each patch stays upstreamable.

## What is patched

- Cache keys: each package hash is computed once per run instead of once per concurrent caller
  (branch `good-v2.14.0`, `internal/cache`). Key bytes are unchanged, so existing caches stay valid.
- `--new-from-*`: renames from outside the working directory are detected, so a moved directory is not
  reported as all-new code (`go.mod` replace of `github.com/golangci/revgrep` with `feds01/revgrep`).
- Cold start: export data is built only for packages whose cached facts will be read, and the C code of cgo dependencies
  is processed only when a dependency is type-checked from source (branch `perf-cold-load`, `pkg/lint`, `pkg/goanalysis`).
- forbidigo: no `go/printer` for identifiers and selectors (`go.mod` replace of
  `github.com/ashanbrown/forbidigo/v2` with `feds01/forbidigo`).

## Build

Use a Go toolchain at least as new as the one your code targets (golangci-lint refuses older ones):

```sh
go build -trimpath -o good ./cmd/golangci-lint
```

Build with `-trimpath`, as stock release builds are. Revive type-checks with `go/importer`, which fails in a trimmed binary, so that
work is skipped: on a cold core/backend run this halves the wall time (about 10s against 19s) and removes about 90 CPU seconds of system
time, with identical issue positions. The cost is wording: `untyped string` instead of `string` in context-keys-type messages.
Revive rules that need type information can go quiet in a trimmed binary, on other code too.
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
