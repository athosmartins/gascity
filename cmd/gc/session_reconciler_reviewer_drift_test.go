package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// addRunningGateReviewerDesiredWithNewConfig registers and starts a
// gate-reviewer session with the drift test command, mirroring
// addRunningWorkerDesiredWithNewConfig but for the reviewer template so the
// reviewer session reaches the pool-routed config-drift branch.
func (e *reconcilerTestEnv) addRunningGateReviewerDesiredWithNewConfig(name string) {
	tp := TemplateParams{
		Command:      "new-cmd",
		SessionName:  name,
		TemplateName: gateReviewerTemplateName,
	}
	e.desiredState[name] = tp
	_ = e.sp.Start(context.Background(), name, runtime.Config{Command: "new-cmd"})
}

// createPendingVerdictBead creates an ephemeral quality-gate verdict bead in
// the still-pending state, exactly as the dispatcher's `bd create ... -t chore
// --ephemeral -l type:quality-gate-verdict -l verdict:pending` does. Crucially
// it leaves the assignee EMPTY — the dispatcher never assigns the verdict bead
// to a reviewer session, which is precisely why the assignee-keyed live-work
// probe cannot protect a mid-review reviewer.
func (e *reconcilerTestEnv) createPendingVerdictBead(gateRun string, reviewerIndex string) beads.Bead {
	b, err := e.store.Create(beads.Bead{
		Title:     "reviewer-verdict: pending",
		Type:      "chore",
		Status:    "open",
		Ephemeral: true,
		Labels: []string{
			"type:quality-gate-verdict",
			"gate-run:" + gateRun,
			"reviewer-index:" + reviewerIndex,
			"verdict:pending",
		},
	})
	if err != nil {
		panic("creating pending verdict bead: " + err.Error())
	}
	return b
}

// driftHashes returns a (started, current) core-fingerprint pair that genuinely
// differs, so the reconciler observes real config drift.
func driftHashes(t *testing.T) (started string, current string) {
	t.Helper()
	started = runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"})
	current = runtime.CoreFingerprint(runtime.Config{Command: "new-cmd"})
	if started == current {
		t.Fatalf("test setup error: started hash %q should differ from current %q", started, current)
	}
	return started, current
}

// TestReconcileSessionBeads_ConfigDriftDeferredOnGateReviewerMidReview is the
// ga-lsgte regression: a gate-reviewer session whose config_hash has drifted
// must NOT be drained while a quality-gate verdict bead is still
// verdict:pending. Draining it mid-review starves the gate run of a verdict
// (3/3 never reached → TIMEOUT → story stuck "in flight"). The verdict bead is
// UNASSIGNED, so the dog-pool assignee-keyed live-work probe does not see it;
// the reviewer-specific verdict probe must.
func TestReconcileSessionBeads_ConfigDriftDeferredOnGateReviewerMidReview(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: gateReviewerTemplateName}}}
	env.addRunningGateReviewerDesiredWithNewConfig("gate-reviewer-adhoc-aaa")
	session := env.createSessionBead("gate-reviewer-adhoc-aaa", gateReviewerTemplateName)
	started, _ := driftHashes(t)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": started,
	})

	// A still-pending verdict bead means a review is in flight. Note: it is
	// deliberately UNASSIGNED (no Assignee), matching the dispatcher.
	env.createPendingVerdictBead("ga-wisp-run1", "1")

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("ga-lsgte: gate-reviewer mid-review must NOT be drained on config drift, got drain=%+v stderr=%s",
			ds, env.stderr.String())
	}
}

// TestReconcileSessionBeads_ConfigDriftDrainsGateReviewerWithNoPendingVerdict
// is the anti-over-protection control: a config-drifted gate-reviewer with NO
// pending verdict bead (review already delivered, or never started) MUST still
// drain. Otherwise a genuinely-stale reviewer would be pinned forever.
func TestReconcileSessionBeads_ConfigDriftDrainsGateReviewerWithNoPendingVerdict(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: gateReviewerTemplateName}}}
	env.addRunningGateReviewerDesiredWithNewConfig("gate-reviewer-adhoc-bbb")
	session := env.createSessionBead("gate-reviewer-adhoc-bbb", gateReviewerTemplateName)
	started, _ := driftHashes(t)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": started,
	})

	// No pending verdict bead at all → reviewer is not mid-review → drains.
	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("ga-lsgte control: config-drifted reviewer with NO pending verdict SHOULD drain; stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}

// TestReconcileSessionBeads_ConfigDriftDrainsGateReviewerWhenVerdictClosed
// proves the protection releases as soon as the verdict is no longer pending:
// a closed (delivered) verdict bead does not protect the reviewer, so it drains
// on the next tick — bounding the protection window exactly to the review.
func TestReconcileSessionBeads_ConfigDriftDrainsGateReviewerWhenVerdictClosed(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: gateReviewerTemplateName}}}
	env.addRunningGateReviewerDesiredWithNewConfig("gate-reviewer-adhoc-ccc")
	session := env.createSessionBead("gate-reviewer-adhoc-ccc", gateReviewerTemplateName)
	started, _ := driftHashes(t)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": started,
	})

	// Verdict delivered: bead closed, verdict:pending replaced by verdict:PASS.
	vb := env.createPendingVerdictBead("ga-wisp-run2", "1")
	if err := env.store.Update(vb.ID, beads.UpdateOpts{
		RemoveLabels: []string{"verdict:pending"},
		Labels:       []string{"verdict:PASS"},
	}); err != nil {
		t.Fatalf("relabel verdict bead: %v", err)
	}
	if err := env.store.Close(vb.ID); err != nil {
		t.Fatalf("close verdict bead: %v", err)
	}

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds == nil {
		t.Fatalf("ga-lsgte: reviewer with a DELIVERED (closed, non-pending) verdict SHOULD drain; stderr=%s",
			env.stderr.String())
	}
}

// TestReconcileSessionBeads_ConfigDriftStillDrainsNonReviewerDespitePendingVerdict
// confirms the guard is scoped to the gate-reviewer template only: a non-
// reviewer pool session (a plain worker) is NOT protected by the existence of a
// pending verdict bead belonging to some unrelated gate run, so it drains
// normally. This prevents the verdict probe from leaking protection to the
// whole town.
func TestReconcileSessionBeads_ConfigDriftStillDrainsNonReviewerDespitePendingVerdict(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addRunningWorkerDesiredWithNewConfig()
	session := env.createSessionBead("worker", "worker")
	started, _ := driftHashes(t)
	env.setSessionMetadata(&session, map[string]string{
		"started_config_hash": started,
	})

	// A pending verdict bead exists, but it belongs to a gate run, not this
	// worker — the worker is not a reviewer and must still drain.
	env.createPendingVerdictBead("ga-wisp-run3", "2")

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("non-reviewer worker must still drain on config drift despite an unrelated pending verdict; stderr=%s",
			env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want config-drift", ds.reason)
	}
}
