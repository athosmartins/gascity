# HQStore — Round-3 Build Plan & Log

> **Status:** Build APPROVED (operator, 2026-05-23). Building **HQStore** (author-own,
> in-process coordination-state store) to replace beads/Dolt for HQ. This document is the
> canonical build plan + running log — kept current so we don't forget and can explain it
> to others. Pairs with `discovery.md` (the *why* + requirements + the Round-2 recommendation).
>
> **Branch:** all work lives on `experiment/coordination-store`. **No public PRs** for the
> migration itself until cut-over is shipped + documented. (Exception: the benchmark harness
> may go up as a *draft* PR to show methodology — see Stream 2.)

## Why this is low-risk despite being a "cut-over"

HQStore lands as a **new, dormant, flag-gated backend alongside Dolt** (the R2.3 "provider
swap" model). Almost all work ships as **additive PRs that don't change the running city's
behavior**; the risk concentrates in a single **small, reversible cut-over flip** at the end,
which we test on an isolated **test city** first.

## Decisions (locked)

| # | Decision | Date | Source |
|---|---|---|---|
| D-A | Build HQStore (author-own), not adopt. authorcore 9/9 @282µs; SQLite 8/9 (WAL floor + CGo); Dolt 1/9. | 2026-05-23 | R2.4 + operator |
| D-B | Bridge fixes (FK cascades + mail auto-archive) folded into the migration import, not pre-patched on Dolt. | 2026-05-22 | operator |
| D-C | Round-2 scope HQ-only; rig DBs evaluated separately later. | 2026-05-22 | operator |
| D-D | Development isolated on a **separate test city on this host** (standalone-managed, own dolt port + minimal agents). Not container/VM unless insufficient. | 2026-05-23 | operator |
| D-E | Benchmark harness exposed as a **draft** PR (methodology); no merge pressure. | 2026-05-23 | operator |

## Streams

### Stream 1 — Test city (the sandbox) — FIRST
A second gc city on this host: own directory, **standalone-managed** controller (NOT the
systemd supervisor), own dolt server on a **distinct port** (target 28240; avoid 28231 live /
28232 the recent rogue), **minimal** agent set (enough to generate sessions/wisps/beads load),
seeded with representative data so we can exercise migration + cut-over against realistic state.
All risky work (shadow mode, migration, the flip) happens here; the live city is never at risk.
Open question: exact gc multi-city mechanics (clean standalone init, port/dir isolation, a
lightweight agent+auth setup that doesn't entangle with live).

### Stream 2 — Draft benchmark PR (PR #1) — in parallel
Put `internal/benchmarks/coordstore` (harness + adapters) + the R2.1/R2.1b results on a
clean-building branch; open as **draft**. Shows methodology; low-risk (tooling only).
Caveat: the harness currently builds on the architect's branch base, not `main` — needs a
branch where it compiles.

### Stream 3 — Phased HQStore build (dispatched once the test city is up)
Per R2.4/R2.3, each step additive/dormant until the last:
1. HQStore core (`internal/beads/hqstore_core.go`) — registered provider, NOT default.
2. WAL (`hqstore_wal.go`) · 3. Checkpoint/recovery (`hqstore_checkpoint.go`) · 4. TTL sweeper
   (`hqstore_ttl.go`) · 5. Lifecycle (`hqstore.go`) · 6. Tests (SIGKILL/partial-line/concurrency).
   **Gate:** harness scores 9/9 with HQStore adapter.
7. `gc bd-store-bridge` shim + PATH shadow (flag-gated).
8. Migration importer (Dolt→HQStore) — folds in D-B bridge fixes.
9. Shadow mode (parallel, diff vs Dolt, not serving).
10. **Cut-over PR** — single provider flip; ≤60s; Dolt backup hot; 48h rollback. The only cut-over.

## Documentation discipline (operator directive 2026-05-23)
- This doc (`round3-build-plan.md`) = canonical plan + the **Build Log** below (append per milestone).
- `discovery.md` = the requirements + recommendation (the *why*).
- Each spike documents its work in a doc/PR description; PRs explain themselves.
- Beads epic `ga-aec8q` + round-3 spikes = the trackable work record.

## Build Log
- **2026-05-23** — Build approved; test-city isolation chosen (D-D); plan doc created. Streams 1+2 kicking off; round-3 spikes being laid down. (mayor)

## Open questions
1. ~~Test-city data seed~~ **Resolved:** synthetic seed chosen (safer/cleaner; live data copy option documented in round3-test-city.md for Phase 4 cut-over). (R3.1)
2. ~~Minimal agent set~~ **Resolved:** dog (×2) + claude (×1) + control-dispatcher (always-on). All on-demand; generates session/wisp/bead/formula entities on activation. (R3.1)
3. Harness clean-build branch for the draft PR (rebase onto a base where it compiles). **Resolved:** PR #2524 opened. (R3.2)

## Constraints / gotchas (don't forget)
- **`/tmp` is RAM-backed** on this host (tmpfs) — cleared on restart. ALL durable artifacts (the test city + its dolt data, docs, git worktrees, configs) MUST live on durable disk (`/home/jaword/...`), never `/tmp`. (Learned 2026-05-23: the experiment worktree was briefly created under /tmp; relocated to /home/jaword/projects/gascity-coordstore-wt. Committed git history was safe regardless — objects live in the repo's .git on disk.)

## Build Log (cont.)
- **2026-05-23** — Streams 1+2 dispatched: R3.1 test city → gascity/builder (DURABLE dir, port ~28240, minimal agents); R3.2 draft benchmark PR → gascity/architect. R3.3 (dormant HQStore build) + R3.4 (migration/cut-over) created, HELD until test city ready. Experiment worktree relocated off /tmp → /home/jaword/projects/gascity-coordstore-wt. (mayor)

> **Clarification (operator 2026-05-23):** /tmp is FINE to use — faster, less btrfs churn — for transient/high-churn working data. The rule is only: don't make /tmp the *sole source of truth*. Durability comes from git commits (durable in .git) / a durable copy. The earlier "never /tmp" framing above was over-strong. Test-city runtime (dolt data) may live on /tmp for speed if the SETUP is scripted/reproducible.
- **2026-05-23** — Stream 2 complete: draft benchmark PR opened as [PR #2524](https://github.com/gastownhall/gascity/pull/2524) (`benchmarks/coordstore-harness` branch, rebased onto main). Harness + 6-backend adapter sweep + R2.1b results doc included. Smoke suite passes on all 3 in-process backends. (gascity/architect)
- **2026-05-23** — R3.2 DONE: draft benchmark PR opened — gastownhall/gascity#2524 (methodology + 6-backend results exposed; branch benchmarks/coordstore-harness off main). Stream 2 complete. (architect)
- **2026-05-23** — R3.1 DONE: HQStore test city standing at `/home/jaword/gctest`. Supervisor-managed (PID 1883575), dolt port **28533** (hash of city path). Agents: dog (×2, on-demand), claude (×1, on-demand), control-dispatcher (always-on). Beads store initialized with 9 seed entities covering all coordination-state types (open/in-progress/closed beads, parent-child hierarchy, message wisp). Docs: `docs/coordination-store/round3-test-city.md`. Unblocks R3.3 (dormant HQStore build) + R3.4 (migration/cut-over). (architect)
- **2026-05-23** — R3.1 DONE: test city up at `/home/jaword/gctest` (dolt 28533, isolated, durable disk). Doc: round3-test-city.md (reproduce-from-scratch). NOTE: architect made it SUPERVISOR-managed (shared machine supervisor) — flagged to confirm robust per-city isolation before the R3.4 cut-over (else switch to standalone). Also flagged a live-dolt-port ambiguity (doc says 28232; gascity bd uses 28231 — ties to the MCD bd breakage + a rogue 28232 server). (architect→mayor)
- **2026-05-23** — R3.2 DONE: draft benchmark PR gastownhall/gascity#2524. Its red CI = inherited gascity main-CI (integration/required) failures, NOT the harness (which passes 9/9). (architect)
- **2026-05-23** — R3.3 (dormant HQStore build) DISPATCHED → gascity/builder: build on test city /home/jaword/gctest:28533, per R2.2, gated at harness 9/9, additive/dormant (live keeps Dolt), no cut-over. R3.4 (migration+cut-over) HELD until the 9/9 gate. (mayor)
