package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// ga-okcgb: gc sling <rig>/claude-headless (and other pool/implicit-agent
// targets) can silently fail to spawn. The reconciler correctly DETECTS
// this -- rollbackPendingCreate/rollbackPendingCreateClearingClaim fire
// with a specific, non-empty reason on every real call site (confirmed
// live against the running supervisor's own log: 36 occurrences over 12
// days, e.g. "lease expired and no live runtime", hitting mila-wa,
// batista-ps, and wa-worker-adhoc -- not exclusive to any one template or
// rig). But previously the ONLY record of that already-known reason was a
// single stderr line in the supervisor's own log. Anyone investigating
// later -- including the human who ran `gc sling` and got back "success"
// -- had NOTHING durable to find: the session bead just closes, and the
// work bead it was meant to run reappears open/unassigned with zero trace
// of what happened. "The wisp closed on its own" and "the wisp was
// consumed successfully" were indistinguishable after the fact.
//
// These tests assert the rollback reason is now persisted as bead
// metadata before the bead closes, for both rollback variants.

func newRollbackTestSession(t *testing.T) (*beads.MemStore, beads.Bead) {
	t.Helper()
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{
		Metadata: map[string]string{"pending_create_claim": "true"},
	})
	if err != nil {
		t.Fatalf("seeding session bead: %v", err)
	}
	return store, created
}

func TestRollbackPendingCreate_RecordsDurableReason(t *testing.T) {
	store, session := newRollbackTestSession(t)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	rollbackPendingCreate(&session, store, nil, now, &stderr,
		"pending_create_lease_expired", "lease expired and no live runtime")

	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatalf("re-fetching session bead: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("session bead status = %q, want closed", got.Status)
	}
	if reason := got.Metadata["pending_create_rollback_reason"]; reason != "lease expired and no live runtime" {
		t.Fatalf("pending_create_rollback_reason = %q, want %q -- this is the ga-okcgb bug: "+
			"a rollback the reconciler already understood left NO durable trace on the bead, "+
			"only a stderr line nobody investigating after the fact would see",
			reason, "lease expired and no live runtime")
	}
	if action := got.Metadata["pending_create_rollback_action"]; action != "pending_create_lease_expired" {
		t.Errorf("pending_create_rollback_action = %q, want %q", action, "pending_create_lease_expired")
	}
	if at := got.Metadata["pending_create_rollback_at"]; strings.TrimSpace(at) == "" {
		t.Errorf("pending_create_rollback_at is empty, want an RFC3339 timestamp")
	} else if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("pending_create_rollback_at = %q is not RFC3339: %v", at, err)
	}
}

func TestRollbackPendingCreateClearingClaim_RecordsDurableReason(t *testing.T) {
	store, session := newRollbackTestSession(t)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	rollbackPendingCreateClearingClaim(&session, store, nil, now, &stderr,
		"pending_create_rollback", "live runtime belongs to another session")

	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatalf("re-fetching session bead: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("session bead status = %q, want closed", got.Status)
	}
	if reason := got.Metadata["pending_create_rollback_reason"]; reason != "live runtime belongs to another session" {
		t.Fatalf("pending_create_rollback_reason = %q, want %q", reason, "live runtime belongs to another session")
	}
}

// TestRollbackPendingCreate_EmptyDetailDoesNotWriteBlankReason guards
// against silently writing a blank reason when no detail is available --
// absent metadata ("never checked") must not collapse into empty-string
// metadata ("checked and found nothing"), the same error-vs-empty family
// this whole bead is about, one layer down in the fix itself.
func TestRollbackPendingCreate_EmptyDetailDoesNotWriteBlankReason(t *testing.T) {
	store, session := newRollbackTestSession(t)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	rollbackPendingCreate(&session, store, nil, now, &stderr, "", "")

	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatalf("re-fetching session bead: %v", err)
	}
	if reason, ok := got.Metadata["pending_create_rollback_reason"]; ok {
		t.Errorf("pending_create_rollback_reason should be absent when no detail is supplied, got %q", reason)
	}
}

