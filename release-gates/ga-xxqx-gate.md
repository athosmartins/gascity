# Release gate - supervisor drift auto-restart (ga-xb45.1 / ga-xxqx)

**Verdict:** PASS

- Deploy bead: `ga-xb45.1`
- Source bead: `ga-xxqx` (closed)
- Branch: `quad341:builder/ga-xxqx-1`
- HEAD: `030f1578c` (`feat(cmd/gc): wire supervisor drift detection into gc start`)
- Diff: 33 files, +1729 / -37
- Project manifest: `docs/PROJECT_MANIFEST.md` is not present in this checkout; gate uses the deployer prompt's release criteria.

## Criteria

| # | Criterion | Verdict | Evidence |
|---|-----------|---------|----------|
| 1 | Reviewer PASS verdict in bead notes | PASS | `gascity/reviewer` verdict is PASS on the rebuilt branch; branch status note says current `origin/main` is the merge-base. |
| 2 | Acceptance criteria met | PASS | The branch exposes `build_id` on `/health`, adds the supervisor health client/restart helpers, wires drift detection and auto-restart into `gc start`, adds `--no-auto-restart`, regenerates OpenAPI/client/dashboard schema artifacts, and covers the flag/outcome, restart, polling, health, and config behavior with focused tests. |
| 3 | Tests pass on final branch | PASS | Deployer re-ran focused drift/API/config/docsync tests, `go vet ./...`, `make dashboard-check`, dashboard preview smoke, and `make test-fast-parallel`; all passed. |
| 4 | No high-severity review findings open | PASS | Review notes list INFO findings only; unresolved HIGH count is 0. |
| 5 | Final branch is clean | PASS | `git status --short --branch` was clean before this gate file was added. |
| 6 | Branch diverges cleanly from main | PASS | `origin/main` is an ancestor of `HEAD`; branch is 4 ahead / 0 behind. |

## Validation

- `go test ./cmd/gc -run 'Test(DecideDriftAction|PrintSupervisorIdentity|PrintDriftReport|RestartSupervisor_|HTTPSupervisorClient_|PollReady)' -count=1` - PASS
- `go test ./internal/api -run 'TestSupervisorHealth|TestOpenAPISpecInSync|TestOrderResponseSchemaKeepsMigrationFieldsOptional' -count=1` - PASS
- `go test ./internal/config -run TestDaemonAutoRestartOnDrift -count=1` - PASS
- `go test ./test/docsync -run TestSchemaFreshness -count=1` - PASS
- `go vet ./...` - PASS
- `make dashboard-check` - PASS
- `npm run preview -- --host 127.0.0.1 --port 49632`; `curl -fsS http://127.0.0.1:49632/ | wc -c` returned `22773`
- `make test-fast-parallel` - PASS
- `git config core.hooksPath` - `.githooks`

## Push target

Pushing to fork (`quad341/gascity`) unless `origin` accepts the deploy push dry-run; PR head will match the selected push target.
