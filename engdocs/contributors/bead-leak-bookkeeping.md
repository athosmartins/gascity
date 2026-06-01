# Bead Leak Bookkeeping Report

Date: 2026-06-01
Branch: `analysis/rescue-drain-rebase-main-20260530`

This report tracks the open-bead stability work for idle cities. The target
invariant is: after startup and any configured orders settle, an idle city must
have a bounded number of open issue-tier beads and wisp-tier beads. Any
infrastructure path that creates an open bead must have an owner that closes it
or a maintenance path that can safely reap it.

## Current Patch

`ga-k5ds4` identified that `reaper.sh` only closed stale wisps with a
`parent-child` edge to a closed target. Graph-v2 workflows and rescue-drain
flows commonly link step wisps to roots or peer steps with `tracks` and
`blocks`; those step wisps could remain open forever even after their targets
closed.

The reaper now treats `parent-child`, `tracks`, and `blocks` as reaper-owned
closure edges. A stale non-closed wisp is closed only when:

- it has at least one of those dependency edges to a closed issue or wisp
- it has no matching edge whose target is open, non-closed, missing, or
  unresolved

Closed-wisp purge protection now uses the same edge set, so a closed target is
not deleted while a non-closed wisp still depends on it through one of those
edges.

The live Dolt schema stores the wisp graph in `wisp_dependencies`, not in the
issue-level `dependencies` table. The reaper's wisp close and purge paths now
probe and query `wisp_dependencies`; the stale issue auto-close exclusion path
still uses `dependencies`. Every mutating SQL block now selects the target
database with `USE <db>` before `UPDATE` or `DELETE`; live Dolt rejected the
qualified `ga.*` update without that active database context.

Live inspection also found generated step-spec debris: old unassigned `spec`
wisps titled `Step spec for ...` with no incoming or outgoing wisp dependency
edges and no issue-level dependencies. Those rows are stranded before graph
wiring completes, so the reaper now closes only that isolated generated-spec
shape. Ordinary no-edge wisps remain report-only.

Stale workflow roots have a separate safe-close path. The reaper now matches
only non-message roots carrying workflow metadata (`gc.kind=workflow` or
`gc.formula_contract=graph.v2`), requires both old `created_at` and old
`updated_at`, requires no assignee, rejects roots with active non-message
dependents, and rejects any unresolved outgoing wisp or issue dependency edge.
Dry-run includes these roots in `would_close_wisps`; live mode closes only that
guarded root shape. This is intentionally not a generic age-only root closer.

Live inspection found another boundedness gap in session beads. Several old
session wisps were `state=asleep` with `sleep_reason=drained` but lacked
`slept_at`, so `gc session prune --state asleep` skipped them forever.
`PruneDetailed` now treats that legacy drained-asleep shape as eligible for
`StateDrained` pruning and falls back to the bead `UpdatedAt` timestamp only
when `slept_at` is missing, not malformed. The reaper's live path now runs
`gc session prune --state drained --before ${GC_REAPER_SESSION_STATE_PRUNE_AGE:-24h} --json`,
adds the result to `sessions-pruned`, and escalates nonzero prune
failures. Dry-run skips this mutating CLI path because `gc session prune` has no
preview mode today.

After the safe-close backlog drained, live `ga` and `mc` still exceeded the
open-wisp alert threshold during active workflow load even though the stale
non-message counts were low. The reaper anomaly now counts only non-message
wisps older than `GC_REAPER_MAX_AGE`; fresh active workflow rows no longer page
as leak evidence. Mail wisps keep their separate optional backlog threshold.
The `mol-dog-reaper` formula instructions now mirror that contract, including
the stale non-message SQL example and the current summary fields; a maintenance
test guards against drifting back to total-open-wisp alert wording.

`ga-vwnt1` fixed a deacon patrol formula leak source. The
`mol-deacon-patrol` final step used to pour the next patrol wisp, sleep for the
backoff interval, and only then burn the current wisp. A restart during that
sleep could strand the current patrol wisp open. Version 15 now resolves the
current wisp, pours and assigns the successor, burns the current wisp
immediately, then sleeps and re-enters `gc hook`; the backoff belongs to the
already-assigned successor instead of an open predecessor.

`ga-6pbt8` identified that `runControlDispatcherWithStoreAndConfig`
hard-quarantined every `ProcessControl` error except `ErrControlPending`.
That swallowed transient store/controller faults before the serve loop could use
`dispatch.IsTransientControllerError` to retry them. The dispatcher now returns
recognized transient controller errors without closing the control bead.
Deterministic hard errors, including malformed control graphs and unsupported
control kinds, still quarantine. If `ProcessControl` already terminally closed
the bead with `gc.final_disposition=controller_error`, the wrapper preserves
that disposition instead of re-closing the bead as `control_quarantined`.

`ga-m9zak` identified that the control-dispatcher serve loop could treat
permanently unadvanceable control work as genuine idle. A non-control bead in
the control work query was skipped before the normal dispatcher could quarantine
it, and a legacy oversized attempt-log error was counted and returned as nil
once the queue contained only that stranded item. The serve loop now sends every
queued bead to the dispatcher so unsupported kinds use the existing
`control_quarantined` disposition, and legacy oversized attempt-log failures
surface as command errors instead of being silently folded into idle backoff.

