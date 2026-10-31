# good

`good` is golangci-lint v2.14.0 plus a few fixes, built as a binary named `good`.
The module path is unchanged (`github.com/golangci/golangci-lint/v2`) so each patch stays upstreamable.

## What is patched

- Cache keys: each package hash is computed once per run instead of once per concurrent caller
  (branch `good-v2.14.0`, `internal/cache`). Key bytes are unchanged, so existing caches stay valid.
- `--new-from-*`: renames from outside the working directory are detected, so a moved directory is not
  reported as all-new code (`go.mod` replace of `github.com/golangci/revgrep` with `feds01/revgrep`).
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

## Verify

```sh
go test ./internal/... ./pkg/...
# same issues as upstream v2.14.0 on a repo:
good run --output.json.path=good.json --max-issues-per-linter=0 --max-same-issues=0
golangci-lint run --output.json.path=stock.json --max-issues-per-linter=0 --max-same-issues=0
# rename fix: from a subdirectory of a branch that moved files into it
good run --new-from-merge-base=origin/main
```