// ga-owqsg: rollbackPendingCreate/rollbackPendingCreateClearingClaim only
// ever touched the SESSION bead. The Pilot dispatch markers stamped onto the
// WORK bead at dispatch time (pilot:dispatched / story:in-flight) were left
// stuck forever whenever the session meant to serve that work never came
// alive -- only the external inflight-reclaim-guard eventually noticed, via
// polling, ~30-40min later. These tests assert the "wake-known-identity"
// tier orphan (work bead directly assigned to the bare template name, no
// concrete session ever materialized) gets compensated immediately, in the
// same call that rolls back the doomed session.

func TestRollbackPendingCreate_CompensatesOrphanedWakeKnownIdentityDispatch(t *testing.T) {
	store, session := newRollbackTestSession(t)
	session.Metadata["template"] = "whatsapp_automation/wa-worker"
	workStore := beads.NewMemStore()
	orphan, err := workStore.Create(beads.Bead{
		Assignee: "whatsapp_automation/wa-worker",
		Labels:   []string{"pilot:dispatched", "story:in-flight", "ctx:ready"},
	})
	if err != nil {
		t.Fatalf("seeding orphaned work bead: %v", err)
	}
	now := time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	rollbackPendingCreate(&session, store, workStore, now, &stderr,
		"pending_create_lease_expired", "lease expired and no live runtime")

	got, err := workStore.Get(orphan.ID)
	if err != nil {
		t.Fatalf("re-fetching work bead: %v", err)
	}
	for _, stale := range []string{"pilot:dispatched", "story:in-flight"} {
		for _, l := range got.Labels {
			if l == stale {
				t.Errorf("work bead %s still carries stale label %q after rollback compensation", orphan.ID, stale)
			}
		}
	}
	found := false
	for _, l := range got.Labels {
		if l == "ctx:ready" {
			found = true
		}
	}
	if !found {
		t.Errorf("work bead %s lost unrelated label ctx:ready -- compensation must only strip the in-flight markers", orphan.ID)
	}
	if got.Assignee != "whatsapp_automation/wa-worker" {
		t.Errorf("work bead %s assignee = %q, want unchanged %q -- clearing the stale labels (not the assignee) is what lets the next reconcile tick naturally retry the wake-known-identity dispatch",
			orphan.ID, got.Assignee, "whatsapp_automation/wa-worker")
	}
}

func TestRollbackPendingCreate_CompensationNoOpWithoutWorkStore(t *testing.T) {
	store, session := newRollbackTestSession(t)
	session.Metadata["template"] = "whatsapp_automation/wa-worker"
	now := time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	// workStore=nil must behave exactly like every call site before this
	// fix -- a no-op, never a panic. Call sites that cannot cheaply resolve
	// the target template's store pass nil deliberately.
	rollbackPendingCreate(&session, store, nil, now, &stderr,
		"pending_create_lease_expired", "lease expired and no live runtime")

	got, err := store.Get(session.ID)
	if err != nil {
		t.Fatalf("re-fetching session bead: %v", err)
	}
	if got.Status != "closed" {
		t.Fatalf("session bead status = %q, want closed -- passing workStore=nil must not affect the session's own rollback", got.Status)
	}
}

func TestRollbackPendingCreate_DoesNotTouchAssignedBeadNotInFlight(t *testing.T) {
	store, session := newRollbackTestSession(t)
	session.Metadata["template"] = "whatsapp_automation/wa-worker"
	workStore := beads.NewMemStore()
	unrelated, err := workStore.Create(beads.Bead{
		Assignee: "whatsapp_automation/wa-worker",
		Labels:   []string{"ctx:ready"},
	})
	if err != nil {
		t.Fatalf("seeding unrelated work bead: %v", err)
	}
	now := time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC)
	var stderr strings.Builder

	rollbackPendingCreate(&session, store, workStore, now, &stderr,
		"pending_create_lease_expired", "lease expired and no live runtime")

	got, err := workStore.Get(unrelated.ID)
	if err != nil {
		t.Fatalf("re-fetching work bead: %v", err)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "ctx:ready" {
		t.Errorf("work bead %s labels = %v, want unchanged [ctx:ready] -- a bead without pilot:dispatched/story:in-flight is not an orphaned Pilot dispatch and must not be touched",
			unrelated.ID, got.Labels)
	}
}
