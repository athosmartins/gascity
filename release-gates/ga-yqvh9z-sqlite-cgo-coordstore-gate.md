# Release Gate: ga-yqvh9z - SQLite-cgo coordstore backend rebase refresh

Deploy bead: ga-yqvh9z
Source review bead: ga-oiwi81
Source builder bead: ga-snab2n.1
Branch: builder/ga-aec8q.16-sqlite-cutover
PR: https://github.com/gastownhall/gascity/pull/2738
Reviewed commit: dc3ba827598e6a48cd5a7fb2e90729efdff6dbde
Gate evaluated: 2026-06-01

Note: `docs/PROJECT_MANIFEST.md` is not present in this checkout. This gate
uses the release criteria from the deployer prompt loaded by `gc prime`, with
test scope aligned to `TESTING.md` and the PR #2738 review notes.

## Summary

This gate evaluates the refreshed PR #2738 branch after it was rebased onto
current `origin/main`. The change remains an opt-in SQLite-cgo coordination
store backend for bead storage. The existing Dolt-backed path remains the
default; the new backend is reachable only from a build that includes
`-tags sqlite_cgo` and an explicit `[beads] provider = "sqlite"` setting.

The refresh keeps the PR #2738 review boundary intact, resolves the known
rebase conflicts, excludes the retention-aware follow-up commit, and preserves
the import/shadow-diff tooling, provider wiring, store-health reporting,
benchmark adapters, tests, and validation docs already reviewed for this
backend.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `bd show ga-oiwi81` is closed with `REVIEW VERDICT: pass` from `gascity/reviewer` for PR #2738 at commit `dc3ba827598e6a48cd5a7fb2e90729efdff6dbde`. |
| 2 | Acceptance criteria met | PASS | `ga-snab2n.1` required the PR #2738 branch to be refreshed onto current `origin/main`, resolve the known conflict files without unrelated scope, preserve the PR review boundary, exclude follow-up commit `07a9fd70a`, and record evidence. The branch head is `dc3ba827598e6a48cd5a7fb2e90729efdff6dbde`; `git merge-base --is-ancestor 07a9fd70a HEAD` returned `1`; `git diff --check origin/main...HEAD`, focused conflict-area tests, tagged SQLite-cgo tests, `go vet ./...`, and `make test-fast-parallel` all passed. |
| 3 | Tests pass | PASS | Local gate commands passed in a clean detached worktree at PR head: docs schema freshness, focused `internal/beads` suite, `go test -tags sqlite_cgo ./internal/beads ./cmd/gc`, `go vet ./...`, and `make test-fast-parallel`. Required GitHub checks for PR #2738 also pass. |
| 4 | No high-severity review findings open | PASS | `ga-oiwi81` review notes record no blockers: security observations are all INFO, SQL inputs are parameterized or controlled, the backend is build-tagged/default-off, and the reviewer found no high-severity issues. |
| 5 | Final branch is clean | PASS | `git status --short --branch` in `/tmp/gascity-deploy-ga-yqvh9z` was clean before this gate file was added. Cleanliness is rechecked after committing this gate file. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` exited 0 and produced tree `4d5ef13c2ff864a0e6e897bb8d9dd2119808eeb3`. GitHub reports PR #2738 `mergeable=MERGEABLE` and `mergeStateStatus=CLEAN`. |
| 7 | Single feature theme | PASS | The branch is large, but it has one release theme: an opt-in SQLite-cgo coordination-store backend for beads with the provider wiring, migration comparison tools, benchmark adapters, tests, and docs needed to validate that backend. No independent user-facing feature outside that backend stack is included. |

## Acceptance Evidence

- PR #2738 remains on branch `builder/ga-aec8q.16-sqlite-cutover` and is open
  against `main`.
- The reviewed head is
  `dc3ba827598e6a48cd5a7fb2e90729efdff6dbde`.
- `origin/main` during this gate resolved to
  `0d3702f0205ad3fef2bd188da7ed01845c2f95f0`.
- The excluded retention-aware follow-up commit `07a9fd70a` is not in the PR
  branch ancestry.
- Changed paths stay within the coordination-store/beads backend stack:
  `cmd/gc` provider and import/shadow commands, coordination-store docs,
  config docs/schema, `internal/beads`, and coordstore benchmark adapters.

## Commands

```text
gc prime
bd prime
gc hook gascity/deployer
bd update ga-yqvh9z --claim
bd show ga-yqvh9z
bd show ga-oiwi81
bd show ga-snab2n.1
gh auth status
gh pr view 2738 --json number,title,state,url,headRefName,headRepositoryOwner,baseRefName,mergeable,mergeStateStatus,isDraft,commits,statusCheckRollup,reviewDecision,latestReviews
git fetch origin main builder/ga-aec8q.16-sqlite-cutover
git worktree add --detach /tmp/gascity-deploy-ga-yqvh9z origin/builder/ga-aec8q.16-sqlite-cutover
git status --short --branch
git diff --check origin/main...HEAD
git merge-tree --write-tree origin/main HEAD
git merge-base --is-ancestor 07a9fd70a HEAD
gh pr checks 2738 --required
GOTOOLCHAIN=auto go test ./test/docsync -run TestSchemaFreshness -count=1
GOTOOLCHAIN=auto go test ./internal/beads -run 'TestFileStore|TestBdStore|TestHQStore|TestSQLite|Test.*Tier' -count=1
GOTOOLCHAIN=auto go test -tags sqlite_cgo ./internal/beads ./cmd/gc -count=1
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto make test-fast-parallel
```

## Test Summary

```text
go test ./test/docsync -run TestSchemaFreshness -count=1
ok  	github.com/gastownhall/gascity/test/docsync	3.151s

go test ./internal/beads -run 'TestFileStore|TestBdStore|TestHQStore|TestSQLite|Test.*Tier' -count=1
ok  	github.com/gastownhall/gascity/internal/beads	1.423s

go test -tags sqlite_cgo ./internal/beads ./cmd/gc -count=1
ok  	github.com/gastownhall/gascity/internal/beads	5.717s
ok  	github.com/gastownhall/gascity/cmd/gc	381.119s

go vet ./...
clean

make test-fast-parallel
All fast jobs passed

gh pr checks 2738 --required
Analyze (actions)              pass
Analyze (go)                   pass
Analyze (javascript-typescript) pass
Analyze (python)               pass
Check                          pass
```