`ga-eld2x` identified a persisted route-key leak for graph-v2 workflow roots.
The authoring key `gc.run_target` is useful inside formulas, but the runtime
claim path reads the persisted delivery key `gc.routed_to`. Before this patch,
graph workflow roots could persist only `gc.run_target`; scale checks could
spawn a worker for the root, but the worker could not claim it, so the work
could sit open until idle cleanup. Both graph workflow decorators now stamp
`gc.routed_to` on roots. `gc doctor --fix` also has a
`run-target-routed-to-backfill` check that repairs existing workflow roots by
copying `gc.run_target` to `gc.routed_to` when the canonical key is missing.
The hook regression coverage now encodes that boundary: run-target-only roots
are legacy repair candidates, not claimable work-query routes.
The companion reader cleanup now removes `gc.run_target` fallback from runtime
pool demand, named demand, pool assignment release, store selection, pool
desired-state wake, and workflow-run API projection readers. The graph-v2 root
decorators no longer persist `gc.run_target` on new workflow roots; they stamp
only the canonical `gc.routed_to` delivery key.
Live legacy roots were backfilled on 2026-06-01T10:58:02Z by grouping exact
`ga.wisps` workflow-root IDs through the `gc ... bd update` wrapper with
`--set-metadata gc.routed_to=<run_target>`. The first repair covered 139 `ga`
roots that still had `gc.run_target` with empty `gc.routed_to` (129 closed, 6
in progress, 4 open). A follow-up all-database scan on 2026-06-01T11:13:41Z
found 13 more legacy roots in configured rig or legacy stores (`bd=2`, `gp=1`,
`gt=5`, `my_db=5`); those were backfilled as well. Post-repair SQL across
`bd`, `ga`, `gg`, `gp`, `gt`, `mc`, `my_db`, and `rig` found `0` workflow
roots with `gc.run_target` and missing `gc.routed_to`.

`TestPlainIdleOpenBeadCountsStayBounded` and
`TestGastownIdleOpenBeadCountsStayBounded` now run in Tier B nightly
acceptance. `.github/workflows/nightly.yml` schedules the Tier B job daily at
06:00 UTC and calls `make test-acceptance-b`; the Makefile target runs
`go test -tags acceptance_b -timeout 10m -v ./test/acceptance/tier_b/...`. The
plain-city test starts an isolated file-backed city with a fast exec order. The
Gastown test starts an isolated Gastown city with fake sessions, shortens the
patrol interval, adds a fast formula order and a fast exec order, and samples
open issue-tier and wisp-tier counts across repeated controller cycles. Local
runs keep the fast default window (`3s` warmup, `8` samples, `2s` interval).
The nightly workflow overrides that to `10s` warmup, `36` samples, and a `5s`
interval so the scheduled regression watches idle cities for roughly three
minutes. The tests fail if either open-count series grows beyond a small
transient jitter window after warmup.
The same long-window settings passed locally on 2026-06-01 with both probes:
`GC_IDLE_BEAD_STABILITY_WARMUP=10s GC_IDLE_BEAD_STABILITY_SAMPLES=36 GC_IDLE_BEAD_STABILITY_INTERVAL=5s go test -tags acceptance_b -timeout 12m ./test/acceptance/tier_b -run 'Test(PlainIdleOpenBeadCountsStayBounded|GastownIdleOpenBeadCountsStayBounded)$' -count=1`
completed in `420.809s`.

`ga-hiew1` split into two retention checks in this branch. First, the built-in
Dolt compactor order remains installed and dispatched through the managed Dolt
layout, so its `gc dolt compact` execution receives the live managed database
environment instead of poisoned shell overrides. Existing order/controller tests
cover that path. Second, `wisp gc` now treats expired closed graph-v2 workflow
roots as GC candidates by indexed workflow-root metadata
(`gc.kind=workflow` or `gc.formula_contract=graph.v2`) and validates matches
with `sourceworkflow.IsWorkflowRoot` before deleting the root closure. Open or
recent workflow roots are left alone, and roots matching both metadata queries
are de-duplicated.

The Dolt compactor also now accepts the order-dispatched explicit loopback
external target shape (`GC_DOLT_MANAGED_LOCAL=0`, local `GC_DOLT_HOST`, explicit
`GC_DOLT_PORT`) without requiring managed runtime state. Non-local external
targets still skip without querying. This covers cities such as
`/data/projects/maintainer-city`, where the canonical endpoint is a locally
managed-by-operator Dolt server on `127.0.0.1:3307`, not a `gc start` managed
runtime. When a database is already under compaction quarantine, the command now
prints the marker path, reason, and creation timestamp so the required manual
review artifact is visible from the failed run output. Explicit
`GC_DOLT_COMPACT_ONLY_DBS` entries also seed database discovery, so an operator
can target a database even when `gc rig list` times out and the local metadata
fallback misses that rig. Pending-push dry-run now validates marker shape and
freshness before claiming it would retry a remote push, matching the live retry
guard. Legacy pending-push markers that predate the full marker contract can
self-heal only when the script can re-derive a remote, active branch/refspec,
remote branch head, and prove that remote head is reachable from the local
Dolt log. Unverified legacy markers still stop before any force-push.

The compactor now also purges per-database `.dolt/git-remote-cache`
directories before quarantine and commit-threshold checks. Those directories
are rebuildable fetch caches, not canonical table storage, and live inspection
found they can dwarf the actual Noms data even when a database is far below the
flatten threshold. Default pending-push and pending-GC retry paths preserve the
cache until the remote repair has completed because the running Dolt SQL server
may need the existing cache to fetch or push. This cleanup is independent of
history flattening: dry-run reports the cache it would purge, while a live run
removes only that cache directory and can still skip flattening for
below-threshold databases. `GC_DOLT_COMPACT_REMOTE_CACHE_ONLY=1` stops
immediately after this rebuildable cache cleanup, so operators can reclaim cache
bloat without retrying pending remote pushes or touching quarantine/pending-GC
state.

Pending-push retry now also runs local `DOLT_GC('--full')` first when oldgen
archives are present. That lets a locally compacted database keep reclaiming
storage even if the remote repair remains blocked, while still avoiding another
flatten and preserving the pending-push marker until a remote fetch/push
succeeds.

