# S5: Durability & Restart-Resilience Matrix

**Spike:** `ga-aec8q.5`  
**Date:** 2026-05-22  
**Author:** gascity/architect  
**Method:** Static code analysis — `internal/beads/`, `internal/session/`, `internal/events/`, `cmd/gc/session_reconciler.go`, `cmd/gc/wisp_gc.go`, `cmd/gc/order_dispatch.go`, `internal/citylayout/`, `internal/sessionlog/`

---

## Scope

"gc restarts but continues" — what coordination state must survive a controller crash mid-operation so the city can resume without manual intervention?

---

## Entity Durability Matrix

| Entity | Must Survive Restart? | Crash Consistency Need | Recovery Time | Tolerable Loss |
|---|---|---|---|---|
| **Work Beads** (tasks, epics, step beads, convoy members) | **YES — hard requirement** | Strong write durability per bead; no cross-bead atomicity required | Seconds — CachingStore.PrimeActive() does one `bd list` sweep on startup | In-flight `bd create`/`bd close` at crash time (at-most-once risk for that mutation; bead may not appear) |
| **Session Beads** (type:session, with state metadata) | **YES — hard requirement** | Strong write durability; state + metadata (state, state_reason, held_until, quarantined_until, instance_token) must be readable on next reconcile tick | 30–60s — reconciler first tick after restart | Session bead written between two bd writes (e.g. state set to "creating", gc crashes before updating metadata) — reconciler will converge on re-observation |
| **Molecule Root + Step Beads** | **YES — hard requirement** | Same as work beads; step beads must be readable to resume mid-molecule | Seconds (PrimeActive) | Same at-most-once risk as work beads |
| **Convoy Beads** | **YES — hard requirement** | Same as work beads; convoy bead + member links (via metadata) must survive | Seconds | Same at-most-once risk |
| **Mail Messages / Wisps** (type:message, Ephemeral=true) | **YES — required for agent work assignment** | Best-effort; losing a message in the window between `bd create` and first inbox poll is tolerable | Seconds — next `bd query ephemeral=true type=message` poll | Message created but not committed by the `bd create` process at crash time |
| **Order-Tracking Beads** (label: order-tracking) | **YES — required for cooldown correctness** | Moderate — must exist post-crash to prevent duplicate order firings within cooldown window; swept by wispGC after TTL | Seconds (PrimeActive) | Tracking bead being created at crash time → order may fire twice at next opportunity (one extra fire, bounded) |
| **Events (events.jsonl + archives)** | **YES — audit log durability required** | Crash-safe by design: O_APPEND writes, bounded-wait flock, gzip rotation in goroutine | Immediate — no recovery; readers resume tailing from last known offset | Events buffered in-process after flock acquisition but before kernel O_APPEND completes (sub-millisecond window, OS-level) |
| **Session Logs** (JSONL transcript files) | **YES — required for conversation resume** | Crash-safe by design: O_APPEND pattern, file rotation | Immediate — sessions resume reading from last entry | Last partial entry at crash (truncated JSON) |
| **Session Name Lock Files** (.gc/runtime/session-name-locks/*.lock) | **NO — ephemeral** | None — advisory kernel flock; released by OS on process death | Zero — no recovery needed | All locks; kernel reclaims on gc exit |
| **In-Memory CachingStore** | **NO — ephemeral** | None — pure cache | Seconds — rebuilt from backing BdStore via PrimeActive on startup | Entire cache |
| **Name Reservation Mutexes** (in-process sync.Mutex map) | **NO — ephemeral** | None — in-process only | Zero — no recovery needed | Entire map |
| **Dolt Server State Files** (.beads/dolt-server.pid, dolt-server.port, dolt-state.json) | **NO — stale on crash** | Advisory only; gc probes live process table on startup | Seconds — dolt_recover_managed.go reads and reconciles on startup | Stale state (expected and handled) |

---

## What "gc restarts but continues" requires (load-bearing state)

The minimum set that must be durable for a city to resume automatically:

1. **All non-closed session beads with state metadata** — the reconciler derives desired-vs-actual state from these on its first tick post-restart. Without them, sessions that were running are never restarted.

2. **All non-closed work beads (tasks, molecules, step beads)** — agents find their work by querying open beads. Lost work beads mean lost work.

3. **All unread mail messages (wisps)** — unread messages ARE the work assignment for the recipient agent. Loss means the agent is never woken with its task.

4. **Order-tracking beads for orders within their cooldown window** — without them, orders fire again on next controller restart, possibly causing duplicate dispatches.

---

## Crash-consistency per entity

### Session Beads
The reconciler is designed for this. On gc start:
1. It calls `store.List(all active session beads)` via CachingStore.PrimeActive().
2. For each bead, it projects `BaseState` from metadata (`state`, `state_reason`).
3. It then drives lifecycle toward `DesiredState` from config.

The system handles mid-operation states gracefully:
- `BaseStateCreating` + runtime missing → reconciler detects stale-creating (grace window), then force-closes or re-creates.
- `BaseStateDraining` + runtime missing → reconciler progresses drain.
- `BaseStateActive` + runtime missing → reconciler re-creates session.

**Consistency requirement: each individual bead write must be atomic and visible after restart.** Cross-bead atomicity is NOT required — the reconciler converges.

### Work Beads / Molecules
No cross-bead atomicity needed. If gc crashes mid-molecule:
- Completed steps are closed (visible after restart).
- The current step bead is open (reconciler / agent re-finds it and continues).
- The root molecule bead tracks overall state via child step status.

**Consistency requirement: individual bead write atomicity.**

### Mail / Wisps
Mail uses the `ephemeral=true` (wisps) tier — a separate Dolt table with TTL GC. Unread messages have `status=open`. On startup, the recipient's next inbox check (`bd query ephemeral=true type=message assignee=X status=open`) returns them.

**Consistency requirement: individual bead write atomicity.** Loss window: `bd create` subprocess dies between Dolt write and CLI exit — rare, bounded to the in-flight message.

### Events
`FileRecorder` uses `O_APPEND` for kernel-level write atomicity (POSIX guarantee on local FS for writes ≤ PIPE_BUF). Each event is a complete JSON line. On crash:
- Events already `O_APPEND`-flushed to kernel are durable.
- Events in-process but not yet written are lost (sub-millisecond window at most).

**Consistency requirement: O_APPEND atomicity (already met).**

### Order Tracking Beads
Created as real beads with `order-tracking` label when an order fires. Swept by wispGC after configurable TTL. If gc crashes before the tracking bead is created:
- Next controller restart evaluates the order again.
- The cooldown check finds no tracking bead → order fires again.
- This is an at-most-once-extra-fire risk, not an unbounded loop.

**Consistency requirement: individual bead write atomicity.**

---

## Restart-recovery time targets (tech-agnostic)

| Phase | Entity | Target Recovery Time |
|---|---|---|
| T+0s | gc process starts, Dolt/store comes up | 0–5s |
| T+5s | CachingStore.PrimeActive() completes (one full `bd list` sweep) | 5–30s (depends on bead count; at 1000 open beads: ~10s; at 10k: degrades) |
| T+30s | First reconciler tick: session beads read, desired state computed, stale sessions flagged | 30–60s |
| T+60s | Session re-create or drain-completion in flight | 60–120s |
| T+120s | City fully resumed, all agents awake | 120s |

**This is today's reality with Dolt.** A faster persistence layer could cut T+5s → T+0.5s (no fork per bd call) and T+30s → T+5s (direct in-process store queries).

---

## What can safely be in-memory only (zero durability)

- CachingStore (warm cache of open beads) — rebuilt from backing store in seconds.
- Session name/alias lock files — advisory flock, kernel-reclaimed on crash.
- Name reservation mutexes — in-process, rebuilt as sessions are established.
- Dolt server PID/port files — advisory, re-probed on startup.
- The `instance_token` for in-flight session incarnations — lost on crash is fine; reconciler generates a new token on next create.

---

## Risks and residual gaps

1. **At-most-once-extra mutation on crash during bd write (~30-80ms fork window):** Any `bd create` or `bd close` that loses its subprocess mid-flight leaves the bead in the pre-write state. The reconciler converges on the pre-write state and re-drives. This is a known NDI property (nondeterministic idempotence), not a bug.

2. **CachingStore PrimeActive latency scales with bead count:** At 23k+ beads (the current volume including closed), `bd list --all` is slow. PrimeActive only reads non-closed beads, so open-bead count is the relevant metric. But as the closed-bead table bloats (see S6), even open-bead `bd list` degrades because Dolt scans the full commit graph.

3. **Order tracking bead TTL is not currently enforced at startup for cross-restart deduplication:** If the TTL expires before the tracking bead is swept, and gc restarts in that window, the order may double-fire. This is a narrow edge but real.

4. **Events rotation goroutines drain on Close() but not on kill:** A hard `SIGKILL` leaves in-flight gzip+rename rotation goroutines incomplete. The archived log is intact; the in-flight rotation file survives as a `.rotating-*` leftover and must be manually recovered or is picked up on next open via the archive migration path (`feat(events): migrate legacy archives on recorder open` — merged 2026-05-22).

---

## Summary: durability requirements (tech-agnostic)

A replacement persistence layer for HQ coordination state must provide:

| Requirement | Priority |
|---|---|
| Individual write atomicity (per-record) | P0 |
| Reads-after-write consistency (same process, next query sees the write) | P0 |
| Durability: writes survive process crash and OS restart | P0 |
| Two-tier storage: durable "main" tier + ephemeral "wisps" tier with TTL | P1 |
| Restart-recovery time: full open-bead catalog readable in < 5s at 10k records | P1 |
| Cross-record atomicity (transactions): NOT required by current usage | — |
| Cross-node replication: NOT required (local-only, gascity-scoped) | — |
| History / time-travel: NOT required | — |
