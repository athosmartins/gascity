# Release Gate: ga-sinlbg

Bead: ga-sinlbg - needs-deploy: tmux server teardown after stop orphans
Branch: builder/ga-yxnz9x.3-stop-teardown
Head: b1edf7792dae2f838129ff85036bc0516c5e5ba9
Base checked: origin/main at f3012f1daa852186723bcabd2cd189c7577ba4e1
Gate run: 2026-05-29T21:39:01-07:00

Note: `docs/PROJECT_MANIFEST.md` is not present in this worktree. This gate
uses the deployer release criteria from the agent instructions and the local
test-command guidance in `TESTING.md`.

## Commit Set

| Commit | Scope | Summary |
| --- | --- | --- |
| 3e4ac7dca | internal/runtime, internal/runtime/tmux | feat(runtime): add tmux server lifecycle provider |
| b1edf7792 | cmd/gc | fix(cmd/gc): teardown server after orphan stop |

Stack context: PR #2774 is the prerequisite ServerLifecycleProvider PR and is
still open. This branch includes the provider commit because the stop teardown
implementation depends on it; human merge order should keep #2774 before this
PR.

## Checklist

| # | Criterion | Result | Evidence |
| --- | --- | --- | --- |
| 1 | Review PASS present | PASS | Review bead ga-ojymf7 is closed with `REVIEW VERDICT: PASS`; deploy bead description records reviewer PASS for `builder/ga-yxnz9x.3-stop-teardown`. |
| 2 | Acceptance criteria met | PASS | `cmdStopBody` stop teardown behavior is covered by focused tests for ordering, non-lifecycle providers, and non-fatal teardown errors. Changed paths are limited to `cmd/gc` and `internal/runtime`; no API, OpenAPI, event payload, or config schema files changed. |
| 3 | Tests pass | PASS | Focused cmd/gc stop tests passed; `go test -count=1 ./internal/runtime/tmux ./internal/runtime` passed; `make test-fast-parallel` passed (`All fast jobs passed`); `go vet ./...` passed. |
| 4 | No high-severity review findings open | PASS | Review notes for ga-ojymf7 record 0 blockers and 0 warnings. |
| 5 | Final branch is clean | PASS | `git status --short` was empty before writing this gate file. The branch will be rechecked after committing the gate file and before push. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree origin/main HEAD` exited 0 and produced tree `44dbc331c106251f9ecba95fcd27900599408d2e`. |
| 7 | Single feature theme | PASS | The commits form one tmux server lifecycle teardown stack: the `cmd/gc stop` teardown behavior depends on the runtime provider surface from the prerequisite commit. |

## Test Log

```text
$ go test -count=1 ./cmd/gc -run 'TestCmdStopBodyTeardownRunsAfterStopOrphansBeforeBeadsShutdown|TestCmdStopBodySkipsTeardownForNonLifecycleProvider|TestCmdStopBodyReportsTeardownErrorWithoutFailing|TestCmdStop'
ok  	github.com/gastownhall/gascity/cmd/gc	5.129s

$ go test -count=1 ./internal/runtime/tmux ./internal/runtime
ok  	github.com/gastownhall/gascity/internal/runtime/tmux	1.158s
ok  	github.com/gastownhall/gascity/internal/runtime	20.819s

$ make test-fast-parallel
[fsys-darwin-compile] ok
[unit-cmd-gc-2-of-6] ok
[unit-cmd-gc-4-of-6] ok
[unit-cmd-gc-3-of-6] ok
[unit-cmd-gc-5-of-6] ok
[unit-core] ok
[unit-cmd-gc-6-of-6] ok
[unit-cmd-gc-1-of-6] ok
All fast jobs passed

$ go vet ./...
# no output
```