When a preserved/rebuilt `.dolt/git-remote-cache` directory is missing the bare
`repo.git` expected by Dolt during `DOLT_FETCH`, the compactor now initializes
that bare repo only when the error path resolves under the database's
`git-remote-cache` root, then retries the fetch once. This keeps pending remote
repair recoverable after cache-only cleanup removes a stale cache directory, but
still refuses to create arbitrary paths from a malformed error message.

## Creation Paths

| Path | Beads opened | Bookkeeping owner |
| --- | --- | --- |
| `internal/molecule/graph_apply.go` graph workflow instantiation | Wisp root plus logical/step wisps. Non-root graph steps are linked to the root with `tracks`; explicit ordering uses `blocks`; legacy containment uses `parent-child`. | Normal workflow execution closes runnable steps. `molecule.CloseSubtree` closes owned descendants during explicit cleanup. `reaper.sh` now closes stale step leftovers when all reaper-owned dependency targets are closed, and closes stale inactive workflow roots only when root-specific dependency/dependent guards prove there is no live graph pressure. |
| `cmd/gc/order_dispatch.go` order dispatch | Ephemeral order-tracking bead labeled `gc:order-tracking`; wisp orders also create a molecule/wisp root via `molecule.Instantiate`. | `dispatchOne` defers `closeOrderTrackingBead`. Wisp roots are intentionally not auto-closed solely because descendants finish; the reaper handles stale roots/steps only when dependency evidence proves closure is safe. |
| `cmd/gc/dispatch_runtime.go` control-dispatcher serve loop | Graph-v2 control beads such as `check`, `drain`, `fanout`, `retry`, `retry-eval`, `scope-check`, and `workflow-finalize` drive workflow progression and may appear in the controller work query. | Routed control work now always reaches `runControlDispatcherWithStoreAndConfig`. Unsupported or misrouted kinds are quarantined with explicit `gc.control_quarantined` metadata; legacy oversized attempt-log failures return a visible serve error instead of masquerading as idle. |
| Graph-v2 helper paths in `internal/graphv2/invocation.go`, `internal/dispatch/drain.go`, `internal/dispatch/retry.go`, and `internal/dispatch/ralph.go` | Synthetic input convoys, drain-unit convoys, retry attempt/eval beads, and cloned ralph retry/check beads. | Control dispatch owns normal progression and terminal metadata. The synthetic convoys are linked with `tracks` so convoy close/check paths can close completed units; retry and ralph clones remain graph-owned work and fall under graph-v2 finalization plus stale dependency-edge cleanup if stranded. |
| `examples/gastown/packs/gastown/formulas/mol-deacon-patrol.toml` patrol loop | One root-only deacon patrol wisp per cycle. Each cycle pours the next patrol wisp and assigns it to the same deacon session. | Version 15 burns the current patrol wisp before the backoff sleep, then re-enters `gc hook` after sleeping. This keeps at most the successor wisp open across the backoff window. |
| Graph-v2 routing decorators in `internal/graphroute/graphroute.go` and `cmd/gc/cmd_sling.go` | Workflow roots plus routed child steps. `gc.run_target` remains a formula-authoring hint; `gc.routed_to` is the persisted claim key. | Patched roots now persist `gc.routed_to` so the runtime claim path can see them. Existing roots can be backfilled by `gc doctor --fix` through `run-target-routed-to-backfill`. |
| `cmd/gc/bead_policy_store.go` storage policy wrapper | Applies default ephemeral storage to wisp/order-tracking policies and no-history storage to session/wait/nudge policies. | Policy only selects storage tier; lifecycle is owned by the creating subsystem and maintenance scripts. |
| Session pool creation in `cmd/gc/build_desired_state.go` and lifecycle paths | Session beads, including generic ephemeral session beads for managed pools. | Session lifecycle/reconciler close or retire sessions. `reaper.sh` prunes closed `gm-*` session beads through `bd prune` and prunes terminal drained session states through `gc session prune`; orphan-sweep preserves live ephemeral session assignees. |
| `cmd/gc/cmd_wait.go` and `cmd/gc/nudge_beads.go` session wait/nudge queue | Wait beads labeled `gc:wait`; queued nudge beads labeled `gc:nudge`, including wait-delivery nudges. | Waits close through ready delivery, cancel, expire, or failure paths. Nudge dispatch marks terminal state and closes through `markQueuedNudgeTerminal`; session-close and wait-cancel paths withdraw queued wait nudges. |
| `internal/mail/beadmail` mail provider | Ephemeral message beads for sends and replies. | Mail is user/controller work, not stale non-message workflow debris. Reaper excludes messages from the non-message leak alert and tracks mail backlog through the separate mail-wisp threshold. |
| `cmd/gc/cmd_handoff.go` auto context handoff | PreCompact hooks call `gc handoff --auto "context cycle"`, opening a self-addressed system message so the compacted session can see the handoff. | Auto-handoff mail is now labeled `gc:auto-handoff` and `gc:archive-after-inject`. `gc mail check --inject` uses the local provider path for hook injection, then archives only messages carrying those labels after successful injection. Legacy unlabeled empty-body context-cycle mail can be audited and archived with the bounded `gc mail archive --all-recipients --subject-prefix "context cycle" --empty-body` selector. Ordinary user mail remains open until read/archive/delete. |
| `internal/extmsg/*_service.go` external-messaging projections | Task-typed mirror beads for transcript entries/state, bindings, memberships, groups, group participants, and delivery context. | These are projection/state rows. Binding, membership, and participant services close superseded or removed rows explicitly; transcript/state rows are retention data and should be bounded by an extmsg-specific retention policy, not generic wisp age cleanup. |
| `cmd/gc/convergence_store.go` convergence loop | `convergence` beads with `Status=in_progress` for manual convergence loops. | The convergence handler writes terminal metadata and state on approval, stop, or no-convergence. Reconcile paths repair partial/orphaned convergence state; these are not reaper-owned workflow wisps. |
| Manual/API task and convoy creators in `cmd/gc/cmd_bd_store_bridge.go`, `cmd/gc/cmd_handoff.go`, `cmd/gc/cmd_prompt.go`, `cmd/gc/cmd_sling.go`, `cmd/gc/cmd_convoy.go`, `internal/convoy/convoy.go`, `internal/api/huma_handlers_beads.go`, and `internal/api/huma_handlers_convoys.go` | User-visible issue-tier tasks, prompt-synthesis tasks, handoff tasks, auto-convoys, and explicit convoys, often with `tracks` dependencies. | User/controller workflow owns closure. These are not age-reaped unless they are wisp-tier stale closure candidates; convoy `check`/`autoclose` handles owned convoys whose tracked work is terminal. |

