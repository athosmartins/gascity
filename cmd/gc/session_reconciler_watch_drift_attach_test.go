package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Regression suite for the config-watch reload disruption bug.
//
// Production symptom ("all sessions suddenly stopped"): editing any watched
// config file (including a town-deltas pack asset) bumps config_revision; the
// controller's internal config-watch reload (source="watch") rebuilds config
// and lets the session reconciler treat every drifted session as needing
// drain / re-prime / restart — including the human's attached Mayor session and
// pinned crew. Unlike `gc reload --soft`, the watch path never accepted drift
// in place, so the only protection was the reconciler's live attachment probe,
// which can transiently false-negative and irreversibly kill a named session.
//
// The fix (acceptConfigDriftForProtectedSessions, wired into the watch/non-soft
// reload tick BEFORE the reconciler) accepts drift in place for attached,
// pinned, and actively-running sessions so the reconciler sees no drift for
// them. Idle/unattached sessions are left untouched and still reconcile.
//
// These tests run the SAME two-phase order as the real tick: first the protected
// drift-acceptance pass, then the full reconcile — and assert the protected
// session is neither hash-drifted (so no re-prime/restart), nor drained, nor
// stopped, while the idle control still drains.

// storedConfigHash reads the durable started_config_hash from the store (not the
// stale in-memory bead) so the test observes what the acceptance pass actually
// wrote.
func storedConfigHash(t *testing.T, store beads.Store, id string) string {
	t.Helper()
	b, err := store.Get(id)
	if err != nil {
		t.Fatalf("reading session bead %s: %v", id, err)
	}
	return b.Metadata["started_config_hash"]
}

func reloadSnapshot(t *testing.T, store beads.Store) *sessionBeadSnapshot {
	t.Helper()
	snap, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("loading session snapshot: %v", err)
	}
	return snap
}

// TestAttachedSessionConfigDriftAcceptedInPlaceOnWatchReload: an alive, attached
// (external tmux / Remote Control) session in drift must have its drift accepted
// in place by the watch-reload pass — started_config_hash updated to the current
// hash — and then survive the reconcile with NO drain and NO stop.
func TestAttachedSessionConfigDriftAcceptedInPlaceOnWatchReload(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	startedHash := runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": startedHash,
	})
	env.sp.SetAttached("worker", true)

	// Phase 1: watch-reload protected-drift acceptance (what the tick runs
	// before the reconciler when configChanged and the reload is not --soft).
	res := acceptConfigDriftForProtectedSessions(
		env.store, env.desiredState, reloadSnapshot(t, env.store),
		env.sp, env.dt, "", env.cfg, env.clk, &env.stderr,
	)
	if res.Updated != 1 {
		t.Fatalf("attached session drift must be accepted in place; updated=%d stderr=%s", res.Updated, env.stderr.String())
	}
	got := storedConfigHash(t, env.store, session.ID)
	if got == startedHash || got == "" {
		t.Fatalf("started_config_hash must be rebaselined off the stale hash; got=%q stale=%q", got, startedHash)
	}

	// Phase 2: reconcile with the post-acceptance bead — reconciler sees no
	// drift, so no drain / re-prime / restart.
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("attached session must NOT be drained by acceptance pass; got %+v", ds)
	}
	env.reconcile(reloadSnapshot(t, env.store).Open())
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("attached session must NOT drain on config drift after acceptance; got drain=%+v stderr=%s", ds, env.stderr.String())
	}
}

// TestPinnedNamedSessionConfigDriftAcceptedInPlaceOnWatchReload: the catastrophic
// case — a pinned, alive, always-named session in drift. The unguarded watch
// path restart-in-place kills the agent and re-primes it. The acceptance pass
// must rebaseline the hash so the named-session restart branch never fires; the
// session stays running and undrained.
func TestPinnedNamedSessionConfigDriftAcceptedInPlaceOnWatchReload(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "worker", StartCommand: "true"}},
		NamedSessions: []config.NamedSession{{Template: "worker", Mode: "always"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "worker")
	env.desiredState[sessionName] = TemplateParams{
		Command:                 "new-cmd",
		SessionName:             sessionName,
		TemplateName:            "worker",
		ConfiguredNamedIdentity: "worker",
		ConfiguredNamedMode:     "always",
	}
	if err := env.sp.Start(t.Context(), sessionName, runtime.Config{Command: "new-cmd"}); err != nil {
		t.Fatalf("starting named session: %v", err)
	}
	session := env.createSessionBead(sessionName, "worker")
	env.markSessionActive(&session)
	startedHash := runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "worker",
		namedSessionModeMetadata:     "always",
		"started_config_hash":        startedHash,
		"pin_awake":                  "true",
	})

	res := acceptConfigDriftForProtectedSessions(
		env.store, env.desiredState, reloadSnapshot(t, env.store),
		env.sp, env.dt, "", env.cfg, env.clk, &env.stderr,
	)
	if res.Updated != 1 {
		t.Fatalf("pinned named session drift must be accepted in place; updated=%d stderr=%s", res.Updated, env.stderr.String())
	}
	if got := storedConfigHash(t, env.store, session.ID); got == startedHash || got == "" {
		t.Fatalf("pinned named session hash must be rebaselined; got=%q stale=%q", got, startedHash)
	}

	env.reconcile(reloadSnapshot(t, env.store).Open())
	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("pinned named session must NOT be restarted/killed (re-primed) on config drift; stderr=%s", env.stderr.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("pinned named session must NOT be drained on config drift; got drain=%+v stderr=%s", ds, env.stderr.String())
	}
}

