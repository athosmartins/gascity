# Release Gate: ga-o5jrsf - SQLite-cgo coordstore gate fix

Deploy bead: ga-o5jrsf
Source review bead: ga-pytgv1
Source builder bead: ga-5rzhde
Existing PR: https://github.com/gastownhall/gascity/pull/2738
PR branch: builder/ga-aec8q.16-sqlite-cutover
Reviewed fix branch: builder/ga-5rzhde-sqlite-cgo-fix
Reviewed fix commit: 5bbcc947d0dbef57bf75daf6ea38b16e1b233ab8
Gate evaluated: 2026-06-01

Note: `docs/PROJECT_MANIFEST.md` is not present in this checkout. This gate
uses the release criteria from the deployer prompt loaded by `gc prime`, with
test scope aligned to `TESTING.md` and the PR #2738 review notes.

## Summary

This gate evaluates a one-commit fix on top of the already-open PR #2738
branch. The fix restores two SQLite-cgo store helper paths that the deploy gate
requires:

- create-time dependency indexing now uses `depsFromBeadFields`, so both
  structured `Dependencies` and legacy `Needs` are persisted consistently;
- imported or explicitly supplied bead IDs can again advance sequence recovery
  through `numericIDSuffix`.

The existing PR remains an opt-in, build-tagged SQLite-cgo coordination-store
backend for beads. The fix commit changes only `internal/beads/sqlite_cgo_store.go`
relative to the current PR head.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `bd show ga-pytgv1` is closed with `REVIEWER VERDICT: PASS` for commit `5bbcc947d0dbef57bf75daf6ea38b16e1b233ab8`. |
| 2 | Acceptance criteria met | PASS | The deploy bead requires the standard gate on `5bbcc947d`. The reviewed diff from PR head `1111c8ad2a4fdff18200224a20e9d6d8706ff83b` to the fix commit changes only `internal/beads/sqlite_cgo_store.go`; `git diff --check 1111c8ad2 5bbcc947d` is clean. |
| 3 | Tests pass | PASS | Local gate commands passed in a clean worktree at `5bbcc947d`: `GOTOOLCHAIN=auto go test -tags sqlite_cgo ./internal/beads ./cmd/gc -count=1`, `GOTOOLCHAIN=auto make test-fast-parallel`, and `GOTOOLCHAIN=auto go vet ./...`. |
| 4 | No high-severity review findings open | PASS | `ga-pytgv1` records only INFO findings: dependency-field substitution is canonical, `numericIDSuffix` matches the existing reference behavior, SQL remains parameterized, and the relevant tests cover both restored paths. |
| 5 | Final branch is clean | PASS | `/tmp/gascity-deploy-ga-o5jrsf` was clean before this gate file was added; cleanliness is rechecked after committing the gate artifact. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main 5bbcc947d` exited 0 and produced tree `8d91e927c7ac8632af7800029eade3e1309a2b9f`. The fix commit is a fast-forward child of the current PR #2738 head. |
| 7 | Single feature theme | PASS | The deploy delta is one SQLite-cgo beads-store fix on top of PR #2738. The PR's single release theme remains the opt-in SQLite-cgo coordination-store backend and its validation surface. |

## Acceptance Evidence

- PR #2738 is open against `main` from branch
  `builder/ga-aec8q.16-sqlite-cutover`.
- Current PR #2738 head before this update was
  `1111c8ad2a4fdff18200224a20e9d6d8706ff83b`.
- The reviewed fix commit is
  `5bbcc947d0dbef57bf75daf6ea38b16e1b233ab8`.
- `origin/main` during this gate resolved to
  `49cd242ad6316b744419f71440079494c51a621e`.
- The fix commit changes only `internal/beads/sqlite_cgo_store.go` relative to
  the existing PR head.

## Commands

```text
gc prime
bd prime
gc hook gascity/deployer
bd update ga-o5jrsf --claim
bd show ga-o5jrsf
bd show ga-pytgv1
gh auth status
git fetch --all --prune
git worktree add /tmp/gascity-deploy-ga-o5jrsf -b deploy/ga-o5jrsf 5bbcc947d
git status --short --branch
git diff --check 1111c8ad2 5bbcc947d
git merge-tree --write-tree origin/main 5bbcc947d
git diff --name-only 1111c8ad2 5bbcc947d
GOTOOLCHAIN=auto go test -tags sqlite_cgo ./internal/beads ./cmd/gc -count=1
GOTOOLCHAIN=auto make test-fast-parallel
GOTOOLCHAIN=auto go vet ./...
```

## Test Summary

```text
go test -tags sqlite_cgo ./internal/beads ./cmd/gc -count=1
ok  	github.com/gastownhall/gascity/internal/beads	4.125s
ok  	github.com/gastownhall/gascity/cmd/gc	339.505s

make test-fast-parallel
All fast jobs passed

go vet ./...
clean
```
