# S3: Read-path & Query-shape Profile of HQ Coordination State

**Spike:** `ga-aec8q.3`
**Date:** 2026-05-22
**Author:** gascity/deep-investigator
**Method:** Live measurement on the deployed mgmt dolt :28231 (`mysqladmin extended-status -r`, 120-sample `SHOW FULL PROCESSLIST` tally, `ss`/proc-FS for client attribution) + static analysis of `gascity:internal/beads/bdstore.go` and `internal/beads/caching_store.go`. Cross-references: `gm-qmk0og` CPU root cause + `.gc/agents/deep-investigator/artifacts/gm-qmk0og-reaper-split/live-cpu-rootcause.md`, sibling S5 (durability), sibling S6 (anti-requirements).

---

## Scope

Characterize HQ's hot READ paths against the coordination store. Per path: frequency, query shape (point / filter / range / full-scan), latency sensitivity, current cost. Express the result as **tech-agnostic access-pattern requirements** — a replacement store must satisfy these, however it's implemented.

---

## Aggregate read load (live, deployed `gc` binary 2026-05-21 11:15)

Measured on :28231 (the live mgmt dolt sql-server hosting all five rig DBs incl. hq):

| Metric | Live (3s delta) | Lifetime (~5h uptime at sample) |
|---|---|---|
| New connections | **~143 / sec** | 2,831,309 |
| `Com_select` | **~586 / sec** | 10,559,990 |
| `Com_insert` + `Com_update` + `Com_delete` | **0 / 3s window** | 28,788 + 9,862 + 1,035 = 39,685 |
| Total `Questions` | ~1,181 / sec | 23,463,256 |
| `Slow_queries` | 0 / 3s | 3 lifetime |
| `Threads_running` | 1–2 instantaneous | — |

**Lifetime read:write ratio ≈ 265 : 1.** The store is overwhelmingly read-dominated. CPU at sample = 86–90 % of one core; `processlist` simultaneously empty of slow/stuck queries — pure volume, dominated by short fast reads.

### Workload volume (hq today)
| Table | Rows | Distribution |
|---|---|---|
| `issues` | 23,360 | task 21,417 (96 % closed) · session 164 (47 open) · convoy 28 (22 open) · message 1,677 (all closed) · molecule 11 open · misc 81 |
| `wisps` (ephemeral tier) | 6,378 | message **2,365 open** + 417 closed · task 3,596 closed (order-tracking remnants) |
| `wisp_labels` | 23,014 | ~3.7 labels per wisp |
| `labels` | 447 | issues-tier labels (sparse) |
| `dependencies` | 13 | almost unused on issues tier |
| `wisp_dependencies` | 0 | unused on wisps tier |

The volume that read paths actually scan is bounded: ~200 open issues + ~2,400 open message wisps + ~3,600 closed tracking wisps = ~6.2k "active-shaped" rows; the other ~21k closed issues are inert weight that still costs per scan (see S6 for the anti-requirement on history).

---

## Hot read-path inventory

