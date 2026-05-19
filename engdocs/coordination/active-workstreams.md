# Active Workstream Coordination

Last updated: 2026-05-18 22:15 PT by Mabel

This is a temporary cross-agent coordination channel, not product documentation.
Do not merge this file into public docs unless we explicitly promote it.

Use this file for concise handoffs between active agents. Prefer factual state,
links, branch names, and explicit interface constraints over narrative.

Severity labels:

- `red`: blocks another workstream.
- `yellow`: coordinate before touching the affected area.
- `green`: informational.

## Attention Protocol

Every workstream handoff should include an attention block so agents can poll
this file without D. Box becoming the notification bus.

Use this shape:

```markdown
### Attention Needed

Needs Mabel: yes/no

Needs D. Box: yes/no

Urgency: red/yellow/green

Reason: short factual reason, or "none".
```

If a workstream is blocked on another agent, mark the urgency and name the
needed owner in `Reason`.

## Current Attention Summary

- `green`: JSON rollup is final machine-move ready at
  `gastownhall/gascity:codex/json-rollup` commit `82a6253d`; PR #2349 is open
  and labeled `status/reviewing`. Mabel has taken over mega wrap-up from
  Jasmine.
- `green`: Registry-gc-pack has Mabel's #2126 compatibility answer. #2126 does
  not add new registry-specific constraints beyond preserving `gc import
  migrate` until doctor parity, preserving legacy `gc pack fetch/list`, keeping
  current PackV2 import fields stable, and coordinating before compatibility
  behavior changes.
- `green`: gc4gc / Operational Substrate is portable through
  `https://github.com/donbox/gc4gc`; stable and producer/dev branches are
  published separately.
- `green`: Registry-gc-pack is final machine-move ready at
  `gastownhall/gascity:codex/pack-registry-workstream` commit `f82f3c4e`.
  Cleo verified that meaningful registry/gc pack work is pushed, old feeder
  branches are superseded or disposable, and the new machine does not need old
  fan-out worktrees. Mabel re-ran the targeted readiness matrix on the old
  machine at this checkpoint and found no validation blockers. Draft PR #2351
  is open for visibility but is not queued for review.
- `green`: Registry/gc pack design PR #2119 is closed as superseded by #2351.
  Landed #2129 remains the explicit `[[exports]]` design source; #2351 does
  not implement `[[exports]]`.
- `green`: Jasmine's JSON work has a final machine-move checkpoint; the new
  machine does not need old JSON fan-out worktrees.
- `yellow`: Mabel's coordination state is portable through this branch, but the
  new machine should bootstrap from this file before resuming pack work.

## Machine Move Readiness

### Current State

This file is the canonical handoff for moving active Gas City pack/package work
to a new machine.

Mabel / coordination state:

- Source of truth: `gastownhall/gascity:codex/workstream-coordination`.
- Coordination file: `engdocs/coordination/active-workstreams.md`.
- Current branch is not a product PR and should not be merged unless explicitly
  promoted.
- Mabel can resume from this file plus live GitHub PR state.

Known portable workstreams:

- JSON: `gastownhall/gascity:codex/json-rollup` is final machine-move ready at
  pushed commit `82a6253d`; review PR is #2349.
- Registry/gc pack: `gastownhall/gascity:codex/pack-registry-workstream`
  is final machine-move ready at pushed checkpoint `f82f3c4e`.
- gc4gc: `donbox/gc4gc:master`, `donbox/gc4gc:codex/gc4gc-producer-dev`, and
  `donbox/gc4gc:codex/gc4gc-producer-snapshot-20260518` exist.
- Pack deprecation: #2126 is the source of truth for the deprecation train.
- Docs/source reconciliation: #2318 is the source of truth for PackV2 docs
  source reconciliation.

Remaining move-readiness asks:

- Mabel: monitor PR #2349 review/merge pipeline state. Required CI is green;
  a non-required Container Scan image-vulnerability check is currently failing
  and should be treated as repo/baseline security signal unless Julian's merge
  pipeline marks it blocking.
- Grace: no blocking ask; gc4gc is portable.
- Penelope: intentionally separate on another machine.

### New Machine Bootstrap For Mabel / Coordination

```sh
mkdir -p /Users/dbox/repos/gc
cd /Users/dbox/repos/gc

git clone https://github.com/gastownhall/gascity.git gascity-workstream-coordination
cd gascity-workstream-coordination
git fetch origin codex/workstream-coordination
git switch codex/workstream-coordination

sed -n '1,220p' engdocs/coordination/active-workstreams.md
```

Suggested first prompt for Mabel on the new machine:

```text
Mabel, resume Gas City pack/package coordination from:

- repo: /Users/dbox/repos/gc/gascity-workstream-coordination
- branch: codex/workstream-coordination
- file: engdocs/coordination/active-workstreams.md

Please read the coordination file, refresh live PR/branch state for #2126,
#2318, #2129, #2349, #2351, Cleo's registry/gc pack workstream, Penelope's
reuse/customization notes if available, and Grace's gc4gc handoff, then tell me
where we are and what is safe to do next on this machine. Treat #2119 as
closed/superseded by #2351.
```

## Communication Mechanism

Chosen mechanism: repo-backed coordination branch.

- Repository: `gastownhall/gascity`
- Branch: `codex/workstream-coordination`
- File: `engdocs/coordination/active-workstreams.md`

Agents should fetch this branch when they need the latest shared coordination
state. Agents may propose updates on their own branches or directly update this
coordination branch when asked, but this branch is not a product PR.

## Workstream Handoff

### Workstream

JSON

### Current Branch / PR

Branch: `codex/json-rollup`

PR: #2349 <https://github.com/gastownhall/gascity/pull/2349>

Base: `origin/main`

Owner: Mabel, inherited from Jasmine

Worktree: `/Users/dbox/repos/gc/gascity-json-rollup`

### Latest State

Mabel owns JSON rollout wrap-up from Jasmine's handoff. The previous
many-small-PR strategy is replaced by a single JSON rollup / review-train PR
so Julian can review one coherent `gc --json` / `--json-schema` surface
instead of many small PRs.

`codex/json-rollup` is pushed through commit `82a6253d` (`feat: add json
output for session order actions`) and is final machine-move ready. PR #2349 is
open and labeled `status/reviewing`. Live GitHub state refreshed by Mabel on
2026-05-18 PT shows required CI green, no outstanding requested reviewers, and
merge state still blocked by review/merge-pipeline state rather than known
branch test failures. One non-required Container Scan image-vulnerability check
is failing.

