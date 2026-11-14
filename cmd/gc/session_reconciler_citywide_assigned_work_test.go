package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// ga-3l2z0dz: the "does this session still hold live assigned work?" guard
// (sessionHasOpenAssignedWorkForReachableStore and its awake / first-open /
// stranded-collector siblings) searched ONLY the city store whenever the
// session's agent resolved to the city scope (storeRef == ""). That is also
// what an agent whose work_dir is OUTSIDE every rig resolves to
// (wa-worker-remote, dog-remote, kimi-worker-remote, dog), yet such an agent
// claims from a rig store: its in_progress bead was invisible, so the
// drain-ack close gate, the max-age restart, the drained pool-slot close and
// closeSessionBeadIfReachableStoreUnassigned all concluded "no work" and
// closed or restarted a session holding a live bead. (The config-drift drain
// is the same guard; its call site is ga-gn35963.)
//
// Every helper here is prefixed cwaw so it cannot collide with another
// patch's test file in package main.

const cwawTemplate = "wa-worker-remote"

type cwawCase struct {
	env      *reconcilerTestEnv
	cityPath string
	waStore  beads.Store
	psStore  beads.Store
	session  beads.Bead
}

// newCwawCase builds a city with two rigs and one session of agent `ag`
// (template cwawTemplate) named `sessionName`.
func newCwawCase(t *testing.T, ag config.Agent, sessionName string) *cwawCase {
	t.Helper()
	cityPath := t.TempDir()
	waRig := filepath.Join(cityPath, "whatsapp_automation")
	psRig := filepath.Join(cityPath, "property_scrapers")
	for _, dir := range []string{waRig, psRig} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ag.Name = cwawTemplate
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Rigs: []config.Rig{
			{Name: "whatsapp_automation", Path: waRig},
			{Name: "property_scrapers", Path: psRig},
		},
		Agents: []config.Agent{ag},
	}
	session := env.createSessionBead(sessionName, cwawTemplate)
	return &cwawCase{
		env:      env,
		cityPath: cityPath,
		waStore:  beads.NewMemStore(),
		psStore:  beads.NewMemStore(),
		session:  session,
	}
}

// cwawOutsideRigAgent is the real wa-worker-remote shape: no dir, no scope,
// work_dir outside every rig (relative to the city, like agent.toml).
func cwawOutsideRigAgent() config.Agent {
	return config.Agent{WorkDir: ".gc/agents/wa-worker-remote"}
}

func (c *cwawCase) rigStores() map[string]beads.Store {
	return map[string]beads.Store{"whatsapp_automation": c.waStore, "property_scrapers": c.psStore}
}

// requireCityResolved pins the precondition the whole file depends on: the
// agent resolves to the city scope ("") even though its work lives in a rig.
func (c *cwawCase) requireCityResolved(t *testing.T) {
	t.Helper()
	ref, ok := assignedWorkStoreRefForSession(c.cityPath, c.env.cfg, c.session)
	if !ok || ref != "" {
		t.Fatalf("test setup: assignedWorkStoreRefForSession = (%q, %v), want (\"\", true)", ref, ok)
	}
}

// cwawCreateWork creates a work bead assigned to `assignee` with `status`.
// MemStore.Create ignores both the ID and the Status it is handed (it always
// mints "gc-N" and stores "open"), so the status is applied with Update.
func cwawCreateWork(t *testing.T, store beads.Store, label, assignee, status string) beads.Bead {
	t.Helper()
	created, err := store.Create(beads.Bead{Title: "work " + label, Type: "task", Assignee: assignee})
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
	return got
}

// cwawVariants are the four guard entry points that shared the blind spot.
// Each reports (found, err) for the session against the given stores.
var cwawVariants = []struct {
	name string
	run  func(c *cwawCase, rigStores map[string]beads.Store) (bool, error)
}{
	{"open", func(c *cwawCase, rs map[string]beads.Store) (bool, error) {
		return sessionHasOpenAssignedWorkForReachableStore(c.cityPath, c.env.cfg, c.env.store, rs, c.session)
	}},
	{"awake", func(c *cwawCase, rs map[string]beads.Store) (bool, error) {
		return sessionHasAwakeAssignedWorkForReachableStore(c.cityPath, c.env.cfg, c.env.store, rs, c.session)
	}},
	{"first-open", func(c *cwawCase, rs map[string]beads.Store) (bool, error) {
		_, found, err := firstOpenAssignedWorkBeadForReachableStore(c.cityPath, c.env.cfg, c.env.store, rs, c.session)
		return found, err
	}},
	{"stranded-collector", func(c *cwawCase, rs map[string]beads.Store) (bool, error) {
		out, err := collectSessionAssignedWork(c.cityPath, c.env.cfg, c.env.store, rs, c.session)
		return len(out) > 0, err
	}},
}