The creation-path table above was cross-checked against non-test
`Create(beads.Bead...)` call sites on 2026-06-01. The remaining unlisted
matches are storage wrappers (`bead_policy_store`, bead stores themselves) or
session aliases already covered by the session row (`adoption_barrier`,
`session_name_lookup`, `session_beads`).

## Cleanup Paths

| Path | Responsibility | Current status |
| --- | --- | --- |
| `examples/gastown/packs/maintenance/assets/scripts/reaper.sh` | Close stale non-closed wisps with closed dependency targets; close isolated generated step-spec debris; close stale inactive workflow roots with no live dependency pressure; purge old closed wisps; auto-close stale city issues; prune closed `gm-*` session beads; prune terminal drained session-state beads; escalate only stale non-message open-wisp backlog. | Patched for `parent-child`/`tracks`/`blocks` closure and purge protection through `wisp_dependencies`, plus a narrow unassigned `Step spec for ...` no-edge cleanup, guarded workflow-root cleanup, a `gc session prune --state drained` pass for legacy drained-asleep session rows, and a stale-only alert query so fresh workflow load is not reported as a reaper leak. |
| `examples/gastown/packs/maintenance/assets/scripts/wisp-compact.sh` | Promote old non-closed ephemeral beads for stuck detection and delete expired closed wisps. | Still separate from the safe-close decision. It must not become an age-only closer. |
| `internal/molecule/cleanup.go` | Close molecule subtrees by ownership metadata and parent-child descendants. | Handles explicit teardown, not abandoned workflow drift. |
| `cmd/gc/wisp_gc.go` / `wisp autoclose` | Close attached workflow roots and owned workflow beads from CLI-driven cleanup. Purge expired closed wisps, order-tracking beads, and closed graph-v2 workflow-root closures. | Patched to include workflow-root closure GC through indexed metadata queries guarded by `sourceworkflow.IsWorkflowRoot`. |
| `cmd/gc/dispatch_runtime.go` / `cmd/gc/cmd_convoy_dispatch.go` | Drain and execute graph-v2 control beads claimed by the control dispatcher. | The serve loop no longer pre-skips unexpected `gc.kind` values or suppresses legacy oversized attempt-log errors. Unexpected queued beads flow into the existing hard-error quarantine path, and oversized attempt-log errors stop the command with a named cause. |
| `cmd/gc/order_dispatch.go` | Close order-tracking beads after dispatch attempt completion. | Existing defer is the primary owner; stale tracking-bead bugs should be treated as order-dispatch defects. |
| `cmd/gc/cmd_order.go` / `cmd/gc/order_dispatch.go` order-tracking sweeps | Close orphaned or stale `gc:order-tracking` beads and stale order wisp subtrees. | Runs from the built-in `order-tracking-sweep` order and manual `gc order tracking sweep`; close reasons are stamped before close so stale/order-orphan cleanup is observable. |
| `cmd/gc/cmd_wait.go`, `cmd/gc/cmd_nudge.go`, and `internal/session/waits.go` | Close terminal wait beads and queued nudge beads. | Wait set/cancel/delivery/expiry paths call `setWaitTerminalState` and close the wait bead. Nudge terminal paths stamp `terminal_reason`, `commit_boundary`, and `terminal_at` before close; session close cancels outstanding waits. |
| `cmd/gc/cmd_mail.go` / `internal/mail/beadmail` auto-handoff injection cleanup | Archive system auto-handoff mail after it has been injected into the next provider hook context; provide an explicit all-recipient filtered archive path for legacy system-mail cleanup. | The inject path deliberately bypasses the supervisor API so the local provider can perform the archive side effect. The beadmail provider checks both system labels before deleting; ordinary injected mail still stays open. Filtered archive now requires either `--to` or `--all-recipients`, still requires a subject/from content filter, stays bounded by `--limit`, and can require an empty body before deleting legacy system mail. |
| `internal/extmsg/binding_service.go`, `internal/extmsg/group_service.go`, and `internal/extmsg/transcript_service.go` | Close superseded external-message bindings, memberships, and participants. | Projection cleanup is domain-specific: old bindings close on replacement/unbind, memberships close on leave, and group participants close on removal. Transcript entries and transcript state are retained projection history and need explicit retention policy if they ever become a growth source. |
| `cmd/gc/doctor_run_target_backfill.go` | Mechanical repair for workflow roots with `gc.run_target` but missing `gc.routed_to`. | New `gc doctor --fix` check backfills the canonical claim key without touching non-workflow beads or already-routed roots. |
| `examples/dolt/commands/compact/run.sh` | Bound Dolt storage by flattening high-commit databases, running full GC, retrying safe pending-push/pending-GC markers, and pruning rebuildable `.dolt/git-remote-cache` directories. | Patched so remote-cache cleanup runs before commit-count skips and before blocking quarantine markers, while preserving the cache during pending remote repair retries; pending-push retry runs local full GC when oldgen archives are present; missing bare remote-cache repos are initialized and fetched once when the path is safely under `.dolt/git-remote-cache`; dry-run reports exact cache and local-GC actions; cache-only mode reclaims cache bloat without retrying pending remote pushes. |