// newIdleUnpinnedAlwaysNamedDriftEnv builds the EXACT catastrophic production
// state: an ALIVE, IDLE, UNPINNED, UNATTACHED always-on named session
// (mode="always" — the human's Mayor) whose started_config_hash no longer
// matches the current config. Critically it sets NONE of the cheat signals that
// the prior tests leaned on:
//   - no pin_awake          (so sessionPinnedAwake / attach-guard is false)
//   - no sp.SetAttached      (so IsAttached returns FALSE — the real Mayor state,
//     "session,config" not "attached" in gc session list)
//   - no sp.SetLastActivity  (so GetLastActivity is zero → NOT recent_activity)
//
// markSessionActive only sets state=active + last_woke_at metadata; it does NOT
// touch the provider's activity clock, so namedSessionActiveUseReason returns
// idle. The ONLY thing that can protect this session is the probe-independent
// mode=="always" clause in sessionProtectedFromConfigDrift. On the unpatched
// code it reaches resetConfiguredNamedSessionForConfigDrift and is killed +
// re-primed — the recurring bug.
func newIdleUnpinnedAlwaysNamedDriftEnv(t *testing.T) (*reconcilerTestEnv, beads.Bead, string, string) {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Agents:        []config.Agent{{Name: "worker", StartCommand: "true"}},
		NamedSessions: []config.NamedSession{{Template: "worker", Mode: "always"}},
	}
	sessionName := config.NamedSessionRuntimeName(env.cfg.Workspace.Name, env.cfg.Workspace, "worker")
	env.desiredState[sessionName] = TemplateParams{
		Command:                 "new-cmd",
		SessionName:             sessionName,
		TemplateName:            "worker",
		ConfiguredNamedIdentity: "worker",
		ConfiguredNamedMode:     "always",
	}
	if err := env.sp.Start(t.Context(), sessionName, runtime.Config{Command: "new-cmd"}); err != nil {
		t.Fatalf("starting named session: %v", err)
	}
	session := env.createSessionBead(sessionName, "worker")
	env.markSessionActive(&session)
	startedHash := runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "worker",
		namedSessionModeMetadata:     "always",
		"started_config_hash":        startedHash,
		// NO pin_awake. NO attach. NO activity. Genuinely idle.
	})
	// Sanity: this is real drift, and the session is genuinely not attached so
	// the test cannot pass by accident via the attach/pin guard.
	if cur := runtime.CoreFingerprint(runtime.Config{Command: "new-cmd"}); cur == startedHash {
		t.Fatalf("test setup error: stored hash %q must differ from current", startedHash)
	}
	if env.sp.IsAttached(sessionName) {
		t.Fatalf("test setup error: session must NOT be attached (the whole point)")
	}
	if sessionPinnedAwake(session) {
		t.Fatalf("test setup error: session must NOT be pinned (the whole point)")
	}
	return env, session, sessionName, startedHash
}

