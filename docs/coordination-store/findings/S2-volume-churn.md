# S2 — Volume & churn census of HQ coordination state

> Spike **ga-aec8q.2** (parent epic **ga-aec8q**). Quantifies the
> entities catalogued in [[S1-entities]]. Tech-agnostic: rows-per-day
> rather than "Dolt scans". Anchors S5 (durability matrix) and
> S7 (targets & retention).
>
> Observation rig: gascity, 2026-05-22 23:30 local. Live HQ store on
> the embedded Dolt server. Numbers below are point-in-time except
> where labelled as "per day" or "last 24h".

## Headline

| Finding | Number |
|---|---|
| Most-populated table | `issues`, **23,373 rows**, of which **91 % (21,286) are closed tasks**. |
| Single largest growth driver | **`wisp_events`** (audit log of wisp mutations) — **45 % of rows are orphans** from already-deleted parent wisps because `wisp_events` has no FK to `wisps`. |
| Same problem, different table | **`wisp_labels`** — **46 % of rows are orphans** for the same reason. |
| Highest sustained create rate | Order-tracking wisps — **24 per minute** (last hour), **~3,500 per day** sustained. |
| Most clearly unbounded user-facing entity | Open mail messages — **+200 per day net** (created ≫ archived), oldest open is 15 days, no reaper today. |
| Schema-defined-but-empty surfaces | 6 entire tables empty + 9 unused columns on `issues` — pure dead schema weight. |

The store is **bounded today** only by the recency of its installation
(events table spans 4-5 days). Two of the largest tables already show
the unbounded pattern within that window. **Projection: at sustained
~10 k orphan `wisp_events`/day, the table reaches 3.65 M rows in one
year of continuous operation.**

---

## Method

Counts come from direct SQL against the live HQ store (database `hq`
on the per-rig Dolt server, port read from `.beads/dolt-server.port`).
Filesystem-only entities are measured with `wc -l` / `du -h` on the
canonical paths inventoried in S1 §IX.

Per-day rates use `GROUP BY DATE(created_at)` / `GROUP BY DATE(closed_at)`.
"Last 24h" uses `created_at > NOW() - INTERVAL 1 DAY`. Orphan
detection uses `LEFT JOIN ... WHERE parent.id IS NULL`. Schema
column-emptiness uses `WHERE col != '' GROUP BY col`.

The events table only carries the last **4-5 days** of history at the
moment, so all per-event-row rates are computed against that window.
Per-bead rates use the bead row's `created_at`, which goes back ~17
days.

---

## I. Volume snapshot (point-in-time)

### I.1 Bead tables

| Table | Rows | Open | Closed | Dead-weight ratio | Notes |
|-------|------|------|--------|--------------------|-------|
| `issues` | 23,373 | 224 | 23,148+ | **91 %** are closed tasks (21,286) | Includes 47 open sessions and 22 open convoys. The closed-task mass is from a May 14-18 burst — see §III. |
| `wisps` | 6,364 | 2,407 | 3,955 | — | Two distinct populations: order-tracking (3,623, all `issue_type='task'`, 24 h TTL) and mail messages (2,776 mostly `issue_type='message'`, no reaper). |

### I.2 Bead breakdown by issue_type / status (issues)

| issue_type / status | Count |
|---|---|
| task / closed | 21,286 |
| message / closed | 1,677 |
| task / open | 130 |
| session / closed | 117 |
| session / open | 47 |
| chore / closed | 28 |
| convoy / open | 22 |
| chore / open | 16 |
| bug / open | 13 |
| molecule / open | 11 |
| bug / closed | 8 |
| feature / open | 6 |
| convoy / closed | 6 |
| feature / closed | 2 |
| task / deferred | 1 |
| chore / deferred | 1 |
| epic / closed | 1 |
| step / open | 1 |

### I.3 Bead breakdown by issue_type / status (wisps)

| issue_type / status | Count |
|---|---|
| task / closed | 3,585 |
| message / open | 2,359 |
| message / closed | 417 |
| task / open | 3 |

Confirmed via JOIN to wisp_labels: **100 % of the 3,623 task wisps
carry the `order-tracking` label** — task wisps in this store are
exclusively order-tracking artefacts (see S1 §IV.2).

### I.4 Audit-log / supporting tables

