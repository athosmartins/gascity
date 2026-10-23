package main

import (
	"testing"
	"time"
)

// ga-2hd02v.1.22: a manual session that idle_timeout put to sleep must stay
// asleep. Before, every manual, non-closed, non-drained session was "desired"
// no matter why it was asleep, so the idle kill was undone by the very next
// reconcile and the session came back as a fresh process with nothing to do.

func manualIdleKilled() AwakeSessionBead {
	return AwakeSessionBead{
		ID: "mc-1", SessionName: "s-mc-1", Template: "gascity/claude",
		State: "asleep", SleepReason: "idle-timeout", ManualSession: true,
	}
}

func manualIdleInput(bead AwakeSessionBead) AwakeInput {
	return AwakeInput{
		Agents:       []AwakeAgent{{QualifiedName: "gascity/claude"}},
		SessionBeads: []AwakeSessionBead{bead},
		Now:          now,
	}
}

func TestManual_IdleTimeoutSleep_StaysAsleep(t *testing.T) {
	result := ComputeAwakeSet(manualIdleInput(manualIdleKilled()))
	assertAsleep(t, result, "s-mc-1")
}

func TestManual_IdleTimeoutSleep_StaysAsleepAcrossTicks(t *testing.T) {
	// The kill is not a one-tick event: the bead keeps state=asleep and
	// sleep_reason=idle-timeout until something wakes it, tick after tick.
	in := manualIdleInput(manualIdleKilled())
	for i := 0; i < 5; i++ {
		in.Now = now.Add(time.Duration(i) * 10 * time.Minute)
		assertAsleep(t, ComputeAwakeSet(in), "s-mc-1")
	}
}

func TestManual_IdleTimeoutSleep_ExplicitWakeWakes(t *testing.T) {
	// `gc session wake` records wake_request and leaves sleep_reason alone, so
	// the explicit wake is what has to bring the session back.
	bead := manualIdleKilled()
	bead.ExplicitWake = true
	result := ComputeAwakeSet(manualIdleInput(bead))
	assertAwake(t, result, "s-mc-1")
	assertReason(t, result, "s-mc-1", "explicit-wake")
}

func TestManual_IdleTimeoutSleep_AssignedWorkWakes(t *testing.T) {
	in := manualIdleInput(manualIdleKilled())
	in.WorkBeads = []AwakeWorkBead{{ID: "ga-w1", Assignee: "s-mc-1", Status: "in_progress"}}
	result := ComputeAwakeSet(in)
	assertAwake(t, result, "s-mc-1")
	assertReason(t, result, "s-mc-1", "assigned-work")
}

func TestManual_IdleTimeoutSleep_PendingCreateWakes(t *testing.T) {
	bead := manualIdleKilled()
	bead.PendingCreate = true
	assertAwake(t, ComputeAwakeSet(manualIdleInput(bead)), "s-mc-1")
}

func TestManual_IdleTimeoutSleep_PendingInteractionWakes(t *testing.T) {
	in := manualIdleInput(manualIdleKilled())
	in.PendingSessions = map[string]bool{"s-mc-1": true}
	assertAwake(t, ComputeAwakeSet(in), "s-mc-1")
}

func TestManual_IdleTimeoutSleep_AttachedWakes(t *testing.T) {
	in := manualIdleInput(manualIdleKilled())
	in.AttachedSessions = map[string]bool{"s-mc-1": true}
	assertAwake(t, ComputeAwakeSet(in), "s-mc-1")
}

func TestManual_IdleTimeoutSleep_ReplacementStartIsNotBlocked(t *testing.T) {
	// preWakeCommit keeps sleep_reason=idle-timeout until the replacement has
	// actually started, but it moves state to creating: that start must go on.
	bead := manualIdleKilled()
	bead.State = "creating"
	assertAwake(t, ComputeAwakeSet(manualIdleInput(bead)), "s-mc-1")
}

func TestManual_OtherSleepReasonsStillDesired(t *testing.T) {
	// Only idle-timeout is held back. Every other way a manual session can be
	// asleep keeps the old behavior (always desired), so the change is narrow.
	for _, reason := range []string{"", "idle", "city-stop", "drained-by-hand", "runtime-missing"} {
		bead := manualIdleKilled()
		bead.SleepReason = reason
		result := ComputeAwakeSet(manualIdleInput(bead))
		if !result["s-mc-1"].ShouldWake || result["s-mc-1"].Reason != "manual" {
			t.Errorf("sleep_reason %q: got %+v, want awake with reason manual", reason, result["s-mc-1"])
		}
	}
}

func TestManual_IdleTimeoutSleep_ActiveSessionUnaffected(t *testing.T) {
	// A live manual session keeps running; the guard needs state=asleep.
	bead := manualIdleKilled()
	bead.State = "active"
	in := manualIdleInput(bead)
	in.RunningSessions = map[string]bool{"s-mc-1": true}
	assertAwake(t, ComputeAwakeSet(in), "s-mc-1")
}
