package session

import (
	"context"
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// getTransientOnceStore wraps a MemStore and fails the first Get for a given id
// with a transient Dolt i/o-timeout, then delegates to the embedded store. It
// models the ga-f8r9e storm: the start-path bead read inside StartRuntimeOnly →
// sessionBead → loadSessionBead aborts on "i/o timeout" before op=start logs.
type getTransientOnceStore struct {
	*beads.MemStore
	failID   string
	failErr  error
	armed    bool
	getCalls int
}

func (s *getTransientOnceStore) Get(id string) (beads.Bead, error) {
	if id == s.failID {
		s.getCalls++
		if s.armed {
			s.armed = false
			return beads.Bead{}, s.failErr
		}
	}
	return s.MemStore.Get(id)
}

// TestLoadSessionBead_RetriesOnTransientDoltError proves the ga-f8r9e fix: a
// single i/o-timeout on the session-bead read is retried EXACTLY ONCE on a
// fresh connection and the load succeeds, so op=start is no longer starved by
// one transient storm error.
func TestLoadSessionBead_RetriesOnTransientDoltError(t *testing.T) {
	base := beads.NewMemStore()
	sp := runtime.NewFake()

	// Seed a real session bead via the manager so it passes the session-bead
	// guard inside loadSessionBead.
	seedMgr := NewManager(base, sp)
	info, err := seedMgr.Create(context.Background(), "helper", "f8r9e load retry", "claude", "/tmp", "claude", nil, ProviderResume{}, runtime.Config{})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	ioTimeout := errors.New("read tcp 127.0.0.1:1->127.0.0.1:52756: i/o timeout")
	store := &getTransientOnceStore{MemStore: base, failID: info.ID, failErr: ioTimeout, armed: true}
	mgr := NewManager(store, sp)

	b, sessName, err := mgr.loadSessionBead(info.ID, false)
	if err != nil {
		t.Fatalf("expected transient retry to succeed, got: %v", err)
	}
	if b.ID != info.ID {
		t.Fatalf("loaded wrong bead: got %s want %s", b.ID, info.ID)
	}
	if sessName == "" {
		t.Fatalf("expected non-empty session name")
	}
	if store.getCalls != 2 {
		t.Fatalf("expected exactly 2 Get calls (fail + retry), got %d", store.getCalls)
	}
}

// TestLoadSessionBead_DoesNotRetryNonTransient ensures a non-transient error
// (e.g. a genuine not-found) surfaces immediately without a retry.
func TestLoadSessionBead_DoesNotRetryNonTransient(t *testing.T) {
	base := beads.NewMemStore()
	sp := runtime.NewFake()
	seedMgr := NewManager(base, sp)
	info, err := seedMgr.Create(context.Background(), "helper", "f8r9e load noretry", "claude", "/tmp", "claude", nil, ProviderResume{}, runtime.Config{})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	sentinel := errors.New("bead not found")
	store := &getTransientOnceStore{MemStore: base, failID: info.ID, failErr: sentinel, armed: true}
	mgr := NewManager(store, sp)

	if _, _, err := mgr.loadSessionBead(info.ID, false); err == nil {
		t.Fatalf("expected non-transient error to surface, got nil")
	}
	if store.getCalls != 1 {
		t.Fatalf("expected exactly 1 Get call (no retry on non-transient), got %d", store.getCalls)
	}
}