Mabel dequeued the individual JSON feeder PRs on 2026-05-18 PT by removing
`status/reviewing` and prepending a superseded-by-#2349 banner to each PR body.
They remain open as provenance until #2349 lands.

Current JSON source of truth is this workstream section plus
`codex/json-rollup`, not any individual JSON PR.

### PRs In Play

- #2349, <https://github.com/gastownhall/gascity/pull/2349>: active JSON
  rollup PR, open, labeled `status/reviewing`.
- Feeder PRs #2222, #2250, #2256, #2257, #2258, #2259, #2265, #2266, #2267,
  #2270, #2271, #2273, #2274, #2287, #2288, #2291, and #2317: superseded by
  #2349, removed from `status/reviewing`, left open only as provenance until
  #2349 lands.

### Immediate Next Step

Track #2349 through review/merge. Do not close feeder PRs until #2349 merges.

### Victory Checklist

- #2349 remains required-CI green and merges.
- Superseded feeder PRs are closed/abandoned with a short pointer to #2349.
- Latest `main` is smoke-tested against gc4gc for the JSON commands Jasmine
  already validated.
- Coordination handoff marks JSON rollout landed.
- Any remaining command gaps become ordinary follow-up issues, not blockers for
  the rollout train.

### Explicit Non-Goals / Deferred Work

- Do not revive individual JSON feeder PRs unless #2349 fails and we explicitly
  change strategy.
- Do not introduce `--format json`.
- Do not require every command to support structured failure JSON before this
  train lands; failure JSON is staged command-by-command.

Included provenance PRs already incorporated into `codex/json-rollup`:

- #2317: schema-platform plumbing plus native management action JSON.
- #2222: session detail JSON plus oddball/root command JSON.
- #2250: formula/order inspection JSON.
- #2257: convoy inspection JSON.
- #2258: agent/rig routing inspection JSON.
- #2259: mail/trace/events inspection JSON.
- #2265: miscellaneous inspection command JSON.
- #2266: runtime/nudge/drain inspection JSON.
- #2267: doctor diagnostics JSON.
- #2270: session mutation action JSON.
- #2271: lifecycle action summary JSON.
- #2273: graph/converge/order/formula action summary JSON.
- #2274: convoy/mail action summary JSON.
- #2287: open passthrough/custom schema support.
- #2291: session/order action JSON.
- #2256: service/skill inspection JSON, incorporated after preserving the
  already-merged `skill list --json` contract and taking the service additions.

Integration fixes currently added directly on the rollup:

- Removed a duplicate `OrderFiringCurrentCheck.WarmupEligible` method that
  blocked package builds after combining branches.
- Aligned the `version --json` test with the preserved `versionJSONResult`
  payload name.
- Preserved the existing `skill list --json` payload (`count` / `entries`)
  instead of switching to the alternate #2256 shape (`city_path` / `skills`).
- Suppressed deprecated config warnings during chat auto-suspend config load so
  auto-suspend tests are not polluted by unrelated stderr.

Excluded / superseded by the first train:

- #2288: superseded by #2317's adoption branch payload.
- The individual JSON PRs listed above have been marked superseded and removed
  from `status/reviewing`. They should be abandoned/closed once #2349 is
  accepted, because their payload is incorporated into the rollup.
- Old local JSON fan-out worktrees are no longer needed for machine continuity;
  keep only if someone wants them for archaeology before deleting.

### Interface Contracts Other Agents Must Honor

- Human-readable output remains default.
- `--json` emits deterministic machine-readable output.
- stdout must be JSON-only when `--json` is used.
- Human diagnostics and warnings go to stderr unless intentionally represented
  in JSON.
- `--json-schema` exposes command schema metadata. The role-specific form
  `--json-schema=result` is accepted for result schemas.
- Result schemas live under `schemas/<command-path>/result.schema.json`.
- Shared failure schema lives at `schemas/failure.schema.json`.
- Do not introduce `--format json`.

### Attention Needed

Needs Mabel: yes

Needs D. Box: no

Urgency: yellow

Reason: first-rollup scope is assembled, pushed, PR-visible, and validated
enough for machine move. Mabel is now tracking #2349 through review/merge and
has dequeued the superseded feeder PRs. Mabel will close/abandon those feeder
PRs only after #2349 merges.

Structured failure JSON policy:

- New JSON-enabled commands should use the shared failure schema where the
  platform path applies.
- Full structured failure JSON for every command is staged command-by-command,
  not a reason to block otherwise clean result-schema work.
- Commands with intentional command-authored nonzero JSON must preserve that
  behavior and declare compatible schemas/tests.

Schema extension conventions:

- JSON Schema remains the schema language.
- Gas City extensions use `x-gc-*`.
- `x-gc-jsonl` remains the convention for JSONL record-count metadata. Absence
  means a single JSON document unless command docs/schema say otherwise.
- Keep schemas open where the producer is a passthrough or custom command and
  Gas City does not own the payload shape.

Validation matrix for `codex/json-rollup`:

- `git diff --check`: passed.
- `make fmt-check`: passed.
- `make vet`: passed.
- `make check-docs`: passed.
- `GOOS=linux make lint`: passed.
- `go test ./cmd/gc -run 'TestJSON|Test.*JSON|TestJSONSchema|TestJSONSchemaManifest|TestJSONCommandOutputMatchesDeclaredResultSchema|TestDo.*JSON|Test.*JSONOutput' -count=1`: passed.
- `go test ./cmd/gc -run 'TestAutoSuspendChatSessions|TestSkill|TestService|TestMail|TestConvoy|TestConverge|TestGraph|TestOrder' -count=1`: passed.
- `go test ./cmd/gc -run 'TestSessionAction|TestSession.*JSON|TestSession.*Submit|TestSession.*Nudge|TestOrder.*JSON|TestOrderRun|TestOrderCheck|TestJSONSchema|TestAutoSuspendChatSessions' -count=1`: passed.
- `go test ./cmd/gc -count=1`: failed locally on baseline/environment
  failures that also reproduce on clean `origin/main` in this environment:
  `TestRealizePoolDesiredSessions_ParallelizesDistinctAliasCreates`,
  `TestRunStartDriftCheck_RestartReturnsContinue`,
  `TestFileOpenedByAnyProcessUsesUnixSocketTableForStaleSocket`,
  `TestFinalizeInitCanonicalizesBdStoreBeforeProviderReadinessBlock`, and
  `TestFinalizeInitCanonicalizesBdStoreBeforeProviderReadinessBlockWithoutSkip`.