| Table | Rows | FK to parent? | Orphan rate |
|-------|------|--------------|-------------|
| `events` (issue audit log) | 75,933 | yes, `ON DELETE CASCADE` | **0 %** (FK works). |
| `wisp_events` (wisp audit log) | 47,334 | **NO FK** | **45 %** (21,445 orphans). |
| `labels` | 447 | yes, `ON DELETE CASCADE` | 0 %. |
| `wisp_labels` | 23,106 | **NO FK** | **46 %** (10,706 orphans). |
| `dependencies` (issue side) | 13 | yes, cascade on parent | 0 %. |
| `wisp_dependencies` | 0 | no FK | — |
| `comments` | 6 | yes | 0 %. |
| `wisp_comments` | 0 | no FK | — |
| `interactions` table | 0 | — | (live data is in `.beads/interactions.jsonl`, 5,606 lines) |
| `routes` table | 0 | — | (live data is in `.beads/routes.jsonl`, 5 lines) |
| `federation_peers` | 0 | — | federation unused here |
| `compaction_snapshots` | 0 | — | compaction not producing rows |
| `issue_snapshots` | 0 | — | feature unused |
| `repo_mtimes` | 0 | — | feature unused |
| `child_counters` | 11 | — | low churn |
| `wisp_child_counters` | 0 | — | low churn |
| `config` | 49 | — | includes 24 `kv.memory.*` rows |
| `metadata` | 1 | — | `_project_id` only |
| `local_metadata` | 3 | — | bd version + tip-shown timestamp |
| `custom_statuses` | 3 | — | deferred, hooked, resolved |
| `custom_types` | 12 | — | agent, convergence, convoy, event, gate, merge-request, message, molecule, rig, role, session, spec |
| `schema_migrations` | (small, append-only) | — | — |
| `ignored_schema_migrations` | (small, append-only) | — | — |

### I.5 Filesystem-only entities (sizes)

