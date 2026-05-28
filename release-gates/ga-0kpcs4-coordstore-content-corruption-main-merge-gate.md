# Release gate: ga-0kpcs4 coordstore content-corruption main merge

Evaluated: 2026-05-28T15:46:34Z

## Scope

- Deploy bead: `ga-0kpcs4` - Review: Coordstore content-corruption main merge resolution
- Source bead: `ga-aec8q.22` - Coordstore content-corruption gate
- Reviewed branch: `builder/ga-9m5wfz-merge-main`
- Evaluated commit: `302fa920e`
- Current `origin/main`: `3203b502f`
- Merge base with `origin/main`: `0f50effe7`

The `docs/PROJECT_MANIFEST.md` file referenced by the deployer prompt is not
present in this checkout, so this gate uses the six release criteria from the
active deployer instructions.

## Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | `ga-0kpcs4` notes contain `REVIEW VERDICT: pass` for `builder/ga-9m5wfz-merge-main @ 302fa920e`. |
| 2 | Acceptance criteria met | PASS | Source scope covered by tests: content-corruption detection in `internal/benchmarks/coordstore/...`, SQLite sequence recovery tests, `UpdatedAt` domain/API/dashboard propagation, and the merge-resolution test surface in `internal/beads/filestore_test.go`. |
| 3 | Tests pass | PASS | `go test ./internal/beads -count=1`; `go test ./internal/api ./internal/api/genclient -count=1`; `go test ./internal/benchmarks/coordstore/... -count=1`; `make test-fast-parallel`; `go vet ./...`; `make dashboard-check`. All passed with Dolt 2.0.7 first in `PATH`. |
| 4 | No high-severity review findings open | PASS | `ga-0kpcs4` review notes list one INFO finding and no HIGH findings. Source review notes on `ga-9m5wfz` list only LOW findings. |
| 5 | Final branch is clean | PASS | `git status --short` was empty before adding this release-gate file; this file is the only deployer change and is committed with the gate. |
| 6 | Branch diverges cleanly from main | PASS | `git merge-tree --write-tree HEAD origin/main` exited 0 and produced tree `afa20f591b66a7cb3049c64ac7e525d9b463662f`; no merge conflict was reported against current `origin/main`. |

## Acceptance Evidence

- `go test ./internal/beads -count=1` passed.
- `go test ./internal/api ./internal/api/genclient -count=1` passed.
- `go test ./internal/benchmarks/coordstore/... -count=1` passed.
- `make dashboard-check` passed, including generated client/schema output,
  dashboard build, TypeScript typecheck, and dashboard Go tests.
- `make test-fast-parallel` passed all fast shards.

## Advisory Checks

- `git diff --check origin/main...HEAD` reported trailing whitespace in
  previously authored coordination-store markdown files. This is not one of
  the six release criteria and was not introduced by the deployer gate commit.
