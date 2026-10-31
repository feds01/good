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
go build -o good ./cmd/golangci-lint
```

Do not build with `-trimpath`: revive type-checks with `go/importer`, which fails in a trimmed binary, so revive loses type
information (for example `untyped string` instead of `string` in context-keys-type messages) and some of its rules go quiet.
The binary is faster that way (about 80 CPU seconds less on a cold core/backend run) because that work is skipped.
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