// The incident: work_dir outside every rig, the claimed bead in a rig store.
// FAILS on HEAD (every variant reads only the city store).
func TestCityResolvedSessionGuardSeesRigStoreWork(t *testing.T) {
	for _, v := range cwawVariants {
		for _, by := range []string{"session name", "session id"} {
			for _, status := range []string{"in_progress", "open"} {
				if v.name == "awake" && status == "open" {
					continue // awake counts open work only when ready; not the point here
				}
				t.Run(v.name+"/"+by+"/"+status, func(t *testing.T) {
					c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
					c.requireCityResolved(t)
					assignee := c.session.Metadata["session_name"]
					if by == "session id" {
						assignee = c.session.ID
					}
					cwawCreateWork(t, c.waStore, "wa-p7ag21.14.2.3", assignee, status)
					found, err := v.run(c, c.rigStores())
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if !found {
						t.Fatalf("a %s bead assigned to the session in a rig store was not seen: the guard would let the session be closed/restarted with live work", status)
					}
				})
			}
		}
	}
}

// Work in the city store still counts (a city-store agent is the common case).
func TestCityResolvedSessionGuardStillSeesCityStoreWork(t *testing.T) {
	for _, v := range cwawVariants {
		t.Run(v.name, func(t *testing.T) {
			c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
			cwawCreateWork(t, c.env.store, "city-work", c.session.Metadata["session_name"], "in_progress")
			found, err := v.run(c, c.rigStores())
			if err != nil || !found {
				t.Fatalf("found=%v err=%v, want true/nil", found, err)
			}
		})
	}
}

// No work anywhere: the guard must still release the session (E12 "vazio").
func TestCityResolvedSessionGuardReleasesWhenNoWorkAnywhere(t *testing.T) {
	for _, v := range cwawVariants {
		t.Run(v.name, func(t *testing.T) {
			c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
			// Someone else's work in a rig store, and a closed bead of ours.
			cwawCreateWork(t, c.waStore, "someone-else", "other-session", "in_progress")
			cwawCreateWork(t, c.waStore, "ours-done", c.session.Metadata["session_name"], "closed")
			found, err := v.run(c, c.rigStores())
			if err != nil || found {
				t.Fatalf("found=%v err=%v, want false/nil", found, err)
			}
		})
	}
}

// E12 "falhou/ilegível": an unreadable rig store must NOT read as "no work".
// FAILS on HEAD (the rig store is never consulted, so no error surfaces).
func TestCityResolvedSessionGuardFailsClosedWhenRigStoreUnreadable(t *testing.T) {
	for _, v := range cwawVariants {
		t.Run(v.name, func(t *testing.T) {
			c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
			c.waStore = &listErrStore{Store: c.waStore, err: errors.New("dolt unavailable")}
			found, err := v.run(c, c.rigStores())
			if err == nil {
				t.Fatalf("found=%v err=nil: an unreadable rig store was reported as a definite answer", found)
			}
		})
	}
}

// Scope control: a RIG-resolved agent (dir set) still searches only its own
// rig — the widening must not leak other rigs' beads into it.
func TestRigResolvedSessionGuardStillScopedToItsOwnRig(t *testing.T) {
	for _, v := range cwawVariants {
		t.Run(v.name, func(t *testing.T) {
			c := newCwawCase(t, config.Agent{Dir: "whatsapp_automation"}, cwawTemplate+"-1")
			c.session = c.env.createSessionBead(cwawTemplate+"-2", "whatsapp_automation/"+cwawTemplate)
			name := c.session.Metadata["session_name"]
			cwawCreateWork(t, c.psStore, "other-rig-work", name, "in_progress")
			cwawCreateWork(t, c.env.store, "city-work", name, "in_progress")
			if found, err := v.run(c, c.rigStores()); err != nil || found {
				t.Fatalf("other rig / city work leaked into a rig-scoped session: found=%v err=%v", found, err)
			}
			cwawCreateWork(t, c.waStore, "own-rig-work", name, "in_progress")
			if found, err := v.run(c, c.rigStores()); err != nil || !found {
				t.Fatalf("own-rig work not seen: found=%v err=%v", found, err)
			}
			// An unreadable OTHER rig must not matter to a rig-scoped session.
			c.psStore = &listErrStore{Store: c.psStore, err: errors.New("other rig down")}
			if found, err := v.run(c, c.rigStores()); err != nil || !found {
				t.Fatalf("unreadable unrelated rig broke a rig-scoped session: found=%v err=%v", found, err)
			}
		})
	}
}