// TestIdleUnpinnedAlwaysNamedSessionNotDisruptedOnConfigChangedTick reproduces
// the catastrophic path on the CONFIG-CHANGED tick: the watch-reload acceptance
// pass must protect the idle, unpinned, unattached always-named Mayor (via the
// probe-independent mode=="always" clause) and rebaseline its hash, so the
// following reconcile sees no drift and never kills / re-primes / drains it.
func TestIdleUnpinnedAlwaysNamedSessionNotDisruptedOnConfigChangedTick(t *testing.T) {
	env, session, sessionName, startedHash := newIdleUnpinnedAlwaysNamedDriftEnv(t)

	// Phase 1: config-changed tick → protected-drift acceptance pass.
	res := acceptConfigDriftForProtectedSessions(
		env.store, env.desiredState, reloadSnapshot(t, env.store),
		env.sp, env.dt, "", env.cfg, env.clk, &env.stderr,
	)
	if res.Updated != 1 {
		t.Fatalf("idle unpinned always-named session drift must be accepted in place (mode=always clause); updated=%d stderr=%s", res.Updated, env.stderr.String())
	}
	if got := storedConfigHash(t, env.store, session.ID); got == startedHash || got == "" {
		t.Fatalf("always-named session hash must be rebaselined off the stale hash; got=%q stale=%q", got, startedHash)
	}

	// Phase 2: reconcile with the post-acceptance bead — no drift, no disruption.
	env.reconcile(reloadSnapshot(t, env.store).Open())
	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("idle unpinned always-named session must NOT be restarted/killed (re-primed) on config drift; stderr=%s", env.stderr.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("idle unpinned always-named session must NOT be drained on config drift; got drain=%+v stderr=%s", ds, env.stderr.String())
	}
}

// TestIdleUnpinnedAlwaysNamedSessionNotResetOutOfBand is the test the review
// said was missing. It exercises the OUT-OF-BAND (non-configChanged) steady-state
// tick: drift surfaces WITHOUT the acceptance pass running first (stale hash
// carried in from session start, or drift only actionable after an idle/detach
// transition). The reconciler reaches its drift-reset decision directly. The
// short-circuit in the named-session reset block (sessionProtectedFromConfigDrift
// → silentRebaselineSessionHashes) must soft-accept in place — NOT kill / re-prime
// / drain. On the unpatched reconciler this session hits
// resetConfiguredNamedSessionForConfigDrift and dies.
func TestIdleUnpinnedAlwaysNamedSessionNotResetOutOfBand(t *testing.T) {
	env, session, sessionName, startedHash := newIdleUnpinnedAlwaysNamedDriftEnv(t)

	// NO acceptance pass — straight into the reconciler on a steady-state tick.
	env.reconcile([]beads.Bead{session})

	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("out-of-band: idle unpinned always-named session must NOT be restarted/killed on config drift; stderr=%s", env.stderr.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("out-of-band: idle unpinned always-named session must NOT be drained on config drift; got drain=%+v stderr=%s", ds, env.stderr.String())
	}
	// The short-circuit soft-accepts in place: the durable hash must be
	// rebaselined so the session does not re-trigger drift every subsequent tick.
	if got := storedConfigHash(t, env.store, session.ID); got == startedHash || got == "" {
		t.Fatalf("out-of-band: always-named session hash must be rebaselined in place; got=%q stale=%q", got, startedHash)
	}

	// A second steady-state tick must be stable too (no flap, no disruption).
	env.reconcile(reloadSnapshot(t, env.store).Open())
	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("out-of-band: always-named session must stay running on a follow-up steady-state tick; stderr=%s", env.stderr.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("out-of-band: always-named session must NOT drain on a follow-up steady-state tick; got drain=%+v stderr=%s", ds, env.stderr.String())
	}
}

// TestIdleSessionConfigDriftNotAcceptedAndStillDrains is the negative control:
// an unpinned, unattached, idle, non-named session in drift must NOT be accepted
// in place by the protected-drift pass (so the fix doesn't over-reach and pin
// everything alive) and MUST still drain when the reconciler runs — preserving
// normal config-drift reconciliation for genuinely idle sessions.
func TestIdleSessionConfigDriftNotAcceptedAndStillDrains(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	startedHash := runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": startedHash,
	})
	// No attach, no pin, no pending interaction → not protected.

	res := acceptConfigDriftForProtectedSessions(
		env.store, env.desiredState, reloadSnapshot(t, env.store),
		env.sp, env.dt, "", env.cfg, env.clk, &env.stderr,
	)
	if res.Updated != 0 {
		t.Fatalf("idle session drift must NOT be accepted in place; updated=%d", res.Updated)
	}
	if got := storedConfigHash(t, env.store, session.ID); got != startedHash {
		t.Fatalf("idle session hash must be left untouched for the reconciler; got=%q want=%q", got, startedHash)
	}

	env.reconcile(reloadSnapshot(t, env.store).Open())
	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("idle session SHOULD drain on config drift; stderr=%s", env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("idle drain reason = %q, want config-drift", ds.reason)
	}
}
