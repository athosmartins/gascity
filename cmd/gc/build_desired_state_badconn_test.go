package main

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// badConnOnceStore wraps a real beads.Store and makes the first List / Ready
// call after each arming fail with a stale-pooled-connection error, then
// delegates to the embedded store on every subsequent call. It models the
// gc-aov9u failure: the supervisor's long-lived Dolt pool hands out a socket
// the server already reaped (30s idle timeout), so the first query returns
// "invalid connection"; the retry pulls a fresh connection and succeeds.
type badConnOnceStore struct {
	beads.Store
	listErr  error
	listFail bool
	listN    int
	readyErr  error
	readyFail bool
	readyN    int
}

func (s *badConnOnceStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.listN++
	if s.listFail {
		s.listFail = false
		return nil, s.listErr
	}
	return s.Store.List(query)
}

func (s *badConnOnceStore) Ready(queries ...beads.ReadyQuery) ([]beads.Bead, error) {
	s.readyN++
	if s.readyFail {
		s.readyFail = false
		return nil, s.readyErr
	}
	return s.Store.Ready(queries...)
}

// seedOpenBead inserts one open, assigned, ready bead so the underlying List /
// Ready return a non-empty result the test can assert against.
func seedOpenBead(t *testing.T, store beads.Store) string {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:    "badconn-retry-fixture",
		Status:   "open",
		Assignee: "wa/claude",
	})
	if err != nil {
		t.Fatalf("seed bead: %v", err)
	}
	return b.ID
}

func TestListBothTiersForControllerDemand_RetriesOnBadConn(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"typed driver.ErrBadConn", driver.ErrBadConn},
		{"wrapped driver.ErrBadConn", fmt.Errorf("query failed: %w", driver.ErrBadConn)},
		{"string invalid connection", errors.New("Error 1105: invalid connection")},
		{"string bad connection", errors.New("dial dolt: bad connection")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := beads.NewMemStore()
			id := seedOpenBead(t, base)
			store := &badConnOnceStore{Store: base, listErr: tc.err, listFail: true}

			rows, err := listBothTiersForControllerDemand(store, beads.ListQuery{Status: "open"})
			if err != nil {
				t.Fatalf("expected retry to succeed, got error: %v", err)
			}
			if store.listN != 2 {
				t.Fatalf("expected exactly 2 List calls (fail + retry), got %d", store.listN)
			}
			var found bool
			for _, b := range rows {
				if b.ID == id {
					found = true
				}
			}
			if !found {
				t.Fatalf("retry result missing seeded bead %s; rows=%d", id, len(rows))
			}
		})
	}
}

func TestLiveReadyForControllerDemand_RetriesOnBadConn(t *testing.T) {
	base := beads.NewMemStore()
	id := seedOpenBead(t, base)
	store := &badConnOnceStore{Store: base, readyErr: driver.ErrBadConn, readyFail: true}

	rows, err := liveReadyForControllerDemandQuery(store, beads.ReadyQuery{})
	if err != nil {
		t.Fatalf("expected retry to succeed, got error: %v", err)
	}
	if store.readyN != 2 {
		t.Fatalf("expected exactly 2 Ready calls (fail + retry), got %d", store.readyN)
	}
	var found bool
	for _, b := range rows {
		if b.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("retry result missing seeded bead %s; rows=%d", id, len(rows))
	}
}

// A non-bad-conn error must NOT be retried and must surface to the caller.
func TestListBothTiersForControllerDemand_DoesNotRetryOtherErrors(t *testing.T) {
	base := beads.NewMemStore()
	seedOpenBead(t, base)
	sentinel := errors.New("table beads doesn't exist")
	store := &badConnOnceStore{Store: base, listErr: sentinel, listFail: true}

	_, err := listBothTiersForControllerDemand(store, beads.ListQuery{Status: "open"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error to surface unretried, got: %v", err)
	}
	if store.listN != 1 {
		t.Fatalf("expected exactly 1 List call (no retry on non-bad-conn), got %d", store.listN)
	}
}

// A bad-conn error on BOTH the first attempt and the retry must surface — the
// helper retries exactly once, it does not loop.
func TestRetryOnBadConn_RetriesExactlyOnce(t *testing.T) {
	calls := 0
	_, err := retryOnBadConn(func() (int, error) {
		calls++
		return 0, driver.ErrBadConn
	})
	if !beads.IsBadConnError(err) {
		t.Fatalf("expected bad-conn error to surface after retry, got: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 attempts (1 + single retry), got %d", calls)
	}
}

// ga-f8r9e: a Dolt i/o-timeout (NOT a bad-conn error) must now be retried
// EXACTLY ONCE by retryOnBadConn, so a single transient storm error no longer
// aborts a wrapped supervisor controller-demand / ready / scale_check probe.
func TestRetryOnBadConn_RetriesIOTimeout(t *testing.T) {
	ioTimeout := errors.New("read tcp 127.0.0.1:1->127.0.0.1:52756: i/o timeout")
	// Sanity: this is the gap — bad-conn classifier does NOT match it.
	if beads.IsBadConnError(ioTimeout) {
		t.Fatalf("test premise broken: IsBadConnError should NOT match i/o timeout")
	}
	calls := 0
	got, err := retryOnBadConn(func() (int, error) {
		calls++
		if calls == 1 {
			return 0, ioTimeout
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("expected i/o-timeout retry to succeed, got: %v", err)
	}
	if got != 42 {
		t.Fatalf("expected retry result 42, got %d", got)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 attempts (fail + retry), got %d", calls)
	}
}

// ga-f8r9e: the i/o-timeout retry is still EXACTLY ONCE — a persistent storm
// surfaces after one retry rather than looping.
func TestRetryOnBadConn_IOTimeoutRetriesExactlyOnce(t *testing.T) {
	ioTimeout := errors.New("dial tcp 127.0.0.1:52756: i/o timeout")
	calls := 0
	_, err := retryOnBadConn(func() (int, error) {
		calls++
		return 0, ioTimeout
	})
	if !beads.IsTransientDoltError(err) {
		t.Fatalf("expected transient error to surface after retry, got: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 attempts (1 + single retry), got %d", calls)
	}
}

// ga-f8r9e: the wrapped controller-demand probe survives a one-shot
// i/o-timeout (previously un-retried because IsBadConnError missed it).
func TestListBothTiersForControllerDemand_RetriesOnIOTimeout(t *testing.T) {
	base := beads.NewMemStore()
	id := seedOpenBead(t, base)
	ioTimeout := errors.New("read tcp 127.0.0.1:1->127.0.0.1:52756: i/o timeout")
	store := &badConnOnceStore{Store: base, listErr: ioTimeout, listFail: true}

	rows, err := listBothTiersForControllerDemand(store, beads.ListQuery{Status: "open"})
	if err != nil {
		t.Fatalf("expected i/o-timeout retry to succeed, got error: %v", err)
	}
	if store.listN != 2 {
		t.Fatalf("expected exactly 2 List calls (fail + retry), got %d", store.listN)
	}
	var found bool
	for _, b := range rows {
		if b.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("retry result missing seeded bead %s; rows=%d", id, len(rows))
	}
}
