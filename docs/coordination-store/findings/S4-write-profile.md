# S4: Write-path & Consistency Profile of HQ Coordination State

**Spike:** `ga-aec8q.4`
**Date:** 2026-05-22
**Author:** gascity/deep-investigator
**Method:** Live measurement on the deployed mgmt dolt :28231 (`mysqladmin extended-status -r` over multiple 5s windows, 300-sample `SHOW FULL PROCESSLIST` write-query catch) + static analysis of `gascity:internal/beads/bdstore.go`, `internal/beads/caching_store_writes.go`, `internal/events/recorder.go`, `internal/session/{chat.go, manager.go}`, `internal/beads/flock.go`. Cross-references: S5 (durability), S6 (anti-requirements), S3 (read-profile), gm-qmk0og live-cpu-rootcause.

---

## Scope

Characterize the HQ coordination WRITE paths — bead mutations, event emission, mail-wisp creation, session-state updates, name-lock acquisition, order-tracking lifecycle, dependency edges, labels/comments. Per path: frequency, read:write ratio, and the **real** consistency need (atomic? ordered? last-write-wins-OK? lossy-OK? exactly-once?). Express as tech-agnostic consistency requirements.

---

## Aggregate write load (live, deployed `gc` binary 2026-05-21 11:15)

| Counter | Lifetime (~5h uptime at sample) | Per-sec average | Per-sec instantaneous (sampled 5s deltas) |
|---|---:|---:|---|
| `Com_insert` | 29,260 | ~1.6 | 0–4 (bursty, most windows 0) |
| `Com_update` | 10,000 | ~0.55 | 0–4 (bursty) |
| `Com_delete` | 1,039 | ~0.06 | 0 |
| `Com_replace` | 0 | — | — |
| `Com_commit` | 0 (auto-commit) | — | — |
| **Total writes** | **40,299** | **~2.2 / s avg** | bursty, most seconds 0 |
| (`Com_select` for comparison) | 10.56M | 586 / s | — |

**Lifetime read:write ratio ≈ 265 : 1.** Writes are **rare, fast (< 1 ms — never caught in 300 processlist samples), and bursty** (a session lifecycle transition or an order completion fires a small clump, then quiet for seconds). The store is read-saturated; the write path is not the bottleneck.

The write surface is dominated by **per-bead metadata patches** (UPDATE), not row creation. Lifetime insert/update ratio ≈ 2.9 ⇒ each new bead is updated ~3× on average before close.

---

## Write-path inventory