## Verification Snapshot

- `go test ./examples/gastown -run 'TestReaper(ClosesStaleInactiveWorkflowRoots|DryRunReportsWouldCloseStaleWorkflowRoots)$' -count=1`
  failed before the guarded workflow-root cleanup because the reaper emitted no
  root candidate query/update and dry-run reported `would_close_wisps:0`; it
  passed after the root-safe cleanup path was added.
- `go test ./examples/gastown -run 'TestReaper' -count=1` passed for the full
  reaper maintenance-script regression set.
- `go test ./examples/gastown -count=1` passed for the reaper, wisp-GC, and
  deacon patrol burn-before-backoff regressions.
- `go test ./examples/gastown -run TestDeaconPatrolNextIterationBurnsCurrentBeforeBackoff -count=1`
  failed before the `mol-deacon-patrol` version 15 change and passed after it.
- `go test ./internal/session -count=1` passed for the session prune timestamp
  and drained-asleep alias changes.
- `go test ./cmd/gc -run 'TestCmdSessionPrune|TestSessionActionJSONSchema' -count=1`
  passed for the session prune CLI surface.
- `go test ./examples/dolt -count=1` passed for the compactor endpoint,
  explicit target discovery, pending-push dry-run marker checks, and safe
  legacy pending-push marker recovery.
- `go test ./examples/dolt -run 'TestCompactScript(PurgesRemoteCacheBelowThresholdWithoutFlattening|DryRunReportsRemoteCacheWithoutRemoving)$' -count=1`
  failed before the remote-cache cleanup patch and passed after it.
- `go test ./examples/dolt -run 'TestCompactScriptRemoteCacheOnlySkipsPendingPushRetry$' -count=1`
  failed before `GC_DOLT_COMPACT_REMOTE_CACHE_ONLY` because a pending-push
  marker still triggered a remote push retry after cache purge, then passed
  after cache-only mode left the marker untouched and skipped push/fetch/flatten
  SQL.
- `go test ./examples/dolt -run 'TestCompactScriptPreservesRemoteCacheBeforePendingPushRetry$' -count=1`
  failed before the pending-repair cache-order patch because the compactor
  purged `.dolt/git-remote-cache` and the retry fetch failed before push; it
  passed after default pending-push retry preserved the remote cache and cleared
  the marker.
- `go test ./examples/dolt -run 'TestCompactScriptRunsLocalFullGCBeforePendingPushRetry$' -count=1`
  failed before the pending-push local-GC patch because a pending remote repair
  skipped full GC and left oldgen archives in place; it passed after the retry
  path reclaimed oldgen without flattening again and kept the marker when remote
  fetch still failed.
- `go test ./examples/dolt -run 'TestCompactScriptRepairsMissingGitRemoteCacheBeforePendingPushRetry$' -count=1`
  failed before the missing-cache repair because `DOLT_FETCH` could not open
  the expected `repo.git` under `.dolt/git-remote-cache`; it passed after the
  compactor initialized that bare cache repo, retried fetch, pushed the
  compacted branch, and cleared the pending-push marker.
- `go test ./examples/dolt -run 'TestCompactScript(RepairsMissingGitRemoteCacheBeforePendingPushRetry|PreservesRemoteCacheBeforePendingPushRetry|RunsLocalFullGCBeforePendingPushRetry|RemoteCacheOnlySkipsPendingPushRetry)$' -count=1`
  passed for the combined pending-push retry/cache-preservation/local-GC/cache-only
  regression set.
- `go test -tags acceptance_b -timeout 10m -v ./test/acceptance/tier_b -run TestGastownIdleOpenBeadCountsStayBounded`
  passed on 2026-06-01, proving the idle Gastown regression itself against the
  current branch. Nightly coverage is wired through
  `.github/workflows/nightly.yml` -> `make test-acceptance-b` -> the
  `acceptance_b` Tier B package, with nightly-only long-run overrides for the
  idle stability window.
- `go test -tags acceptance_b -timeout 4m ./test/acceptance/tier_b -run 'TestPlainIdleOpenBeadCountsStayBounded$' -count=1`
  failed before the plain-city probe used a valid provider name, then passed
  after the test initialized the plain city with `claude` while keeping the
  isolated session runtime fake.
- `go test -tags acceptance_b -timeout 5m ./test/acceptance/tier_b -run 'Test(PlainIdleOpenBeadCountsStayBounded|GastownIdleOpenBeadCountsStayBounded|IdleBeadStabilityProbeConfigReadsNightlyOverrides)$' -count=1`
  passed for the plain idle-city probe, the Gastown idle-pack probe, and the
  nightly idle-probe override parser.
- `go test ./internal/graphroute -run 'Test(DecorateGraphWorkflowRecipe_(SetsRootMetadata|RootStampsRoutedToForClaim)|StampLegacyRecipeRouting_RespectsPerStepRunTarget)$' -count=1`
  passed for graph workflow root route stamping.
- `go test ./cmd/gc -run 'Test(BatchOnGraphWorkflowStartsWorkflowWithoutRoutingChild|DefaultScaleCheckCountsIgnoresRunTargetOnlyPersistedWork|DefaultScaleCheckCountsAndNamedDemandIgnoresRunTargetOnlyReadyWork|FilterAssignedWorkBeadsForPoolDemandIgnoresRunTargetOnlyWork|StoreForPoolAssignment_IgnoresRunTargetForStoreRouting|ComputePoolDesiredStates_IgnoresRunTargetOnlyWakeDemand|RunTargetRoutedToBackfillCheck|InstantiateSlingFormulaGraphWorkflowPreservesRoutedTo|DoctorCheckNamesGolden|CmdHookIgnoresRunTargetOnlyRoot)$' -count=1`
  passed for the sling-side route stamping, doctor backfill path, hook
  boundary, and routed_to-only runtime reader cleanup.
