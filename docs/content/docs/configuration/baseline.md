---
title: Baseline
weight: 3
---

A baseline is a file that lists the issues of a project that are known and accepted.
`run` does not report them, so a project can adopt a linter, or upgrade one, without fixing old code first.
It is the alternative to `--new-from-rev` that needs no git history and no patch.

## Set up

```yml
version: "2"

issues:
  baseline: .golangci.baseline.toml
```

The path is relative to the base path (see `run.relative-path-mode`).

```bash
golangci-lint baseline    # writes .golangci.baseline.toml
golangci-lint run         # reports only what is not in it
```

Commit the file.

## Update

| Command                              | Effect                                                                                           |
|--------------------------------------|--------------------------------------------------------------------------------------------------|
| `golangci-lint baseline`             | Adds the issues that are not recorded yet. Nothing is removed. Use it after an upgrade, after enabling a linter or changing its settings. |
| `golangci-lint baseline --prune`     | Removes the entries of issues that no longer exist. Nothing is added.                            |
| `golangci-lint baseline --add --prune` | Both: the file matches the code.                                                               |
| `--dry-run`                          | Prints what would change, writes nothing.                                                        |
| `--with-source`                      | Writes the flagged line next to each entry, so a pull request diff shows what code is ignored.   |

`run` prints how many issues the baseline hid and how many entries are stale (the issue was fixed).
The baseline only shrinks by fixing code: set `issues.fail-on-stale-baseline` (or `--fail-on-stale-baseline`) to fail the run
until `baseline --prune` is committed.
`--no-baseline` reports every issue, for example to fix old code with `--fix`.

`baseline` refuses to run on code that does not compile.

## File format

```toml
version = 1

["internal/api/handler.go"]
entries = [
  { linter = "errcheck", hash = "3f9a2c1e", count = 2, message = "Error return value of `rows.Close` is not checked" },
]
```

Files are sorted by path and entries by linter, message and hash, one entry per line.
There are no line numbers: an entry is the file, the linter, the message and a hash of the text of the flagged line
(whitespace, line endings and a byte order mark are ignored).
The baseline is a multiset: `count` is the number of identical findings in the file.

## Matching

- Edits elsewhere in a file never bring its issues back. Editing the flagged line does, and so does adding a new identical finding.
- An issue whose message was reworded (a linter upgrade) still matches by file, linter and line text.
  `baseline` rewrites the entry with the new message.
- A file that was renamed or moved keeps its entries when at least 80% of the entries of the file that disappeared
  reappear in one new file. Otherwise its issues are reported again, and `baseline` records them under the new path.
- Entries of files that were not analyzed in a run (a partial run, build tags, `run.tests: false`) are neither stale nor pruned.
- `typecheck` errors are never hidden.
- Issues that `//nolint` or an exclusion removes are not in the baseline: their entries become stale.
- `--fix` does not fix baselined issues.
- With `--new-from-*` both filters apply.