| # | Path | Frequency (live) | Shape | R:W ratio (this path) | Today's consistency model | Required loss tolerance |
|---|---|---|---|---|---|---|
| W1 | **Work-bead create** (task / molecule / step / convoy / order-tracking) | ~3 / min (events: 44 created / 894 s; mostly order-tracking ephemerals) | Single-row INSERT into `issues` or `wisps` + child inserts (labels) | ~100 : 1 (point-lookups + ready/list dwarf creates) | Per-record atomic; new id returned to creator (server-generated UUID); read-after-write in same process required. | At-most-once on crash (subprocess dies between Dolt write and CLI exit) — bounded; reconciler converges. |
| W2 | **Bead update** (status, metadata, label, assignee, title, notes) | ~19 / min (events: 278 updated / 894 s) | Single-row UPDATE on `issues` or `wisps` | ~50 : 1 vs per-bead point reads | Per-record atomic; LWW between concurrent writers (no optimistic concurrency observed). | At-most-once on crash (reconciler re-drives). |
| W3 | **`SetMetadataBatch`** — atomic multi-key metadata update on one bead | High on session lifecycle transitions; observed call sites in `session/chat.go:450`, `session/manager.go:549`, `api/handler_session_create.go:481`, `api/session_resolution.go:173` | UPDATE with multiple metadata key-value pairs applied as one logical change | ~5–10 : 1 vs per-session point reads | **Intra-record atomic across the batch.** All keys move together OR none do. Per-bead LWW between writers. *This is the only multi-key atomic contract that materially appears in production.* | At-most-once on crash. Critical case: state transition (`state` + `state_reason` + `held_until` + `instance_token` must be coherent post-restart). |
| W4 | **Bead close / reopen / delete** | ~3 / min (events: 42 closed / 894 s; mostly order-tracking auto-close) | Single-row UPDATE (`status=closed`) or DELETE | dominated by reads | Per-record atomic; idempotent re-close. | At-most-once. Crash-during-close → reconciler observes still-open bead on next tick. |
| W5 | **Event emission** (`gc event emit` / `FileRecorder.Record`) | ~33 / min from bd mutations + session lifecycle (`events.jsonl` rate ≈ 0.56 / s) | **`O_APPEND` write of one JSON line** to `events.jsonl`. NOT a store write — file-system. Bounded-wait flock for cross-process serialization. Gzip rotation in background goroutine on size threshold. | dominated by reads (each consumer tails) | **Per-line atomic** (POSIX O_APPEND ≤ PIPE_BUF guarantee). Ordering within a single writer's stream preserved. **Multi-writer interleaving order = arrival order**, not causal order. | Sub-millisecond loss window (in-process buffered, pre-kernel). Hard SIGKILL leaves rotation file leftover; auto-recovered on next open via legacy-archive migration path. |
| W6 | **Wisp / mail creation** (type=message, ephemeral=true) | RARE — events show ~0.13 / min `mail.sent`; vast majority of wisp churn is order-tracking, not mail | INSERT into `wisps` + child label rows | **~75,000 : 1** (mail polls vs sends) — the most read-skewed path in the system | Per-record atomic. | Best-effort: loss of a message in the create-and-poll race window is tolerable (S5). |
| W7 | **Dependency edge add / remove** (`bd dep add`, `bd dep remove`) | Rare — only 13 rows on `dependencies`, 0 on `wisp_dependencies` lifetime | INSERT or DELETE one row in `dependencies` / `wisp_dependencies` | dominated by reads (every `bd ready` traverses) | Per-edge atomic; no edge-order requirement. | At-most-once. |
| W8 | **Order-tracking bead lifecycle** | 1 create + 1 close per order fire ⇒ ~10 ops/min (40 fired + 39 completed / 894 s) | Two writes per order: ephemeral wisp INSERT at fire, status=closed UPDATE at completion | balanced — each order is ~1 read (cooldown) + 2 writes (W1 + W4) | Per-record atomic. **Exactly-once IS desired** (no duplicate fires) but **not enforced today** — it's at-most-once-extra-fire on crash (S5). Bound: ≤ 1 extra fire per affected order per crash. | At-most-once-extra-fire tolerable. |
| W9 | **Label add / remove** | Embedded in W1/W2/W4 — INSERT/DELETE rows in `labels` / `wisp_labels` | Single-row INSERT/DELETE | dominated by reads (hydration) | Per-row atomic. Set semantics — no row ordering required. | At-most-once. |
| W10 | **Comment add** (`wisp_comments` / `comments`) | Rare in live sample | Single-row INSERT | dominated by reads | Per-row atomic; ordering within bead is by `created_at` (soft sort, not txn-ordered). | At-most-once. |
| W11 | **Session name-lock acquire / release** | Per session create / claim | **`flock(LOCK_EX)` on `.gc/runtime/session-name-locks/*.lock`** — kernel-level; NOT a store write | not applicable | Kernel atomic; advisory; **ephemeral** (released on process death — by design). | All locks lost on crash, by design (reconciler re-establishes). |
| W12 | **Auxiliary file-system locks** (`dolt-server.lock`, `mcp_project_lock`, sourceworkflow) | Per acquiring operation | `flock(LOCK_EX)` on a file | not applicable | Same as W11. | Same as W11. |
| W13 | **`Purge` (bulk close-ephemeral by age)** | Per `wispGC` sweep | Batched closes (one CLI invocation per batch) | one-off | Atomic per closed bead; **batch atomicity NOT required** — the sweep tolerates partial completion (next sweep finishes the job). | All-or-nothing not required. |