- `go test ./cmd/gc -run 'TestRunWorkflowServe(DispatchesUnexpectedNonControlBeadAndProcessesLaterReady|DispatchesUnexpectedNonControlOnly|QuarantinesUnexpectedNonControlBead|ReturnsLegacyOversizedControlError)$' -count=1`
  failed before the serve-loop stranding patch because unexpected queued beads
  were skipped and legacy oversized attempt-log errors returned nil; it passed
  after unexpected beads were dispatched/quarantined and oversized errors
  surfaced.
- `go test ./cmd/gc -run 'TestRunWorkflowServe' -count=1` passed for the
  broader control-dispatcher serve-loop regression set.
- `go test ./cmd/gc -run 'Test(MailCheckInjectArchivesAutoHandoffMessages|MailCheckInjectLeavesTruncatedAutoHandoffMessages|RouteMailCheckInjectUsesLocalPathForArchiveSideEffects|CmdHandoffAutoSendsMailWithoutBlocking)$' -count=1`
  failed before the auto-handoff mail cleanup patch because injected
  `context cycle` messages stayed open and the routed inject path used the API;
  it passed after auto-handoff mail was labeled, local inject archived only
  those labels, ordinary injected mail remained open, and auto-handoff messages
  beyond the hook output truncation limit remained open until actually
  injected.
- `go test ./cmd/gc -run 'TestMailArchiveSelectedAllRecipientsEmptyBody$' -count=1`
  failed before the filtered archive selector because `mail archive` had no
  explicit all-recipient mode and no empty-body guard; it passed after
  `--all-recipients` became an explicit alternative to `--to` and the
  beadmail archive filter could require an empty body while preserving the
  existing subject/from content-filter and limit guards.
- Live route-key verification on 2026-06-01T11:13:41Z found `0` workflow roots
  with `gc.run_target` and missing `gc.routed_to` across `bd`, `ga`, `gg`,
  `gp`, `gt`, `mc`, `my_db`, and `rig`. Before repair, `ga` had `139` such
  roots (`129` closed, `6` in progress, `4` open), and the later all-database
  scan found `bd=2`, `gp=1`, `gt=5`, and `my_db=5`.
- `go test ./internal/api -run TestWorkflowProjectionTargetIgnoresRunTarget -count=1`
  passed for workflow-run API projection using the canonical delivery key.
- `go test ./internal/api -count=1` passed after updating order-feed workflow
  fixtures to use persisted `gc.routed_to`.
- `go test ./internal/dispatch -count=1` passed, preserving compile-time
  formula `gc.run_target` handling for fanout/control paths.
- `make dashboard-check` passed; generated dashboard TypeScript schema/types
  are in sync with the committed OpenAPI contract.
- `sh -n examples/dolt/commands/compact/run.sh`, `go vet ./...`, and
  `git diff --check` passed.
- `make test-fast-parallel` ran on 2026-06-01; `unit-core`, `cmd/gc` shards
  1/5/6, and the Darwin compile shard passed, while unrelated baseline
  `cmd/gc` shards 2/3/4 failed. The shard containing the new serve-loop tests
  did not fail those tests; its failure was the existing
  `TestCmdSlingDefaultFormulaDoesNotMaterializePoolSession` baseline. Log
  directory: `/data/tmp/gc-local-tests.SRvfiG`.
- `.githooks/pre-commit` ran with `core.hooksPath=.githooks`; lint-changed,
  generated docs/schema checks, `go vet ./...`, `unit-core`, `cmd/gc` shards
  1/5/6, and the Darwin compile shard passed, while the same unrelated
  baseline `cmd/gc` shards 2/3/4 failed. Latest log directory:
  `/data/tmp/gc-local-tests.hWuigJ`.

## Remaining Work

- Finish the companion rescue-drain bug `ga-ksno8`: required-artifact
  postcondition store errors must surface instead of consuming retry attempts.
  This code path is not present in this rebase-main worktree as of this report.
- Live dry-run evidence on 2026-06-01 against `/data/projects/maintainer-city`
  with `gc` shimmed to `/bin/true`:
  `reaper — stale_wisps:1397, closed_wisps:0, purged:0, sessions-pruned:0, closed:0, skipped_non_city_issues:0, mail_wisps:126, would_close_wisps:1235 (dry run)`.
- Direct SQL against the same Dolt server matched the dry-run first-pass
  close total: `bd=0`, `ga=1024`, `gg=0`, `gp=0`, `gt=81`, `mc=0`,
  `my_db=130`, `rig=0` for `1235` total. The first live run drained `gt`
  and `my_db` but proved `ga` mutation needed an explicit `USE ga;` before
  `UPDATE`; after that fix, a second live run closed `1031` `ga` wisps.
  Post-drain `ga` had `842` non-message open wisps but only `13` stale
  non-closed wisps older than 24h. The remaining above-threshold open count is
  dominated by fresh active workflow wisps from the non-idle live queue, not
  old reaper-eligible backlog.
- Live Dolt compactor dry-run evidence on 2026-06-01 against
  `/data/projects/maintainer-city` using the branch script and the
  order-dispatched explicit loopback external-target environment reached `mc`
  instead of skipping the endpoint. It then stopped on an existing integrity
  quarantine marker:
  `/data/projects/maintainer-city/.gc/runtime/packs/dolt/compact-quarantine/mc`
  with `reason=post-flatten value hash changed without row-count increase` and
  `created_at=2026-05-20T11:14:29Z`; the dry-run output now reports those marker
  details directly. That marker requires separate manual integrity review before
  live compaction or full GC can run for `mc`.