// Scope control: a GENUINELY city-scoped agent (no dir, no work_dir: it runs at
// the city root and its bd is the city store) keeps the city-store-only answer.
// Rig-store work must not retain it, and an unreadable rig store must not block
// it. Pinned by the existing ...IgnoresUnreachableRigAssignedWork tests; this
// is the guard-level statement of the same invariant. PASSES on HEAD and with
// the fix (only the work_dir-outside-every-rig class is widened).
func TestGenuineCityScopedSessionGuardStaysCityOnly(t *testing.T) {
	for _, v := range cwawVariants {
		t.Run(v.name, func(t *testing.T) {
			c := newCwawCase(t, config.Agent{}, cwawTemplate+"-1")
			c.requireCityResolved(t)
			name := c.session.Metadata["session_name"]
			cwawCreateWork(t, c.waStore, "rig-work", name, "in_progress")
			if found, err := v.run(c, c.rigStores()); err != nil || found {
				t.Fatalf("rig-store work retained a city-scoped session: found=%v err=%v", found, err)
			}
			c.waStore = &listErrStore{Store: c.waStore, err: errors.New("rig down")}
			if found, err := v.run(c, c.rigStores()); err != nil || found {
				t.Fatalf("unreadable rig store blocked a city-scoped session: found=%v err=%v", found, err)
			}
			cwawCreateWork(t, c.env.store, "city-work", name, "in_progress")
			if found, err := v.run(c, c.rigStores()); err != nil || !found {
				t.Fatalf("city-store work not seen: found=%v err=%v", found, err)
			}
		})
	}
}

// --- the four call paths named in ga-3l2z0dz ---------------------------------

// Path 1: drain-ack close gate (finalizeDrainAckStoppedSession). FAILS on HEAD:
// the session is closed "drained" with its bead still in_progress in the rig.
func TestFinalizeDrainAckStoppedSession_CityResolvedSessionWithRigWorkIsNotClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withWork   bool
		wantClosed bool
		wantEvents int
	}{
		{"live rig bead keeps the session", true, false, 1},
		{"no work lets the drained session close", false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
			c.requireCityResolved(t)
			fake := events.NewFake()
			patch := sessionpkg.DrainAckStopPendingPatch(c.env.clk.Now().UTC())
			if err := c.env.store.SetMetadataBatch(c.session.ID, patch); err != nil {
				t.Fatalf("SetMetadataBatch(stop-pending): %v", err)
			}
			c.session.Metadata = patch.Apply(c.session.Metadata)
			var work beads.Bead
			if tc.withWork {
				work = cwawCreateWork(t, c.waStore, "wa-p7ag21.14.2.3", c.session.Metadata["session_name"], "in_progress")
			}

			finalizeDrainAckStoppedSession(
				c.cityPath, c.env.cfg, c.env.store, c.rigStores(), &c.session, cwawTemplate, true,
				newFakeDrainOps(), c.env.dt, c.env.clk, fake, &c.env.stderr,
			)

			got, err := c.env.store.Get(c.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if closed := got.Status == "closed"; closed != tc.wantClosed {
				t.Fatalf("session closed = %v, want %v (stderr=%s)", closed, tc.wantClosed, c.env.stderr.String())
			}
			matches := 0
			for _, ev := range fake.Events {
				if ev.Type != events.SessionDrainAckedWithAssignedWork {
					continue
				}
				matches++
				if !strings.Contains(string(ev.Payload), work.ID) {
					t.Errorf("drain-acked-with-assigned-work payload %s does not name the stranded rig bead %s", ev.Payload, work.ID)
				}
			}
			if matches != tc.wantEvents {
				t.Fatalf("%s events = %d, want %d", events.SessionDrainAckedWithAssignedWork, matches, tc.wantEvents)
			}
		})
	}
}

