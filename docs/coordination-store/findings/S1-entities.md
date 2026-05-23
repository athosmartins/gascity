# S1 — Entity & lifecycle inventory of HQ coordination state

> Spike **ga-aec8q.1** (parent epic **ga-aec8q**). Finding doc, not a
> proposal. **Tech-agnostic by intent**: this catalog describes WHAT the
> coordination data is and HOW it is used, not "which Dolt table it
> happens to live in today". Per-entity storage-location lines exist only
> as forward-references for S2/S5 to anchor measurements against.
>
> Sources: `gc` source tree (`internal/`), `bd` schema and live data from
> the HQ store (database `hq`, port from `.beads/dolt-server.port`),
> filesystem state under `.gc/` and `.beads/`.
>
> Observation rig: gascity rig, 2026-05-22.

## Reading guide

Each entity is given as:

- **Purpose** — what this entity represents in the coordination model.
- **Producers** — who creates instances.
- **Updaters** — who mutates them after creation.
- **Consumers** — who reads / depends on them.
- **Reaper** — who is supposed to delete or close them (and "none" if
  no reaper exists today — that's an unbounded-growth flag for S2/S7).
- **Lifecycle** — birth → states → death.
- **Intent: ephemeral or durable?** — design-time question, ignoring how
  it happens to be stored today.
- **Persisted today in** — single line; for S2/S5 anchoring only.

The intent column is the one to read first: it identifies entities whose
current storage technology (full-history versioned database) is wildly
mismatched with their design-time durability requirement.

---

## Section index

| § | Section | What lives here |
|---|---------|-----------------|
| I | Work primitives | Task, Chore, Bug, Feature, Epic, Step |
| II | Runtime identity | Session, Agent, Role, Rig |
| III | Workflow execution | Molecule, Convoy, Spec |
| IV | Communication | Mail message, Order-tracking wisp, Field-change event row, System event |
| V | Coordination & async dispatch | Gate, Merge-request, Dependency edge, Label, Nudge queue item, Session-name lock, Convergence |
| VI | Routing & sync | Rig route, Federation peer |
| VII | Memory & config | bd memory (kv.memory.*), Custom status, Custom type, Counter, Project metadata, Schema migration |
| VIII | Snapshots & retention | Compaction snapshot, Issue snapshot, Comment, Repo mtime |
| IX | Process-level filesystem state | Controller lock, Dolt server lock/port/log, Pack runtime state, Maintainer-PR-review run, Beads cache, Interactions log, Hooks state, Backup |
| X | Defined-but-unused surfaces | extmsg labels, polymorphic dead columns, JSONL-mirrored tables, federation_peers |
| XI | Derived views | ready_issues, blocked_issues |

Headline ratios (live, 2026-05-22, HQ database):

- **Issues**: 23,373 rows; 91% of those are CLOSED tasks (21,286 / 23,373).
  Dead-weight on the most-read collection in the store.
- **Wisps**: 6,364 rows; 2,365 are OPEN message wisps still accumulating
  (oldest open = 15 days). No reaper for unread/old message wisps today.
- **Events (audit log of bead mutations)**: 75,933 issue events +
  47,075 wisp events = **123,008 audit rows**. Largest single growth
  driver in the store.
- **Wisp labels**: 22,979 rows — dominated by `order-tracking` (7,163)
  and `exec` (6,981) plus per-order-name `order-run:<name>` labels.
  Wisp labels alone are ≈50× the corresponding issue-label count (447).
- **Dependencies**: 13 rows. The HQ dependency graph is essentially
  flat; the rich DAG lives in rig-scoped databases, not HQ.

---

## I. Work primitives

These are the canonical "unit of work" entities. They all share the same
schema row and differ only by `issue_type`. They are what most users
think of as "beads".

### I.1 Task

- **Purpose**: a unit of work assignable to an agent or human.
- **Producers**: humans (`bd create`), agents (autonomous filing of
  follow-up work), the dispatch/sling pipeline (creates ready-to-build,
  ready-to-deploy, etc. tasks).
- **Updaters**: assignee on claim/status changes; the field-change event
  log on every mutation; reconcilers that close orphan tasks.
- **Consumers**: agents looking for work (`bd ready`), humans reviewing
  state, dashboards, the ready/blocked materialized views.
- **Reaper**: no explicit reaper for closed tasks today. Closure is the
  end of the active lifecycle but rows stay forever.
- **Lifecycle**: open → (optional `in_progress`, `blocked`, `deferred`,
  or `hooked` custom status) → closed | `resolved` custom status.
- **Intent**: **durable while active; archivable once closed.** History
  is occasionally referenced for retrospectives but the row itself
  carries no per-row audit value once closed; the event log carries
  the audit.
- **Persisted today in**: `issues` row (`issue_type='task'`).

### I.2 Chore / Bug / Feature / Epic / Step

- **Purpose**: workflow taxonomy variants of Task, used to drive
  view filtering and prompt context. Semantically they share Task's
  lifecycle.
- **Producers / Updaters / Consumers**: same as Task.
- **Reaper**: none. Same as Task.
- **Lifecycle**: same as Task. `epic` is a container type (closed when
  its children are closed); `step` is a non-actionable child of a
  molecule (excluded from ready queries).
- **Intent**: **durable while active; archivable once closed.** Epics
  are slightly more durable because they index history; steps are
  ephemeral once their parent molecule closes.
- **Persisted today in**: `issues` row with the variant `issue_type`.

---

## II. Runtime identity & state

Beads that represent live system actors rather than work.

### II.1 Session

- **Purpose**: the persistent identity bead for a running (or recently
  running) agent session. Carries continuation epoch, generation,
  instance token, alias, template, runtime state, hook-bead pointer,
  agent_state, last_activity. Roughly: "who am I across crashes and
  resumes". Discovery via session-name lookup, not assignee/owner.
- **Producers**: the session manager and runtime providers when a new
  session is started (`gc session start`, controller spawn, pool fill).
- **Updaters**: the lifecycle projection (`internal/session/lifecycle_projection.go`)
  on every state transition (creating → active → asleep → draining →
  drained → archived → orphaned → closed, plus the off-path
  failed-create, suspended, quarantined, stopped states). The
  controller's reconciler tick. Health-patrol restarts. Drain-ack and
  drain-detection from the agent.
- **Consumers**: the controller (deciding which sessions to wake/keep),
  the API (`/v0/sessions`), `gc agents` CLI, dispatch ("which session
  for this work"), nudge delivery.
- **Reaper**: a stop / close path exists (drained → closed), but
  crashed-and-never-rejoined sessions accumulate as
  `state='stopped'` until manual cleanup. HQ shows 164 session beads
  total; 47 open today, with 2 active 11 days old.
- **Lifecycle**: 13+ named base states (see
  `internal/session/lifecycle.go:BaseState*` consts), 4 desired
  states, 5 runtime projections. The richest single-entity lifecycle
  in the system.
- **Intent**: **active state is ephemeral** (the runtime can be
  rebuilt from runtime providers + city config). The bead exists
  primarily to serialize this ephemeral state across the controller's
  reconciliation ticks and to make it observable. History past
  drain/close has no operational value; **archival or reap should be
  the default** once the session is terminal.
- **Persisted today in**: `issues` row (`issue_type='session'`).
  Many denormalized columns are session-specific:
  `hook_bead`, `role_bead`, `agent_state`, `last_activity`,
  `role_type`, `rig`, `started_at`. Of these, **`agent_state` and
  `role_type` are completely empty in the live HQ today** —
  schema weight without payload (anti-requirement flag for S6).

### II.2 Agent (custom type)

- **Purpose**: identity bead for a configured agent role at the city
  level (separate from the per-session bead above). Custom type
  registered in `custom_types`. Used as a long-lived target for
  routing.
- **Producers**: city init / agent config registration.
- **Updaters**: config-edit operations and the controller's
  agent-state syncer.
- **Consumers**: dispatch (`gc.routed_to=<agent>`), `gc agents`
  CLI, the API.
- **Reaper**: deregistration on config removal. In practice these
  beads are very long-lived.
- **Lifecycle**: open (registered) → closed (deregistered).
- **Intent**: **durable.** This is the closest thing in the system to
  a static "registry" entity; lifetime matches the agent's
  configured lifetime.
- **Persisted today in**: `issues` row with `issue_type='agent'`
  (defined in `custom_types`, infrequently materialized in
  practice — see X).

### II.3 Role (custom type)

- **Purpose**: pure metadata describing an agent role (prompts,
  capability hints). Indexed by `role_bead` pointers from Session.
- **Producers**: pack/role config materialization.
- **Updaters**: re-materialization on config change.
- **Consumers**: session-start hydration (template + prompt lookup).
- **Reaper**: deregistration on config removal.
- **Intent**: **durable** — this is configuration as data.
- **Persisted today in**: `issues` row with `issue_type='role'`.
  (Custom type registered, but in this rig the actual prompts and
  templates also live in pack files under `.gc/system/packs/...`,
  so the bead form is duplicative.)

### II.4 Rig

- **Purpose**: identity bead for a registered rig (workspace).
- **Producers**: `gc rig add`.
- **Updaters**: rig suspend/resume.
- **Consumers**: routing, dispatch, the rigs CLI.
- **Reaper**: rig removal.
- **Intent**: **durable** — registration scope.
- **Persisted today in**: `issues` row with `issue_type='rig'`.
  Note: rig prefix → filesystem-path routing is **also** kept in
  `.beads/routes.jsonl` (see VI.1), and the `routes` table in Dolt
  is empty — same data, two surfaces.

---

## III. Workflow execution

Composite work structures built atop tasks.

### III.1 Molecule (root + steps)

- **Purpose**: an instantiation of a compiled formula recipe — a
  root bead plus child step beads that form a DAG of executable
  work. Each step is itself a bead (often `issue_type='task'`)
  carrying dependency edges to its parent and to its predecessors.
- **Producers**: `internal/molecule/Cook` and `Instantiate`; the
  formula compilation layer; the orders system on trigger fire.
- **Updaters**: agents executing steps (each step's status), the
  dispatch system, the formula graph apply (`ApplyGraphPlan`)
  which mutates the bead graph atomically.
- **Consumers**: workers picking up step beads, dashboards showing
  molecule progress, the reaper that closes finished molecules.
- **Reaper**: molecule cleanup logic in
  `internal/molecule/cleanup.go`. Cleanup conditions are formula
  recipes; not all molecule shapes get reaped. 11 open molecules in
  HQ today, oldest 3 days.
- **Lifecycle**: root created with idempotency key → children
  attached → steps execute (own task lifecycle) → root closes when
  terminal step completes (or cleanup logic runs).
- **Intent**: **medium-durable.** Active state is operationally
  critical (the DAG is the execution plan). Closed molecules have
  some retrospective value for "did we get this through". The
  ratio of active to historical molecules should be low; in HQ
  today only 11 are open. The DAG edges (dependencies) are
  themselves first-class entities; see V.3.
- **Persisted today in**: `issues` rows with `issue_type` in
  `{molecule, step}`, plus `mol_type` column (currently empty in
  HQ — anti-requirement flag).

### III.2 Convoy

- **Purpose**: a "multi-bead" grouping for dispatch — typically a
  cross-rig batch tracked together (e.g., a single review request
  spanning multiple rigs). A convoy bead has child items linked
  via `tracks`-type dependencies.
- **Producers**: `internal/convoy/ConvoyCreate`, the dispatch
  pipeline (sling).
- **Updaters**: child-status reconciliation; convoy completion
  events (`ConvoyCreated`, `ConvoyClosed`).
- **Consumers**: the dispatcher, dashboards, the order-tracking
  layer.
- **Reaper**: ConvoyClose when all members terminal.
- **Lifecycle**: created (open) → members tracked → progress
  computed → closed when complete. 28 total convoys, 22 open.
- **Intent**: **medium-durable** — same logic as Molecule.
- **Persisted today in**: `issues` row with `issue_type='convoy'`,
  plus structured fields in the metadata JSON column (parsed by
  `internal/convoy/convoy_fields.go`).

### III.3 Spec (custom type)

- **Purpose**: a frozen specification doc-as-bead, referenced by
  `spec_id` from work beads. Lets work point to a stable spec
  whose body doesn't change as the work evolves.
- **Producers**: spec materialization (`gc spec ...`, pack-driven).
- **Updaters**: rare — specs are intentionally append-only in
  spirit.
- **Consumers**: workers and reviewers looking up the binding
  contract.
- **Reaper**: spec retirement; rarely happens.
- **Lifecycle**: created → referenced → optionally closed (retired).
- **Intent**: **durable** — historical referenceability is the
  point.
- **Persisted today in**: `issues` row with `issue_type='spec'`,
  plus the `spec_id` column on referencing beads.

---

## IV. Communication

The data that flows between actors (humans, agents, the system itself).

### IV.1 Mail message

- **Purpose**: addressed agent-to-agent or human-to-agent
  communication, with subject + body + sender + recipient. The
  built-in `beadmail` backend implements `mail.Provider` (see
  `internal/mail/mail.go`) on top of the wisp table.
- **Producers**: `gc mail send`, automation flows, system reminders
  emitted on session events.
- **Updaters**: Read / MarkRead / MarkUnread / Archive operations
  (each emits an event of the same name into `.gc/events.jsonl`).
  Recipients reading the message flip `read` label state.
- **Consumers**: the recipient agent (inbox check on every turn),
  dashboards showing mail traffic, the mail CLI.
- **Reaper**: Archive / Delete (alias for Archive — closes the
  bead). **No automatic archive policy today.** Result: 2,365 OPEN
  message wisps in HQ, oldest 15 days, accumulating without
  intervention. This is the single clearest unbounded-growth
  pattern in the live data.
- **Lifecycle**: created (open, unread) → read (label flip) →
  archived (closed). Optional thread linkage via `ThreadID`,
  reply linkage via `ReplyTo`.
- **Intent**: **mostly ephemeral.** The notification value of a
  message decays quickly; the audit value rarely justifies
  retaining 2k+ open rows. Some threads carry slow
  multi-turn context — those want medium-durable retention. A
  retention policy ("archive read messages older than N days,
  unread messages older than M days") is the obvious missing
  policy.
- **Persisted today in**: `wisps` row with `issue_type='message'`,
  metadata keys
  `mail.from_session_id`, `mail.from_display`,
  `mail.to_session_id`, `mail.to_display` (sender/recipient by
  session-bead-ID + display name).

### IV.2 Order-tracking wisp

- **Purpose**: a per-order-run audit/observability wisp emitted
  every time the orders system fires an order. Labels capture
  which order ran, which formula, success/failure. Body captures
  the trigger context.
- **Producers**: the orders system on every order run (driven by
  cooldown / cron / condition / event triggers).
- **Updaters**: labels added on completion (`exec`, `wisp-failed`,
  etc.), closed on terminal state.
- **Consumers**: the orders feed in the API
  (`internal/api/handler_orders*.go`), dashboards, debug
  retrospectives.
- **Reaper**: a `wisp-compact` order is registered (see
  `.gc/scripts/wisp-compact.sh`) but examining HQ shows
  `wisp_labels` for order-tracking already at 7,163 entries —
  retention policy is too generous or not running often enough.
  ~1,100 entries per high-frequency order (gate-sweep,
  beads-health, dolt-health) is observed.
- **Lifecycle**: created on trigger fire → labels added → closed
  on completion. Most are short-lived (seconds-to-minutes).
- **Intent**: **highly ephemeral.** This is a debug/audit trail —
  the only consumers are dashboards and post-hoc inspection. A
  3-day window would cover all known operational queries.
- **Persisted today in**: `wisps` row, labelled `order-tracking`
  + per-order `order-run:<name>` + `exec` / `exec-failed`.

### IV.3 Field-change event row

- **Purpose**: an append-only audit row capturing every mutation
  to a bead — `created`, `updated`, `closed`, `claimed`,
  `status_changed`, `label_added`, `label_removed`, `reopened`.
  Carries `actor`, `old_value`, `new_value`, `comment`.
- **Producers**: the bead store emits one of these on every
  mutating operation, automatically.
- **Updaters**: append-only; never updated.
- **Consumers**: `bd history`, "who closed this", time-travel
  retrospectives, dashboards.
- **Reaper**: none today. Cascades when the parent bead is
  hard-deleted (`ON DELETE CASCADE`), but beads are almost never
  hard-deleted.
- **Lifecycle**: created at mutation time → lives forever.
- **Intent**: **medium-durable for active beads, archivable for
  closed beads.** This is the largest single growth driver in
  HQ: 123,008 rows (75,933 issue events + 47,075 wisp events).
  Per-bead amplification: every bead's lifecycle generates ~5
  rows on average (created + several updated + closed for a
  task; a session generates many more).
- **Persisted today in**: `events` table (for issues),
  `wisp_events` table (for wisps).
  Notable: `event_kind`, `actor`, `target`, `payload` columns
  also exist denormalized on the *issues* table — those
  columns appear to be a legacy attempt to model events
  in-band, currently unused (0 rows).

### IV.4 System event (the Event Bus)

- **Purpose**: a separate, cross-package observability stream
  recording infrastructure activity: city lifecycle (resumed,
  suspended), bead lifecycle (created, closed), session lifecycle
  (woke, stopped, crashed, drain_acked_with_assigned_work, …),
  mail operations, order fired/completed/failed, controller
  start/stop, request results, supervisor signals, extmsg events.
- **Producers**: every domain package emits via
  `events.Recorder.Record` — see the long list of constants in
  `internal/events/events.go`.
- **Updaters**: append-only; consumed via reader/scanner.
- **Consumers**: the orders system (event-triggered orders consume
  by `event_type` + cursor), the API's `/v0/events/stream` SSE
  projection, gate evaluators (`gates.go`), dashboards, traces.
- **Reaper**: log rotation (`rotation.go`, `rotation_archive.go`).
- **Lifecycle**: emitted at the moment of action → rotated to
  archive → eventually deleted.
- **Intent**: **ephemeral with bounded retention.** The cursor
  model means we only need to retain enough events to outlast
  the slowest consumer; everything else can roll off.
- **Persisted today in**: `.gc/events.jsonl` (live tail),
  `.gc/events.jsonl.seq` (next-seq pointer), rotated archives.
  This is a **distinct system** from the per-bead `events`
  table in IV.3.

---

## V. Coordination & async dispatch

State that exists to coordinate or sequence actors.

### V.1 Gate (custom type / mechanism)

- **Purpose**: an async wait condition — a bead that opens when
  some external state is true. Used to gate dispatch ("wait for
  the build to be green before slinging the deploy"). Custom type
  `gate` is registered.
- **Producers**: order definitions of `trigger='condition'` or
  `gate='cooldown'`, etc. (see `internal/orders/gates.go`).
- **Updaters**: the gate evaluator on each tick.
- **Consumers**: the dispatcher; molecules waiting on a step.
- **Reaper**: closed when the condition is satisfied.
- **Lifecycle**: open (waiting) → satisfied (closed).
- **Intent**: **ephemeral** — the gate has no value after it
  fires; only the event it produces matters.
- **Persisted today in**: order-config (TOML on disk under
  `<pack>/orders/`) plus per-evaluator state in the orders
  cursor/last-run tracking. The schema has `await_type`,
  `await_id`, `timeout_ns`, `waiters` columns ostensibly for
  bead-modeled gates, **all currently empty** in HQ — another
  defined-but-unused surface.

### V.2 Merge-request (custom type)

- **Purpose**: bead representing an in-flight code-review /
  merge request, processed by automation (e.g., the
  maintainer-pr-review pack). Bead carries the GitHub PR
  number, decision state, and labels indicating review
  progress.
- **Producers**: PR-ingest automation; user filing.
- **Updaters**: review automation, label sweepers.
- **Consumers**: review automation, dashboards.
- **Reaper**: closed when the PR merges or closes.
- **Lifecycle**: open (PR live) → closed (merged or rejected).
- **Intent**: **ephemeral while in-flight; closed entries
  archivable.** Recent counts: 40 `needs-human-review` and
  26 `mpr-rebase-routed` labels in HQ, with corresponding
  on-disk state under `.gc/maintainer-pr-review/...` (IX.4).
- **Persisted today in**: `issues` row with
  `issue_type='merge-request'`, plus the rich filesystem run
  state.

### V.3 Dependency edge

- **Purpose**: directed edges in the bead DAG —
  `blocks`, `parent-child`, `waits-for`, `tracks`,
  `relates-to`. Drives the readiness query (a bead is ready
  iff no `blocks` predecessor is still open) and the molecule
  step graph.
- **Producers**: bead-create-with-deps, formula
  instantiation (`ApplyGraphPlan` writes entire dependency
  sets atomically), molecule-attach, dispatch-time wiring.
- **Updaters**: dependencies are mostly immutable once
  written; closure happens via the parent bead's lifecycle.
- **Consumers**: the readiness materialized view, blocked-
  issues view, dispatchers, dashboards, cleanup logic.
- **Reaper**: cascade on parent-bead delete (uncommon).
- **Lifecycle**: created at structure-build time → lives as
  long as its endpoints exist.
- **Intent**: **durable for the lifetime of the endpoints.**
  Note: HQ has only 13 dependency rows because HQ is
  primarily a coordination database, not a workflow database.
  The rig database (`gascity`) has 5,520 dependency rows —
  the rich DAG lives there.
- **Persisted today in**: `dependencies` table; can point to
  `issues.id`, `wisps.id`, or an external ref (cross-rig).

### V.4 Label

- **Purpose**: many-to-many tags on beads/wisps. Used for
  routing (`gc.routed_to=<x>`), workflow state (`needs-investigation`,
  `needs-human-review`, `ready-to-build`), agent/template
  identification (`agent:<x>`, `template:<x>`), order
  tracking (`order-tracking`, `order-run:<name>`), source
  attribution (`source:mail`, `source:session`),
  read/unread state for mail.
- **Producers**: bead-create-with-labels, label-add operations,
  automation.
- **Updaters**: add/remove operations, automation flows.
- **Consumers**: every query that filters or routes by tag.
- **Reaper**: cascade on parent-bead delete.
- **Lifecycle**: added → removed (or persists to bead close).
- **Intent**: **lifetime of parent bead.** Labels are a
  flat tag space; multiplicity is up to the producer. The
  current asymmetry (447 issue labels vs 22,979 wisp labels)
  is driven entirely by per-order-run wisp-labelling
  (`order-tracking` + per-run name), not by an intentional
  modelling choice.
- **Persisted today in**: `labels` table, `wisp_labels` table.

### V.5 Nudge queue item

- **Purpose**: a persisted deferred-nudge waiting to be
  delivered to an agent — `(bead_id, agent, session_id,
  message, deliver_after, expires_at)` with claim/lease
  bookkeeping for at-most-once delivery.
- **Producers**: dispatch (sling) when slinging across a
  cool-down or to an asleep session; mail handoff
  ("send a system-reminder when this agent wakes").
- **Updaters**: claim/lease, deliver, fail, dead-letter.
- **Consumers**: the supervisor's nudge dispatcher (when
  configured via `daemon.nudge_dispatcher`).
- **Reaper**: delivered items go through Pending → InFlight →
  (delivered: removed) | (expired/failed: Dead bucket).
  Dead-letter entries are not auto-pruned today.
- **Lifecycle**: Pending → InFlight → done | Dead.
- **Intent**: **ephemeral**, with the caveat that items must
  survive a controller restart (durable storage required).
- **Persisted today in**: `.gc/runtime/nudges/state.json`
  with `.gc/runtime/nudges/state.lock`; the wake socket is at
  `.gc/runtime/nudges/wake.sock`. This entity is
  filesystem-only — there is no Dolt mirror today.

### V.6 Session-name lock

- **Purpose**: advisory cross-process lock guarding
  session-name assignment so two simultaneous create operations
  cannot grab the same name.
- **Producers**: any code path that creates a new aliased
  session (see `internal/session/names_test.go`
  `WithCitySessionNameLock`).
- **Updaters**: acquired and released within a single
  operation.
- **Consumers**: the lock holder for the duration of the
  protected critical section.
- **Reaper**: released on operation completion. Stale lock
  files are tolerated via flock-based detection of dead
  holders.
- **Lifecycle**: acquired → held → released (file remains
  on disk as flock target).
- **Intent**: **ephemeral process-coordination state**, not
  application data. Pure synchronization primitive.
- **Persisted today in**: `.gc/session-name-locks/<hashed-name>`
  files. The directory may not exist when no locks have
  ever been taken.

### V.7 Convergence (custom type)

- **Purpose**: bead representing a "convergence point" — work
  expected to land into a single state, used by the
  convergence loop for crash-retry idempotency on molecule
  instantiation. The `IdempotencyKey` option in
  `molecule.Options` is the producer-side input.
- **Producers**: molecule instantiation through the
  convergence loop.
- **Updaters**: the convergence reconciler on retry.
- **Consumers**: the convergence reconciler.
- **Reaper**: convergence cleanup when the target state is
  reached.
- **Lifecycle**: claimed (open) → converged (closed).
- **Intent**: **ephemeral** — the only point is to make
  crash-retry safe.
- **Persisted today in**: `issues` row with
  `issue_type='convergence'`. Custom type registered;
  rare in this rig.

---

## VI. Routing & sync

State controlling how requests and data flow between rigs and remotes.

### VI.1 Rig route (prefix → path)

- **Purpose**: maps bead-ID prefix (`gm`, `ga`, `mc`, `be`,
  `projectwrenunity`) to a filesystem path of the rig that
  owns that prefix. Drives cross-rig bead lookup ("which DB
  holds `ga-abc.1`?") and `gc mail send` cross-rig delivery.
- **Producers**: `gc rig add` writes this; `bd init` of a new
  prefix.
- **Updaters**: very rare — when a rig is moved on disk.
- **Consumers**: every cross-rig bead query.
- **Reaper**: route removal when a rig is deregistered.
- **Lifecycle**: created → updated rarely → deleted.
- **Intent**: **durable, low-churn configuration.**
- **Persisted today in**: `.beads/routes.jsonl` (live source
  of truth, 5 rows). The `routes` Dolt table exists in the
  schema and is **empty** — a defined-but-unused surface.

### VI.2 Federation peer

- **Purpose**: pairing record for sync against another bead
  store (`remote_url`, credentials, sovereignty mode,
  last_sync).
- **Producers**: `bd remote add` / federation pairing.
- **Updaters**: sync runs update `last_sync`.
- **Consumers**: bd's federation-sync code path.
- **Reaper**: unpair operation.
- **Lifecycle**: paired → synced repeatedly → unpaired.
- **Intent**: **durable, low-churn configuration.**
- **Persisted today in**: `federation_peers` table
  (**empty in HQ today** — federation not in use here).

---

## VII. Memory & config

Long-lived configuration that lives in the store, not on disk.

### VII.1 bd memory (kv.memory.*)

- **Purpose**: a key-value store for `bd remember`'d notes —
  persistent agent memories that survive sessions. Each is
  one row keyed by `kv.memory.<slug>` with the content as the
  value.
- **Producers**: `bd remember` invocations by agents and
  humans.
- **Updaters**: `bd remember --key <key> "new content"`.
- **Consumers**: `bd memories <keyword>`, `bd prime`
  context injection.
- **Reaper**: `bd forget <key>`.
- **Lifecycle**: created → updated → deleted.
- **Intent**: **durable**, intentionally so — the point is
  cross-session continuity.
- **Persisted today in**: `config` table; 24 entries live
  today (out of 49 total config rows).

### VII.2 Custom status / Custom type

- **Purpose**: extends the default status enum and bead-type
  taxonomy. Status carries a `category` (`open` / `done` /
  `frozen` etc.) that views (ready_issues, blocked_issues)
  use to decide whether an entry should be reachable.
- **Producers**: `bd add-status` / `bd add-type` (or schema
  migration).
- **Updaters**: rare.
- **Consumers**: the readiness views, type-aware queries.
- **Reaper**: deregistration is rare.
- **Lifecycle**: created at schema-extension time → lives
  with the store.
- **Intent**: **durable, very low churn.**
- **Persisted today in**: `custom_statuses` (3 rows:
  `deferred`/open, `hooked`/open, `resolved`/closed),
  `custom_types` (12 rows: `agent`, `convergence`,
  `convoy`, `event`, `gate`, `merge-request`, `message`,
  `molecule`, `rig`, `role`, `session`, `spec`).

### VII.3 Counter

- **Purpose**: allocator state for next ID. `issue_counter`
  for top-level IDs (`ga-aec8q`); `child_counters` /
  `wisp_child_counters` for sub-IDs (`ga-aec8q.1`).
- **Producers**: bead create on every bead.
- **Updaters**: monotonic increment on bead create.
- **Consumers**: bead create.
- **Reaper**: none — values are append-only.
- **Lifecycle**: increment-only.
- **Intent**: **durable** — must persist across restarts.
- **Persisted today in**: `issue_counter`, `child_counters`,
  `wisp_child_counters` (HQ: 11 child_counter rows).

### VII.4 Project metadata

- **Purpose**: project-identity facts (`_project_id`) and
  per-machine state (`bd_version`, `bd_version_max`,
  `tip_claude_setup_last_shown`).
- **Producers**: `bd init`, version upgrades.
- **Updaters**: version upgrades.
- **Consumers**: bd's version-compat checks.
- **Reaper**: none.
- **Intent**: **durable, low-churn.**
- **Persisted today in**: `metadata` table (project-wide),
  `local_metadata` table (per-machine, not synced).

### VII.5 Schema migration

- **Purpose**: applied-migrations ledger. Two tables:
  `schema_migrations` (versions applied) and
  `ignored_schema_migrations` (intentionally skipped).
- **Producers**: bd schema-init/upgrade.
- **Updaters**: monotonic on each migration apply.
- **Consumers**: bd's on-startup migration check.
- **Reaper**: none — append-only ledger.
- **Intent**: **durable, append-only.**
- **Persisted today in**: tables of the same names.

---

## VIII. Snapshots & retention

### VIII.1 Compaction snapshot

- **Purpose**: compacted summary of older closed beads — when
  compaction runs, a full closed bead's body is replaced
  with a snapshot row pointing to a hash; `compaction_level`,
  `compacted_at`, `compacted_at_commit`, `original_size`
  columns on the live bead track the operation.
- **Producers**: the compaction job
  (config: `auto_compact_enabled`, `compact_batch_size`,
  `compact_parallel_workers`, `compact_tier1_days`,
  `compact_tier2_days`, `compact_tier1_dep_levels`,
  `compact_tier2_dep_levels`, `compact_tier2_commits`,
  `compaction_enabled`).
- **Updaters**: only the compaction job itself.
- **Consumers**: the bead reader, falling back to the
  snapshot when the live row is compacted.
- **Reaper**: tier-2 graduation deletes tier-1; deletion of
  the parent bead cascades.
- **Lifecycle**: appears at compaction time → may be
  upgraded (tier-1 → tier-2) → deleted with parent.
- **Intent**: **medium-durable** — compaction is the
  store's main retention mechanism today.
- **Persisted today in**: `compaction_snapshots` table
  (**0 rows in HQ today** — compaction either not yet run
  or not configured to compact this rig's data).

### VIII.2 Issue snapshot

- **Purpose**: complete point-in-time snapshot of a bead.
  Schema column present.
- **Producers**: ad-hoc snapshot creation, certain workflow
  paths.
- **Lifecycle**: created → optionally restored from →
  deleted.
- **Intent**: **rarely used.** 0 rows in HQ today.
- **Persisted today in**: `issue_snapshots` table.

### VIII.3 Comment

- **Purpose**: a threaded note on a bead, separate from the
  bead's free-text `notes` field.
- **Producers**: `bd comment`.
- **Updaters**: append-only typically.
- **Consumers**: bead viewers.
- **Reaper**: cascade on parent delete.
- **Intent**: **lifetime of parent bead.** 6 issue
  comments + 0 wisp comments in HQ — barely used in practice;
  `notes` field is preferred.
- **Persisted today in**: `comments`, `wisp_comments`.

### VIII.4 Repo mtime

- **Purpose**: cached mtimes for files in the project repo —
  used by some bd workflows to detect change without
  re-stat-ing the filesystem.
- **Producers**: bd's mtime tracker.
- **Lifecycle**: written on observation → overwritten on
  next observation.
- **Intent**: **ephemeral cache.**
- **Persisted today in**: `repo_mtimes` table (**0 rows in
  HQ** — feature unused).

---

## IX. Process-level filesystem state

State that the coordination model treats as primary but cannot live in
Dolt because it represents process facts (PIDs, sockets, ports) or
because it pre-dates the store.

### IX.1 Controller lock

- `.gc/controller.lock` — flock target ensuring only one
  controller runs per city. Releases on process exit.
- **Intent**: **ephemeral process synchronization.**

### IX.2 Dolt server lock / port / log

- `.beads/dolt-server.lock` — flock on the Dolt server PID.
- `.beads/dolt-server.port` — live port the Dolt server
  bound to (rewritten on each restart; see the bd port-drift
  memory in this project).
- `.beads/dolt-server.log` — server stdout/stderr.
- **Intent**: **ephemeral process state.**

### IX.3 Pack runtime state

- `.gc/runtime/packs/<pack>/...` — per-pack runtime state
  (e.g., `dolt.pid`, `dolt.lock`, `dolt-state.json`,
  `dolt-provider-state.json`, `dolt-config.yaml`,
  `dolt.log`).
- `.gc/system/packs/<pack>/...` — installed pack manifests
  (read-mostly).
- **Intent**: **ephemeral process state** (runtime/),
  **durable configuration** (system/).

### IX.4 Maintainer-PR-review run state

- `.gc/maintainer-pr-review/<owner>-<repo>/pr-<n>/runs/<ts>/*.json`
  — per-run JSON artefacts from the maintainer-pr-review
  pack: `repo-policy.json`, `label-status.json`,
  `metadata.json`, `files.json`, `reviews.json`,
  `comments.json`, `repo-info.json`, `codeql-alerts.json`,
  `runner-status.json`, `ensemble-status.json`,
  `fix-executor.json`, `review-decision.json`,
  `human-hold.json` (optional), `publish-result.json`.
- **Intent**: **medium-durable run artefacts**, retention
  TBD by pack.

### IX.5 Beads cache (.gc/beads.json)

- `.gc/beads.json` (~14 KB) — a SNAPSHOT of beads at some
  seq (`seq: 21`). Format: a list of `{id, title, status,
  issue_type, created_at}`. Body lacks descriptions.
  **Purpose unclear** — looks like a passive export
  cache. Stale (last touched May 1, current date is
  May 22). Possibly orphaned from an earlier version of
  the bd export pipeline.
- **Intent**: presumably **ephemeral cache**, but it's
  stale enough to suggest it's no longer maintained.
  Flagging for S6 (anti-requirements audit).

### IX.6 Interactions log (.beads/interactions.jsonl)

- 5,597 entries. Each entry is `{id, kind, created_at,
  actor, issue_id, extra}` recording a `field_change`
  caused by an agent (session ID) closing or updating an
  issue. The Dolt `interactions` table is **empty** —
  this entire surface is JSONL-only.
- **Producers**: bd hooks that intercept agent operations.
- **Intent**: **medium-durable audit log**, with
  retention TBD. Duplicates information already captured
  in the `events` audit table (IV.3) plus session
  attribution — possible redundancy flag for S6.

### IX.7 Hooks state + log

- `.beads/hooks/` — hook scripts and state.
- `.beads/hooks.log` — hook execution log.
- **Intent**: **scripts are durable config; log is
  ephemeral.**

### IX.8 Formulas

- `.beads/formulas/` — formula store (TOML files).
- **Intent**: **durable configuration.**

### IX.9 Backup

- `.beads/backup/` — periodic backups of the Dolt store.
  PR #2478 (the `bd-backup-size` doctor canary) is open
  *specifically* because this directory has shown
  unbounded growth.
- **Intent**: **bounded-retention durable backups.**
  Current retention policy is the subject of an in-flight
  PR — anti-requirement flag.

### IX.10 Other supporting files

- `.beads/identity.toml` — bd identity (machine/user).
- `.beads/config.yaml` — bd config (issue_prefix,
  sync.remote, dolt.auto-push, etc.).
- `.beads/metadata.json` — bd metadata snapshot.
- `.beads/export-state.json` — export cursor state.
- `.beads/push-state.json` — push cursor state.
- `.beads/last-touched` — mtime stamp.
- `.beads/embeddeddolt/` — embedded Dolt assets.
- All **durable configuration** or **small bookkeeping**
  files; none implicated as growth drivers.

---

## X. Defined-but-unused entity surfaces

These exist in the schema or codebase but carry no data in the live HQ
today. They are **anti-requirement flags** for S6: every one of them is
schema or code surface that pays cost without earning benefit.

| Surface | Defined as | Live rows | Notes |
|---------|------------|-----------|-------|
| `issues.wisp_type` column | `varchar(32)` | 0 | Schema weight. |
| `issues.mol_type` column | `varchar(32)` | 0 | Schema weight. |
| `issues.event_kind` column | `varchar(32)` | 0 | Attempt at in-band event modelling; abandoned. |
| `issues.actor` / `target` / `payload` cols | text | 0 | Same as above. |
| `issues.await_type` / `await_id` / `timeout_ns` / `waiters` | varied | 0 | Bead-modeled gates; gates live in TOML today. |
| `issues.role_type` column | `varchar(32)` | 0 | Schema weight. |
| `issues.agent_state` column | `varchar(32)` | 0 | Schema weight. |
| `routes` table | full table | 0 | Authoritative copy lives in `.beads/routes.jsonl`. |
| `interactions` table | full table | 0 | Authoritative copy lives in `.beads/interactions.jsonl`. |
| `federation_peers` table | full table | 0 | Federation is not used in this rig. |
| `compaction_snapshots` table | full table | 0 | Compaction not actively producing tier rows here. |
| `issue_snapshots` table | full table | 0 | Snapshot feature unused. |
| `repo_mtimes` table | full table | 0 | Feature unused. |
| extmsg labels (`gc:extmsg-*`) | label-based bead pattern | 0 | extmsg not active in this rig. |
| `interactions` table column `extra json` | json | 0 | (table empty, but column itself shows the wire-shape ambiguity discussed in S6). |
| `.gc/beads.json` | JSON cache | stale 21-day-old snapshot | Either ratchet it or remove it. |

---

## XI. Derived views

These are **not entities** in the storage sense — they are computed
projections — but they must be enumerated because the readiness model
is what makes coordination work.

### XI.1 `ready_issues`

A computed view: an issue is ready when (a) it is open OR in an "active"
custom status, (b) it is not ephemeral, (c) all `blocks` predecessors are
closed or pinned, (d) no transitively-parent has an unexpired `defer_until`,
and (e) `defer_until` itself is in the past (or null). Implemented as a
recursive CTE.

### XI.2 `blocked_issues`

An open issue with at least one open `blocks` predecessor. Returns the
issue plus a `blocked_by_count` column.

**Recompute cost**: both views walk the dependencies graph. Even the
sparse HQ dep set is cheap to traverse, but a heavy rig DB with 5k+
edges and recursive descent puts these views on the hot read path.

---

## Cross-references for S2 and S5

The volume/churn census (S2) should anchor on:
- **Highest-volume entities**: field-change event rows
  (123k), issues (23k), wisps (6k), labels (23k wisp + 0.4k issue).
- **Highest-churn entities**: order-tracking wisps, system events,
  session beads (lifecycle transitions amplify into many event rows),
  message wisps (create-only with no reaper).
- **Highest dead-weight ratios**: closed tasks (91% of issues), closed
  message wisps relative to value (notification-only).
- **Unbounded entities (no reaper today)**: closed tasks,
  archived message wisps, field-change events, dead-letter nudges,
  order-tracking wisps (compaction policy too loose),
  `.beads/backup/` (in-flight fix).

The durability/restart-resilience matrix (S5) should split entities by:
- **Must-survive-restart, no rebuild possible**: bead identity rows
  (Task/Bug/Feature/Epic/Step/Spec), dependencies, labels, counters,
  config, custom types, schema_migrations, federation_peers, routes.
- **Must-survive-restart, but value decays fast**: open mail message,
  open order-tracking wisp, open convoy/molecule (decays only after
  closure).
- **Must-survive-restart, ephemeral state with reconstructible body**:
  session bead (live runtime is the authoritative source, bead is the
  serialization), gate (re-evaluable), convergence claims, nudge-queue
  items.
- **Best-effort-only**: field-change events past N days, system events
  past M cursors, comments, interactions.jsonl.
- **Pure process state, freely rebuildable**: controller lock, Dolt
  server lock/port/log, pack runtime state, session-name locks.