- `gc4gc` smoke tests: passed for JSON parseability and empty stderr on
  `status --json`, `status --json-schema`, `session list --json`,
  `rig list --json`, `formula list --json`, and `order list --json`.

Local-only JSON work state:

- The rollup branch is pushed at `origin/codex/json-rollup` through `82a6253d`.
- No meaningful JSON code changes are local-only.
- All intended first-rollup feeder branches are either incorporated or
  superseded by #2349.
- The new machine does not need old JSON fan-out worktrees to continue the
  rollout.

### Blockers / Cross-Workstream Risks

- `green`: Registry/gc pack command schemas/tests can use the #2349 contract as
  the current JSON baseline.
- `yellow`: Pack-defined commands may eventually need schema discovery rules;
  flag pack-facing schema changes to Jasmine rather than patching JSON rollout
  branches directly.
- `yellow`: Do not introduce `--format json` or command-specific schema
  discovery conventions in registry work.
- `yellow`: If registry commands need JSON schemas before the rollup lands, use
  `schemas/<command-path>/result.schema.json`, shared failure schema
  compatibility, and `x-gc-jsonl` for JSONL record-count metadata.

### Needed From Other Agents

- Mabel: monitor PR #2349 CI/review and remediate any branch-related failure.
- Mabel: after #2349 merges, close/abandon the superseded feeder PRs with a
  short pointer to #2349, then smoke latest `main` against gc4gc and mark JSON
  landed here.
- Cleo: flag any registry/gc pack command schema needs before freezing command
  output shapes.
- Jasmine: no blocking help needed unless #2349 surfaces a branch-specific
  regression that needs original rollout context.

### Last Updated

2026-05-18 22:05 PT by Mabel

### New Machine Bootstrap

Repos to clone:

- `gastownhall/gascity`
- `gastownhall/gc4gc` or the available local equivalent for smoke testing, if
  needed.

Branches to fetch / checkout:

- `origin/main`
- `origin/codex/workstream-coordination`
- `origin/codex/json-rollup`

Worktrees to create:

- `/Users/dbox/repos/gc/gascity-workstream-coordination` on
  `codex/workstream-coordination`.
- `/Users/dbox/repos/gc/gascity-json-rollup` on `codex/json-rollup`.

Local-only state:

- None for JSON rollup code through pushed commit `82a6253d`.
- No old JSON fan-out worktree is needed to continue the rollout on a new
  machine.

Commands to validate setup:

```sh
git -C /Users/dbox/repos/gc/gascity-workstream-coordination status --short --branch
git -C /Users/dbox/repos/gc/gascity-json-rollup status --short --branch
git -C /Users/dbox/repos/gc/gascity-json-rollup fetch origin --prune
git -C /Users/dbox/repos/gc/gascity-json-rollup log --oneline -1
gh pr view 2349 --json number,state,mergeStateStatus,labels,statusCheckRollup
```

Old-machine worktrees safe to ignore:

- All individual JSON shard worktrees after #2349 exists and `codex/json-rollup`
  is fetched at `82a6253d` or later.
- Deleted/gone provenance branches for already-merged or superseded JSON PRs.

Old-machine worktrees that must not be deleted yet:

- `/Users/dbox/repos/gc/gascity-workstream-coordination`
- `/Users/dbox/repos/gc/gascity-json-rollup` only if this machine will continue
  monitoring/remediating PR #2349 before the new machine takes over.
- Any Cleo/Mabel/Penelope pack worktrees they own.

Exact first prompt for Jasmine on a new machine:

> Jasmine, continue the JSON rollup from
> `engdocs/coordination/active-workstreams.md` on
> `origin/codex/workstream-coordination`. Clone/fetch `gastownhall/gascity`,
> create worktrees for `codex/workstream-coordination` and
> `codex/json-rollup`, then continue monitoring PR #2349 from pushed commit
> `82a6253d`. Refresh CI/review state, remediate branch-related failures, and
> treat the old individual JSON PRs/branches as superseded by #2349 unless
> explicitly reopened.

## Workstream Handoff

### Workstream

Pack Deprecation

### Current Branch / PR

Branch: `codex/packv2-wave2-goodbye-packv1`

PR: #2126, <https://github.com/gastownhall/gascity/pull/2126>

Base: `main`

Owner: Mabel / relevant implementation agents

### Latest State

#2126 is the source of truth for PackV1/PackV2 deprecation enforcement. It is
green and mergeable as of this update. It should remain conceptually separate
from registry/gc pack implementation.

Related docs/source reconciliation:

- #2318, <https://github.com/gastownhall/gascity/pull/2318>

#2318 has been upmerged with `main` and pushed at `37202b2f`. It now removes
the moved PackV2 migration page from public docs navigation, points
engineering/historical references at `engdocs/design/packv2`, and validates
locally with docsync plus focused config/logutil/cmd tests.

### PRs In Play

- #2126, <https://github.com/gastownhall/gascity/pull/2126>: active
  PackV1/PackV2 deprecation enforcement PR.
- #2318, <https://github.com/gastownhall/gascity/pull/2318>: related
  docs/source reconciliation PR; separate from #2126 because it fixes source of
  truth and public-doc placement rather than runtime behavior.

### Immediate Next Step

Track #2126 through review/merge. Keep #2318 moving independently as the docs
source-of-truth cleanup.

### Victory Checklist

- #2126 merges with the agreed hard-error/warning/remediation behavior.
- Legacy pre-PackV2 constructs are no longer normalized as current authoring
  guidance by writers, generated docs, or in-repo content.
- Remediation messages remain actionable for hard-failed constructs.
- `gc import migrate` is not removed or replaced until doctor / `doctor --fix`
  parity exists for the existing migrate corpus.
- Any deferred doctor/remediation parity gaps are tracked explicitly.

### Explicit Non-Goals / Deferred Work

- Do not fold registry/gc pack implementation into #2126.
- Do not make #2126 implement the registry surface, JSON rollout, or explicit
  `[[exports]]`.
- Do not remove `gc import migrate` in this train.

### Attention Needed

Needs Mabel: no

Needs D. Box: no

Urgency: green

Reason: Mabel confirmed that #2126 does not introduce new hard constraints for
registry/gc pack beyond the compatibility invariants listed below.

### Interface Contracts Other Agents Must Honor