// Path 2: closeSessionBeadIfReachableStoreUnassigned. FAILS on HEAD: it closes
// the session bead although a live rig bead is assigned to it.
func TestCloseSessionBeadIfReachableStoreUnassigned_CityResolvedSessionWithRigWork(t *testing.T) {
	t.Run("live rig bead blocks the close", func(t *testing.T) {
		c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
		cwawCreateWork(t, c.waStore, "wa-p7ag21.14.2.3", c.session.Metadata["session_name"], "in_progress")
		closed := closeSessionBeadIfReachableStoreUnassigned(c.cityPath, c.env.cfg, c.env.store, c.rigStores(), c.session, "drained", c.env.clk.Now().UTC(), &c.env.stderr)
		got, _ := c.env.store.Get(c.session.ID)
		if closed || got.Status == "closed" {
			t.Fatalf("closed=%v status=%q: a session with a live rig bead was closed", closed, got.Status)
		}
	})
	t.Run("unreadable rig store blocks the close", func(t *testing.T) {
		c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
		c.waStore = &listErrStore{Store: c.waStore, err: errors.New("dolt unavailable")}
		closed := closeSessionBeadIfReachableStoreUnassigned(c.cityPath, c.env.cfg, c.env.store, c.rigStores(), c.session, "drained", c.env.clk.Now().UTC(), &c.env.stderr)
		got, _ := c.env.store.Get(c.session.ID)
		if closed || got.Status == "closed" {
			t.Fatalf("closed=%v status=%q: closed while a rig store was unreadable", closed, got.Status)
		}
	})
	t.Run("no work: the close still happens", func(t *testing.T) {
		c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate+"-1")
		if !closeSessionBeadIfReachableStoreUnassigned(c.cityPath, c.env.cfg, c.env.store, c.rigStores(), c.session, "drained", c.env.clk.Now().UTC(), &c.env.stderr) {
			t.Fatalf("an unassigned session must close; stderr=%s", c.env.stderr.String())
		}
	})
}

// cwawMaxAgeReconcile runs one reconcile tick with the city path and rig
// stores wired (maxAgeReconcile passes neither, so it cannot reach this bug).
func (c *cwawCase) cwawMaxAgeReconcile(tr maxSessionAgeTracker) {
	poolDesired := map[string]int{cwawTemplate: 1}
	cfgNames := configuredSessionNames(c.env.cfg, c.cityPath, c.env.store)
	reconcileSessionBeadsTraced(
		context.Background(), c.cityPath, []beads.Bead{c.session}, c.env.desiredState, cfgNames, c.env.cfg, c.env.sp,
		c.env.store, nil, nil, c.rigStores(), nil, c.env.dt, poolDesired, false, nil, "",
		nil, c.env.clk, c.env.rec, 0, 0, &c.env.stdout, &c.env.stderr, nil,
		withMaxSessionAgeTracker(tr),
	)
}

// newCwawAgedRunningCase wires a running, 6h-old session against a 5h max age.
func newCwawAgedRunningCase(t *testing.T) (*cwawCase, maxSessionAgeTracker) {
	t.Helper()
	ag := cwawOutsideRigAgent()
	ag.MaxSessionAge = "5h"
	c := newCwawCase(t, ag, cwawTemplate+"-1")
	name := c.session.Metadata["session_name"]
	c.env.addDesired(name, cwawTemplate, true)
	c.env.markSessionActive(&c.session)
	c.env.setSessionMetadata(&c.session, map[string]string{
		"creation_complete_at": c.env.clk.Now().Add(-6 * time.Hour).UTC().Format(time.RFC3339),
	})
	tr := newMaxSessionAgeTracker()
	tr.setConfig(name, 5*time.Hour, 0)
	return c, tr
}

