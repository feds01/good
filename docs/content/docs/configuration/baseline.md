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

The path is relative to the directory of the configuration file, so it does not depend on the directory the linter is run from
(or on `run.relative-path-mode`).

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
Only the linters that ran can make an entry stale or be pruned: `--enable-only=godot` leaves the entries of the other linters alone.
Use `-v` to list the stale entries.

A few linters report different issues from one run to the next (for example `canonicalheader`).
Their issues show up as stale in some runs and as new in others: run `baseline` again to keep the union, and do not
`--prune` (or use `--fail-on-stale-baseline`) on a project that has such findings in its baseline.

## File format

```toml
version = 2

["internal/api/handler.go"]
entries = [
  { linter = "errcheck", hash = "3f9a2c1e", count = 2, message = "Error return value of `rows.Close` is not checked" },
]
```

Files are sorted by path and entries by linter, message and hash, one entry per line.
There are no line numbers: an entry is the file, the linter, the message and a hash of the flagged line with its nearest
non-blank line above and below (whitespace, line endings and a byte order mark are ignored).
The baseline is a multiset: `count` is the number of identical findings in the file, and must be at least 1.

A file in format version 1 (it hashed the flagged line alone) is rejected: delete it and run `baseline` to record version 2.
The new file lists every issue the code has now, so review its diff.

## Matching

- Edits elsewhere in a file never bring its issues back. Editing the flagged line, or the line directly above or below it, does,
  and so does adding a new identical finding with the same neighbors.
- With `--new-from-*`, the issues on unchanged lines use the entries first, so the one that is reported is the new copy.
- An issue whose message was reworded (a linter upgrade) still matches by file, linter and the line with its neighbors.
  `baseline` rewrites the entry with the new message.
- A file that was renamed or moved keeps its entries when it had at least 3 and at least 80% of them reappear in one new file.
  Otherwise its issues are reported again, and `baseline` records them under the new path.
- Entries of files that were not analyzed in a run (a partial run, build tags, `run.tests: false`) are neither stale nor pruned.
- `typecheck` errors are never hidden.
- Issues that `//nolint` or an exclusion removes are not in the baseline: their entries become stale.
- `--fix` does not fix baselined issues.
- Fixing one finding and adding an identical one with the same neighbors in the same file stays hidden: nothing tells them apart.
