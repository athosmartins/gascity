# S6: Anti-requirements — Dolt/beads Feature Usage Audit

**Spike:** `ga-aec8q.6`
**Date:** 2026-05-22
**Author:** gascity/architect
**Method:** Static code analysis — exhaustive `grep` across all `*.go` production files (non-test) in `internal/` and `cmd/gc/`, cross-referenced with architecture docs (`engdocs/design/beads-dolt-contract-redesign.md`, `engdocs/architecture/beads.md`), and persistent memory about the live HQ Dolt topology.

---

## Purpose

Quantify the overkill: which Dolt/beads capabilities does HQ actually use vs. what it pays for implicitly. Per-entity. This is the evidence base for "right-sizing away from Dolt."

---

## Dolt Feature Audit — HQ Production Usage

### Features Actually Used

| Feature | Used? | Evidence (code location) | Notes |
|---|---|---|---|
| **Create bead** | YES | `bdstore.go:619` — `bd create --json` | Core write path |
| **Get bead by ID** | YES | `bdstore.go:685` — `bd show --json <id>` | Used on every cache miss |
| **Update bead** (title, status, description, assignee, labels, metadata) | YES | `bdstore.go:704,847` — `bd update --json` | Core write path |
| **Close bead** | YES | `bdstore.go:1291` — `bd close --json` | Core write path |
| **Reopen bead** | YES | `bdstore.go:1308` — `bd reopen --json` | Used during drain/recovery |
| **Delete bead** | YES | `bdstore.go:1320` — `bd delete --force --json` | Wisp GC, order tracking cleanup |
| **List beads** (by label, assignee, status, type, metadata-field, parent, createdBefore, limit) | YES | `bdstore.go:1347–1428` | CachingStore reconcile reads |
| **Ready query** (open, unblocked) | YES | `bdstore.go:1598` — `bd ready --json` | Agent work finding |
| **Ephemeral/wisps tier** (separate table, `ephemeral=true` flag) | YES | `bdstore.go:listEphemeral`, `beads.Bead.Ephemeral` | Mail messages use this |
| **bd query** (ephemeral=true filter) | YES | `bdstore.go:~1430` — `bd query --json <clauses>` | Inbox check for wisps |
| **Dependency graph** (dep add, dep remove, dep list) | YES | `bdstore.go:1652,1668,1712` — `bd dep` | Molecule steps, gate ordering |
| **SetMetadataBatch** | YES | `bdstore.go:Tx.SetMetadataBatch` | Session state metadata writes (high-frequency hot path) |
| **bd purge** (close closed ephemeral beads) | YES | `bdstore.go:257–299` | Wisp/order-tracking TTL enforcement |
| **bd config set** | YES | `bdstore.go:241` | Initial store configuration |
| **Ping / bd list --limit 0** | YES | `bdstore.go:1193` | Health check |

### Features NOT Used