// Path 3: max-age restart. FAILS on HEAD: the aged session is killed mid-task.
func TestMaxSessionAge_CityResolvedSessionWithRigWorkIsNotRestarted(t *testing.T) {
	t.Run("live rig bead defers the restart", func(t *testing.T) {
		c, tr := newCwawAgedRunningCase(t)
		c.requireCityResolved(t)
		cwawCreateWork(t, c.waStore, "wa-p7ag21.14.2.3", c.session.Metadata["session_name"], "in_progress")
		c.cwawMaxAgeReconcile(tr)
		if !c.env.sp.IsRunning(c.session.Metadata["session_name"]) {
			t.Fatalf("aged session with a live rig bead was killed; stderr=%s", c.env.stderr.String())
		}
	})
	t.Run("unreadable rig store defers the restart (fail closed)", func(t *testing.T) {
		c, tr := newCwawAgedRunningCase(t)
		c.waStore = &listErrStore{Store: c.waStore, err: errors.New("dolt unavailable")}
		c.cwawMaxAgeReconcile(tr)
		if !c.env.sp.IsRunning(c.session.Metadata["session_name"]) {
			t.Fatalf("aged session was killed while a rig store was unreadable; stderr=%s", c.env.stderr.String())
		}
	})
	t.Run("no work: the aged session is still restarted", func(t *testing.T) {
		c, tr := newCwawAgedRunningCase(t)
		c.cwawMaxAgeReconcile(tr)
		if c.env.sp.IsRunning(c.session.Metadata["session_name"]) {
			t.Fatalf("aged session with no work must be restarted; stderr=%s", c.env.stderr.String())
		}
		got, _ := c.env.store.Get(c.session.ID)
		if got.Metadata["sleep_reason"] != "max-session-age" {
			t.Fatalf("sleep_reason = %q, want max-session-age", got.Metadata["sleep_reason"])
		}
	})
}

// newCwawDrainedPoolSlotCase builds a pool-managed, asleep session whose
// runtime is gone. Session and template share one name, as in the existing
// stranded-diagnostic test this mirrors.
func newCwawDrainedPoolSlotCase(t *testing.T) *cwawCase {
	t.Helper()
	c := newCwawCase(t, cwawOutsideRigAgent(), cwawTemplate)
	c.env.addDesired(cwawTemplate, cwawTemplate, false) // runtime is dead
	c.env.setSessionMetadata(&c.session, map[string]string{
		"state":                "asleep",
		"sleep_reason":         "idle",
		poolManagedMetadataKey: boolMetadata(true),
	})
	return c
}

func (c *cwawCase) cwawDrainedPoolSlotTick() {
	reconcileSessionBeadsAtPath(
		context.Background(), c.cityPath, []beads.Bead{c.session}, c.env.desiredState,
		map[string]bool{cwawTemplate: true}, c.env.cfg, c.env.sp, c.env.store, newFakeDrainOps(),
		nil, c.rigStores(), nil, c.env.dt, nil, false, nil, "", nil,
		c.env.clk, c.env.rec, 0, 0, &c.env.stdout, &c.env.stderr,
	)
}

// Path 4: drained pool-slot close. FAILS on HEAD: the slot is closed (and no
// stranded diagnostic fires) although its bead is still in_progress in the rig.
func TestDrainedPoolSlot_CityResolvedSessionWithRigWorkIsNotClosed(t *testing.T) {
	t.Run("live rig bead keeps the slot and is named in the stranded diagnostic", func(t *testing.T) {
		c := newCwawDrainedPoolSlotCase(t)
		c.requireCityResolved(t)
		rec := &capturingRecorder{}
		c.env.rec = rec
		work := cwawCreateWork(t, c.waStore, "wa-p7ag21.14.2.3", c.session.Metadata["session_name"], "in_progress")
		c.cwawDrainedPoolSlotTick()
		got, _ := c.env.store.Get(c.session.ID)
		if got.Status == "closed" {
			t.Fatalf("drained pool slot with a live rig bead was closed; stderr=%s", c.env.stderr.String())
		}
		stranded := rec.strandedEvents()
		if len(stranded) != 1 || !strings.Contains(stranded[0].Message, work.ID) {
			t.Fatalf("want one session.stranded event naming %s, got %+v", work.ID, stranded)
		}
	})
	t.Run("unreadable rig store keeps the slot (fail closed)", func(t *testing.T) {
		c := newCwawDrainedPoolSlotCase(t)
		c.waStore = &listErrStore{Store: c.waStore, err: errors.New("dolt unavailable")}
		c.cwawDrainedPoolSlotTick()
		got, _ := c.env.store.Get(c.session.ID)
		if got.Status == "closed" {
			t.Fatalf("drained pool slot was closed while a rig store was unreadable; stderr=%s", c.env.stderr.String())
		}
	})
	t.Run("no work: the drained slot is still freed", func(t *testing.T) {
		c := newCwawDrainedPoolSlotCase(t)
		c.cwawDrainedPoolSlotTick()
		got, _ := c.env.store.Get(c.session.ID)
		if got.Status != "closed" {
			t.Fatalf("drained pool slot with no work must close; status=%q stderr=%s", got.Status, c.env.stderr.String())
		}
	})
}