| # | Path | Frequency (live, today) | Query shape | Latency sensitivity | Current cost | Cite |
|---|---|---|---|---|---|---|
| R1 | **Per-agent mail/inbox poll** | ~150 invocations/s aggregated across all agents (the dominant driver — see gm-qmk0og) | **Filter scan over ephemeral tier** (`ephemeral=true AND status='open' AND issue_type='message' [AND assignee=X]`). DOLT plan: full partition-scan of `wisps` (≈6.4k rows) + `SELECT DISTINCT` over wide column set | **Medium** — must return new mail within ≤ a few seconds so agents don't starve | One fresh `bd` subprocess + new dolt connection + ~8 queries (handshake + scan + label/comment/dep hydration) per invocation. ~$0.6/CPU-sec sustained. | `bdstore.go:1409 listEphemeral` ← `bdstore.go:1338 List` |
| R2 | **Agent work-finding** | Per-agent each reconcile tick (30–120 s adaptive, FNV-staggered per agent) | **Filter scan**: `bd ready --json` ⇒ open beads with no unresolved blocking deps, optionally filtered by `--assignee` / `--label` / `--metadata-field gc.routed_to=…` | **Medium** — slack of one tick acceptable | `bd ready` subprocess + connection + filter scan over open `issues` (~200 rows) + dep edge join | `bdstore.go:1596 Ready` |
| R3 | **CachingStore PrimeActive / reconcile sweep** | One full sweep on gc startup + one per `cacheReconcileInterval{Small=30s, Medium=60s, Large=120s}` per rig, sized by open-bead count | **Range scan** over open beads via `bd list --json --status=open --all` | **Latency-tolerant** (background) but throughput-bounded — at 10k+ open beads sweep cost dominates startup | One `bd` subprocess + connection + full scan of open issues; cache miss path also exercises R5 | `bdstore.go:1331 List`; `caching_store.go:118–123` |
| R4 | **Per-bead hydration after list** | Batched as `IN (?, ?, …)` of 16–32 ids, fires once per list/query result page | **Range-by-id-set** (5 forms observed): `wisp_labels WHERE issue_id IN (…)`, `wisp_comments` count + body, `wisp_dependencies` count + edges, `labels WHERE issue_id IN (…)`, `SELECT 1 FROM wisps WHERE id IN (…)` | **Coupled to R1/R2/R3** — must return in the same tick | Index-fast per IN list given the right index; observed ~10–30× per sampling window in the 120-sample tally | `bdstore.go` hydration helpers around the `List`/`listEphemeral`/`Get` paths |
| R5 | **Point lookup (`bd show <id>` / `Get`)** | High volume on cache miss + on dependency walks + on every gc agent peek | **Point read** by primary key + scattered hydration | **Low** — agents block on this | `bd show` subprocess + connection + `SELECT … FROM issues WHERE id=?`; observed shapes incl. `SELECT 1 FROM wisps WHERE id=? LIMIT 1` (existence probe), `SELECT label FROM labels WHERE issue_id=? ORDER BY label` (per-bead labels) | `bdstore.go:684 Get` |
| R6 | **Dependency graph traversal** | Per-bead on `bd ready` and on molecule/convoy resolution | **Range-by-set + recursion**: `SELECT issue_id, type, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) FROM dependencies WHERE …` (or `wisp_dependencies`) | **Medium** — ready evaluation must be tick-fast | Cheap today (13 rows on issues, 0 on wisps) but the query is the split-column COALESCE pattern (resolved by PR #2399) — cost scales with active dep edges, not store size | `bdstore.go:1667 DepList`, `1708 DepListBatch` (`bd dep list --json`) |
| R7 | **Session-bead state read** | Once per session per reconcile tick (≈ N_sessions × interval⁻¹) | Served from CachingStore in steady state ⇒ in-memory point reads; first-tick post-restart hits the store via R3 | **Medium** | In-memory on the hot path; ~one R3 sweep at start | `cmd/gc/session_reconcile.go:210,235` iterates sessions over cached prime |
| R8 | **Order-tracking lookup (cooldown check)** | Per scheduled order evaluation (~2.7 orders/min observed in events.jsonl) | **Filter scan** of recent `wisps` with `label=order-tracking` + `metadata gc.order=<name>` | **Low** | One `bd query` per evaluation; result set tiny | implicit in order dispatch path |
| R9 | **Event log tail** | One tailer per active reader (orders, watchers, deacon) | **Sequential file read**, NOT a store query — `O_RDONLY` over `events.jsonl` + rotated archives | **Medium** | Near-zero per read (kernel page cache); does not load :28231 | `internal/events/` recorder; readers in patrol/order code |
| R10 | **Session name-lock check** | Per session create / claim | **File-system flock**, NOT a store query — `flock` on `.gc/runtime/session-name-locks/*.lock` | **Low** | Kernel-level; no store I/O | (see S5) |
| R11 | **Connection-handshake & TX-control noise (artefact of fork-per-CLI)** | 1:1 with `bd` invocations ≈ ~150 / s | `SELECT @@max_allowed_packet`, `SHOW DATABASES`, `START TRANSACTION`, `SET @@SESSION.dolt_author_*`, `ROLLBACK`, `SELECT value FROM config WHERE key=?`, `SELECT value FROM metadata WHERE key=?` | — | Pure overhead — every short-lived bd process repeats this | bd CLI session init |

> **Out of band** — 120 / 206 active-query samples on :28231 were `call dolt_fetch(?, 'main')` (continuous, parameterised). hq has no remote, but the shared dolt server hosts gascity which does — almost certainly an order-/patrol-driven remote fetch loop on the gascity DB at :28231. **Not a coordination-state read path**; flagging here so the next iteration triages it separately (likely a `dolt-remotes-patrol` cadence issue).

---

## Observed query-shape distribution (120-sample `SHOW FULL PROCESSLIST` on :28231)

Tally of caught running `Query` rows (parameter-normalized, deduplicated):

| Caught | Query shape (truncated) | Class |
|---:|---|---|
| 120 | `call dolt_fetch(?, 'main')` | infra (out of scope) |
| 22 | `SELECT DISTINCT id, content_hash, title, description, design, acceptance_criteria, notes, …` | **full-scan + DISTINCT (R1)** |
| 10 | `SELECT issue_id, label FROM wisp_labels WHERE issue_id IN (?,?,…)` | range-by-id-set (R4) |
| 8 | `SELECT @@max_allowed_packet` | handshake (R11) |
| 5 | `SELECT id, content_hash, title, … FROM …` | point/filter read (R5/R3) |
| 5 | `SELECT id FROM wisps WHERE id IN (?,?,…)` | existence batch (R4) |
| 5 | `SELECT 1 FROM wisps WHERE id=? LIMIT 1` | point existence (R5) |
| 4 | `START TRANSACTION` | TX-control (R11) |
| 3 | `SELECT label FROM labels WHERE issue_id=? ORDER BY label` | point per-bead labels (R4/R5) |
| 3 | `SELECT issue_id, COUNT(*) FROM wisp_comments WHERE issue_id IN (?,?,…)` | aggregate-by-id-set (R4) |
| 2 | `SELECT … FROM wisp_comments WHERE issue_id IN (…)` | range-by-id-set (R4) |
| 2 | `SELECT issue_id, type FROM wisp_dependencies WHERE COALESCE(…)` | range dep edges (R6) |
| 2 | `SHOW DATABASES`, `SELECT value FROM config WHERE key=?` | handshake / KV (R11) |
| 1–2 | misc (`comments`, `wisp_comments` body, `custom_statuses` count, `dolt_author_*` SET) | tail |

**Two pattern families dominate the *coordination* read load (excluding R0/dolt_fetch):**
1. **One full-scan hub query** (`SELECT DISTINCT id, …` on wisps/issues) producing a result-set of bead-ids.
2. **A fan-out of batched IN-clause hydration queries** for that id-set (labels, comments, dep counts, edges). This is "list-then-hydrate," cleanly index-shaped given the right keys — but the hub query's full-scan dominates total cost, and the hydration adds 3–5× queries per logical read.

---

## Tech-agnostic access-pattern requirements

| # | Requirement | Priority |
|---|---|---|
| AP-1 | **Point read by primary key**, p99 ≤ 1 ms at 25k records | P0 |
| AP-2 | **Filter scan over open subset** of a tier (status='open' etc.) returning ≤ 1k rows, p99 ≤ 10 ms at 25k records | P0 |
| AP-3 | **Batch-by-id-set fetch** (`id IN (…16–64…)`) of hydration tables (labels, comments, dep edges) — must be index-served, p99 ≤ 5 ms per batch | P0 |
| AP-4 | **Filter scan of the ephemeral/wisps tier** by (issue_type, status [, assignee]), returning ≤ 1k rows, p99 ≤ 10 ms at 10k wisps. **This is the dominant hot path today — current full-scan cost is unacceptable.** | P0 |
| AP-5 | **`ready` semantics** — open beads with no unresolved blocking deps, optionally filtered by assignee / label / metadata. Must be derivable from primary + dependency indices without a full edge join. | P0 |
| AP-6 | **Two tiers in one store** — durable "main" + ephemeral "wisps-with-TTL" — with the SAME read API and per-tier index design. (Today: one Dolt DB, two tables; replacement may collapse or split, must preserve the API.) | P0 |
| AP-7 | **Read-after-write within process** (same process that wrote a bead must see it on the next read). Cross-process is best-effort but ≤ cache reconcile interval (currently 30–120 s, see AP-9). | P0 |
| AP-8 | **Connection / session cost ≪ query cost** — a replacement must support either a persistent in-process handle (no fork-per-read) or a connection-pool with negligible handshake cost. Today: ~150 new connections/s × ~8 setup queries each = ~1,200 q/s of pure overhead; this must go to near-zero. | P0 |
| AP-9 | **Background cache-prime / reconcile sweep** completes in ≤ 5 s at 10k open records, ≤ 30 s at 100k. (Current Dolt `bd list` sweep is the cold-start critical path.) | P1 |
| AP-10 | **Index on (issue_type, status) and (assignee, issue_type, status) on the ephemeral tier**, or an equivalent skip-list shape — to convert AP-4 from full-scan to seek+scan. (Today's single-column indices on wisps don't compose for the mail-poll predicate.) | P1 |
| AP-11 | **Range scan by recency** (`created_at DESC` with limit) for archive / inbox-replay views | P1 |
| AP-12 | **Strict result determinism not required** — same query may return rows in different orders unless the caller sorts client-side (today's behaviour). | P2 |
| AP-13 | **No requirement** for ad-hoc SQL, joins beyond hydration, sub-queries, window fns, history/time-travel, branch/merge, or cross-rig replication on the read path. (Cross-references S6.) | — |

---

## Nonobvious findings & risks

1. **Ephemeral/wisps tier bypasses the in-memory cache.** `internal/beads/caching_store.go` has no ephemeral handling (`grep -E 'ephemeral|wisp|message'` ⇒ ∅). Every R1 (mail poll) falls through to `listEphemeral` ⇒ fork bd ⇒ full-scan wisps. **The single largest read-side cost-multiplier on the live system.** The in-flight read-path epic (PR #1147 / #1149 / ga-ustl8) must extend coverage to this tier or the storm persists post-deploy.

2. **Fork-per-CLI is a multiplicative overhead, not a query-shape problem.** Even if every query were an O(1) seek, ~150 conn/s × ~8 handshake/TX queries each = ~1,200 q/s of pure setup. AP-8 is therefore as load-bearing as the query-shape requirements.

3. **List-then-hydrate fanout is structurally fine** (labels/comments/deps fetched in batched IN clauses, not N+1) — *if* the store supports cheap `id IN (…)` reads. A KV store without efficient range-by-id-set would need explicit changes here.

4. **Closed-bead bloat hurts even though we don't read it.** R3 (`bd list --status=open`) still walks the full issues partition in Dolt because the storage-layer doesn't pre-segment by status. 21k closed task rows are inert weight on every prime. A replacement should either index `status` natively or physically separate closed records (TTL/archive on close).

5. **Out-of-band `dolt_fetch` saturating active-query slots** is not in the coordination read path but is co-tenant on the same server and consumes a meaningful fraction of `:28231` CPU. It does not change the requirements above, but the next iteration should triage it (likely a `dolt-remotes-patrol` cadence issue against the gascity DB on the shared server).

6. **No requirement surfaced for cross-record transactional reads.** Every observed query stands alone; reconcile semantics are converged across ticks, not held by transactions. The replacement does **not** need MVCC / snapshot isolation across the read path.

---

## Summary

The HQ coordination read workload is **read-heavy (≈265:1), short, and shape-uniform** (point reads + filter scans + list-then-hydrate). The dominant cost is **not query complexity** — it's the per-CLI fork + connection setup overhead and one specific predicate (mail poll on the ephemeral tier) that today resolves to a full-scan. A right-sized replacement only needs the access patterns in AP-1 through AP-11; none of the Dolt-specific superpowers (full SQL, history, branch/merge, replication) appear on the read path.