- Do not remove or change `gc import migrate` semantics until doctor /
  `doctor --fix` parity exists for the migrate corpus.
- No new `gc pack` replacement command for `gc import migrate`.
- Remediation messaging must remain actionable for hard-failed legacy
  constructs.
- Coordinate before changing legacy `gc pack fetch` or `gc pack list`
  compatibility.
- Preserve current PackV2 import fields for now: `source`, `version`,
  `export`, `transitive`, and `shadow`.

### Blockers / Cross-Workstream Risks

- `red`: Removing `gc import migrate` before doctor parity would break the
  migration contract.
- `yellow`: Registry/gc pack work may touch compatibility messaging around
  `gc import` and legacy `gc pack` commands; coordinate before changing those
  behaviors.
- `green`: Pack deprecation can proceed independently from registry/gc pack as
  long as compatibility invariants are preserved.

### Needed From Other Agents

- Cleo: keep deprecation/remediation changes out of the registry workstream
  unless a compatibility invariant directly affects canonical `gc pack`
  behavior.
- Jasmine: flag if JSON diagnostics or stderr behavior affects deprecation
  warning/error tests.

### Last Updated

2026-05-18 20:22 PT by Mabel

## Workstream Handoff

### Workstream

PackV2 Docs / Source Reconciliation

### Current Branch / PR

Branch: `codex/packv2-doc-source-reconcile`

PR: #2318, <https://github.com/gastownhall/gascity/pull/2318>

Base: `main`

Owner: Mabel

### Latest State

#2318 is the active docs/source reconciliation PR. It moves obsolete or
transitional PackV2 material out of public docs and into engineering docs, so
future agents and users do not treat old design notes as current product
guidance.

### PRs In Play

- #2318: open, non-draft, labeled `status/reviewing`, currently the source of
  truth for PackV2 docs/source reconciliation.

### Immediate Next Step

Track #2318 through review/merge. Keep it separate from #2126 and #2349.

### Victory Checklist

- #2318 merges.
- Public docs no longer expose stale PackV2 design/rollout notes as current
  product guidance.
- Engineering PackV2 historical/design material lives under
  `engdocs/design/packv2` with clear status.
- Generated config/schema references and fatal/troubleshooting links point at
  current public docs or engineering docs intentionally.
- Docsync and focused config/logutil/cmd validation remain green.

### Explicit Non-Goals / Deferred Work

- #2318 does not implement deprecation enforcement; that is #2126.
- #2318 does not implement JSON behavior; that is #2349.
- #2318 does not implement registry/gc pack; that is #2351.
- #2318 does not implement explicit `[[exports]]`; #2129 is landed design for
  future implementation.

### Cross-Workstream Dependencies

- #2126 should remain the behavior source for deprecation enforcement.
- #2351 should reference current docs/design paths after #2318 lands.
- Pack reuse/customization docs should avoid using moved engineering notes as
  user-facing guidance.

### Last Updated

2026-05-18 22:15 PT by Mabel

## Workstream Handoff

### Workstream

Registry-gc-pack

### Current Branch / PR

Branch: `codex/pack-registry-workstream`

PR: draft #2351, <https://github.com/gastownhall/gascity/pull/2351>

Base: `upstream/main` / `gastownhall/gascity@03c80562`

Owner: Cleo

Current implementation worktree:

- Worktree: `/Users/dbox/repos/gc-pr2119`
- Current branch: `codex/pack-registry-workstream`
- Pushed branch: `gastownhall/gascity:codex/pack-registry-workstream`
- Current checkpoint commit: `f82f3c4e`
- State: clean and pushed after registry hardening, first `gc pack`
  dependency-command bridge, docs/reference update, and doctor guard for
  durable `registry:` selectors.
- Machine-move readiness: ready.

Older local branches have been inspected and are not required by the new
machine:

- `codex/pack-registry-1a-core`: superseded. Its unique commits are the old
  PR #2119 design-doc commits plus old JSON-platform branch commits. The
  workstream contains the newer design docs and uses the merged/current JSON
  baseline instead.
- `codex/pack-registry-mainline`: disposable for active registry/gc pack work.
  It has no unique commits relative to the workstream.
- `codex/pack-registry-latest-main`: disposable for active registry/gc pack
  work. It has no unique commits relative to the workstream.

### Latest State

Cleo will maintain one long-lived registry/gc pack workstream branch for
several days rather than preparing small immediate review PRs. Registry
operations still come first inside that workstream.

The registry/gc pack source of truth is now
`gastownhall/gascity:codex/pack-registry-workstream`.

### PRs In Play

- #2351, <https://github.com/gastownhall/gascity/pull/2351>: draft registry/gc
  pack implementation PR and current source of truth.
- #2119, <https://github.com/gastownhall/gascity/pull/2119>: closed as
  superseded by #2351.
- #2129, <https://github.com/gastownhall/gascity/pull/2129>: merged explicit
  `[[exports]]` design note; related design input, not implemented by #2351.
- #2318, <https://github.com/gastownhall/gascity/pull/2318>: related PackV2
  docs/source reconciliation PR.

### Immediate Next Step

Decide when to move #2351 from draft/review-train staging into active review.
Until then, keep it visible but unqueued.

### Victory Checklist

- #2351 is converted out of draft when the review queue is ready.
- #2351 CI and review are green, then #2351 merges.
- Registry/gc pack validation matrix passes at final review checkpoint.
- JSON/schema behavior remains compatible with #2349 and does not invent
  command-specific schema conventions.
- Legacy `gc import`, legacy `gc pack fetch/list`, and current PackV2 import
  fields `source`, `version`, `export`, `transitive`, and `shadow` do not
  regress.
- `gc import migrate` is not removed, replaced, or redirected to a new `gc pack`
  command until doctor / `doctor --fix` parity exists.
- A follow-up issue exists for explicit `[[exports]]` implementation from
  #2129.

### Explicit Non-Goals / Deferred Work

- #2351 does not implement #2129 explicit `[[exports]]`.
- #2351 does not replace `gc import migrate`.
- Dependency `gc pack show`, `gc pack outdated`, and canonical dependency
  `gc pack list` are deferred; existing `gc pack list` remains the legacy
  `[packs]` status command.
- Do not fold PackV2 deprecation enforcement into #2351 except for explicit
  compatibility checkpoints.

Mabel refreshed live state on 2026-05-18 PT:

