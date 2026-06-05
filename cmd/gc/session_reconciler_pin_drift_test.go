package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// ga-84rm faithful reproduction suite.
//
// These tests exercise the FULL reconcile loop (reconcileSessionBeads ->
// reconcileSessionBeadsTracedWithNamedDemand) against an in-memory store and a
// fake runtime, and assert the actual drain/restart decision — not just
// deferral-metadata bookkeeping. They reproduce the observed production bug:
// pinned and externally-attached crew sessions (Oracle/Mila, which carry
// pin_awake=true but NOT configured_named_session) received an
// outcome=drain config-drift decision on every config_revision bump
// (drifted_fields=["CopyFiles"]).
//
// On the unpatched live-equivalent code (v1.2.0 / v1.2.1 parent eedcee360):
//   - TestPinnedSessionSparedFromConfigDriftDrain FAILS (drain initiated).
//
// With the config-drift pin guard in sessionAttachedForConfigDrift these tests
// pass: a pinned OR externally-attached session — named or pool/ordinary — is
// deferred (kept), never drained or restarted-in-place, on config drift.

// TestPinnedSessionSparedFromConfigDriftDrain reproduces the exact Oracle/Mila
// case: an alive, idle, NON-named session (no configured_named_session) that is
// pinned (pin_awake=true) whose started_config_hash no longer matches the
// current fingerprint. The unpatched ordinary-session drift branch
// (session_reconciler.go beginSessionDrain "config-drift") drained it; the pin
// guard must defer instead.
func TestPinnedSessionSparedFromConfigDriftDrain(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	startedHash := runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": startedHash,
		"pin_awake":           "true",
	})

	// Sanity: hashes genuinely differ (real config drift, not a no-op).
	if cur := runtime.CoreFingerprint(runtime.Config{Command: "new-cmd"}); cur == startedHash {
		t.Fatalf("test setup error: stored hash %q must differ from current %q", startedHash, cur)
	}

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("ga-84rm: pinned session must NOT be drained on config drift; got drain=%+v stderr=%s",
			ds, env.stderr.String())
	}
}

// TestAttachedSessionSparedFromConfigDriftDrain covers the externally-attached
// (Remote Control tmux client) case for a non-named session.
func TestAttachedSessionSparedFromConfigDriftDrain(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"}),
	})
	env.sp.SetAttached("worker", true)

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("ga-84rm: externally-attached session must NOT be drained on config drift; got drain=%+v stderr=%s",
			ds, env.stderr.String())
	}
}

// TestPinnedNamedSessionSparedFromConfigDriftRestart covers the named-session
// path: a pinned, alive, always-named session in drift must not be killed and
// restarted-in-place (which would lose conversation context). The shared
// attached/pin guard (checked before the named restart branch) defers it.
func TestPinnedNamedSessionSparedFromConfigDriftRestart(t *testing.T) {
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
	_ = env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "new-cmd"})
	session := env.createSessionBead(sessionName, "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "worker",
		namedSessionModeMetadata:     "always",
		"started_config_hash":        runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"}),
		"pin_awake":                  "true",
	})

	env.reconcile([]beads.Bead{session})

	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("ga-84rm: pinned named session must NOT be restarted/killed on config drift; stderr=%s",
			env.stderr.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("ga-84rm: pinned named session must NOT be drained on config drift; got drain=%+v stderr=%s",
			ds, env.stderr.String())
	}
}

// TestUnpinnedIdleSessionStillDrainsOnConfigDrift is the negative control: an
// unpinned, unattached, idle, non-named session in drift MUST still drain. This
// guards against the guard over-reaching and pinning everything alive.
func TestUnpinnedIdleSessionStillDrainsOnConfigDrift(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"}),
	})

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("ga-84rm control: unpinned idle session SHOULD drain on config drift; stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}
