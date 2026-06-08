package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

type softReloadAcceptanceResult struct {
	Updated        int
	Failed         int
	FailedSessions []string
	CanceledDrains int
	OpenSessions   int
	DesiredEmpty   bool
}

func (r softReloadAcceptanceResult) warnings() []string {
	var warnings []string
	if r.Failed > 0 {
		detail := formatSoftReloadFailedSessions(r.FailedSessions)
		if detail != "" {
			detail = " (" + detail + ")"
		}
		warnings = append(warnings, fmt.Sprintf("soft reload: failed to accept config drift on %d session(s)%s; affected sessions may still drain", r.Failed, detail))
	}
	if r.DesiredEmpty && r.OpenSessions > 0 {
		warnings = append(warnings, fmt.Sprintf("soft reload: desired state is empty; %d open session(s) will not have config drift accepted", r.OpenSessions))
	}
	return warnings
}

func formatSoftReloadFailedSessions(names []string) string {
	if len(names) == 0 {
		return ""
	}
	const limit = 5
	shown := names
	if len(shown) > limit {
		shown = shown[:limit]
	}
	detail := strings.Join(shown, ", ")
	if extra := len(names) - len(shown); extra > 0 {
		detail = fmt.Sprintf("%s, and %d more", detail, extra)
	}
	return detail
}

// acceptConfigDriftAcrossSessions writes the current per-session config
// hash into every open session bead's started_config_hash metadata
// whose desired-state entry produces a different hash than the one
// recorded on the bead. After the function returns, the reconciler's
// drift-detection check (storedHash != currentHash) sees no drift for
// any updated session, so the immediately-following reconcile tick
// proceeds without firing config-drift drains for those sessions.
//
// Used by `gc reload --soft` so an operator editing a running city's
// .gc/settings.json doesn't drain every drifted session — the new
// config is accepted in-place instead. The caller is expected to have
// just rebuilt desired state from the freshly reloaded config.
//
// Sessions are skipped (no metadata write) when:
//   - the session is closed
//   - the session has no session_name metadata
//   - the session has no started_config_hash yet (the existing drift
//     check already skips these — the next first-start path will
//     stamp the right value)
//   - the session's name has no entry in desired (orphaned by the
//     config edit; normal orphan/suspended drain handles them on the
//     next tick)
//   - the recomputed current hash already equals the stored hash
//
// The hash computation uses sessionCoreConfigForHash, the same canonical
// reconciler drift-hash helper used by live and asleep drift detection.
//
// Returns accepted-session, failed-session, stale-drain-cancelation, and
// empty-desired-state diagnostics for the controller reply.
func acceptConfigDriftAcrossSessions(
	store beads.Store,
	desired map[string]TemplateParams,
	sessionBeads *sessionBeadSnapshot,
	sp runtime.Provider,
	dt *drainTracker,
	stderr io.Writer,
) softReloadAcceptanceResult {
	result := softReloadAcceptanceResult{DesiredEmpty: len(desired) == 0}
	if store == nil {
		return result
	}
	if sessionBeads == nil {
		var err error
		sessionBeads, err = loadSessionBeadSnapshot(store)
		if err != nil {
			fmt.Fprintf(stderr, "soft reload: listing session beads: %v\n", err) //nolint:errcheck // best-effort stderr
			return result
		}
	}
	open := sessionBeads.Open()
	result.OpenSessions = len(open)
	if len(open) == 0 {
		return result
	}
	if len(desired) == 0 {
		return result
	}

	for i := range open {
		session := open[i]
		if session.Status == "closed" {
			continue
		}
		name := strings.TrimSpace(session.Metadata["session_name"])
		if name == "" {
			continue
		}
		storedHash := strings.TrimSpace(session.Metadata["started_config_hash"])
		if storedHash == "" {
			continue
		}
		tp, ok := desired[name]
		if !ok {
			continue
		}
		agentCfg := sessionCoreConfigForHash(tp, session)
		currentHash := runtime.CoreFingerprint(agentCfg)
		if storedHash == currentHash {
			continue
		}
		if err := clearSoftReloadConfigDriftDrainAck(session, sp, dt); err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "soft reload: clearing config-drift drain ack metadata for %s: %v; leaving config hash unchanged\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		metadata, err := softReloadAcceptedHashMetadata(agentCfg, currentHash)
		if err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "soft reload: preparing config hash metadata for %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		if err := store.SetMetadataBatch(session.ID, metadata); err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "soft reload: updating config hash metadata for %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		if cancelSoftReloadConfigDriftDrain(session, sp, dt) {
			result.CanceledDrains++
		}
		result.Updated++
	}
	return result
}