- `gastownhall/gascity:codex/pack-registry-workstream` exists at `f82f3c4e`.
- The old-machine worktree is clean at `f82f3c4e`.
- The branch is six commits ahead of `gastownhall/main` and not behind it as
  of `gastownhall/main@03c80562`.
- Draft PR #2351 is open for `codex/pack-registry-workstream`; it is visible
  for discussion/handoff but intentionally not labeled for review yet.

Dirty/unpushed work has been migrated and pushed. No meaningful local-only
registry work should remain on the old machine.

Final machine-move checkpoint:

- `codex/pack-registry-workstream` contains all meaningful current registry/gc
  pack work Cleo intends to keep.
- All meaningful local-only registry/gc pack work is pushed to
  `gastownhall/gascity:codex/pack-registry-workstream`.
- Feeder branches are either superseded or disposable as listed above.
- The new machine does not need old fan-out worktrees to continue the
  workstream.
- The old `/Users/dbox/repos/gc-pr2119` worktree should be kept only until the
  new machine validates checkpoint `f82f3c4e`.

Completed inside the workstream since the preservation checkpoint:

- Registry operations hardening: remote-cache trust boundary, per-registry
  cache locking, safer config/cache add ordering, source validation on
  hand-edited `registries.toml`, and richer registry schema conformance tests.
- `gc pack add/remove/sync/upgrade/why` command bridge. `add` supports registry
  selectors and writes concrete durable import sources while preserving
  registry/ref/hash metadata in `packs.lock`.
- `gc pack check` thin wrapper over the existing import-state verification
  path.
- Product-surface boundary finalized: dependency `gc pack show`,
  `gc pack outdated`, and canonical dependency `gc pack list` are explicitly
  deferred. Existing `gc pack list` remains the legacy `[packs]` status command.
- Legacy `gc pack fetch/list` remain on the old `[packs]` behavior and have
  regression coverage.
- `gc import` output remains stable through shared handlers; it has not entered
  warning/removal mode.
- `gc doctor` now flags durable `registry:` sources with remediation text.

Next milestone:

- Keep draft PR #2351 visible while #2349 and other active review trains move.
  When D. Box/Mabel decide the registry/gc pack train should enter review,
  convert #2351 out of draft and apply the appropriate review-routing label.
  Mabel's refreshed validation found no branch-readiness blocker; Cleo
  completed the final product-surface pass and pushed the result.

### Attention Needed

Needs Mabel: yes

Needs D. Box: no

Urgency: yellow

Reason: Draft PR #2351 is open for visibility but not queued. Mabel should
monitor sequencing and decide when to convert it to ready-for-review after the
current JSON/deprecation trains move.

### Interface Contracts Other Agents Must Honor

- Registry operations land first.
- Dependency mutation must not race ahead of registry config/catalog
  correctness.
- Preserve current PackV2 import fields: `source`, `version`, `export`,
  `transitive`, `shadow`.
- Do not implement #2129 `[[exports]]` in this workstream; treat it as design
  input/future direction.
- Registry handles such as `main:lighthouse` are command-time selectors only.
- Durable `pack.toml` imports must store concrete `source` plus optional
  `version`, not `registry:<registry>:<pack>`.
- Lock/cache internals may preserve registry/ref/commit/hash metadata.
- Preserve `gc import` compatibility and legacy `gc pack fetch/list`
  compatibility.
- `gc import migrate` has no `gc pack` replacement; doctor / `doctor --fix`
  must reach parity before removal.
- Compose with Jasmine's JSON rollup conventions once stable.

File ownership boundaries for Cleo's workstream:

- Cleo owns new registry/gc pack implementation files and tests:
  `internal/gchome`, `internal/packregistry`, `internal/packsource`,
  packman registry/hash/lock additions, `cmd/gc/cmd_pack.go`,
  `cmd/gc/cmd_pack_registry*_test.go`, and `schemas/pack/**`.
- Other agents should not edit `gc pack registry` command behavior or
  `schemas/pack/**` without coordinating here first.
- Pack deprecation agents may edit deprecation docs/doctor surfaces, but should
  coordinate before touching `gc import`, legacy `gc pack fetch/list`, or
  shared PackV2 import semantics.

### Blockers / Cross-Workstream Risks

- `red`: Do not base registry command JSON/schema tests on an unstable or
  superseded JSON branch without Jasmine confirmation.
- `red`: Do not change `gc import migrate` removal semantics in registry work.
- `yellow`: Coordinate with Pack Deprecation before changing legacy `gc pack`
  `fetch/list` behavior.
- `yellow`: Coordinate with Jasmine before freezing registry command JSON
  schemas or failure behavior.
- `green`: Registry/gc pack overlap with Pack Deprecation is small and should
  be managed through compatibility checkpoints, not branch merging.

### Needed From Other Agents

- Jasmine: confirm JSON rollup branch and schema/failure conventions.
- Mabel: keep Pack Deprecation source-of-truth visible and flag compatibility
  drift.
- Mabel: answered. #2126 introduces no additional registry-specific hard
  constraints beyond preserving `gc import migrate` until doctor parity,
  preserving legacy `gc pack fetch/list`, preserving current PackV2 import
  fields, and coordinating before compatibility behavior changes.
- Cleo: continue from `f82f3c4e`; draft PR #2351 is open. Coordinate before
  pushing additional product-surface changes so the draft remains reviewable.

### JSON Assumptions

- Use `--json`; do not add `--format json`.
- Registry result schemas live under
  `schemas/pack/registry/<command>/result.schema.json`.
- Every public and nested public schema field needs `description`.
- Real `--json` stdout must validate against `--json-schema=result`.
- Failure behavior must follow Jasmine's JSON rollup once stable; until then,
  treat structured failure JSON as a coordination point, not a unilateral
  registry decision.

Needed from Jasmine:

- Current JSON rollup branch.
- Final source of truth for `--json-schema=result` and shared failure schema.
- Whether `x-gc-jsonl` is accepted, and its exact shape if accepted.
- Whether new commands should require structured failure JSON immediately or
  only when they opt into schema-backed buffering.

### Pack Deprecation Assumptions

- #2126 remains a separate PackV1/PackV2 deprecation train.
- Registry/gc pack preserves current PackV2 `source`, `version`, `export`,
  `transitive`, and `shadow` behavior for now.
- #2129 `[[exports]]` is design input/future direction, not this workstream's
  implementation scope.
- `gc import migrate` remains until doctor / `doctor --fix` parity exists.

Needed from Mabel:

- No current action. Continue to flag any future deprecation-train change that
  would alter `gc import`, `gc pack fetch/list`, or PackV2 import field
  semantics.

