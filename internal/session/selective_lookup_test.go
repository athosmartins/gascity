package session_test

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// ga-1g2if1: a session identity lookup (one session_name, one alias, one
// agent) matches a handful of live beads at most. It must declare that with
// ListQuery.Selective: for a status-filtered lookup NativeDoltStore then lets
// the backing beads search take its narrow id-only scan instead of streaming
// ~52 columns for every row the JSON metadata predicate visits (0.4-0.75 s ->
// 0.05-0.08 s on hq.wisps); it ignores the hint when closed rows are included.
// These tests pin that the flag is set, not what each store does with it. The
// hint never changes which beads come back; the store-level tests in
// internal/beads pin that exactness.

func newSelectiveLookupStore() *listCountingStore {
	return &listCountingStore{Store: beads.NewMemStore()}
}

func metadataLookupsOf(calls []beads.ListQuery) []beads.ListQuery {
	var out []beads.ListQuery
	for _, q := range calls {
		if len(q.Metadata) > 0 {
			out = append(out, q)
		}
	}
	return out
}

func requireSelectiveMetadataLookups(t *testing.T, calls []beads.ListQuery) {
	t.Helper()
	lookups := metadataLookupsOf(calls)
	if len(lookups) == 0 {
		t.Fatal("no metadata lookup reached the store")
	}
	for _, q := range lookups {
		if !q.Selective {
			t.Errorf("metadata lookup %+v was not marked Selective; an identity lookup must say so the store can take its narrow id-only scan", q)
		}
	}
}

func createSessionBeads(t *testing.T, store beads.Store, n int, meta map[string]string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := store.Create(beads.Bead{
			Title:    fmt.Sprintf("dup-%02d", i),
			Type:     session.BeadType,
			Labels:   []string{session.LabelSession},
			Metadata: meta,
		}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
}

func TestResolveSessionIDMetadataLookupsAreSelective(t *testing.T) {
	store := newSelectiveLookupStore()
	b, err := store.Create(beads.Bead{
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"alias": "overseer"},
	})
	if err != nil {
		t.Fatal(err)
	}

	id, err := session.ResolveSessionID(store, "overseer")
	if err != nil {
		t.Fatalf("ResolveSessionID: %v", err)
	}
	if id != b.ID {
		t.Fatalf("ResolveSessionID = %q, want %q", id, b.ID)
	}
	requireSelectiveMetadataLookups(t, store.listCalls)
}

func TestResolveSessionIDAllowClosedFallbackLookupsAreSelective(t *testing.T) {
	store := newSelectiveLookupStore()
	b, err := store.Create(beads.Bead{
		Type:     session.BeadType,
		Labels:   []string{session.LabelSession},
		Metadata: map[string]string{"session_name": "sky"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(b.ID); err != nil {
		t.Fatal(err)
	}

	id, err := session.ResolveSessionIDAllowClosed(store, "sky")
	if err != nil {
		t.Fatalf("ResolveSessionIDAllowClosed: %v", err)
	}
	if id != b.ID {
		t.Fatalf("ResolveSessionIDAllowClosed = %q, want %q", id, b.ID)
	}
	requireSelectiveMetadataLookups(t, store.listCalls)
}

func TestExactMetadataSessionCandidatesLookupsAreSelective(t *testing.T) {
	store := newSelectiveLookupStore()
	createSessionBeads(t, store, 1, map[string]string{"agent_name": "worker", "template": "worker"})

	got, err := session.ExactMetadataSessionCandidates(store, false,
		map[string]string{"agent_name": "worker"},
		map[string]string{"template": "worker"},
	)
	if err != nil {
		t.Fatalf("ExactMetadataSessionCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %d, want 1 (deduped across filters)", len(got))
	}
	requireSelectiveMetadataLookups(t, store.listCalls)
}

// The session helpers themselves never cap a result: however many beads match,
// they all come back. (MemStore ignores the hint, so this pins the helpers, not
// the probe; the probe's exactness is pinned in internal/beads.)
func TestExactMetadataSessionCandidatesDoNotCapTheResult(t *testing.T) {
	const dup = 40 // more than one probe page
	store := newSelectiveLookupStore()
	createSessionBeads(t, store, dup, map[string]string{"session_name": "dup-name"})

	got, err := session.ExactMetadataSessionCandidates(store, false, map[string]string{"session_name": "dup-name"})
	if err != nil {
		t.Fatalf("ExactMetadataSessionCandidates: %v", err)
	}
	if len(got) != dup {
		t.Fatalf("ExactMetadataSessionCandidates = %d beads, want all %d", len(got), dup)
	}
}