- Live session-bead inspection on 2026-06-01 found stale `mc` session wisps
  shaped as `state=asleep` plus `sleep_reason=drained` with missing `slept_at`;
  the current patch makes those rows closeable by the reaper's drained-state
  prune pass. A non-mutating SQL check at 2026-06-01T09:05:58Z found `0`
  drained-session candidates older than 24h by `updated_at` in both `ga` and
  `mc`, so no destructive prune was run. `mc` still has `21` created-old
  drained-asleep rows whose newest `updated_at` is `2026-06-01 00:40:42`;
  the reaper intentionally ages them from the terminal-state update timestamp.
- Live measurement at 2026-06-01T09:05:58Z: raw `status='open'` wisp counts
  were `ga=645` and `mc=575`, so `ga-k5ds4` remains open under its literal raw
  threshold AC. The stale leak surface was bounded: `ga` had `661`
  open/hooked/in-progress non-message rows but only `13` older than 24h; `mc`
  had `447` open/hooked/in-progress non-message rows plus `136` mail rows, with
  only `24` non-message rows older than 24h and `0` drained-session candidates
  older than 24h by `updated_at`. This is the evidence for changing the reaper
  page from total open non-message rows to stale non-message rows.
- Live re-measurement at 2026-06-01T09:54:06Z: raw `status='open'` wisp counts
  were `ga=576` and `mc=614`. The rows above 500 were still dominated by live
  workflow/mail load rather than reaper-eligible stale step wisps: `ga` open
  rows were `spec=444`, `task=129`, `convoy=3`, with only `task=9` and
  `convoy=3` older than 24h by `created_at`; `mc` open rows were `task=344`,
  `message=142`, `spec=89`, `session=39`, with only `message=78` and
  `session=24` older than 24h by `created_at`. This live city is not idle, so
  the raw threshold remains a live backlog acceptance gap rather than proof of
  an idle-city leak.
- Live re-measurement at 2026-06-01T13:08:59Z: raw `status='open'` wisp counts
  were still above the literal `ga-k5ds4` closure threshold on the busy live
  city (`ga=559`, `mc=737`). The stale non-message surface remained bounded
  (`ga=12`, `mc=24` older than 24h by `created_at`; `gt=0`, `my_db=0`), which
  continues to support the stale-only reaper alert boundary. This is not enough
  to close `ga-k5ds4` because its AC explicitly asks for raw open wisps below
  500 in both `ga` and `mc`.
- Stale stuck-root cleanup for `ga-k5ds4` is now implemented in the branch
  reaper and verified against the live `/data/projects/maintainer-city` Dolt
  server in dry-run mode. The guarded candidate query found nothing safe to
  close, so no live mutation was run from that path. Keep `ga-k5ds4` open until
  `ga` and `mc` are below the literal raw `<500` AC, or the AC is explicitly
  revised to the stale non-message invariant.
- Live route-key inspection and repair at 2026-06-01T10:58:02Z found `139`
  `ga.wisps` workflow roots with `gc.run_target` and missing `gc.routed_to`:
  `129` closed, `6` in progress, and `4` open. A later all-database scan at
  2026-06-01T11:13:41Z found 13 more legacy roots outside `ga`/`mc`: `bd=2`,
  `gp=1`, `gt=5`, and `my_db=5`. Registered rig stores were repaired through
  the `gc ... bd update` wrapper with
  `--set-metadata gc.routed_to=<run_target>`; the legacy `my_db` rows were
  repaired with the same scoped SQL predicate. Post-repair SQL found `0`
  matching rows across `bd`, `ga`, `gg`, `gp`, `gt`, `mc`, `my_db`, and `rig`.
- Branch reaper dry-run on the same live server after the stale-only alert
  patch reported `stale_wisps:115`, `mail_wisps:134`, `would_close_wisps:0`,
  and made no escalation mail call with `GC_REAPER_ALERT_THRESHOLD=500`.
- Branch reaper dry-run after the guarded stale workflow-root cleanup patch
  ran on 2026-06-01T13:37:24Z against the same live server with explicit
  `GC_DOLT_PORT=3307`. Direct read-only SQL found zero guarded workflow-root
  candidates in `ga`, `mc`, `gt`, `my_db`, `bd`, `gp`, `gg`, and `rig`; the
  script dry-run matched that with `stale_wisps:115`, `mail_wisps:156`, and
  `would_close_wisps:0`. Raw `status='open'` counts were still `ga=619` and
  `mc=730`, while stale non-message counts stayed bounded at `ga=13` and
  `mc=24`. No live reaper mutation was run because the new root path had no
  safe candidates to close.
- Live mail cleanup on 2026-06-01T14:41Z matched exactly `81` open `mc`
  message wisps titled `context cycle` with empty bodies using
  `gc mail archive --all-recipients --subject-prefix "context cycle" --empty-body --include-read --limit 200 --dry-run --json`.
  The live run archived the same bounded set, and the follow-up dry-run
  returned `count:0`.
- Latest read-only live measurement after that cleanup at 2026-06-01T14:41Z:
  raw `status='open'` wisp counts still exceeded the literal AC (`ga=574`,
  `mc=703`). `mc` open mail wisps dropped to `84`, and there were `0` open
  `context cycle` messages.
  Rows older than 24h were `ga=12` and `mc=51`. The remaining `mc` message
  backlog was advisory/human-visible mail (`Reaper anomalies`, `Dolt health`,
  `JSONL spike`, and a few one-off approval/blocker messages), not empty-body
  auto-handoff mail.
- Dolt compaction remains blocked for `mc` by
  `/data/projects/maintainer-city/.gc/runtime/packs/dolt/compact-quarantine/mc`
  (`post-flatten value hash changed without row-count increase`,
  `created_at=2026-05-20T11:14:29Z`). A branch dry-run with
  `GC_DOLT_COMPACT_ONLY_DBS=ga,mc` reached that marker and failed before any GC.
