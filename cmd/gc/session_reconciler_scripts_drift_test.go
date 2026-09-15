package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// ga-n9bw — Exemption 2 (content-based scripts-only) in the fresh-wake
// ephemeral config-drift branch of reconcileSessionBeads.
//
// Root cause these tests pin: the CopyFiles fingerprint hashes the entire city
// scripts/ tree (staged into each worktree as runtime.ScriptsCopyRelDst =
// ".gc/scripts"), so editing ANY scripts/ file re-hashes it → config-drift →
// the reconciler drains fresh-wake ephemeral workers (gate-reviewers,
// wa-worker/ps-worker) that are still in their creating window. That staged
// copy is inert for these workers, so the drain only burns the dispatch
// (NEVERSTARTED builders, drained reviewers → gate throughput collapse).
//
// The tests deliberately drive a STALE creating session (pending_create_started
// _at pushed past asyncStartDriftExemptionTimeout) so Exemption 1 (the
// time-based async-start window) cannot fire. Only Exemption 2 can defer the
// drain here — which makes these tests self-evidently non-vacuous: the sibling
// TestReconcileSessionBeads_ConfigDriftDrainsFreshWakePoolWorkerStaleCreating
// proves the SAME stale-creating fresh-wake session drains on a Command drift,
// so a not-drained result under a scripts-only drift can ONLY be Exemption 2.

const scriptsDriftWorkerTemplate = "wa-worker"

// scriptsCopyEntry returns a probed CopyFiles entry for the staged city
// scripts/ tree with the given content hash — the entry whose churn Exemption 2
// suppresses. RelDst matches runtime.ScriptsCopyRelDst exactly.
func scriptsCopyEntry(contentHash string) runtime.CopyEntry {
	return runtime.CopyEntry{
		Src:         "/city/scripts",
		RelDst:      runtime.ScriptsCopyRelDst,
		Probed:      true,
		ContentHash: contentHash,
	}
}

// settingsCopyEntry returns a probed CopyFiles entry for a NON-scripts staged
// file (managed Claude settings). Included alongside the scripts entry so the
// tests prove Exemption 2 isolates the scripts entry among multiple copy
// entries rather than blanket-exempting all CopyFiles drift.
func settingsCopyEntry(contentHash string) runtime.CopyEntry {
	return runtime.CopyEntry{
		Src:         "/city/settings.json",
		RelDst:      ".claude/settings.json",
		Probed:      true,
		ContentHash: contentHash,
	}
}

// setupStaleCreatingFreshWakeDrift wires a fresh-wake (wake_mode=fresh)
// wa-worker session `name` as a STALE creating session whose live desired
// config is `tp` and whose stored start-time fingerprint reflects `mutate(cur)`
// — the config as it was BEFORE the drift. The stored breakdown is written in
// the same BreakdownV1 shape the start path uses (json.Marshal of
// CoreFingerprintBreakdown), and started_config_hash is the matching
// CoreFingerprint, so the reconciler observes real drift between them.
//
// `mutate` receives the exact config the reconciler will hash for the live side
// (sessionCoreConfigForHash(tp, session)) and returns the pre-drift config, so
// the injected drift is provably confined to whatever `mutate` changes.
func setupStaleCreatingFreshWakeDrift(
	t *testing.T,
	name string,
	tp TemplateParams,
	mutate func(current runtime.Config) runtime.Config,
) (*reconcilerTestEnv, beads.Bead) {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: scriptsDriftWorkerTemplate, WakeMode: "fresh"}}}
	env.desiredState[name] = tp
	_ = env.sp.Start(context.Background(), name, runtime.Config{Command: tp.Command})
	session := env.createSessionBead(name, scriptsDriftWorkerTemplate)

	// currentCfg is exactly what the reconciler hashes for drift detection.
	currentCfg := sessionCoreConfigForHash(tp, session)
	oldCfg := mutate(currentCfg)

	started := runtime.CoreFingerprint(oldCfg)
	if started == runtime.CoreFingerprint(currentCfg) {
		t.Fatalf("test setup: stored (old) and live (current) core hashes must differ")
	}
	breakdownJSON, err := json.Marshal(runtime.CoreFingerprintBreakdown(oldCfg))
	if err != nil {
		t.Fatalf("marshaling stored breakdown: %v", err)
	}

	// Past the async-start window → Exemption 1 (time-based) cannot fire, so
	// only Exemption 2 (scripts-only content) can defer a drain here.
	stalePast := time.Now().Add(-asyncStartDriftExemptionTimeout - time.Minute).UTC().Format(time.RFC3339)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash":       started,
		"core_hash_breakdown":       string(breakdownJSON),
		"pending_create_started_at": stalePast,
	})
	env.markSessionCreating(&session)
	return env, session
}

