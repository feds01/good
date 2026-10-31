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
  settings (`CGO_CFLAGS`, `CGO_CPPFLAGS`, `CGO_CXXFLAGS`, `CC`, `CXX`, read with `go env`, so `go env -w` counts), so a header
  or flag change invalidates the package and its dependants. Only headers outside the package directory are not hashed.
  A package that has no export data (it does not compile) is analyzed from source, so the output does not depend on the
  state of the lint cache. Errors that only the compiler reports, such as `missing function body`, are no longer
  reported for analyzed packages; type errors still are. `go build` and the tests catch the others.
- Baseline: `issues.baseline` points to a committed file of known issues that `run` does not report, so old code
  does not have to be fixed after an upgrade or when a linter is enabled. `good baseline` adds the new findings,
  `good baseline --prune` removes the fixed ones (branch `baseline`, `pkg/result/processors/baseline*.go`,
  docs in `docs/content/docs/configuration/baseline.md`). `run` matches exactly on file, linter, message and a hash of the
  flagged line with its nearest non-blank neighbors, so edits elsewhere in a file do not bring issues back, and a renamed file keeps its entries.
  The baseline exists only in `good`: using it in CI means running `good` there, stock golangci-lint does not know `issues.baseline`.
- Fewer file opens: a source file is read once per run for the cgo check and the processors, and the hash of each file
  is kept in the lint cache directory (`filehash-*`) with the size, modification time, change time, inode and device of
  the file, so a file is read for its cache key only when one of them changed or the file changed within the last 2
  seconds. The keys are unchanged. Only on macOS and Linux. Shards not written for 14 days and temporary files of a
  crashed save are deleted. Set `GOLANGCI_LINT_NO_HASH_MEMO=1` to hash every file on every run: do it on NFS, FUSE
  and exFAT mounts, where stat can be staler than an open or the change time is not kept. Delete the `filehash-*`
  files, or run `cache clean`, to start over.
- forbidigo: no `go/printer` for identifiers and selectors (`go.mod` replace of
  `github.com/ashanbrown/forbidigo/v2` with `feds01/forbidigo`).
- revive: the rules reuse the type information golangci-lint already computed (`go.mod` replace of
  `github.com/mgechev/revive` with `feds01/revive`, branch `use-pass-types`). revive no longer type-checks
  each package a second time, so `time-naming`, `var-declaration`, the `errorf` check on `t.Error(fmt.Sprintf(...))`
  and `context-keys-type` give the same result in `-trimpath` and regular builds.
  Like the other typed linters, revive does not run on a package that fails to type-check (a `typecheck` issue is
  reported instead), and an import that cannot be resolved is a `typecheck` error.
- Deterministic output: issues are put in a total order before the processors that keep the first of several issues
  (`uniq-by-line`, `max-same-issues`, ...), `SortResults` breaks ties on linter, text and severity, and the summary
  lines no longer follow map order, so identical runs print identical JSON, cold or warm.
  - canonicalheader no longer reports nothing on a package that uses both `http.Header` and `ResponseWriter.Header()`,
    which depended on map order (`go.mod` replace of `github.com/golangci/canonicalheader` with `feds01/canonicalheader`).
  - Of several issues on one line, `uniq-by-line` keeps one from a real linter (first by name), else a formatter, else
    nolintlint; then the leftmost. A formatter never hides a real issue, because `max-per-file-from-linter` would drop it.
  - With limits (`max-same-issues`, `max-issues-per-linter`) the issues kept are the first N in file order, where stock
    kept whatever the linters reported first. The output is a deterministic subset of the unlimited output, but not the
    same subset as stock, so per-linter counts differ (`max-same-issues` counts texts shared by several linters).

## Build

Use a Go toolchain at least as new as the one your code targets (golangci-lint refuses older ones):

```sh
go build -o good ./cmd/golangci-lint
```

`-trimpath` is optional: revive uses the type information of the analysis pass, so its rules give the same result in
both builds. The Go patch version (1.27.1 against 1.27.3) makes no measurable difference.

## Release

Releases are built by the `Release good` workflow (run it from the Actions tab or with
`gh workflow run good-release.yml --ref good -f sha=<40-hex commit on good>`). It runs `scripts/build-dist.sh`, which
builds `linux/amd64`, `darwin/arm64` and `darwin/amd64` tarballs and attests `checksums.txt`. The release is tagged
`good-<first 12 of the sha>`, and `good version` reports the same string, so the lint cache is never shared between commits.
The tarballs are reproducible: `scripts/build-dist.sh <sha>` on any machine gives the same sha256 as the release.

## Verify

```sh
go test ./internal/... ./pkg/...
# same issues as upstream v2.14.0 on a repo:
good run --output.json.path=good.json --max-issues-per-linter=0 --max-same-issues=0
golangci-lint run --output.json.path=stock.json --max-issues-per-linter=0 --max-same-issues=0
# rename fix: from a subdirectory of a branch that moved files into it
good run --new-from-merge-base=origin/main
```