> **Cross-record transactions.** `BdStore.Tx` exists (`bdstore.go:870`, minimal contract added 2026-05-22 via PR #2309). I found **no production call site** that requires multi-record atomicity — every observed write is a single-record mutation, even when a logical operation (e.g. session create) writes multiple records, because the reconciler converges across ticks (S5). Tx is plumbing for future use, not a current load-bearing requirement.

---

## Per-path consistency contracts (today's reality, stated tech-agnostically)

### W1 / W2 / W4 — work-bead create / update / close
- **Atomicity:** per-record. After commit, the writer's next read in the same process MUST see the new state.
- **Ordering:** none required across creators. Within one writer, sequential.
- **Concurrency:** **last-write-wins is acceptable.** No version vectors, no optimistic concurrency control, no CAS observed in production. Two writers patching the same bead's metadata serialize on the store; the second clobbers the first's overlapping keys.
- **Loss tolerance:** at-most-once on crash. The reconciler observes whatever the store has and re-drives. **The system is designed for nondeterministic idempotence (NDI)** — re-applying the same logical mutation is safe.

### W3 — `SetMetadataBatch`
- **Atomicity:** **intra-record across multiple keys.** This is the one cross-key atomic guarantee any path actually needs. If `state="draining"`, `state_reason="quarantine"`, and `quarantined_until=<ts>` must move together, the batch must commit them as one observable change to any subsequent reader.
- **Ordering, concurrency, loss tolerance:** same as W1/W2.

### W5 — event emission
- **Atomicity:** per-line, via POSIX `O_APPEND` (write ≤ PIPE_BUF). No store atomicity needed — `events.jsonl` is a file, not a store entity.
- **Ordering:** **per-writer FIFO preserved.** Cross-writer is arrival-order (interleaving of independent processes). The system does NOT depend on cross-process causal order.
- **Concurrency:** multi-writer via flock; readers tail without locking.
- **Loss tolerance:** sub-millisecond window for in-process unflushed events. Acceptable — events are an audit / animation channel, not a coordination primitive on the critical path.

### W6 — mail / wisp creation
- Same as W1, but on the ephemeral tier. **Loss in the create-and-poll race window is tolerable** by design (S5): the worst case is the recipient agent missing a single message; the system already tolerates this (agents are crash-safe and the sender can re-send / the user can re-issue the command).

### W8 — order-tracking lifecycle
- **Cooldown correctness wants exactly-once.** Not enforced today: a crash between fire and tracking-bead-create can cause one extra fire on next opportunity. The product accepts at-most-one-extra-fire per crash as the operational bound (S5).
- **Atomicity:** per-record. The fire and the close are two independent atomic operations.

### W11 / W12 — file-system flocks
- Not store writes. Kernel-level advisory locks, ephemeral. Replacement of the coordination store does not affect these.

### W13 — Purge / wispGC
- **Batch atomicity NOT required.** The sweep is naturally retry-safe; partial completion is fine.

---

## Tech-agnostic write-path requirements

| # | Requirement | Priority |
|---|---|---|
| WP-1 | **Per-record atomic create/update/delete**, p99 ≤ 5 ms at 25k records | P0 |
| WP-2 | **Server-generated stable ID returned to the writer on create** (today: UUID via Dolt `DEFAULT (uuid())`) | P0 |
| WP-3 | **Read-after-write within the same process** (writer's next read sees the write) | P0 |
| WP-4 | **Intra-record multi-field atomic write** — set N fields on one record as one observable change. (`SetMetadataBatch` semantics; load-bearing for session-state transitions.) | P0 |
| WP-5 | **Last-write-wins between concurrent writers** is sufficient. No optimistic concurrency / CAS / version vectors required. | P0 |
| WP-6 | **At-most-once on crash is tolerable for every observed write path.** The system is built for NDI (re-apply safe). **Cross-record atomicity is NOT required** anywhere. | P0 |
| WP-7 | **Two tiers — durable "main" + ephemeral "wisps-with-TTL" — with the SAME write API.** The TTL-purge path (W13) is non-atomic by design. | P0 |
| WP-8 | **Append-only event log with per-line atomicity and per-writer FIFO order**, multi-writer via OS-level serialization. (Today: `O_APPEND` + flock.) Cross-process causal order NOT required. | P0 |
| WP-9 | **Advisory ephemeral locks** (acquired-by-process, released by OS on process exit). NOT a store concern — kernel flock today suffices. | P0 |
| WP-10 | **Write throughput target: 10 ops/s sustained, 50 ops/s burst.** Headroom over current ~2.2 / s avg. | P1 |
| WP-11 | **Write latency: p99 ≤ 10 ms.** Bursts must not block reads (today's read storm masks this). | P1 |
| WP-12 | **NOT required**: multi-row transactions across different beads, snapshot isolation, MVCC, conflict-resolution policies beyond LWW, total order across writers, exactly-once delivery semantics (the system already tolerates at-most-one-extra). | — |

---

## Nonobvious findings & risks

1. **`Com_commit = 0` over the whole 5h sample.** All writes execute under auto-commit; no explicit transactions are issued by the bd client today. The `Tx` plumbing on the gascity side (bdstore.go:870, 2026-05-22) is not yet used in production hot paths. A replacement does NOT need to support multi-statement transactions to handle today's workload — but the contract WP-4 (intra-record multi-field atomic) must be preserved as a primitive.

2. **No INSERT/UPDATE/DELETE caught in 300 processlist samples.** Writes are sub-millisecond and rare enough that the busy reader-storm masks them entirely. The write path is **not** a performance hotspot today — implication: a replacement can prioritize read-path properties (S3) and a write path with even modest throughput (~50 ops/s p99 ≤ 10 ms) is more than enough.

3. **Insert/update ratio ≈ 1:0.34 ⇒ each new bead is updated ~3× before close.** This matches the lifecycle: create → claim → status changes → close. Storage layout should make UPDATE on the same row cheap (no rewrite-everything) — a KV with append-only journaling and periodic compaction handles this naturally; an LSM tree handles this naturally; Dolt today rewrites commit graphs and accumulates history nobody reads (the S6 overkill).

4. **The "exactly-once" gap on order-tracking** is real but tolerable today (S5, W8). If a replacement made exactly-once cheap (e.g. via a write-ahead intent), it would close the gap. Not a requirement — but a "free if cheap" candidate.

5. **Mail tier is heavy READ, vanishing WRITE.** Mail-tier R:W ratio ≈ 75,000:1 (S3 R1 = ~150 reads/s; live mail-send rate = ~0.13/min). This means **caching the mail tier with a relaxed reconcile cadence is essentially free on the write side** — sends are so rare that even synchronous cache invalidation per send is trivially cheap. This collapses the design space for fixing R1 (the dominant CPU): a replacement / cache extension on the mail tier has effectively no write-side budget to worry about.

6. **Session-state write hot path is well-bounded.** SetMetadataBatch is called on session lifecycle edges (create, drain, name-bind, transport-bind, etc.), not on every reconcile tick. The reconciler reads state every tick but writes only on transitions. Throughput requirement is modest; latency requirement is tight (a write must be visible before the next tick re-reads, ≤ 5 s).

7. **The deployed bd CLI does not use the `Tx` contract** even though it exists. Adding multi-record atomicity to the workload would be a NEW capability, not a preserved one. The replacement evaluation should treat `Tx` as out-of-scope.

8. **Closed-bead UPDATE is the largest write subtype** (∼21k closed tasks; each was updated to status=closed at some point). Once closed, beads are write-frozen but read forever. A replacement can move closed records to a colder physical tier (read-only, compacted) without breaking any contract.

---

## Summary

HQ coordination WRITES are **rare, fast, per-record, LWW, and crash-tolerant**. The only non-trivial consistency contract is **intra-record multi-field atomic writes** (`SetMetadataBatch`) for session-state transitions. Cross-record transactions, snapshot isolation, exactly-once semantics, and conflict resolution beyond LWW are **not used in production today** and need not be supported by a replacement. The write surface is so light (~2.2 ops/s lifetime average) that any replacement satisfying WP-1..WP-9 with modest throughput (WP-10/11) will be over-provisioned.

A right-sized replacement only needs the consistency primitives in WP-1 through WP-9; none of the Dolt-specific superpowers (multi-row transactions, branch/merge, full SQL DDL, sync) appear on the write path.
