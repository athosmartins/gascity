package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// ga-gn35963 — a scripts-only config drift must not drain a wake_mode=fresh
// pool session that still owns a live (open/in_progress) bead, even when the
// agent's configured store resolves to the CITY store while the bead lives in a
// rig store.
//
// Real shape (agents/wa-worker-remote/agent.toml): no `dir`, no `scope`,
// work_dir = ".gc/agents/wa-worker-remote" — OUTSIDE every rig. Its claimed
// bead (wa-p7ag21.14.2.3, assignee wa-worker-remote-1) lives in the
// whatsapp_automation rig store. assignedWorkRoutingStoreRefForAgent resolves
// such an agent to "" (city store), so the drift guard
// (sessionHasOpenAssignedWorkForReachableStore) searched ONLY the city store,
// read "no work", and the scripts-only drift drained the worker mid-task. The
// sibling shape that works is a work_dir INSIDE the rig (gt-69j2v; see
// TestReconcileSessionBeads_ScriptsDriftDoesNotDrainAwakeWorkerWithAssignedWork).
//
// Every test drives an AWAKE (not creating) session, so the ga-n9bw creating-
// window exemptions cannot fire: only the live-assigned-work guard can defer.

const cityScopeDriftTemplate = "wa-worker-remote"

type cityScopeDriftCase struct {
	env      *reconcilerTestEnv
	cityPath string
	session  beads.Bead
}

// newCityScopeDriftCase wires an awake wake_mode=fresh session of agent `ag`
// (template cityScopeDriftTemplate, session name "<template>-1") whose ONLY
// config drift is the staged .gc/scripts content hash. The city has two rigs,
// whatsapp_automation and property_scrapers.
func newCityScopeDriftCase(t *testing.T, ag config.Agent) *cityScopeDriftCase {
	t.Helper()
	cityPath := t.TempDir()
	waRig := filepath.Join(cityPath, "whatsapp_automation")
	psRig := filepath.Join(cityPath, "property_scrapers")
	for _, dir := range []string{waRig, psRig} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ag.Name = cityScopeDriftTemplate
	ag.WakeMode = "fresh"

	name := cityScopeDriftTemplate + "-1"
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Rigs: []config.Rig{
			{Name: "whatsapp_automation", Path: waRig},
			{Name: "property_scrapers", Path: psRig},
		},
		Agents: []config.Agent{ag},
	}
	tp := TemplateParams{
		Command:      "steady-cmd",
		SessionName:  name,
		TemplateName: cityScopeDriftTemplate,
		Hints: agent.StartupHints{CopyFiles: []runtime.CopyEntry{
			scriptsCopyEntry("scripts-hash-NEW"),
		}},
	}
	env.desiredState[name] = tp
	_ = env.sp.Start(context.Background(), name, runtime.Config{Command: tp.Command})
	session := env.createSessionBead(name, cityScopeDriftTemplate)
	env.markSessionActive(&session)

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
	return &cityScopeDriftCase{env: env, cityPath: cityPath, session: session}
}

// remoteWorkerAgent is the real wa-worker-remote shape: no dir, no scope,
// work_dir outside every rig (relative to the city, like agent.toml).
func remoteWorkerAgent() config.Agent {
	return config.Agent{WorkDir: ".gc/agents/wa-worker-remote"}
}

// reconcile runs one reconcile tick over the session with the given rig stores.
func (c *cityScopeDriftCase) reconcile(rigStores map[string]beads.Store) {
	reconcileSessionBeadsAtPath(
		context.Background(),
		c.cityPath,
		[]beads.Bead{c.session},
		c.env.desiredState,
		map[string]bool{cityScopeDriftTemplate: true},
		c.env.cfg,
		c.env.sp,
		c.env.store,
		newFakeDrainOps(),
		nil,
		rigStores,
		nil,
		c.env.dt,
		nil,
		false,
		nil,
		"",
		nil,
		c.env.clk,
		c.env.rec,
		0,
		0,
		&c.env.stdout,
		&c.env.stderr,
	)
}

func (c *cityScopeDriftCase) requireNotDrained(t *testing.T, why string) {
	t.Helper()
	if ds := c.env.dt.get(c.session.ID); ds != nil {
		t.Fatalf("%s: session must NOT be drained on config-drift; got drain=%+v stdout=%s stderr=%s",
			why, ds, c.env.stdout.String(), c.env.stderr.String())
	}
}

func (c *cityScopeDriftCase) requireDrained(t *testing.T, why string) {
	t.Helper()
	ds := c.env.dt.get(c.session.ID)
	if ds == nil {
		t.Fatalf("%s: session MUST drain on config-drift; stdout=%s stderr=%s",
			why, c.env.stdout.String(), c.env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}

// createAssignedWork creates a work bead assigned to `assignee` with the given
// status. MemStore.Create ignores both the ID and the Status it is handed (it
// always mints "gc-N" and stores "open"), so the status is applied with Update;
// without that an "in_progress" or "closed" bead here would silently be "open".
func createAssignedWork(t *testing.T, store beads.Store, label, assignee, status string) {
	t.Helper()
	created, err := store.Create(beads.Bead{
		Title:    "work " + label,
		Type:     "task",
		Assignee: assignee,
	})
	if err != nil {
		t.Fatalf("Create %s: %v", label, err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: &status}); err != nil {
		t.Fatalf("Update %s to %s: %v", label, status, err)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get %s: %v", label, err)
	}
	if got.Status != status || got.Assignee != assignee {
		t.Fatalf("test setup: %s has status=%q assignee=%q, want %q / %q", label, got.Status, got.Assignee, status, assignee)
	}
}

// The ga-gn35963 incident: work_dir outside every rig, the claimed bead in the
// whatsapp_automation store. FAILS on HEAD (the guard only reads the city store).
func TestConfigDriftDoesNotDrainFreshWorkerWithRigStoreWork_WorkDirOutsideRig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		assignee func(beads.Bead) string
	}{
		{"assignee is the session name", func(s beads.Bead) string { return s.Metadata["session_name"] }},
		{"assignee is the session id", func(s beads.Bead) string { return s.ID }},
	} {
		for _, status := range []string{"in_progress", "open"} {
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				c := newCityScopeDriftCase(t, remoteWorkerAgent())
				waStore := beads.NewMemStore()
				createAssignedWork(t, waStore, "wa-p7ag21.14.2.3", tc.assignee(c.session), status)
				c.reconcile(map[string]beads.Store{
					"whatsapp_automation": waStore,
					"property_scrapers":   beads.NewMemStore(),
				})
				c.requireNotDrained(t, "wa-worker-remote shape with "+status+" work in the WA rig store")
				if !strings.Contains(c.env.stdout.String(), "live assigned work found") {
					t.Errorf("want the 'live assigned work found' deferral line; stdout=%s", c.env.stdout.String())
				}
			})
		}
	}
}