// TestReconcileSessionBeads_ConfigDriftDeferredOnScriptsOnlyContentChange is the
// ga-n9bw fix: a fresh-wake ephemeral worker whose ONLY config drift is a
// content-hash change to the staged scripts/ tree (.gc/scripts) must NOT be
// drained, even once the async-start window has lapsed. A settings entry is
// present and unchanged to prove the exemption isolates the scripts entry.
func TestReconcileSessionBeads_ConfigDriftDeferredOnScriptsOnlyContentChange(t *testing.T) {
	name := "wa-worker-scripts-only"
	tp := TemplateParams{
		Command:      "steady-cmd",
		SessionName:  name,
		TemplateName: scriptsDriftWorkerTemplate,
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			settingsCopyEntry("settings-hash"),
			scriptsCopyEntry("scripts-hash-NEW"),
		}},
	}
	env, session := setupStaleCreatingFreshWakeDrift(t, name, tp, func(current runtime.Config) runtime.Config {
		old := current
		// Same entry set, settings unchanged; only the scripts content hash
		// differs (as a scripts/ file edit would produce).
		old.CopyFiles = []runtime.CopyEntry{
			settingsCopyEntry("settings-hash"),
			scriptsCopyEntry("scripts-hash-OLD"),
		}
		return old
	})

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("ga-n9bw: fresh-wake worker with scripts-only content drift must NOT be drained; got drain=%+v stderr=%s",
			ds, env.stderr.String())
	}
}

