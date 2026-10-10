package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// ga-h9xpwb: mail the recipient has already READ is not assigned work. It keeps
// status=open until somebody archives it, and every "does this session still
// have assigned work?" check used to count it, so a drained pool session could
// not close and was woken again about every 3.5 minutes (99 wake/drain cycles in
// 3 days on one wa-worker). UNREAD mail still counts — mail is meant to wake a
// session — and so does mail whose read state cannot be established: only a
// positive "read" lets the session go.

func createMailBead(t *testing.T, store beads.Store, assignee string, metadata map[string]string, labels ...string) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:    "you have mail",
		Type:     "message",
		Status:   "open",
		Assignee: assignee,
		Metadata: metadata,
		Labels:   labels,
	})
	if err != nil {
		t.Fatalf("create mail bead: %v", err)
	}
	return b
}

func TestHasNonSessionAssignedWork_ReadMailIsNotWork(t *testing.T) {
	t.Parallel()
	task := beads.Bead{ID: "t-1", Type: "task", Status: "open", Assignee: "worker-1"}
	tests := []struct {
		name  string
		items []beads.Bead
		want  bool
	}{
		{"read mail (metadata)", []beads.Bead{{ID: "m-1", Type: "message", Metadata: map[string]string{"mail.read": "true"}}}, false},
		{"read mail (legacy read label)", []beads.Bead{{ID: "m-2", Type: "message", Labels: []string{"read"}}}, false},
		{"unread mail (metadata false)", []beads.Bead{{ID: "m-3", Type: "message", Metadata: map[string]string{"mail.read": "false"}}}, true},
		{"metadata false beats a stale read label", []beads.Bead{{ID: "m-4", Type: "message", Labels: []string{"read"}, Metadata: map[string]string{"mail.read": "false"}}}, true},
		{"mail with no read state at all cannot be proven read", []beads.Bead{{ID: "m-5", Type: "message"}}, true},
		{"mail with an unrecognised read value cannot be proven read", []beads.Bead{{ID: "m-6", Type: "message", Metadata: map[string]string{"mail.read": "maybe"}}}, true},
		{"read mail next to real work", []beads.Bead{{ID: "m-7", Type: "message", Metadata: map[string]string{"mail.read": "true"}}, task}, true},
		{"only mail is exempt: a task carrying mail.read stays work", []beads.Bead{{ID: "t-2", Type: "task", Metadata: map[string]string{"mail.read": "true"}}}, true},
		{"session beads are still never work", []beads.Bead{{ID: "s-1", Type: sessionBeadType, Labels: []string{sessionBeadLabel}}}, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hasNonSessionAssignedWork(tt.items); got != tt.want {
				t.Fatalf("hasNonSessionAssignedWork() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionAssignedWorkProbes_ReadMailIsNotWork(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()
	createMailBead(t, store, "worker-read", map[string]string{"mail.read": "true"})
	createMailBead(t, store, "worker-unread", map[string]string{"mail.read": "false"})
	createMailBead(t, store, "worker-unknown", nil)

	for _, tt := range []struct {
		assignee string
		want     bool
	}{
		{"worker-read", false},
		{"worker-unread", true},
		{"worker-unknown", true},
	} {
		has, err := sessionHasOpenAssignedWorkInStoreByIdentifiers(store, []string{tt.assignee})
		if err != nil {
			t.Fatalf("sessionHasOpenAssignedWorkInStoreByIdentifiers(%s): %v", tt.assignee, err)
		}
		if has != tt.want {
			t.Errorf("sessionHasOpenAssignedWorkInStoreByIdentifiers(%s) = %v, want %v", tt.assignee, has, tt.want)
		}
		first, found, err := firstOpenAssignedWorkBeadInStoreByIdentifiers(store, []string{tt.assignee})
		if err != nil {
			t.Fatalf("firstOpenAssignedWorkBeadInStoreByIdentifiers(%s): %v", tt.assignee, err)
		}
		if found != tt.want {
			t.Errorf("firstOpenAssignedWorkBeadInStoreByIdentifiers(%s) found = %v (bead %q), want %v", tt.assignee, found, first.ID, tt.want)
		}
	}

	// Control: real work next to the read mail is still found.
	if _, err := store.Create(beads.Bead{Title: "do the thing", Type: "task", Status: "in_progress", Assignee: "worker-read"}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	has, err := sessionHasOpenAssignedWorkInStoreByIdentifiers(store, []string{"worker-read"})
	if err != nil || !has {
		t.Fatalf("with a task assigned too: has=%v err=%v, want true", has, err)
	}
	first, found, err := firstOpenAssignedWorkBeadInStoreByIdentifiers(store, []string{"worker-read"})
	if err != nil || !found || first.Type != "task" {
		t.Fatalf("with a task assigned too: first=%+v found=%v err=%v, want the task", first, found, err)
	}
}

func TestFinalizeDrainAckStoppedSession_ReadMailDoesNotHoldTheSession(t *testing.T) {
	for _, tt := range []struct {
		name       string
		metadata   map[string]string
		wantClosed bool
		wantEvents int
	}{
		{"read mail lets the drained session close", map[string]string{"mail.read": "true"}, true, 0},
		{"unread mail still keeps it (mail is meant to wake a session)", map[string]string{"mail.read": "false"}, false, 1},
		{"mail with no read state keeps it (cannot prove it was read)", nil, false, 1},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			env := newReconcilerTestEnv()
			env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
			fake := events.NewFake()
			env.rec = fake

			session := env.createSessionBead("worker", "worker")
			patch := sessionpkg.DrainAckStopPendingPatch(env.clk.Now().UTC())
			if err := env.store.SetMetadataBatch(session.ID, patch); err != nil {
				t.Fatalf("SetMetadataBatch(stop-pending): %v", err)
			}
			session.Metadata = patch.Apply(session.Metadata)
			createMailBead(t, env.store, session.ID, tt.metadata)

			finalizeDrainAckStoppedSession(
				"", env.cfg, env.store, nil, &session, "worker", true,
				newFakeDrainOps(), env.dt, env.clk, env.rec, &env.stderr,
			)

			got, err := env.store.Get(session.ID)
			if err != nil {
				t.Fatalf("Get(%s): %v", session.ID, err)
			}
			if closed := got.Status == "closed"; closed != tt.wantClosed {
				t.Fatalf("session closed = %v, want %v (metadata=%v)", closed, tt.wantClosed, got.Metadata)
			}
			matches := 0
			for _, ev := range fake.Events {
				if ev.Type == events.SessionDrainAckedWithAssignedWork {
					matches++
				}
			}
			if matches != tt.wantEvents {
				t.Fatalf("%s events = %d, want %d", events.SessionDrainAckedWithAssignedWork, matches, tt.wantEvents)
			}
		})
	}
}

func TestCollectSessionAssignedWork_SkipsReadMail(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	session := env.createSessionBead("worker", "worker")
	createMailBead(t, env.store, session.ID, map[string]string{"mail.read": "true"})
	unread := createMailBead(t, env.store, session.ID, map[string]string{"mail.read": "false"})
	task, err := env.store.Create(beads.Bead{Title: "stranded", Type: "task", Status: "in_progress", Assignee: session.ID})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := collectSessionAssignedWork("", env.cfg, env.store, nil, session)
	if err != nil {
		t.Fatalf("collectSessionAssignedWork: %v", err)
	}
	ids := make(map[string]bool, len(got))
	for _, w := range got {
		ids[w.bead.ID] = true
	}
	if len(got) != 2 || !ids[task.ID] || !ids[unread.ID] {
		t.Fatalf("collectSessionAssignedWork = %v, want exactly the task %s and the unread mail %s (the read mail is not work)", ids, task.ID, unread.ID)
	}
}

func TestAppendAssignedUnique_SkipsReadMail(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()
	read := createMailBead(t, store, "worker-1", map[string]string{"mail.read": "true"})
	unread := createMailBead(t, store, "worker-1", map[string]string{"mail.read": "false"})
	task, err := store.Create(beads.Bead{Title: "do the thing", Type: "task", Status: "in_progress", Assignee: "worker-1"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	var dst []beads.Bead
	var stores []beads.Store
	var refs []string
	appendAssignedUnique(&dst, &stores, &refs, []beads.Bead{read, unread, task}, map[string]struct{}{}, store, "")

	got := make(map[string]bool, len(dst))
	for _, b := range dst {
		got[b.ID] = true
	}
	if got[read.ID] || !got[unread.ID] || !got[task.ID] || len(dst) != len(stores) || len(dst) != len(refs) {
		t.Fatalf("snapshot = %v (stores=%d refs=%d), want the unread mail %s and the task %s but not the read mail %s",
			got, len(stores), len(refs), unread.ID, task.ID, read.ID)
	}
}