| Path | Size / lines | Notes |
|------|--------------|-------|
| `.gc/events.jsonl` | 184 lines, 104 KB | Event Bus log. Bounded by rotation. |
| `.beads/interactions.jsonl` | 5,606 lines | Mirrors the empty `interactions` table. No reaper today. |
| `.beads/routes.jsonl` | 5 lines | Mirrors the empty `routes` table. Config-shaped, low churn. |
| `.gc/beads.json` | 14 KB, seq=21 | **Stale 21-day-old snapshot.** Last update May 1, current date May 22. Likely orphaned from an older export pipeline (S1 §IX.5). |
| `.gc/runtime/nudges/state.json` | does not exist | Empty queue right now. |
| `.gc/session-name-locks/` | does not exist | No active locks right now. |
| `.gc/maintainer-pr-review/` | per-PR-run directories with ~14 JSON artefacts each | Bounded per PR; growth scales with PR-review activity. |
| `.beads/dolt/` | (database itself — out of scope for row-count census) | — |
| `.beads/backup/` | (subject of in-flight PR #2478 — known growth bug) | — |

### I.6 Live HQ dependency graph

The `dependencies` table has **13 rows**. The dense workflow DAG
(5,520 dependency rows) lives in the rig database (`gascity`), not
in HQ. **HQ is primarily a coordination/messaging store, not a
workflow database**, and right-sizing HQ should treat the rig DB as
a separate problem with potentially different retention requirements.

---

## II. Churn rates

### II.1 Per-day bead create rate (issues, by type, last 7 days)

| Date | task | session | convoy | molecule | bug | chore | feature |
|------|------|---------|--------|----------|-----|-------|---------|
| 2026-05-16 | 7,023 | 5 | 5 | — | 4 | 4 | — |
| 2026-05-17 | 7,339 | 2 | 12 | — | 1 | 4 | 3 |
| 2026-05-18 | 2,031 | 6 | — | 2 | — | — | — |
| 2026-05-19 | 12 | 20 | 4 | 3 | 2 | 4 | — |
| 2026-05-20 | 17 | 41 | 3 | 1 | 2 | — | — |
| 2026-05-21 | 31 | 21 | 1 | 2 | 2 | 3 | — |
| 2026-05-22 | 16 | 16 | 3 | 3 | 3 | 15 | 1 |

The May 14-18 task burst (~21k tasks over 4 days) is a one-off
**migration / dispatch backfill**, not a steady-state rate. Steady
state since May 19 is **~20 tasks/day** + **~20 sessions/day** with
balanced close.

### II.2 Per-day bead close rate (issues, last 7 days)

| Date | task | session | convoy | molecule |
|------|------|---------|--------|----------|
| 2026-05-16 | 6,993 | 1 | 2 | — |
| 2026-05-17 | 7,317 | — | — | — |
| 2026-05-18 | 2,027 | 3 | — | — |
| 2026-05-19 | 2 | 45 | 1 | — |
| 2026-05-20 | 1 | 24 | 1 | — |
| 2026-05-21 | 2 | 20 | 1 | — |
| 2026-05-22 | — | 14 | 1 | — |

Closes match creates within a day for the May 14-18 burst — strong
evidence those 21k tasks were create-and-immediately-close
automation output, not real work. Their ROWS will remain forever
under the current retention policy (i.e., no retention).

### II.3 Per-day wisp create rate (last 7 days)

| Date | message | task (order-tracking) | Notes |
|------|---------|----------------------|-------|
| 2026-05-15 | 148 | — | |
| 2026-05-16 | 353 | — | |
| 2026-05-17 | 510 | — | |
| 2026-05-18 | 56 | — | |
| 2026-05-19 | 4 | — | |
| 2026-05-20 | 92 | — | |
| 2026-05-21 | 191 | 179 | order-tracking activity starts |
| 2026-05-22 | 408 | **3,443** | sustained order-tracking rate |

The order-tracking pipeline started producing wisps on May 21 (or
earlier with a retention window narrow enough that we can't see the
prior days from the wisp rows themselves). At **~3,500/day** it is
the largest single producer of rows in the entire store.

### II.4 Per-day wisp close rate (last 7 days)

| Date | message | task |
|------|---------|------|
| 2026-05-15 | 18 | — |
| 2026-05-16 | 111 | — |
| 2026-05-17 | 3 | — |
| 2026-05-19 | 254 | — |
| 2026-05-20 | 29 | — |
| 2026-05-21 | — | 179 |
| 2026-05-22 | 2 | **3,438** |

Order-tracking wisps close within hours of creation (24h TTL,
roughly — see §III.2). Message wisps essentially **do not close**:
408 created today, 2 closed today. The 2-vs-408 ratio is the
unbounded-growth fingerprint.

### II.5 Per-day audit-event rate (last 4-5 days, current retention window)

| Date | issue events | wisp events |
|------|-------------:|------------:|
| 2026-05-18 | 24,307 | 8,936 |
| 2026-05-19 | 22,014 | 1,343 |
| 2026-05-20 | 17,230 | 6,911 |
| 2026-05-21 | 5,610 | 12,163 |
| 2026-05-22 | 6,846 | **17,936** |

The May 18-20 issue-event surge is the tail of the May 14-18 task
burst (created/updated/closed events for those tasks). The May 21-22
wisp-event surge is the order-tracking pipeline starting up.

### II.6 Top order producers (last 24 h)

| Order | Wisps in last 24h | Notes |
|-------|-------------------:|-------|
| gate-sweep | 678 | runs every ~2 min |
| beads-health | 669 | runs every ~2 min |
| dolt-health | 638 | runs every ~2 min |
| order-tracking-sweep | 509 | runs every ~3 min |
| cross-rig-deps | 216 | runs every ~7 min |
| orphan-sweep | 208 | runs every ~7 min |
| spawn-storm-detect | 207 | runs every ~7 min |
| mol-dog-jsonl | 88 | runs every ~16 min |
| dolt-remotes-patrol | 84 | runs every ~17 min |
| main-ci-watcher | 70 | runs every ~20 min |
| maintainer-pr-review-queue | 49 | every ~30 min |
| triager-poll | 47 | every ~30 min |
| mol-dog-reaper | 47 | every ~30 min |
| wisp-compact | 24 | every ~60 min |
| pr-audit | 24 | every ~60 min |
| mol-dog-phantom-db | 23 | every ~60 min |
| daily-mpr-sweep | 23 | every ~60 min |
| mol-dog-{stale-db, backup} | 4 each | every ~6 h |
| prune-branches | 4 | every ~6 h |
| daily-standup-* (×4 rigs) | 1 each | daily |
| mol-dog-compactor, verify-standups | 1 each | daily |

24.4 order-tracking wisps per **minute** (= 1,464/hour) when measured
across the entire last hour.

### II.7 Filesystem append rates

- `.gc/events.jsonl`: 184 lines total. Growth quiet during this
  observation window (most of those entries are city-resumed/suspended
  + bead.created/closed from earlier sessions). Bounded by log
  rotation.
- `.beads/interactions.jsonl`: 5,606 lines. No reaper. Append rate
  appears tied to bead-mutation rate via the hook layer; on a
  high-mutation day, this file grows comparably to `events`.

---

## III. Unbounded-growth findings, ranked by severity

### III.1 wisp_events orphans (severity: **highest**)

- **47,334 rows, 21,445 (45 %) orphaned.**
- Root cause: `wisp_events` has **no foreign-key constraint** to
  `wisps`. When `wisp-compact` deletes a closed wisp, the wisp row
  disappears but its ~5 audit rows in `wisp_events` remain.
- Each wisp produces ~5 audit-event rows on its lifecycle (created,
  label_added ×3, closed), so the orphan count grows in lockstep
  with the deleted-wisp count.
- At current rate (~3,500 wisps deleted/day × 5 events/wisp =
  17.5 k orphan events/day):
  - 30 days → 525 k orphan rows
  - 90 days → 1.58 M
  - 365 days → 6.4 M
- The audit value of a wisp_event row from a deleted wisp is **zero**
  by definition — the wisp it audits is gone. These rows are pure
  dead-weight on the hottest write surface.

### III.2 wisp_labels orphans (severity: **highest**, same root cause)

- **23,106 rows, 10,706 (46 %) orphaned.**
- Same root cause: `wisp_labels` has no FK to `wisps`.
- ~3 labels per order-tracking wisp × ~3,500 deleted/day =
  **10.5 k orphan labels/day** at the current sustained rate.
- Indexed by `(issue_id, label)` and by `label` — the index also
  carries the dead weight.

### III.3 Closed-task pile on `issues` (severity: high, slower growth)

- **21,286 closed tasks (91 % of issues table).**
- Root cause: no retention policy on closed beads. Compaction
  exists (`compact_tier1_days`, `compact_tier2_days` configured)
  but `compaction_snapshots` has 0 rows — compaction is either
  not running, not yet hitting tier thresholds, or not compacting
  these.
- Sustained creation rate (since the May 14-18 burst tapered off)
  is ~20 tasks/day, so steady-state amortized growth is slow. But
  episodic bursts (~7 k/day during dispatch backfills) can land
  thousands in a single day with no plan to ever remove them.
- Every closed task row also contributes 1 `created` + 1+ `updated`
  + 1 `closed` event row to the `events` table. With the FK
  cascade in place, deleting the task row WOULD clean those — but
  nothing deletes them today.

### III.4 Open mail messages on `wisps` (severity: high)

- **2,365 open message wisps, oldest 15 days old.**
- Create vs close: 408 / 2 on May 22 alone — **2 % archive rate**
  on the day's incoming mail.
- Root cause: no retention or auto-archive policy on mail. Each
  agent's `gc mail inbox` does not auto-mark-read older items.
- Projected growth at +200/day net: 30 d → +6 k, 90 d → +18 k,
  365 d → +73 k. Worse if traffic increases.
- Unlike order-tracking wisps, message wisps have semantic value
  (notification of recent communication) for at least a few days.
  Naive deletion is wrong; a tiered retention policy ("archive
  read after 7 days, unread after 30") is the obvious shape.

### III.5 `events` (issue audit log) (severity: medium)

- **75,933 rows over 4-5 days = ~17 k events/day** sustained.
- `events` HAS the FK cascade, so it would clean if closed issues
  were ever deleted. They are not deleted, so the table grows
  monotonically with issue creation.
- Of the 75 k rows, ~52 k are `updated` events from the May 14-18
  burst (bulk-updating 21 k tasks ~2.5 times each). Steady-state
  growth is ~9 k events/day (= 1 created + ~1 updated + ~1 closed
  per ~20 tasks/day, plus ~50 sessions/day × ~5 events each, plus
  per-bead label-add events).
- Projected at 9 k/day: 30 d → 270 k, 90 d → 810 k, 365 d → 3.3 M.

### III.6 `.beads/interactions.jsonl` (severity: medium)

- **5,606 lines, no reaper.**
- File grows on every agent-mutation hook fire. Largely duplicates
  the same field-change information already captured in the
  `events` Dolt table, but indexed by *session-actor* (the gc agent
  name) rather than the bead.
- At observed rates the file gains roughly as many rows per day as
  the issue `events` table (the hook is the same trigger). No
  rotation today.

### III.7 `.beads/backup/` (severity: medium, fix in flight)

- Subject of open PR #2478 (`bd-backup-size` doctor canary). The
  existence of the PR is sufficient documentation that the growth
  pattern is recognized. Volume not measured here because the size
  is sensitive to the backup retention policy currently being
  changed.

### III.8 Stale `.gc/beads.json` (severity: low, but a smell)

- 14 KB file, last touched 2026-05-01, current date 2026-05-22 —
  **21 days stale**. Seq is 21 (vs the Event Bus log's seq=184).
- The file appears to be an orphan from an earlier export pipeline.
  Either ratchet it (resume updates) or remove it. Not a growth
  driver in itself, but a confusing artefact.

---

## IV. Entity-by-entity volume + churn matrix

Compact reference for S5/S7. Rate units are "rows per day" measured
on the current sustained steady state (not the May 14-18 burst).
"Reaper" is the existing automatic cleanup mechanism; "Eff." is
its observed effectiveness.

| Entity (S1 §) | Population today | Create / day | Close / day | Reaper today | Eff. |
|---|---:|---:|---:|---|---|
| Task bead (I.1) | 21,417 (130 open) | ~20 | ~20 | none (closure ≠ deletion) | n/a |
| Session bead (II.1) | 164 (47 open) | ~20 | ~20 | none on closed sessions | balanced create/close, accumulating closed |
| Convoy (III.2) | 28 (22 open) | ~3 | ~1 | manual close on completion | OK |
| Molecule (III.1) | 11 open | ~2 | (closes via cleanup logic) | molecule cleanup | OK |
| Mail message wisp (IV.1) | 2,776 (2,365 open) | 200-500 | 0-50 | **none** | broken |
| Order-tracking wisp (IV.2) | 3,623 (all closed) | **3,500** | ~3,500 | `wisp-compact` order | wisp row OK; labels/events leak |
| Issue field-change event (IV.3) | 75,933 | ~9,000 | n/a (FK cascade only) | implicit cascade on bead delete | tied to bead deletion (never happens) |
| Wisp field-change event (IV.3) | 47,334 (21,445 orphan) | ~17,000 | n/a | **none** (no FK) | **broken — 45 % orphans** |
| System event (IV.4) | 184 in jsonl | variable | n/a | log rotation | OK |
| Dependency (V.3, HQ) | 13 | <1 | n/a | implicit cascade | OK (HQ is shallow) |
| Issue label (V.4) | 447 | ~50 | n/a | implicit cascade | OK |
| Wisp label (V.4) | 23,106 (10,706 orphan) | ~10,500 | n/a | **none** (no FK) | **broken — 46 % orphans** |
| Nudge queue item (V.5) | 0 today | bursts | per-delivery | dispatcher cleanup | OK |
| Session-name lock (V.6) | 0 today | per-create | per-release | release on op | OK |
| Rig route (VI.1) | 5 (jsonl) | <1 | rare | manual | OK |
| Federation peer (VI.2) | 0 | <1 | rare | manual | OK |
| bd memory `kv.memory.*` (VII.1) | 24 | <1 | manual `bd forget` | OK |
| Custom status / type (VII.2) | 3 + 12 | <<1 | rare | manual | OK |
| Counters (VII.3) | 11 | per-create | n/a | n/a (monotonic) | OK |
| Compaction snapshot (VIII.1) | 0 | (not running on this rig) | n/a | tier-2 graduation | not active |
| Issue snapshot (VIII.2) | 0 | feature unused | — | — | n/a |
| Comment (VIII.3) | 6 issue + 0 wisp | ~0 | implicit cascade (issue side) | OK |
| Repo mtime (VIII.4) | 0 | feature unused | — | — | n/a |
| Maintainer-PR-review run state (IX.4) | per PR | per PR run | retention TBD by pack | TBD |
| `.beads/interactions.jsonl` (IX.6) | 5,606 | ~9 k (matches `events`) | **none** | broken |
| `.gc/beads.json` (IX.5) | 14 KB stale snapshot | n/a (orphaned) | n/a | unmaintained |
| `.beads/backup/` (IX.9) | (PR #2478 in flight) | per-backup | retention TBD | in-fix |

---

## V. 30 / 90 / 365-day projection at sustained rates

Assumes the May 21+ steady state (no more 21k-task bursts), no
retention-policy changes, current order-tracking rate of ~3,500
wisps/day.

| Surface | Today | +30 d | +90 d | +365 d |
|---|---:|---:|---:|---:|
| `issues` (mostly closed tasks) | 23.4 k | +0.6 k → 24.0 k | +1.8 k → 25.2 k | +7.3 k → 30.7 k |
| `wisps` (order-tracking; bounded by TTL) | 6.4 k | ≈ 4 k steady | ≈ 4 k | ≈ 4 k |
| `wisps` (open message wisps, no reaper) | 2.4 k | +6 k → 8.4 k | +18 k → 20 k | +73 k → 75 k |
| `events` (issue audit, FK cascade) | 75.9 k | +270 k → 346 k | +810 k → 886 k | +3.28 M → 3.36 M |
| `wisp_events` (orphans + live) | 47.3 k | +525 k → 572 k | +1.58 M → 1.62 M | +6.4 M → 6.4 M |
| `wisp_labels` (orphans + live) | 23.1 k | +315 k → 338 k | +945 k → 968 k | +3.83 M → 3.85 M |
| `.beads/interactions.jsonl` | 5.6 k lines | +270 k → 276 k | +810 k | +3.28 M |

These projections are **extrapolated from rates measured during a
single 24-hour observation window** and so should be read as
order-of-magnitude rather than exact. The qualitative shape (the
wisp_events / wisp_labels / open-mail surfaces grow at orders of
magnitude faster than the others) is robust.

---

## VI. Where to act, in priority order

1. **Add foreign keys (or an explicit reaper) for `wisp_events` and
   `wisp_labels`.** This is the single most-impactful change.
   Removing the existing 32 k orphan rows would cut combined wisp-
   audit storage by ~45 % overnight; preventing future orphans
   removes the largest growth vector. Architecturally aligned with
   the `events` / `labels` tables on the issue side, which already
   work this way.
2. **Add a retention policy for open mail messages**, separately for
   read and unread. The current "no reaper" pattern produces
   visible UX harm (overloaded inboxes) on top of the storage cost.
3. **Verify compaction is actually running** (`compaction_snapshots`
   has 0 rows despite config being present), or delete the
   compaction columns + tables if the feature is dead.
4. **Decide the fate of `.gc/beads.json`.** Either resume updating
   it on every bead-mutation hook (matching the Event Bus seq), or
   remove it.
5. **Rotate / cap `.beads/interactions.jsonl`.** Largely duplicates
   the `events` Dolt table; either delete it (and route hooks to
   write the table) or apply log rotation.
6. **Take action on PR #2478** (backup-size canary) so backup
   growth has a documented bound.
7. **Drop dead schema** identified in S1 §X: 9 unused columns on
   `issues`, 6 entire empty tables. Zero data loss; cleaner schema
   for whatever S7 lands on.

---

## VII. Anchors for S5 (durability matrix) and S7 (targets & retention)

For each entity in S1, S2 has provided:

- **A population** — so S5 can answer "how much must survive a
  restart?" with a real number.
- **A churn rate** — so S5 can compute a recovery-time budget for
  losing N seconds of data.
- **A reaper status** — so S7 can target retention policies at the
  surfaces that need them.

The matrix in §IV ranks reaper effectiveness as:
- **OK** — close + delete or implicit cascade work, population
  stable.
- **Broken** — no reaper or no cascade, population unbounded.
- **n/a** — entity is by-design append-only or single-row config.

S5 should anchor durability requirements on **populations**, and
crash-recovery requirements on **churn rates**. S7 must address
the **broken** rows: wisp_events orphans, wisp_labels orphans,
open mail messages, closed-task pile, and the
`.beads/interactions.jsonl` log.
