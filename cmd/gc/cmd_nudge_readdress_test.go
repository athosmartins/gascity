package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// ga-5aovajz.1: a fresh wake bumps continuation_epoch on the SAME session bead.
// A "session"/"mail" nudge queued under the older epoch must reach the live
// conversation instead of being dead-lettered as "session fence mismatch".

func TestSplitQueuedNudgesForTarget_ReaddressesOlderEpochOfSameSession(t *testing.T) {
	live := nudgeTarget{sessionID: "gc-1", continuationEpoch: "9"}
	cases := []struct {
		name        string
		target      nudgeTarget
		item        queuedNudge
		wantDeliver bool
	}{
		{name: "session_source_older_epoch", target: live, wantDeliver: true,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-1", ContinuationEpoch: "3"}},
		{name: "mail_source_older_epoch", target: live, wantDeliver: true,
			item: queuedNudge{ID: "n", Source: "mail", SessionID: "gc-1", ContinuationEpoch: "8"}},
		{name: "no_session_id_older_epoch", target: live, wantDeliver: true,
			item: queuedNudge{ID: "n", Source: "session", ContinuationEpoch: "1"}},
		{name: "wait_source_keeps_fence", target: live, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "wait", SessionID: "gc-1", ContinuationEpoch: "3"}},
		{name: "unknown_source_keeps_fence", target: live, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "", SessionID: "gc-1", ContinuationEpoch: "3"}},
		{name: "item_epoch_newer_than_live", target: live, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-1", ContinuationEpoch: "12"}},
		{name: "item_epoch_unreadable", target: live, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-1", ContinuationEpoch: "abc"}},
		{name: "live_epoch_unreadable", target: nudgeTarget{sessionID: "gc-1", continuationEpoch: "?"}, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-1", ContinuationEpoch: "3"}},
		{name: "different_session", target: live, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-2", ContinuationEpoch: "3"}},
		{name: "target_without_session", target: nudgeTarget{continuationEpoch: "9"}, wantDeliver: false,
			item: queuedNudge{ID: "n", Source: "session", SessionID: "gc-1", ContinuationEpoch: "3"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deliverable, rejected := splitQueuedNudgesForTarget(tc.target, []queuedNudge{tc.item})
			if tc.wantDeliver {
				if len(deliverable) != 1 || len(rejected) != 0 {
					t.Fatalf("deliverable/rejected = %d/%d, want 1/0", len(deliverable), len(rejected))
				}
				return
			}
			if len(deliverable) != 0 || len(rejected) != 1 {
				t.Fatalf("deliverable/rejected = %d/%d, want 0/1", len(deliverable), len(rejected))
			}
		})
	}
}

// The hook path (UserPromptSubmit -> gc nudge drain --inject) is how most
// queued nudges are delivered (413 of 783 closed nudges were accepted_for_injection), so the restart
// scenario is proven end to end through cmdNudgeDrainWithFormat.
func TestCmdNudgeDrainDeliversNudgeQueuedBeforeSessionRestart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject bool
		source string
	}{
		{name: "plain_session", inject: false, source: "session"},
		{name: "hook_inject_session", inject: true, source: "session"},
		{name: "hook_inject_mail", inject: true, source: "mail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearGCEnv(t)
			disableManagedDoltRecoveryForTest(t)
			t.Setenv("GC_BEADS", "file")

			cityDir := t.TempDir()
			writeNamedSessionCityTOML(t, cityDir)
			t.Setenv("GC_CITY", cityDir)

			store, err := openCityStoreAt(cityDir)
			if err != nil {
				t.Fatalf("openCityStoreAt: %v", err)
			}
			created, err := store.Create(beads.Bead{
				Title:  "Session: worker",
				Type:   session.BeadType,
				Status: "open",
				Labels: []string{session.LabelSession},
				Metadata: map[string]string{
					"session_name":       "worker-session",
					"agent_name":         "worker",
					"template":           "worker",
					"state":              string(session.StateActive),
					"continuation_epoch": "1",
				},
			})
			if err != nil {
				t.Fatalf("store.Create session: %v", err)
			}

			// Enqueued while the session was at epoch 1 ...
			item := newQueuedNudgeWithOptions("worker", "gate FAILED, fix the review findings", tc.source, time.Now().Add(-time.Minute), queuedNudgeOptions{
				SessionID:         created.ID,
				ContinuationEpoch: "1",
			})
			if err := enqueueQueuedNudgeWithStore(cityDir, store, item); err != nil {
				t.Fatalf("enqueueQueuedNudgeWithStore: %v", err)
			}
			// ... then the session restarted fresh: same bead, epoch 2.
			if err := store.SetMetadata(created.ID, "continuation_epoch", "2"); err != nil {
				t.Fatalf("SetMetadata continuation_epoch: %v", err)
			}

			var stdout, stderr bytes.Buffer
			code := cmdNudgeDrainWithFormat([]string{created.ID}, tc.inject, "", &stdout, &stderr)
			if code != 0 {
				t.Fatalf("cmdNudgeDrainWithFormat = %d, want 0; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "gate FAILED, fix the review findings") {
				t.Fatalf("stdout = %q, want the nudge queued before the restart", stdout.String())
			}

			pending, inFlight, dead, err := listQueuedNudges(cityDir, "worker", time.Now())
			if err != nil {
				t.Fatalf("listQueuedNudges: %v", err)
			}
			if len(pending) != 0 || len(inFlight) != 0 || len(dead) != 0 {
				t.Fatalf("pending/inFlight/dead = %d/%d/%d, want 0/0/0", len(pending), len(inFlight), len(dead))
			}
			nudgeBead, ok, err := findAnyQueuedNudgeBead(store, item.ID)
			if err != nil || !ok {
				t.Fatalf("findAnyQueuedNudgeBead ok=%v err=%v", ok, err)
			}
			if !isTerminalNudgeState(nudgeBead.Metadata["state"]) {
				t.Fatalf("nudge bead state = %q, want a terminal state after delivery", nudgeBead.Metadata["state"])
			}
			if got := nudgeBead.Metadata["last_error"]; strings.Contains(got, "fence mismatch") {
				t.Fatalf("nudge bead last_error = %q, want no fence mismatch", got)
			}
			if got := nudgeBead.Metadata["state"]; got == "failed" {
				t.Fatalf("nudge bead state = %q, want a delivered state", got)
			}
		})
	}
}

// A "wait" nudge belongs to the conversation that registered the wait, so the
// fence keeps rejecting it after a restart (unchanged behavior, pinned).
func TestCmdNudgeDrainStillFencesWaitNudgeAfterSessionRestart(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	t.Setenv("GC_BEADS", "file")

	cityDir := t.TempDir()
	writeNamedSessionCityTOML(t, cityDir)
	t.Setenv("GC_CITY", cityDir)

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title:  "Session: worker",
		Type:   session.BeadType,
		Status: "open",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name":       "worker-session",
			"agent_name":         "worker",
			"template":           "worker",
			"state":              string(session.StateActive),
			"continuation_epoch": "2",
		},
	})
	if err != nil {
		t.Fatalf("store.Create session: %v", err)
	}
	item := newQueuedNudgeWithOptions("worker", "your wait is ready", "wait", time.Now().Add(-time.Minute), queuedNudgeOptions{
		SessionID:         created.ID,
		ContinuationEpoch: "1",
	})
	if err := enqueueQueuedNudgeWithStore(cityDir, store, item); err != nil {
		t.Fatalf("enqueueQueuedNudgeWithStore: %v", err)
	}

	var stdout, stderr bytes.Buffer
	_ = cmdNudgeDrainWithFormat([]string{created.ID}, true, "", &stdout, &stderr)
	if strings.Contains(stdout.String(), "your wait is ready") {
		t.Fatalf("stdout = %q, want the stale wait nudge NOT delivered", stdout.String())
	}
}
