package main

import (
	"sort"
	"strings"
	"time"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// defaultOnDemandIdleTimeout is the fallback idle timeout for on-demand
// named sessions that don't configure an explicit idle_timeout. Without
// this, on-demand sessions kept alive by the "on-demand:running" override
// would stay awake indefinitely. 5 minutes is long enough to handle a
// conversation turn, short enough to not waste resources.
const defaultOnDemandIdleTimeout = 5 * time.Minute

// AwakeInput contains all pre-computed state needed to decide which sessions
// should be awake. All external I/O (shell commands, tmux checks, store
// queries) happens before this function is called.
type AwakeInput struct {
	Agents             []AwakeAgent
	NamedSessions      []AwakeNamedSession
	SessionBeads       []AwakeSessionBead
	WorkBeads          []AwakeWorkBead // in_progress assigned work plus ready open assigned work
	ScaleCheckCounts   map[string]int  // agent template → scale_check count
	NamedSessionDemand map[string]bool // named-session identity → routed/assigned work demand
	NamedSessionWorkQ  map[string]bool // named-session identity → bridge-carried work_query demand
	WorkSet            map[string]bool // agent template → work_query found pending work
	RunningSessions    map[string]bool // session name → tmux exists
	AttachedSessions   map[string]bool // session name → user attached
	PendingSessions    map[string]bool // session name → pending interaction
	ReadyWaitSet       map[string]bool // session bead ID → durable wait is ready
	ChatIdleTimeout    time.Duration   // global idle timeout for manual/chat sessions (0 = disabled)
	Now                time.Time
}

// AwakeAgent represents an [[agent]] config entry.
type AwakeAgent struct {
	QualifiedName     string   // e.g. "hello-world/polecat"
	DependsOn         []string // template names this agent depends on
	Suspended         bool
	SleepAfterIdle    time.Duration // 0 = disabled
	MinActiveSessions int           // effective min_active_sessions; 0 = no always-warm guarantee
	// SingletonIdentity is true for singleton-crew templates: agents whose
	// configuration collapses every session bead to a single canonical
	// identity (max_active_sessions == 1, no namepool). For these agents at
	// most ONE bead may be awake at a time across ALL activation paths —
	// new/sling, wake, resume, and reconciler-revive. The new/sling path has
	// its own cap+mutex, but the resume/wake/revive path reactivates an
	// EXISTING bead and bypasses that check, so the awake-set must enforce
	// the single-identity mutex here too (ga-b41wn).
	SingletonIdentity bool
}

// AwakeNamedSession represents a [[named_session]] config entry.
type AwakeNamedSession struct {
	Identity    string // qualified name, e.g. "hello-world/refinery"
	Template    string // agent template name
	Mode        string // "always" or "on_demand"
	RuntimeName string // computed runtime session_name (e.g. "hello-world--refinery")
}

// AwakeSessionBead represents an open session bead from the store.
type AwakeSessionBead struct {
	ID                       string
	SessionName              string
	Template                 string
	State                    string // "creating", "active", "asleep", "drained", "closed"
	SleepReason              string
	ManualSession            bool
	PendingCreate            bool      // controller claimed this bead for initial start
	ExplicitWake             bool      // explicit durable wake request is pending
	DependencyOnly           bool      // only wakeable via dependency gate
	NamedIdentity            string    // non-empty for named session beads
	ConfiguredNamedSession   bool      // configured_named_session metadata is true
	Pinned                   bool      // pin_awake durable wake reason
	Drained                  bool      // state=="drained" or sleep_reason=="drained"
	WaitHold                 bool      // user-issued gc wait in progress
	HeldUntil                time.Time // zero = not held
	QuarantinedUntil         time.Time // zero = not quarantined
	IdleSince                time.Time // zero = unknown/not idle
	RestartRequested         bool      // restart_requested metadata is still active
	ContinuationResetPending bool      // continuation_reset_pending metadata is set
}

// AwakeWorkBead represents a work bead with an assignee.
type AwakeWorkBead struct {
	ID       string
	Assignee string
	Status   string // "open", "in_progress"
	Ready    bool   // true for open work only after readiness/blocker filtering
}

// AwakeDecision is the output for a single session.
type AwakeDecision struct {
	ShouldWake      bool
	Reason          string // human-readable reason for debugging
	HasAssignedWork bool   // underlying assigned-work demand before wake reason overrides
}

// ComputeAwakeSet determines which sessions should be awake.
//
// Pure function. Algorithm:
//  1. Build desired set from config + demand signals
//  2. Any session in desired set should wake
//  3. Attached/pending/ready-wait override (wake even if not desired)
//  4. Idle sleep suppression
//  5. Hold + quarantine suppression (overrides everything)
//
// Dependency ordering is NOT enforced here — the reconciler's
// executePlannedStarts handles it via wave-based starts.
func ComputeAwakeSet(input AwakeInput) map[string]AwakeDecision {
	agentsByName := make(map[string]AwakeAgent, len(input.Agents))
	agentsByBaseName := make(map[string]AwakeAgent, len(input.Agents))
	duplicateBaseNames := make(map[string]bool)
	for _, a := range input.Agents {
		agentsByName[a.QualifiedName] = a
		base := awakeAgentBaseName(a.QualifiedName)
		if existing, ok := agentsByBaseName[base]; ok && existing.QualifiedName != a.QualifiedName {
			duplicateBaseNames[base] = true
			continue
		}
		if !duplicateBaseNames[base] {
			agentsByBaseName[base] = a
		}
	}
	lookupAgent := func(name string) (AwakeAgent, bool) {
		if agent, ok := agentsByName[name]; ok {
			return agent, true
		}
		base := awakeAgentBaseName(name)
		if duplicateBaseNames[base] {
			return AwakeAgent{}, false
		}
		agent, ok := agentsByBaseName[base]
		return agent, ok
	}

	// Step 1: Build desired set.
	// Drained beads are excluded from generic template demand, but explicit
	// compatible wake causes (pending create, named-always, assigned work) may
	// still reuse the same bead.
	desired := make(map[string]string) // sessionName → reason

	// Newly created beads that still carry a controller create claim must be
	// launched at least once, even if the work signal that materialized them
	// is no longer visible on the very next tick.
	for _, bead := range input.SessionBeads {
		if !bead.PendingCreate {
			continue
		}
		desired[bead.SessionName] = "pending-create"
	}
	for _, bead := range input.SessionBeads {
		if !bead.ExplicitWake || bead.State == "closed" || bead.DependencyOnly {
			continue
		}
		if agent, ok := agentsByName[bead.Template]; ok && !agent.Suspended {
			desired[bead.SessionName] = "explicit-wake"
		}
	}
	// Named sessions
	for _, ns := range input.NamedSessions {
		if agent, ok := lookupAgent(ns.Identity); ok && agent.Suspended {
			continue
		}
		switch ns.Mode {
		case "always":
			if sn := resolveNamedSessionBeadName(input.SessionBeads, ns); sn != "" {
				bead := findBeadBySessionName(input.SessionBeads, sn)
				if bead != nil && !bead.DependencyOnly {
					desired[sn] = "named-always"
				}
			} else {
				desired[ns.Identity] = "named-always"
			}
		case "on_demand":
			reason := ""
			switch {
			case input.NamedSessionDemand[ns.Identity]:
				reason = "named-demand"
			case input.NamedSessionWorkQ[ns.Identity]:
				reason = "work-query"
			default:
				continue
			}
			if agent, ok := agentsByName[ns.Template]; ok && agent.Suspended {
				continue
			}
			if sn := resolveNamedSessionBeadName(input.SessionBeads, ns); sn != "" {
				bead := findBeadBySessionName(input.SessionBeads, sn)
				if bead != nil && !bead.DependencyOnly && !bead.Drained && bead.State != "closed" {
					desired[sn] = reason
				}
			} else {
				desired[ns.Identity] = reason
			}
		}
	}

	// Agent templates (scaled)
	for template, count := range input.ScaleCheckCounts {
		if count <= 0 {
			continue
		}
		agent, ok := lookupAgent(template)
		if !ok || agent.Suspended {
			continue
		}
		active := collectActiveBeads(input.SessionBeads, template)
		filled := countAssignedScaleSlots(input.SessionBeads, input.WorkBeads, input.NamedSessions, template)
		for _, bead := range active {
			if filled >= count {
				break
			}
			if sessionHasAssignedWork(input.WorkBeads, input.NamedSessions, bead) {
				continue
			}
			desired[bead.SessionName] = "scaled:demand"
			filled++
		}
		creating := collectCreatingBeads(input.SessionBeads, template)
		for _, bead := range creating {
			if filled >= count {
				break
			}
			if sessionHasAssignedWork(input.WorkBeads, input.NamedSessions, bead) {
				continue
			}
			desired[bead.SessionName] = "scaled:creating"
			filled++
		}
	}

	// WorkSet: defense-in-depth wake signal from work_query.
	// When work_query sees pending work but ScaleCheckCounts hasn't caught up
	// (count is 0 or absent), wake exactly one session to handle it. This
	// avoids thundering herd — scale_check will catch up on the next tick.
	for template, hasWork := range input.WorkSet {
		if !hasWork {
			continue
		}
		if input.ScaleCheckCounts[template] > 0 {
			continue // ScaleCheck already covers this template
		}
		agent, ok := lookupAgent(template)
		if !ok || agent.Suspended {
			continue
		}
		if isNamedSessionTemplate(input.NamedSessions, template) {
			continue // named sessions are handled in the named-session pass
		}
		// collectActiveBeads already excludes DependencyOnly and Drained
		if active := collectActiveBeads(input.SessionBeads, template); len(active) > 0 {
			desired[active[0].SessionName] = "work-query"
			continue
		}
		if creating := collectCreatingBeads(input.SessionBeads, template); len(creating) > 0 {
			desired[creating[0].SessionName] = "work-query"
		}
	}

	// Manual sessions
	for _, bead := range input.SessionBeads {
		if !bead.ManualSession || bead.State == "closed" || bead.Drained {
			continue
		}
		if _, ok := agentsByName[bead.Template]; ok {
			desired[bead.SessionName] = "manual"
		}
	}

	// Sessions with assigned work — a session that has in_progress work or
	// ready open work assigned to it must stay awake. Open work must carry
	// Ready=true so a blocked routed assignment cannot become wake demand if
	// a future caller accidentally broadens the collection query.
	assignedWorkDemand := make(map[string]bool)
	for _, bead := range input.SessionBeads {
		if bead.State == "closed" {
			continue
		}
		if agent, ok := lookupAgent(bead.Template); ok && agent.Suspended {
			continue
		}
		for _, wb := range input.WorkBeads {
			assignee := strings.TrimSpace(wb.Assignee)
			if assignee == "" || !workBeadHasAwakeDemand(wb) {
				continue
			}
			if sessionAssigneeMatches(input.NamedSessions, bead, assignee) {
				assignedWorkDemand[bead.SessionName] = true
				desired[bead.SessionName] = "assigned-work"
				break
			}
		}
	}

	// Min-active-sessions wake: keep min_active_sessions pool sessions warm
	// across a city-stop. A pool agent whose only instance is asleep with
	// sleep_reason=city-stop is neither counted toward the min nor woken by
	// the demand-driven passes above, so without this pass a
	// min_active_sessions=1 agent stays cold indefinitely after gc stop &&
	// gc start until work is explicitly slung to it. We revive the existing
	// asleep city-stop bead rather than relying on a fresh spawn (no
	// orphaned-bead churn), mirroring the named-always same-tick wake (#2367)
	// on the pool min path. Scoped to sleep_reason=city-stop so idle_timeout
	// and wake_mode semantics are unchanged. See #2739.
	for _, agent := range input.Agents {
		if agent.Suspended || agent.MinActiveSessions <= 0 {
			continue
		}
		template := agent.QualifiedName
		covered := countMinActiveCovered(input.SessionBeads, desired, template, input.Now)
		if covered >= agent.MinActiveSessions {
			continue
		}
		for _, bead := range cityStopPoolBeads(input.SessionBeads, template) {
			if covered >= agent.MinActiveSessions {
				break
			}
			if _, already := desired[bead.SessionName]; already {
				continue
			}
			if minActiveHardBlocked(bead, input.Now) {
				continue
			}
			desired[bead.SessionName] = "min-active"
			covered++
		}
	}

	for _, bead := range input.SessionBeads {
		if !bead.ContinuationResetPending || bead.RestartRequested || bead.WaitHold {
			continue
		}
		switch desired[bead.SessionName] {
		case "pending-create", "explicit-wake":
			continue
		default:
			desired[bead.SessionName] = "reset-pending"
		}
	}

	// Step 2-3: Decide awake
	result := make(map[string]AwakeDecision)

	for _, bead := range input.SessionBeads {
		name := bead.SessionName
		decision := AwakeDecision{
			HasAssignedWork: assignedWorkDemand[name],
		}

		// Desired set (demand-driven wake). wait_hold suppresses normal
		// demand-driven wake so a session intentionally parked on human
		// input stays asleep until either its durable wait becomes ready
		// or it still needs its initial launch.
		if reason, inDesired := desired[name]; inDesired {
			if !bead.WaitHold || bead.PendingCreate || bead.ExplicitWake {
				decision.ShouldWake = true
				decision.Reason = reason
			}
		}

		// Attached override — even drained beads wake if user is attached
		if input.AttachedSessions[name] && !bead.WaitHold {
			decision.ShouldWake = true
			decision.Reason = "attached"
		}

		// Pending interaction override — even drained beads wake
		if input.PendingSessions[name] && !bead.WaitHold {
			decision.ShouldWake = true
			decision.Reason = "pending"
		}

		// Ready wait — durable wait deadline passed, resume session
		if input.ReadyWaitSet[bead.ID] {
			decision.ShouldWake = true
			decision.Reason = "wait-ready"
		}

		// On-demand running override — on-demand sessions that are
		// currently running stay awake even when demand drops to zero.
		// They drain via idle_timeout, not demand absence. This
		// supports message-driven wake: a message starts the session,
		// it stays alive handling it, then idles until timeout.
		// Drain-ack agents are unaffected — they manage their own
		// lifecycle by calling drain-ack before this check matters.
		if !decision.ShouldWake && !bead.Drained && !bead.WaitHold &&
			bead.SleepReason != "idle-timeout" {
			if input.RunningSessions[name] && isOnDemandSession(input.NamedSessions, bead) {
				decision.ShouldWake = true
				decision.Reason = "on-demand:running"
			}
		}

		// Durable pin override — wakes and keeps the session awake while
		// still respecting hard blockers applied below.
		pinBlockedByState := bead.State == "suspended" || bead.State == "closed" || bead.Drained
		if !decision.ShouldWake && bead.Pinned && !pinBlockedByState && !bead.DependencyOnly && !bead.WaitHold {
			if agent, ok := lookupAgent(bead.Template); ok && !agent.Suspended {
				decision.ShouldWake = true
				decision.Reason = "pin"
			}
		}

		// Idle sleep: desired sessions idle too long should sleep.
		// Attached, pending, pinned, mode=always named, and sessions with
		// assigned demand work are exempt. Assigned demand work means either
		// in_progress ownership or open work with Ready=true; blocked open
		// assignments do not prevent idle sleep.
		if decision.ShouldWake && !input.AttachedSessions[name] && !input.PendingSessions[name] && !bead.Pinned && !bead.IdleSince.IsZero() &&
			!isAlwaysNamedSession(input.NamedSessions, bead) &&
			desired[name] != "assigned-work" && desired[name] != "min-active" &&
			desired[name] != "reset-pending" {
			agent, hasAgent := lookupAgent(bead.Template)
			var idleTimeout time.Duration
			switch {
			case bead.ManualSession && input.ChatIdleTimeout > 0:
				idleTimeout = input.ChatIdleTimeout
			case hasAgent && agent.SleepAfterIdle > 0:
				idleTimeout = agent.SleepAfterIdle
			case isOnDemandSession(input.NamedSessions, bead):
				idleTimeout = defaultOnDemandIdleTimeout
			}
			if idleTimeout > 0 && input.Now.Sub(bead.IdleSince) >= idleTimeout {
				decision.ShouldWake = false
				decision.Reason = "idle-sleep"
			}
		}

		// Hold suppression — overrides everything
		if !bead.HeldUntil.IsZero() && input.Now.Before(bead.HeldUntil) {
			decision.ShouldWake = false
			decision.Reason = "held"
		}

		// Quarantine suppression — overrides everything
		if !bead.QuarantinedUntil.IsZero() && input.Now.Before(bead.QuarantinedUntil) {
			decision.ShouldWake = false
			decision.Reason = "quarantined"
		}

		// NOTE: Dependency ordering is NOT enforced here. The reconciler's
		// executePlannedStarts handles dependency-aware wave ordering via
		// allDependenciesAliveForTemplate at wave boundaries. Applying
		// the gate here would prevent candidates from reaching the start
		// list, breaking wave-based starts (where dep starts in wave 0
		// and dependent starts in wave 1).

		result[name] = decision
	}

	// Single-identity mutex for singleton-crew templates (ga-b41wn).
	//
	// A singleton-crew agent (max_active_sessions == 1, no namepool) collapses
	// every session bead to ONE canonical identity, so at most one of its beads
	// may be awake at a time. The gc session new / SLING activation path already
	// enforces this via a cap + mutex (ga-i67t), but the resume / wake /
	// reconciler-revive path reactivates an EXISTING bead and never re-checks
	// the cap. That left a window where a second bead — e.g. a manual
	// `--resume` bead with continuation_reset_pending — could be marked awake
	// alongside the live canonical session, producing TWO active sessions for
	// the same crew identity on the same rig (observed: digo-wa).
	//
	// Every activation path funnels through ComputeAwakeSet, so deduping here
	// closes ALL vectors with one surgical check. Among the beads this tick
	// marked ShouldWake for a singleton-crew template, keep exactly one
	// (refuse-or-reuse: the survivor is the healthiest/most-canonical existing
	// session) and suppress the rest.
	applySingletonIdentityMutex(input, result)

	return result
}

// singletonTemplateSet returns the set of template (qualified agent) names that
// are singleton-crew: at most one awake bead is permitted across all activation
// paths.
func singletonTemplateSet(agents []AwakeAgent) map[string]bool {
	var set map[string]bool
	for _, a := range agents {
		if !a.SingletonIdentity {
			continue
		}
		if set == nil {
			set = make(map[string]bool)
		}
		set[a.QualifiedName] = true
	}
	return set
}

// applySingletonIdentityMutex enforces the single-identity mutex: for each
// singleton-crew template, at most one bead may remain ShouldWake. When more
// than one bead was marked awake this tick, the most preferred survivor is kept
// and the rest are demoted to ShouldWake=false with reason "singleton-mutex".
//
// Survivor preference (refuse-or-reuse — favor the already-live, canonical
// session over a freshly-reviving duplicate):
//  1. attached to a user
//  2. currently running (tmux alive) and state=active
//  3. NOT a manual/resume bead (the configured canonical identity)
//  4. NOT pending a continuation reset (avoid keeping the half-reset resume bead)
//  5. lowest bead ID (stable tie-break)
func applySingletonIdentityMutex(input AwakeInput, result map[string]AwakeDecision) {
	singletons := singletonTemplateSet(input.Agents)
	if len(singletons) == 0 {
		return
	}

	// Group awake beads by singleton template.
	awakeByTemplate := make(map[string][]AwakeSessionBead)
	for _, bead := range input.SessionBeads {
		if !singletons[bead.Template] {
			continue
		}
		// Namepool / configured-named beads carry distinct identities and are
		// never collapsed to one canonical identity; the SingletonIdentity flag
		// already excludes namepool, and configured-named singleton agents have
		// exactly one identity, so grouping by template is the identity here.
		d, ok := result[bead.SessionName]
		if !ok || !d.ShouldWake {
			continue
		}
		awakeByTemplate[bead.Template] = append(awakeByTemplate[bead.Template], bead)
	}

	for _, beadsForTemplate := range awakeByTemplate {
		if len(beadsForTemplate) < 2 {
			continue // no collision
		}
		winner := beadsForTemplate[0]
		for _, candidate := range beadsForTemplate[1:] {
			if singletonSurvivorPreferred(input, candidate, winner) {
				winner = candidate
			}
		}
		for _, bead := range beadsForTemplate {
			if bead.SessionName == winner.SessionName {
				continue
			}
			result[bead.SessionName] = AwakeDecision{
				ShouldWake:      false,
				Reason:          "singleton-mutex",
				HasAssignedWork: result[bead.SessionName].HasAssignedWork,
			}
		}
	}
}

// singletonSurvivorPreferred reports whether candidate should win over current
// as the single awake bead for a singleton-crew identity. See
// applySingletonIdentityMutex for the preference order.
func singletonSurvivorPreferred(input AwakeInput, candidate, current AwakeSessionBead) bool {
	cs, ws := singletonSurvivorScore(input, candidate), singletonSurvivorScore(input, current)
	if cs != ws {
		return cs > ws
	}
	// Stable tie-break: lowest bead ID wins (deterministic across ticks).
	return candidate.ID < current.ID
}

func singletonSurvivorScore(input AwakeInput, bead AwakeSessionBead) int {
	score := 0
	if input.AttachedSessions[bead.SessionName] {
		score += 8
	}
	if input.RunningSessions[bead.SessionName] && bead.State == "active" {
		score += 4
	}
	if !bead.ManualSession {
		score += 2
	}
	if !bead.ContinuationResetPending {
		score++
	}
	return score
}

func countAssignedScaleSlots(beads []AwakeSessionBead, workBeads []AwakeWorkBead, named []AwakeNamedSession, template string) int {
	n := 0
	for _, bead := range beads {
		if bead.Template != template || bead.State == "closed" {
			continue
		}
		if bead.NamedIdentity != "" || bead.ConfiguredNamedSession || bead.ManualSession {
			continue
		}
		if sessionHasAssignedWork(workBeads, named, bead) {
			n++
		}
	}
	return n
}

func awakeAgentBaseName(name string) string {
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

func findNamedSessionName(beads []AwakeSessionBead, identity string) string {
	for _, b := range beads {
		if b.NamedIdentity == identity {
			return b.SessionName
		}
	}
	return ""
}

// resolveNamedSessionBeadName locates the session_name of the bead that
// represents a configured named session. The primary match is the bead's
// NamedIdentity (configured_named_identity metadata). The fallback matches
// a configured_named_session bead by its session_name when that matches the
// identity's deterministic runtime name AND its template matches.
//
// The fallback recovers a configured named session whose NamedIdentity is
// MISSING on its bead — for example, a bead minted before
// configured_named_identity was added. Beads with a non-empty NamedIdentity
// that doesn't match any [[named_session]] identity are NOT recovered by
// this fallback (those return "" at the bead.NamedIdentity != ns.Identity
// check below); a config-change migration that leaves a stale NamedIdentity
// must be handled separately. Without this fallback, ComputeAwakeSet
// silently drops the bead from `desired` and the session stays asleep
// forever even though the config says mode=always. See #1493.
func resolveNamedSessionBeadName(beads []AwakeSessionBead, ns AwakeNamedSession) string {
	if sn := findNamedSessionName(beads, ns.Identity); sn != "" {
		return sn
	}
	if ns.RuntimeName == "" {
		return ""
	}
	bead := findBeadBySessionName(beads, ns.RuntimeName)
	if bead == nil || !bead.ConfiguredNamedSession {
		return ""
	}
	if ns.Template != "" && bead.Template != ns.Template {
		return ""
	}
	if bead.NamedIdentity != "" && bead.NamedIdentity != ns.Identity {
		return ""
	}
	return bead.SessionName
}

func findBeadBySessionName(beads []AwakeSessionBead, name string) *AwakeSessionBead {
	for i := range beads {
		if beads[i].SessionName == name {
			return &beads[i]
		}
	}
	return nil
}

// isMinActivePoolBead reports whether a bead is a pool-managed instance of
// template that may participate in the min_active_sessions guarantee. Named
// and manual sessions are excluded (they carry their own keep-awake rules),
// as are drained and closed beads (not live, not revivable here).
// Dependency-only beads are excluded too: they wake exclusively via the
// dependency gate, so they neither count toward the min nor are eligible for
// min-active revival — matching collectActiveBeads.
func isMinActivePoolBead(b AwakeSessionBead, template string) bool {
	return b.Template == template &&
		b.NamedIdentity == "" && !b.ConfiguredNamedSession &&
		!b.ManualSession && !b.Drained && !b.DependencyOnly && b.State != "closed"
}

func minActiveHardBlocked(b AwakeSessionBead, now time.Time) bool {
	return b.WaitHold ||
		(!b.HeldUntil.IsZero() && now.Before(b.HeldUntil)) ||
		(!b.QuarantinedUntil.IsZero() && now.Before(b.QuarantinedUntil))
}

// countMinActiveCovered counts pool session beads for template that already
// satisfy the min_active_sessions guarantee: non-asleep live beads
// (active/creating) plus any bead an earlier pass already marked
// desired-awake this tick. An asleep bead with no wake reason does not count —
// that is precisely the deficit the min-active pass fills.
func countMinActiveCovered(beads []AwakeSessionBead, desired map[string]string, template string, now time.Time) int {
	n := 0
	for _, b := range beads {
		if !isMinActivePoolBead(b, template) {
			continue
		}
		if minActiveHardBlocked(b, now) {
			continue
		}
		if b.State == "asleep" {
			if _, awake := desired[b.SessionName]; awake {
				n++
			}
			continue
		}
		// Only live beads (active/creating) count as covering the guarantee.
		// Transitional or non-runnable states (suspended, draining,
		// quarantined, failed-create, stopped, ...) do not — counting them
		// would mask a real deficit and leave the pool cold when there are
		// zero live sessions.
		if b.State == "active" || b.State == "creating" {
			n++
		}
	}
	return n
}

// cityStopPoolBeads returns the asleep, city-stop pool beads for template in
// deterministic order (by bead ID). These are the revival candidates for the
// min_active_sessions wake — restricting to sleep_reason=city-stop keeps
// idle_timeout / wake_mode semantics untouched.
func cityStopPoolBeads(beads []AwakeSessionBead, template string) []AwakeSessionBead {
	var out []AwakeSessionBead
	for _, b := range beads {
		if isMinActivePoolBead(b, template) && b.State == "asleep" && b.SleepReason == "city-stop" {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func isNamedSessionTemplate(named []AwakeNamedSession, template string) bool {
	for _, ns := range named {
		if ns.Template == template {
			return true
		}
	}
	return false
}

func collectActiveBeads(beads []AwakeSessionBead, template string) []AwakeSessionBead {
	var result []AwakeSessionBead
	for _, b := range beads {
		// Exclude both NamedIdentity-tagged beads AND ConfiguredNamedSession
		// beads whose NamedIdentity happens to be missing — the latter are
		// still configured named sessions (recovered via the runtime-name
		// fallback in namedSessionMatches / resolveNamedSessionBeadName).
		// Treating them as generic pool candidates would re-introduce the
		// #1493 failure mode in a different shape: a configured named
		// session getting woken by generic template scale_check demand.
		if b.Template == template && b.State == "active" &&
			b.NamedIdentity == "" && !b.ConfiguredNamedSession &&
			!b.ManualSession && !b.Drained && !b.DependencyOnly {
			result = append(result, b)
		}
	}
	return result
}

func sessionHasAssignedWork(workBeads []AwakeWorkBead, named []AwakeNamedSession, bead AwakeSessionBead) bool {
	for _, wb := range workBeads {
		assignee := strings.TrimSpace(wb.Assignee)
		if assignee == "" || !workBeadHasAwakeDemand(wb) {
			continue
		}
		if sessionAssigneeMatches(named, bead, assignee) {
			return true
		}
	}
	return false
}

func workBeadHasAwakeDemand(bead AwakeWorkBead) bool {
	switch bead.Status {
	case "in_progress":
		return true
	case "open":
		return bead.Ready
	default:
		return false
	}
}

func sessionAssigneeMatches(named []AwakeNamedSession, bead AwakeSessionBead, assignee string) bool {
	if assignee == "" {
		return false
	}
	if assignee == bead.ID || assignee == bead.SessionName {
		return true
	}
	if bead.NamedIdentity != "" {
		return assignee == bead.NamedIdentity
	}
	if !bead.ConfiguredNamedSession {
		return false
	}
	// This configured-named fallback mirrors sessionAssignmentIdentifiersForConfig
	// so awake decisions and cleanup guards recognize the same identities.
	for _, ns := range named {
		if ns.RuntimeName == "" || ns.RuntimeName != bead.SessionName {
			continue
		}
		if ns.Template != "" && ns.Template != bead.Template {
			continue
		}
		if assignee == ns.Identity {
			return true
		}
	}
	return false
}

func isOnDemandSession(named []AwakeNamedSession, bead AwakeSessionBead) bool {
	return namedSessionMatches(named, bead, "on_demand")
}

func isAlwaysNamedSession(named []AwakeNamedSession, bead AwakeSessionBead) bool {
	return namedSessionMatches(named, bead, "always")
}

// namedSessionMatches reports whether bead represents a configured named
// session of the given mode. The fallback path (bead.ConfiguredNamedSession
// + matching runtime name + template) mirrors resolveNamedSessionBeadName
// so a bead with missing/stale NamedIdentity is still recognized as named —
// otherwise idle-sleep suppression and on-demand keep-awake silently lose
// their exemption for affected beads. See #1493.
func namedSessionMatches(named []AwakeNamedSession, bead AwakeSessionBead, mode string) bool {
	for _, ns := range named {
		if ns.Mode != mode {
			continue
		}
		if bead.NamedIdentity != "" && ns.Identity == bead.NamedIdentity {
			return true
		}
		if bead.NamedIdentity == "" && bead.ConfiguredNamedSession &&
			ns.RuntimeName != "" && ns.RuntimeName == bead.SessionName &&
			(ns.Template == "" || ns.Template == bead.Template) {
			return true
		}
	}
	return false
}

func collectCreatingBeads(beads []AwakeSessionBead, template string) []AwakeSessionBead {
	var result []AwakeSessionBead
	for _, b := range beads {
		// See collectActiveBeads above for why ConfiguredNamedSession beads
		// must be excluded even when NamedIdentity is empty.
		if b.Template == template && isCreatingCandidateState(b.State) &&
			b.NamedIdentity == "" && !b.ConfiguredNamedSession &&
			!b.ManualSession && !b.Drained && !b.DependencyOnly {
			result = append(result, b)
		}
	}
	return result
}

func isCreatingCandidateState(state string) bool {
	switch sessionpkg.State(state) {
	case sessionpkg.StateStartPending, sessionpkg.StateCreating:
		return true
	default:
		return false
	}
}