// TestReconcileSessionBeads_ConfigDriftStillDrainsOnCommandChangeWithScriptsEntry
// is the regression guard: a non-scripts drift (Command) on the SAME stale
// fresh-wake creating session STILL drains, even though a scripts CopyFiles
// entry is present in the fingerprint. Proves Exemption 2 does not leak to
// genuine config changes.
func TestReconcileSessionBeads_ConfigDriftStillDrainsOnCommandChangeWithScriptsEntry(t *testing.T) {
	name := "wa-worker-cmd-drift"
	tp := TemplateParams{
		Command:      "new-cmd",
		SessionName:  name,
		TemplateName: scriptsDriftWorkerTemplate,
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			scriptsCopyEntry("scripts-hash-SAME"),
		}},
	}
	env, session := setupStaleCreatingFreshWakeDrift(t, name, tp, func(current runtime.Config) runtime.Config {
		old := current
		old.Command = "old-cmd" // only Command drifts; scripts entry identical
		old.CopyFiles = []runtime.CopyEntry{scriptsCopyEntry("scripts-hash-SAME")}
		return old
	})

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("regression: Command drift on fresh-wake worker MUST still drain despite a scripts entry in the fingerprint; stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}

// TestReconcileSessionBeads_ConfigDriftStillDrainsWhenScriptsChangesAlongsideCommand
// hardens the guard: when a scripts content change co-occurs with a real
// Command change, the drift is NOT scripts-only (two fields drift), so the
// session STILL drains. Prevents a scripts edit from masking a bundled genuine
// config change.
func TestReconcileSessionBeads_ConfigDriftStillDrainsWhenScriptsChangesAlongsideCommand(t *testing.T) {
	name := "wa-worker-cmd-and-scripts-drift"
	tp := TemplateParams{
		Command:      "new-cmd",
		SessionName:  name,
		TemplateName: scriptsDriftWorkerTemplate,
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			scriptsCopyEntry("scripts-hash-NEW"),
		}},
	}
	env, session := setupStaleCreatingFreshWakeDrift(t, name, tp, func(current runtime.Config) runtime.Config {
		old := current
		old.Command = "old-cmd" // Command AND scripts both drift
		old.CopyFiles = []runtime.CopyEntry{scriptsCopyEntry("scripts-hash-OLD")}
		return old
	})

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("hardening: Command+scripts drift MUST drain (drift is not scripts-only); stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}

// TestReconcileSessionBeads_ScriptsOnlyDriftStillDrainsResumeWakeCreating pins
// the scoping: the scripts-only exemption applies to wake_mode=fresh ephemeral
// workers ONLY. A resume-wake session (EffectiveWakeMode() != "fresh") in the
// same stale creating state with the same scripts-only drift STILL drains, so
// the exemption cannot leak to named/always-on sessions that must restart
// promptly.
func TestReconcileSessionBeads_ScriptsOnlyDriftStillDrainsResumeWakeCreating(t *testing.T) {
	name := "worker"
	tp := TemplateParams{
		Command:      "steady-cmd",
		SessionName:  name,
		TemplateName: "worker",
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			scriptsCopyEntry("scripts-hash-NEW"),
		}},
	}
	env := newReconcilerTestEnv()
	// "worker" has no WakeMode set → EffectiveWakeMode() == "resume", not fresh.
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.desiredState[name] = tp
	_ = env.sp.Start(context.Background(), name, runtime.Config{Command: tp.Command})
	session := env.createSessionBead(name, "worker")

	currentCfg := sessionCoreConfigForHash(tp, session)
	oldCfg := currentCfg
	oldCfg.CopyFiles = []runtime.CopyEntry{scriptsCopyEntry("scripts-hash-OLD")}
	started := runtime.CoreFingerprint(oldCfg)
	if started == runtime.CoreFingerprint(currentCfg) {
		t.Fatalf("test setup: stored/live core hashes must differ")
	}
	breakdownJSON, err := json.Marshal(runtime.CoreFingerprintBreakdown(oldCfg))
	if err != nil {
		t.Fatalf("marshaling stored breakdown: %v", err)
	}
	stalePast := time.Now().Add(-asyncStartDriftExemptionTimeout - time.Minute).UTC().Format(time.RFC3339)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash":       started,
		"core_hash_breakdown":       string(breakdownJSON),
		"pending_create_started_at": stalePast,
	})
	env.markSessionCreating(&session)

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("scoping: resume-wake worker with scripts-only drift MUST still drain (exemption is fresh-wake only); stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}

// TestReconcileSessionBeads_ScriptsDriftDoesNotDrainAwakeWorkerWithAssignedWork
// closes the gap the ga-n9bw tests above leave open (see file header): they
// only drive a STALE-CREATING session, so only Exemptions 1/2 are exercised.
// A wa-worker past its creating window — awake, actively doing a task — has
// no exemption 1/2 protection at all (those are gated to wasCreating) and
// relies entirely on the plain "live assigned work" defer at
// session_reconciler.go (sessionHasOpenAssignedWorkForReachableStore,
// "Skipping config-drift drain ...: live assigned work found").
//
// Before gt-69j2v's fix, that check resolved a dir-less wa-worker's store via
// assignedWorkStoreRefForSession -> assignedWorkStoreRefForAgent ->
// configuredRigName, which returns "" (city-scoped) for a dir-less agent BY
// DESIGN (see TestConfiguredRigNameDirLessAgentWithRigWorkDirStaysCityScoped
// in internal/workdir — that contract must NOT change, ga-f7k65a). So the
// check looked in the CITY store instead of the whatsapp_automation rig store
// where wa-worker's real claimed bead lives, found nothing, and the session
// drained mid-task. This is the exact shape of the wa-x0ppe incident (P1 bug
// lost committed-but-unpushed work twice on 2026-09-11): the reconciler's own
// "live assigned work" protection existed and was completely inert for every
// dir-less pool worker. The fix routes this ONE lookup through
// assignedWorkRoutingStoreRefForAgent (workdirutil.RoutingRigName) instead,
// leaving ConfiguredRigName/assignedWorkStoreRefForAgent's other callers
// (pool-demand, session-wake, desired-state) untouched.
func TestReconcileSessionBeads_ScriptsDriftDoesNotDrainAwakeWorkerWithAssignedWork(t *testing.T) {
	name := "wa-worker-1"
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "whatsapp_automation")
	if err := os.MkdirAll(filepath.Join(rigPath, "crew", "worker"), 0o755); err != nil {
		t.Fatal(err)
	}

	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Rigs: []config.Rig{{Name: "whatsapp_automation", Path: rigPath}},
		Agents: []config.Agent{{
			Name:     scriptsDriftWorkerTemplate,
			WakeMode: "fresh",
			WorkDir:  filepath.Join(rigPath, "crew", "worker"),
		}},
	}
	tp := TemplateParams{
		Command:      "steady-cmd",
		SessionName:  name,
		TemplateName: scriptsDriftWorkerTemplate,
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			scriptsCopyEntry("scripts-hash-NEW"),
		}},
	}
	env.desiredState[name] = tp
	_ = env.sp.Start(context.Background(), name, runtime.Config{Command: tp.Command})
	session := env.createSessionBead(name, scriptsDriftWorkerTemplate)
	env.markSessionActive(&session) // awake and working, NOT creating -- the untested branch.

	currentCfg := sessionCoreConfigForHash(tp, session)
	oldCfg := currentCfg
	oldCfg.CopyFiles = []runtime.CopyEntry{scriptsCopyEntry("scripts-hash-OLD")}
	started := runtime.CoreFingerprint(oldCfg)
	if started == runtime.CoreFingerprint(currentCfg) {
		t.Fatalf("test setup: stored (old) and live (current) core hashes must differ")
	}
	breakdownJSON, err := json.Marshal(runtime.CoreFingerprintBreakdown(oldCfg))
	if err != nil {
		t.Fatalf("marshaling stored breakdown: %v", err)
	}
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": started,
		"core_hash_breakdown": string(breakdownJSON),
	})

	// The session's real, live work -- claimed in its own rig's store, exactly
	// like a real wa-worker's `bd` context, not the city store.
	rigStore := beads.NewMemStore()
	if _, err := rigStore.Create(beads.Bead{
		ID:       "wa-x0ppe",
		Title:    "in-flight P1 fix",
		Type:     "task",
		Status:   "in_progress",
		Assignee: session.ID,
	}); err != nil {
		t.Fatalf("Create rig work: %v", err)
	}
	rigStores := map[string]beads.Store{"whatsapp_automation": rigStore}

	reconcileSessionBeadsAtPath(
		context.Background(),
		cityPath,
		[]beads.Bead{session},
		env.desiredState,
		map[string]bool{scriptsDriftWorkerTemplate: true},
		env.cfg,
		env.sp,
		env.store,
		newFakeDrainOps(),
		nil,
		rigStores,
		nil,
		env.dt,
		nil,
		false,
		nil,
		"",
		nil,
		env.clk,
		env.rec,
		0,
		0,
		&env.stdout,
		&env.stderr,
	)

	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("gt-69j2v: awake wa-worker with real in_progress work in its rig store must NOT be drained on config-drift; got drain=%+v stdout=%s stderr=%s",
			ds, env.stdout.String(), env.stderr.String())
	}
}