### First Stable Checkpoint Validation Gates

Run from `/Users/dbox/repos/gc-pr2119` on `codex/pack-registry-workstream`:

```sh
go test ./internal/packsource ./internal/packregistry ./internal/packman ./internal/config
go test ./cmd/gc -run 'TestPackRegistry|TestPackRegistryJSON|TestPackAdd|TestPackSync|TestPackCheck|TestPackCommandTree|TestDoImport|TestImport|TestImportStateDoctor|TestDoDoctor|TestJSONSchema|TestJSONUnsupported|TestJSONExecutionFailure|TestSyncLock|TestCheckInstalled'
make check-docs
git diff --check
```

These targeted gates passed on the old machine at `a64fb1ba`. Mabel re-ran the
same matrix on 2026-05-18 PT and all four commands passed again:

- `go test ./internal/packsource ./internal/packregistry ./internal/packman ./internal/config`
- `go test ./cmd/gc -run 'TestPackRegistry|TestPackRegistryJSON|TestPackAdd|TestPackSync|TestPackCheck|TestPackCommandTree|TestDoImport|TestImport|TestImportStateDoctor|TestDoDoctor|TestJSONSchema|TestJSONUnsupported|TestJSONExecutionFailure|TestSyncLock|TestCheckInstalled'`
- `make check-docs`
- `git diff --check`

Cleo re-ran the matrix after the final product-surface pass at `f82f3c4e`; all
four commands passed again.

A broader `go test ./cmd/gc -count=1` attempt was previously stopped after
running long with no additional output; use the targeted matrix above plus
CI/full package testing as review prep.

Additional required gates:

- `gc pack registry` text behavior covers list/add/remove/refresh/search/show.
- Registry JSON output validates against checked-in result schemas.
- Unsupported JSON command paths use platform behavior.
- Diagnostics do not pollute JSON stdout.
- Registry add/search/show cover stale caches, partial reachability, ambiguous
  bare names, removed snapshots, and invalid registry/catalog inputs.

### Last Updated

2026-05-18 22:18 PT by Mabel

## New Machine Bootstrap

### Repos To Clone

- Main implementation repo:
  `https://github.com/gastownhall/gascity.git`
- Optional preservation mirror from the old machine:
  `donbox/gascityworkplace:codex/pack-registry-workstream`

### Branches To Fetch / Checkout

```sh
git clone https://github.com/gastownhall/gascity.git /Users/dbox/repos/gc-pr2119
cd /Users/dbox/repos/gc-pr2119
git fetch origin main
git fetch origin codex/pack-registry-workstream
git switch codex/pack-registry-workstream
```

Coordination branch:

```sh
git fetch https://github.com/gastownhall/gascity.git codex/workstream-coordination:refs/remotes/upstream/workstream-coordination
git worktree add -B codex/workstream-coordination /Users/dbox/repos/gc-workstream-coordination upstream/workstream-coordination
```

### Worktrees To Create

- `/Users/dbox/repos/gc-pr2119` for registry/gc pack implementation.
- `/Users/dbox/repos/gc-workstream-coordination` for coordination updates.

### Local-Only State

None required. Registry/gc pack implementation state is pushed to
`origin/codex/pack-registry-workstream` at
`f82f3c4e`.

Old stashes on the old machine are preservation artifacts only:

- `registry workstream migration to codex/pack-registry-workstream`
- `pack registry work before true latest main`
- `pack registry work before main upmerge`

They should not be needed unless the pushed branch is lost.

### Setup Validation Commands

```sh
git status --short --branch --untracked-files=all
git log --oneline -5 --decorate
go test ./internal/packsource ./internal/packregistry ./internal/packman ./internal/config
go test ./cmd/gc -run 'TestPackRegistry|TestPackRegistryJSON|TestPackAdd|TestPackSync|TestPackCheck|TestPackCommandTree|TestDoImport|TestImport|TestImportStateDoctor|TestDoDoctor|TestJSONSchema|TestJSONUnsupported|TestJSONExecutionFailure|TestSyncLock|TestCheckInstalled'
make check-docs
git diff --check
```

### Old-Machine Worktrees Safe To Ignore

- `/Users/dbox/repos/gc-pr2119` branches
  `codex/pack-registry-1a-core`,
  `codex/pack-registry-mainline`, and
  `codex/pack-registry-latest-main` are obsolete for active work.
- `/Users/dbox/repos/gc/.claude/worktrees/elated-mclaren` is not part of the
  registry/gc pack workstream. Its owner should decide deletion, but the new
  registry/gc pack machine does not need it.

### Old-Machine Worktrees Not Safe To Delete Yet

- `/Users/dbox/repos/gc-pr2119` should not be deleted until the new machine has
  fetched and validated `codex/pack-registry-workstream`.
- `/Users/dbox/repos/gc-workstream-coordination` should not be deleted until
  this coordination update is pushed and visible from the new machine.

### First Prompt For Cleo On The New Machine

```text
Cleo, continue the registry/gc pack workstream from:

- repo: /Users/dbox/repos/gc-pr2119
- branch: codex/pack-registry-workstream
- checkpoint commit: f82f3c4e
- coordination file: /Users/dbox/repos/gc-workstream-coordination/engdocs/coordination/active-workstreams.md

First, refresh upstream/main and the coordination branch, verify the setup with
the commands in the New Machine Bootstrap section, then continue review-prep
for the registry/gc pack workstream. Keep registry/gc pack separate from
PackV2 deprecation except for explicit compatibility checkpoints.
```

## Workstream Handoff

### Workstream

Pack Reuse / Customization Design

### Current Branch / PR

Branch: managed by Penelope on another machine

PR: no active PR in this coordination branch. Related sources are #2129
(merged explicit `[[exports]]` design), #2351 (registry/gc pack implementation,
does not implement `[[exports]]`), and future guide/implementation PRs.

Base: not tracked in this coordination file

Owner: Penelope

### Latest State

Penelope is continuing the user-facing pack reuse/customization guide and
design exploration on a separate machine. Do not migrate or interrupt that
context from this coordination branch.

### Attention Needed

Needs Mabel: no

Needs D. Box: no

Urgency: green

Reason: Penelope is intentionally staying on a separate machine; only update
this coordination file if her guide decisions affect #2129, #2351, registry/gc
pack CLI wording, or import/export semantics.

### PRs In Play

- #2129: merged explicit `[[exports]]` design note and current source for that
  future direction.