// sessionProtectedFromConfigDrift reports whether a session must NEVER be
// drained, re-primed, or restarted as a side effect of a config change — i.e.
// it is attached, pinned, or actively running. This mirrors the conditions
// under which the session reconciler DEFERS config-drift disruption, but is
// evaluated by the watch-reload acceptance path BEFORE the reconciler runs so
// the protected session's started_config_hash is updated in place and the
// reconciler sees no drift for it at all.
//
// Why accept-in-place instead of relying solely on the reconciler's deferral:
// the reconciler's attachment probe (sp.IsAttached / worker-handle observation)
// is a live runtime call that can return a transient false negative. A single
// false-negative tick on a named session routes straight to restart-in-place,
// which kills the agent and re-primes it — irreversibly destroying the human's
// conversation context (see session_reconciler.go: "a single transient
// IsAttached false negative would destroy conversation context irreversibly").
// Accepting drift in place for protected sessions removes the drift before the
// reconciler can act on it, so a later false-negative cannot fire a disruption.
//
// Only the strongest, positively-proven signals protect a session here:
//   - pinned (pin_awake=true) or attached (tmux / Remote Control / worker-handle)
//   - a pending interaction keeping it awake
//   - a named session in active use (recent activity / unreportable activity)
//
// A genuinely idle, unattached, unpinned session returns false and is left for
// the reconciler to reconcile normally. An attachment-probe ERROR (without a
// positive attached result) also returns false: the reconciler's own
// attachErr path skips disruption for that same tick, so not accepting here
// cannot disrupt the session, and we avoid swallowing config for a session we
// could not prove is in use.
// sessionIsAlwaysOnNamed reports whether a session bead is a configured named
// session whose mode is "always" — an always-on session (Mayor, deacon, boot,
// witness, ...) that the controller is meant to keep running continuously. Mode
// is read from the bead's configured_named_mode metadata via namedSessionMode,
// the same source cmd_handoff.go uses for its always-session check. This is the
// probe-independent protection signal: it depends on NO live runtime call and
// NO activity timing, so it holds even for an idle, unpinned, unattached Mayor
// whose IsAttached probe reads false.
func sessionIsAlwaysOnNamed(session beads.Bead) bool {
	return isNamedSessionBead(session) && namedSessionMode(session) == "always"
}

func sessionProtectedFromConfigDrift(
	session beads.Bead,
	sp runtime.Provider,
	cityPath string,
	store beads.Store,
	cfg *config.City,
	name string,
	clk clock.Clock,
) bool {
	// PROBE-INDEPENDENT clause (checked first, before any live runtime call):
	// a named session whose configured mode is "always" (Mayor, deacon, boot,
	// witness, ...) is, by configuration, meant to run continuously. A config
	// edit must NEVER drain, re-prime, or restart it — even when it is idle,
	// unpinned, and not recognized as attached (in `gc session list` it shows
	// "session,config", not "attached"). The three signals below all depend on
	// the flaky sp.IsAttached probe or on recent (<2min) activity; for an idle
	// always-on Mayor all three return false, and the reconciler then routes it
	// straight to resetConfiguredNamedSessionForConfigDrift — the recurring bug
	// that killed the human's Mayor on a config-revision bump. This clause is
	// independent of the probe and of activity, so it protects the always-on
	// session deterministically on every tick. Mode is read from the bead's
	// configured_named_mode metadata via namedSessionMode (same source used by
	// cmd_handoff.go's always-session check).
	if sessionIsAlwaysOnNamed(session) {
		return true
	}
	if attached, _ := sessionAttachedForConfigDrift(session, sp, cityPath, store, cfg, name); attached {
		return true
	}
	if pendingInteractionKeepsAwake(session, sp, name, clk) {
		return true
	}
	if isNamedSessionBead(session) {
		if _, active := namedSessionActiveUseReason(session, sp, name, clk); active {
			return true
		}
	}
	return false
}