| Feature | Used? | Evidence | Overkill Cost Paid |
|---|---|---|---|
| **Commit history / time-travel** | **NO** | Zero occurrences of `dolt_history`, `dolt_diff`, `dolt_log`, `AsOf`, `AS OF` in any production `*.go` | Git-object write per commit; unbounded storage growth; Dolt scans commit graph on every query |
| **Branch / merge** | **NO** | Zero occurrences of `DOLT_MERGE`, `dolt_branch`, `dolt_checkout`, `dolt_merge`, `DOLT_CHECKOUT` in any production `*.go` | Merge-resolution overhead; non-fast-forward error surface; unnecessary branching complexity |
| **Cross-rig Dolt sync / remotes** | **NO** | Zero `bd dolt push`, `bd dolt pull` in production `*.go`; memory note confirms gascity Dolt is LOCAL-ONLY with no remote configured; CLAUDE.md explicitly says "skip bd dolt push at session close" | Remote tracking overhead; non-fast-forward / no-common-ancestor errors (expected and documented as bugs/expected) |
| **Real SQL transactions (BEGIN/COMMIT/ROLLBACK)** | **NO** | `BdStore.Tx()` is simulated: it calls `bd show` per bead, applies changes via sequential `bd update` calls, NO `BEGIN`/`COMMIT` | No actual atomicity across multi-bead mutations; pays for SQL engine without using ACID |
| **SQL DDL / schema migrations** | **NO** | `internal/migrate/migrate.go` is TOML config migration (pack layout), NOT SQL schema; zero `ALTER TABLE`/`CREATE TABLE` in production `*.go` | Schema versioning overhead; migration complexity |
| **Full SQL (ad-hoc queries)** | **NO** | All queries are bd CLI arguments (`--label`, `--assignee`, `--status`, `--metadata-field`), never raw SQL from gc | SQL engine overhead for what is essentially key-value filtered scan |
| **Dolt backup remotes** | **ADVISORY ONLY** | `internal/doctor/checks_dolt_backup.go` is a `gc doctor` warning that a backup isn't configured; not an active backup path | N/A operationally; risk if backup matters |
| **Dolt remote replication / HA** | **NO** | No usage found; single local Dolt server per city | N/A |

---

## Per-Entity Anti-Requirement Analysis

| Entity Class | Dolt Feature Used | Commit History Needed? | Branch/Merge Needed? | Cross-Rig Sync Needed? | SQL Transactions Needed? | Full SQL Needed? |
|---|---|---|---|---|---|---|
| Work Beads (tasks, epics) | CRUD + label/assignee/status filter | **NO** | **NO** | **NO** | **NO** | **NO** |
| Session Beads | CRUD + metadata batch write | **NO** | **NO** | **NO** | **NO** | **NO** |
| Molecule/Step Beads | CRUD + parent filter | **NO** | **NO** | **NO** | **NO** | **NO** |
| Mail Messages (wisps) | Ephemeral tier CRUD + assignee filter | **NO** | **NO** | **NO** | **NO** | **NO** |
| Convoy Beads | CRUD + children query | **NO** | **NO** | **NO** | **NO** | **NO** |
| Order-Tracking Beads | CRUD + label filter + TTL close | **NO** | **NO** | **NO** | **NO** | **NO** |
| Dependency Graph | `bd dep add/remove/list` | **NO** | **NO** | **NO** | **NO** | **NO** |

**Conclusion: Zero entity classes benefit from Dolt's differentiating features.** Every entity class uses only the core CRUD + filter surface.

---

## What the overkill costs

### 1. Write latency (largest impact)
Every `bd write` = `dolt-commit` = git-object creation under the hood.

From persistent memory (`architecture-bdstore-uses-bd-cli-fork-per-call`):
- ~30–80ms per bd CLI fork
- ~1.6s average per dolt-commit (the write itself)
- ~990 bd calls per reconcile tick → **68–120s wall-clock per reconcile cycle**

The 1.6s per commit is paid for the git-object creation, commit-graph update, and WAL flush — none of which are needed since we never query history.

### 2. Unbounded storage growth (the triggering incident)
Dolt's git-like storage retains every commit forever. Every closed bead (status change, metadata update) generates a commit. The live symptom:
- Epic mentions 23,113/23,356 issues closed → ~99% of bead rows are historical dead weight
- These don't just consume disk — they bloat the commit graph Dolt scans on every query, degrading ALL read latency over time

### 3. CPU cost at scale
The triggering incident (`gm-qmk0og` reference in epic) was CPU root-caused to full-table scans on hot paths. Dolt's B-tree index on top of a commit-graph-backed storage layer is not optimized for high-churn, always-current data. Every `bd list` without a narrow index filter becomes an expensive scan.

### 4. Fork-per-call overhead
`BdStore` forks a `bd` subprocess for every operation. At the ~990 calls/reconcile-tick rate, fork overhead alone is 30–80s per tick. A native in-process store would reduce this to microseconds.