- #2351: current registry/gc pack implementation PR; related but does not
  implement explicit `[[exports]]`.
- Future PR needed for user-facing pack reuse/customization guide and any
  explicit `[[exports]]` implementation.

### Immediate Next Step

Penelope continues the guide/design work on the separate machine and surfaces
any decisions that change #2351 wording or future import/export semantics.

### Victory Checklist

- User-facing pack reuse/customization guide exists and is aligned with shipped
  behavior.
- Explicit `[[exports]]` implementation follow-up exists and references #2129.
- Guide examples do not imply unimplemented behavior in #2351.
- Any terminology changes are reflected in registry/gc pack docs before #2351
  exits draft.

### Explicit Non-Goals / Deferred Work

- Do not implement explicit `[[exports]]` in #2351.
- Do not make Penelope's guide block #2349, #2318, or #2351 unless it exposes a
  real product contract mismatch.

### Interface Contracts Other Agents Must Honor

- Treat #2129 `[[exports]]` as future design input, not as implemented registry
  behavior.
- Keep user-facing guide language aligned with actual implementation state.

### Blockers / Cross-Workstream Risks

- `yellow`: Reuse/customization guide may update terminology or examples used
  by #2351 and future registry docs.

### Needed From Other Agents

- Penelope: surface guide decisions that change registry/gc pack CLI wording or
  import/export semantics.

### Last Updated

2026-05-18 22:15 PT by Mabel

## Workstream Handoff

### Workstream

gc4gc / Operational Substrate

### Current Branch / PR

Coordination branch: `codex/workstream-coordination`

PR: none expected

Stable consumer repo: `https://github.com/donbox/gc4gc` on `master`

Producer/dev repo: `/Users/dbox/repos/gc/gc4gc-grace` on
`codex/gc4gc-producer-dev`

Owner: Grace

### Latest State

The gc4gc side quest is in producer/consumer split mode.

Stable consumer state:

- `/Users/dbox/repos/gc/gc4gc` is the stable consumer-facing copy.
- Branch: `master`.
- Latest known stable commit: `8d992e5 Point gc4gc at agent runtime checkout`.
- Remote: `https://github.com/donbox/gc4gc.git`.
- Mabel/Codex may consume stable artifacts here without Grace mediating.

Producer/dev state:

- `/Users/dbox/repos/gc/gc4gc-grace` is Grace's producer worktree.
- Branch: `codex/gc4gc-producer-dev`.
- Remote branch for current clean producer/dev baseline:
  `codex/gc4gc-producer-dev` at commit `52e6ec3`.
- Remote archival snapshot of Grace's old exact dev worktree:
  `codex/gc4gc-producer-snapshot-20260518` at commit `e38b97b`.
- The snapshot branch preserves the old dirty/untracked producer state as Git
  history. Prefer the clean `codex/gc4gc-producer-dev` branch for new work.
- Producer/dev may contain unpromoted or temporarily unstable producer changes.
- Do not ask Mabel/Codex to consume dev-worktree runs unless explicitly
  requested.

Gas City runtime used by stable gc4gc:

- Stable gc4gc invokes `gc` through
  `/Users/dbox/repos/gc/gc4gc/assets/scripts/gc-json.sh`.
- That helper defaults to
  `/Users/dbox/repos/gc/gascity-agent-runtime`.
- Expected runtime branch: `codex/gc4gc-agent-runtime-dolt-leak`.
- Expected managed-Dolt leak fix commits include:
  - `bd6b0152 Fix managed Dolt test process leaks`
  - `9c205d19 Tighten Dolt leak guard cleanup`
  - `5694e03f Clean up city init managed Dolt test`

Stable run artifact contract:

- Run artifacts live under `.runtime/runs/<run-id>/`.
- Stable core artifacts are:
  - `input.json`
  - `status.json`
  - `execution-manifest.json`
  - `result.json`
  - `findings.json`
  - `summary.md`
  - `proposed-comment.md`
- Optional additive artifacts are allowed.
- Do not rename, remove, or semantically repurpose stable core artifacts without
  an explicit compatibility plan.
- Do not write into or inspect `.gc/`; it is opaque Gas City-owned state.

Current stable lanes:

- `pack-pr-review` has a stable canary run for PR #2117:
  `.runtime/runs/20260515-005739-pack-pr-review-2117`.
- `gc-json-audit` is promoted as an additive audit lane with docs, skill,
  auditor agents, runbook, and experimental formula.
- Jasmine manually validated two JSON audit shards from stable gc4gc:
  - `.runtime/json-audit/20260516/status-config-supervisor/report.md`
  - `.runtime/json-audit/20260516/formula-order-dispatch/report.md`
- Remaining first-wave JSON audit shards:
  - `session-runtime-wait`
  - `convoy-workflow`
  - `mail-events-trace`

Current stable rigs:

- `gc4gc` points at `/Users/dbox/repos/gc/gc4gc`, prefix `gc`, initialized.
- `agent-runtime` points at `/Users/dbox/repos/gc/gascity-agent-runtime`,
  prefix `rt`, suspended, not initialized.
- `json-platform` points at
  `/Users/dbox/repos/gc/gascity-json-schema-platform`, prefix `jp`,
  suspended, not initialized.
- Do not casually initialize or resume suspended rigs during read-oriented
  audit work.

Known unpromoted work:

- `pack-design-drift-check` exists in Grace's dev worktree and produced a valid
  canary against PR #2119, but it is not promoted into stable gc4gc yet.
- Do not treat `pack-design-drift-check` as stable consumer surface until it
  goes through promotion and validation.

### PRs In Play

- No product PR expected for stable gc4gc itself.
- `donbox/gc4gc:master` is the stable consumer source.
- `donbox/gc4gc:codex/gc4gc-producer-dev` is the producer/dev source.
- `donbox/gc4gc:codex/gc4gc-producer-snapshot-20260518` is archaeology only.

### Immediate Next Step

Use stable gc4gc only when it helps current review/audit work. Do not promote
producer/dev features without a canary, validation, and consumer-side
verification.

### Victory Checklist

- New machine can clone stable gc4gc and the patched Gas City runtime.
- Stable `gc-json.sh sling --json --dry-run --no-convoy --stdin
  json-auditor-1` returns successful dry-run JSON from the gc4gc checkout.
- Mabel/Codex can consume stable run artifacts without Grace mediating.
- Any producer changes are promoted through the documented
  producer/consumer process.