// acceptConfigDriftForProtectedSessions is the watch-reload counterpart to
// acceptConfigDriftAcrossSessions. Where the latter (gc reload --soft) accepts
// drift for EVERY open session, this accepts drift in place ONLY for sessions
// that sessionProtectedFromConfigDrift reports as attached / pinned / actively
// running. Idle/unattached sessions are left untouched so the reconciler still
// drains/restarts them on drift.
//
// It runs on every config-changed tick that is NOT an explicit `gc reload
// --soft` (i.e. internal config-watch reloads and plain `gc reload`), enforcing
// the invariant that a config_revision change never drains, re-primes, or
// restarts an attached, pinned, or active session.
func acceptConfigDriftForProtectedSessions(
	store beads.Store,
	desired map[string]TemplateParams,
	sessionBeads *sessionBeadSnapshot,
	sp runtime.Provider,
	dt *drainTracker,
	cityPath string,
	cfg *config.City,
	clk clock.Clock,
	stderr io.Writer,
) softReloadAcceptanceResult {
	result := softReloadAcceptanceResult{DesiredEmpty: len(desired) == 0}
	if store == nil {
		return result
	}
	if sessionBeads == nil {
		var err error
		sessionBeads, err = loadSessionBeadSnapshot(store)
		if err != nil {
			fmt.Fprintf(stderr, "config reload: listing session beads: %v\n", err) //nolint:errcheck // best-effort stderr
			return result
		}
	}
	open := sessionBeads.Open()
	result.OpenSessions = len(open)
	if len(open) == 0 || len(desired) == 0 {
		return result
	}

	for i := range open {
		session := open[i]
		if session.Status == "closed" {
			continue
		}
		name := strings.TrimSpace(session.Metadata["session_name"])
		if name == "" {
			continue
		}
		storedHash := strings.TrimSpace(session.Metadata["started_config_hash"])
		if storedHash == "" {
			continue
		}
		tp, ok := desired[name]
		if !ok {
			continue
		}
		agentCfg := sessionCoreConfigForHash(tp, session)
		currentHash := runtime.CoreFingerprint(agentCfg)
		if storedHash == currentHash {
			continue
		}
		// Only attached/pinned/active sessions are accepted in place; idle ones
		// fall through to the reconciler. Evaluated only on a real drift so the
		// (potentially live) attachment probe is not run for steady-state
		// sessions.
		if !sessionProtectedFromConfigDrift(session, sp, cityPath, store, cfg, name, clk) {
			continue
		}
		if err := clearSoftReloadConfigDriftDrainAck(session, sp, dt); err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "config reload: clearing config-drift drain ack metadata for %s: %v; leaving config hash unchanged\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		metadata, err := softReloadAcceptedHashMetadata(agentCfg, currentHash)
		if err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "config reload: preparing config hash metadata for %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		if err := store.SetMetadataBatch(session.ID, metadata); err != nil {
			result.Failed++
			result.FailedSessions = append(result.FailedSessions, name)
			fmt.Fprintf(stderr, "config reload: updating config hash metadata for %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			continue
		}
		if cancelSoftReloadConfigDriftDrain(session, sp, dt) {
			result.CanceledDrains++
		}
		result.Updated++
	}
	return result
}

func softReloadAcceptedHashMetadata(agentCfg runtime.Config, currentHash string) (map[string]string, error) {
	breakdown, err := json.Marshal(runtime.CoreFingerprintBreakdown(agentCfg))
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"started_config_hash": currentHash,
		"core_hash_breakdown": string(breakdown),
	}, nil
}

func cancelSoftReloadConfigDriftDrain(session beads.Bead, sp runtime.Provider, dt *drainTracker) bool {
	if dt == nil {
		return false
	}
	ds := dt.get(session.ID)
	if ds == nil || ds.reason != "config-drift" {
		return false
	}
	return cancelSessionConfigDriftDrain(session, sp, dt)
}

func clearSoftReloadConfigDriftDrainAck(session beads.Bead, sp runtime.Provider, dt *drainTracker) error {
	if dt == nil {
		return nil
	}
	ds := dt.get(session.ID)
	if ds == nil || ds.reason != "config-drift" || !ds.ackSet {
		return nil
	}
	name := strings.TrimSpace(session.Metadata["session_name"])
	if err := clearReconcilerDrainAckMetadata(sp, name); err != nil {
		return err
	}
	ds.ackSet = false
	return nil
}