### 5. Non-fast-forward / no-common-ancestor errors
The memory note (`bd-backend-postgres-no-dolt-push`) documents that `bd dolt push` errors with non-fast-forward / no-common-ancestor — EXPECTED because gascity Dolt has no remote. This is a footgun surface that exists purely because Dolt is git-underneath, adding error-handling complexity for a feature (sync) we don't use.

---

## What we actually need from the persistence layer

Stripping all unused features, the functional requirement is:

| Capability | Priority |
|---|---|
| Key-value create/read/update/delete by string ID | P0 |
| Filtered list: by label, assignee, status, type, metadata-key=val, parent-id, createdBefore | P0 |
| Atomic per-record write (individual write is atomic and immediately readable) | P0 |
| Batch metadata key-value write (SetMetadataBatch) | P0 |
| Two-tier storage: durable "main" + ephemeral "wisps" with configurable TTL | P1 |
| Dependency graph: directed edges between record IDs with list/add/remove | P1 |
| TTL-based record expiry for ephemeral tier | P1 |
| Bulk close/delete (bd purge equivalent) | P2 |
| Per-record metadata (map[string]string) | P0 |
| **Commit history** | **NOT NEEDED** |
| **Branch/merge** | **NOT NEEDED** |
| **Cross-node sync/replication** | **NOT NEEDED** |
| **SQL DDL / schema migrations** | **NOT NEEDED** |
| **ACID multi-record transactions** | **NOT NEEDED** |
| **Full SQL** | **NOT NEEDED** |
| **Remote backup** | OPTIONAL (operator policy) |

---

## What could safely be dropped

If migrating away from Dolt/beads, these are the features to NOT reimplement:

1. **Dolt commit history** — drop entirely. No consumer reads it.
2. **Dolt branch/merge** — drop entirely. No consumer uses it.
3. **dolt-sync / remote push-pull** — drop entirely. Local-only usage.
4. **SQL DDL** — use a fixed schema (or schemaless document store) with no migration surface.
5. **ACID transactions** — individual write atomicity is sufficient. No multi-bead transaction ever reads its own in-flight writes.
6. **Full SQL** — a well-indexed document/KV store with the listed filter predicates is sufficient.

---

## Implications for solution landscape

Any of the following would satisfy the actual requirements while eliminating the overkill:

| Option | Commit History | Branch/Merge | Sync | ACID Tx | Write Latency | Fork Tax |
|---|---|---|---|---|---|---|
| **Dolt (current)** | Has (unused) | Has (unused) | Has (unused) | Has (unused) | ~1.6s/write | ~50ms/call |
| **SQLite in-process** | No | No | No | Yes (unnecessary) | <1ms/write | Zero |
| **BoltDB / bbolt** | No | No | No | Yes (unnecessary) | <1ms/write | Zero |
| **Custom append-log + index** | No | No | No | Per-record only | <0.1ms/write | Zero |
| **Redis / Valkey** (with AOF) | No | No | Optional | No (list atomic) | <1ms/write | Network RTT |
| **PostgreSQL** | No | No | Optional | Yes (unnecessary) | 1–5ms/write | Network RTT |

The simplest migration path: **in-process SQLite** (single file, zero fork, <1ms writes, full filter support, TTL via scheduled DELETE). Write latency drops from ~1.6s to <1ms. Fork tax drops to zero. Storage is bounded by configurable retention, not git history.

---

## Conclusion

The assertion in the epic holds: **Dolt's differentiating features are 100% unused by HQ coordination state.** HQ pays:
- ~1.6s write latency (dolt-commit overhead)
- ~50ms fork tax per bd call
- Unbounded storage growth (commit history never GC'd)
- CPU degradation over time (bloated commit graph)

...for features it never uses. The minimum-viable persistence layer for HQ coordination state is a structured document store with:
- CRUD + filter queries
- Per-record metadata
- Two-tier ephemeral/durable storage
- TTL GC on ephemeral tier
- Dependency graph edges

No commit history. No branching. No cross-node sync. No ACID multi-record transactions.