- Product gaps discovered through gc4gc are filed or tied to existing issues.

### Explicit Non-Goals / Deferred Work

- Do not make gc4gc a second cockpit or human-facing replacement for Codex.
- Do not rely on `.gc/` or `.runtime/` as durable migrated Git state.
- Do not promote formula-driven JSON audit fanout until canaries make it boring.

### Interface Contracts Other Agents Must Honor

- gc4gc exists to let Codex users get Gas City benefits inside their existing
  Codex workflow.
- Stable consumer worktree remains the only consumer-facing runtime unless
  explicitly promoted.
- Do not make gc4gc a parallel human-facing operating surface.
- Do not change JSON or registry implementation from this lane unless Jasmine
  or Cleo asks.
- Use `.runtime/` for gc4gc-produced artifacts, not `assets/` and not `.gc/`.
- Use Gas City surfaces honestly where that is the product path. Wrappers should
  be explicit, boring, and transparent.
- For promotion, validate in Grace dev, run a canary, produce a promotion packet,
  then have the consumer side verify before treating the change as live.

### Attention Needed

Needs Mabel: no

Needs D. Box: no

Urgency: green

Reason: Handoff is published. Stable gc4gc can be consumed for current artifact
inspection and JSON audit prep. No immediate human decision is required.

### Blockers / Cross-Workstream Risks

- `yellow`: gc4gc should validate against Jasmine's JSON rollup once that
  branch is assembled, but should not block on #2222 queue timing.
- `yellow`: formula-driven JSON audit fanout is not stable yet; keep using
  manual or bead-per-shard routing until another canary proves the lane.
- `yellow`: gc4gc may surface product friction for Cleo's registry/gc pack work
  but should not directly alter implementation branches.
- `green`: stable gc4gc is no longer local-only; it is pushed to
  `https://github.com/donbox/gc4gc`.

Product gaps discovered while using gc4gc:

- Filed: #2140 separates configured suspension policy from operational pause
  state.
- Filed: #2144 makes `gc sling --json` expose partial dispatch failures and
  backend-readiness blockers.
- Still likely worth filing:
  - implicit HQ rig consistency between `gc rig list --json` and
    `gc status --json`
  - `gc supervisor status --json`
  - `gc config show --json`
  - broader `gc config explain --json`
  - `gc formula list/show --json`
  - `gc order list/show --json`
  - schema backfills for existing stable `--json`
  - repeated deprecated system order-path warnings on stderr

### Needed From Other Agents

- Jasmine: continue JSON audit work when ready. Assume #2222 schema-platform
  baseline will land; do not cram formula/order JSON into #2222.
- Cleo: notify Grace if registry/gc pack work needs dogfood validation through
  gc4gc.
- Mabel: safe to consume stable gc4gc artifacts without Grace mediating.
- Grace: keep producer changes additive, validated, and promoted deliberately.

Recommended next actions:

- Finish remaining first-wave JSON audit shards:
  `session-runtime-wait`, `convoy-workflow`, and `mail-events-trace`.
- After another successful canary, consider bead-per-shard routing for the
  remaining shards.
- Keep formula-driven fanout experimental until shard routing and artifact
  consumption are boring.
- Promote `pack-design-drift-check` only after a fresh validation/promotion pass.

New Machine Bootstrap:

Local-only state:

- Stable gc4gc is no longer local-only. Clone it from
  `https://github.com/donbox/gc4gc.git`.
- Grace's producer/dev state is also represented remotely:
  - clean producer/dev branch: `codex/gc4gc-producer-dev`
  - archival snapshot branch: `codex/gc4gc-producer-snapshot-20260518`
- Runtime artifacts under `.runtime/` are local run evidence, not durable GitHub
  records unless an agent explicitly summarizes them into issues, PR comments,
  or coordination docs.
- Portable machine-transition bundle created on the old machine:
  `/Users/dbox/repos/gc/gc4gc-machine-handoff-20260518`.
- The bundle includes stable and Grace-dev Git bundles plus overlays for
  `.runtime/`, `.beads/`, and uncommitted/untracked producer state.
- The bundle intentionally excludes `.gc/`; Gas City owns that opaque state.
- The bundle is fallback evidence only now that the repo/branches are pushed.
- `.runtime/` is not required for bootstrap, but it is useful canary evidence.

Clone/bootstrap commands:

```sh
mkdir -p /Users/dbox/repos/gc
cd /Users/dbox/repos/gc

git clone https://github.com/gastownhall/gascity.git gascity-agent-runtime
cd gascity-agent-runtime
git fetch origin codex/gc4gc-agent-runtime-dolt-leak
git switch codex/gc4gc-agent-runtime-dolt-leak

cd /Users/dbox/repos/gc
git clone https://github.com/donbox/gc4gc.git gc4gc
cd gc4gc
git switch master

cd /Users/dbox/repos/gc
git clone https://github.com/donbox/gc4gc.git gc4gc-grace
cd gc4gc-grace
git switch codex/gc4gc-producer-dev
```

Optional archival snapshot checkout:

```sh
cd /Users/dbox/repos/gc
git clone https://github.com/donbox/gc4gc.git gc4gc-grace-snapshot-20260518
cd gc4gc-grace-snapshot-20260518
git switch codex/gc4gc-producer-snapshot-20260518
```

Validation commands:

```sh
cd /Users/dbox/repos/gc/gc4gc
git status --short --branch
git log -4 --oneline
sed -n '1,30p' assets/scripts/gc-json.sh

git -C /Users/dbox/repos/gc/gascity-agent-runtime status --short --branch
git -C /Users/dbox/repos/gc/gascity-agent-runtime log --oneline -6

assets/scripts/gc-json.sh rig list --json
assets/scripts/gc-json.sh formula show gc-json-audit
printf 'gc4gc fixed-runtime verification\n' \
  | assets/scripts/gc-json.sh sling --json --dry-run --no-convoy --stdin json-auditor-1

git -C /Users/dbox/repos/gc/gc4gc-grace status --short --branch
git -C /Users/dbox/repos/gc/gc4gc-grace log -3 --oneline
```

If the fallback bundle is restored, also validate preserved runtime evidence:

```sh
cd /Users/dbox/repos/gc/gc4gc
assets/scripts/validate-run.sh .runtime/runs/20260515-005739-pack-pr-review-2117
find .runtime/json-audit/20260516 -maxdepth 3 -type f -name report.md -print
```

### Last Updated

2026-05-18 17:36 PT by Grace