// A city-scoped agent is cross-store eligible (vp-kvp): its live work may sit in
// any rig store. FAILS on HEAD for the same reason.
func TestConfigDriftDoesNotDrainCityScopedFreshWorkerWithRigStoreWork(t *testing.T) {
	ag := remoteWorkerAgent()
	ag.Scope = "city"
	c := newCityScopeDriftCase(t, ag)
	psStore := beads.NewMemStore()
	createAssignedWork(t, psStore, "ps-live-1", c.session.Metadata["session_name"], "in_progress")
	c.reconcile(map[string]beads.Store{
		"whatsapp_automation": beads.NewMemStore(),
		"property_scrapers":   psStore,
	})
	c.requireNotDrained(t, "city-scoped agent with in_progress work in a rig store")
}

// Work in the city store itself was already honored; keep it honored.
func TestConfigDriftDoesNotDrainFreshWorkerWithCityStoreWork(t *testing.T) {
	c := newCityScopeDriftCase(t, remoteWorkerAgent())
	createAssignedWork(t, c.env.store, "hq-live-1", c.session.Metadata["session_name"], "in_progress")
	c.reconcile(map[string]beads.Store{"whatsapp_automation": beads.NewMemStore()})
	c.requireNotDrained(t, "work in the city store")
}

// Control: with no live work anywhere the drift drain still happens, so the
// guard is not a blanket suppression of config-drift for these agents.
func TestConfigDriftStillDrainsFreshWorkerWithNoAssignedWork(t *testing.T) {
	c := newCityScopeDriftCase(t, remoteWorkerAgent())
	c.reconcile(map[string]beads.Store{
		"whatsapp_automation": beads.NewMemStore(),
		"property_scrapers":   beads.NewMemStore(),
	})
	c.requireDrained(t, "no assigned work in any store")
}

// Only THIS session's work counts, and only open/in_progress work.
func TestConfigDriftStillDrainsFreshWorkerWhenRigWorkIsNotItsLiveWork(t *testing.T) {
	t.Run("assigned to another session", func(t *testing.T) {
		c := newCityScopeDriftCase(t, remoteWorkerAgent())
		waStore := beads.NewMemStore()
		createAssignedWork(t, waStore, "wa-other-1", "wa-worker-remote-2", "in_progress")
		c.reconcile(map[string]beads.Store{"whatsapp_automation": waStore})
		c.requireDrained(t, "rig work belongs to another session")
	})
	t.Run("closed work", func(t *testing.T) {
		c := newCityScopeDriftCase(t, remoteWorkerAgent())
		waStore := beads.NewMemStore()
		createAssignedWork(t, waStore, "wa-done-1", c.session.Metadata["session_name"], "closed")
		c.reconcile(map[string]beads.Store{"whatsapp_automation": waStore})
		c.requireDrained(t, "the only assigned bead is closed")
	})
}

// A rig store that cannot be read is "can't know", never "no work": skip the
// drain this tick and say why. FAILS on HEAD (HEAD never opens the rig store, so
// it drains).
func TestConfigDriftSkipsDrainWhenRigStoreUnreadable(t *testing.T) {
	c := newCityScopeDriftCase(t, remoteWorkerAgent())
	c.reconcile(map[string]beads.Store{
		"whatsapp_automation": listFailStore{Store: beads.NewMemStore()},
	})
	c.requireNotDrained(t, "an unreadable rig store must fail closed")
	if !strings.Contains(c.env.stderr.String(), "checking assigned work before config-drift drain") {
		t.Errorf("want the assigned-work check error on stderr; stderr=%s", c.env.stderr.String())
	}
}

// Scope stays narrow: an agent whose rig IS resolved (work_dir inside the rig)
// keeps the single-rig answer, so work in a DIFFERENT rig store does not defer.
func TestConfigDriftRigResolvedAgentStillSearchesOnlyItsOwnRig(t *testing.T) {
	c := newCityScopeDriftCase(t, config.Agent{})
	// work_dir inside the whatsapp_automation rig -> RoutingRigName resolves it.
	c.env.cfg.Agents[0].WorkDir = filepath.Join(c.cityPath, "whatsapp_automation", "crew", "worker")
	if err := os.MkdirAll(c.env.cfg.Agents[0].WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	psStore := beads.NewMemStore()
	createAssignedWork(t, psStore, "ps-live-1", c.session.Metadata["session_name"], "in_progress")
	c.reconcile(map[string]beads.Store{
		"whatsapp_automation": beads.NewMemStore(),
		"property_scrapers":   psStore,
	})
	c.requireDrained(t, "rig-resolved agent must not be protected by another rig's store")
}