- `ga` has a stale pending-push marker at
  `/data/projects/maintainer-city/.gc/runtime/packs/dolt/compact-pending-push/ga`
  (`flatten and full GC succeeded but remote push failed`,
  `created_at=2026-05-16T18:03:26Z`). The marker is also legacy/incomplete: it
  contains no `remote=` field. After the legacy-marker recovery patch, a branch
  dry-run with `GC_DOLT_COMPACT_ONLY_DBS=ga` discovers the explicit `ga` target
  even though `gc rig list` times out, derives `remote=origin`,
  `local_branch=main`, `remote_branch=main`, proves remote head
  `7kon6u7jt09nhukq4urpqc598am91u5o` is in the local `ga` Dolt log, and exits
  cleanly with `pending_push=present — dry-run (would retry remote push)`. No
  mutating remote push was run from this report pass. A combined
  `GC_DOLT_COMPACT_ONLY_DBS=ga,mc` dry-run now exits nonzero only because of the
  remaining `mc` quarantine; `ga` reaches the recoverable dry-run retry path.
- Live remote-cache remediation on 2026-06-01T11:24:25Z used the branch
  compactor against `/data/services/gascity-local-dolt` with
  `GC_DOLT_MANAGED_LOCAL=0` and explicit `127.0.0.1:3307`. Dry-run first proved
  `gt` would only purge
  `/data/services/gascity-local-dolt/gt/.dolt/git-remote-cache` and then skip
  flattening at `133` commits. The live run purged that cache and left `gt`
  unchanged at `133` commits, `0` status rows, `15` issues, `1381` wisps, and
  database hash `3mkjngdd1bt41a7absg1acpngnkbkht4`. Additional live runs
  purged remote caches for `mc`, `bd`, `gp`, and `my_db`; `mc` still stopped on
  its integrity quarantine before any GC. Local Dolt storage dropped from about
  `21G` before the `gt` purge to `3.1G` after those cache purges.
- Live `ga` remote-cache remediation on 2026-06-01T11:37:55Z used
  `GC_DOLT_COMPACT_REMOTE_CACHE_ONLY=1` with `GC_DOLT_COMPACT_ONLY_DBS=ga`.
  Dry-run first reported it would purge
  `/data/services/gascity-local-dolt/ga/.dolt/git-remote-cache` and then skip
  compaction state checks. The live cache-only run purged that directory without
  retrying the pending remote push. Post-run SQL still showed `ga` clean at `0`
  status rows with `4789` commits and database hash
  `a7508lcjajm1sicm5qh0qvsdapdgev69`; the legacy pending-push marker remained
  present for an explicit future push decision. After this pass no
  `.dolt/git-remote-cache` directories remained under
  `/data/services/gascity-local-dolt`, and local Dolt storage was about `1.8G`
  total (`ga=1.2G`, `mc=498M`, `gt=33M`, `bd=34M`, `gp=46M`,
  `my_db=59M`).
- A direct live `CALL DOLT_FETCH('origin')` against `ga` after that cache-only
  purge failed because the running Dolt SQL server still referenced the purged
  git remote-cache path. No non-dry pending-push retry was run. The branch now
  preserves remote caches on default pending-push/pending-GC retries so future
  runs do not delete the cache immediately before a remote repair; the existing
  `ga` marker still requires an explicit remote-cache reinitialization or remote
  repair decision before retrying the stale push.
- Live `ga` pending-push local-GC remediation on 2026-06-01T11:57:16Z used the
  branch compactor with `GC_DOLT_DATA_DIR=/data/services/gascity-local-dolt`,
  `GC_DOLT_MANAGED_LOCAL=0`, and `GC_DOLT_COMPACT_ONLY_DBS=ga`. Dry-run first
  reported `pending_push oldgen_archives=present`. The live run completed
  `DOLT_GC('--full')` in `11s`, then attempted the existing remote repair and
  failed at `DOLT_FETCH('origin')` because the remote cache still needs
  reinitialization. The marker was preserved and upgraded with
  `remote=origin`, `expected_remote_head=7kon6u7jt09nhukq4urpqc598am91u5o`,
  `expected_remote_head_verified=1`, `local_branch=main`, and
  `remote_branch=main`; a subsequent dry-run now stops on the stale full marker
  age guard before any force-push. Post-run SQL showed `ga` clean at `0` status
  rows with `4793` commits and database hash
  `2od9635iqrpmfvs92gthl4bftgacr28j`. Local Dolt storage dropped to about
  `1.2G` total, with `ga=521M` and `mc=516M`; no `.dolt/git-remote-cache`
  directories remained.
- Current Dolt retention dry-run on 2026-06-01T13:37:24Z used the branch
  compactor with `GC_DOLT_COMPACT_ONLY_DBS=ga,mc`,
  `GC_DOLT_DATA_DIR=/data/services/gascity-local-dolt`, explicit loopback
  `127.0.0.1:3307`, and `GC_PACK_DIR` pinned to the branch Dolt pack. It
  reached both remaining guardrails and stopped without mutation:
  `mc` still has the integrity quarantine marker from
  `2026-05-20T11:14:29Z`, and `ga` still has the upgraded pending-push marker
  from `2026-05-16T18:03:26Z`, now rejected as stale by the marker age guard
  before any remote push retry. Current local Dolt storage was about `1.4G`
  total (`ga=653M`, `mc=545M`, `gt=33M`, `bd=34M`, `gp=50M`,
  `my_db=59M`). These are explicit manual-review blockers, not remaining
  compactor code paths.
  A 2026-06-01T14:08:45Z marker/storage recheck found the same two marker
  files still present; local Dolt storage remained about `1.4G` total
  (`ga=676M`, `mc=571M`, `gt=33M`, `bd=34M`, `gp=50M`, `my_db=59M`).
