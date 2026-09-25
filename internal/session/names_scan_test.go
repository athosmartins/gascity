package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// ga-1g2if1: the session-name availability checks used to send five sequential
// IncludeClosed lookups (session_name, alias, agent_name, template,
// common_name). With no status predicate, each one is a full scan of every
// closed wisp row and takes seconds on hq.wisps. Closed rows can only change
// the answer for the session_name key (explicit names are permanent), so only
// that lookup may ask for them. The other lookups are status-filtered, and they
// are read LIVE: a lookup that includes closed rows always reaches the backing
// store, so dropping the closed rows must not also drop the freshness the
// check used to get from that.

func metadataKeyOfQuery(q beads.ListQuery) string {
	for k := range q.Metadata {
		return k
	}
	return ""
}

// closedKeysOf returns the metadata keys whose lookup asked for closed rows, and
// the set of keys that were looked up at all.
func closedKeysOf(queries []beads.ListQuery) (closed []string, looked map[string]bool) {
	looked = map[string]bool{}
	for _, q := range queries {
		key := metadataKeyOfQuery(q)
		if key == "" {
			continue
		}
		looked[key] = true
		if q.IncludeClosed {
			closed = append(closed, key)
		}
	}
	return closed, looked
}

func TestEnsureSessionNameAvailableReadsClosedRowsOnlyForSessionName(t *testing.T) {
	store := &listCountingStore{MemStore: beads.NewMemStore()}

	if err := ensureSessionNameAvailable(store, "fresh-name"); err != nil {
		t.Fatalf("ensureSessionNameAvailable: %v", err)
	}

	closed, looked := closedKeysOf(store.queries)
	for _, key := range []string{"session_name", "alias", "agent_name", "template", "common_name"} {
		if !looked[key] {
			t.Errorf("no %q lookup reached the store; the availability check must still cover every identity key", key)
		}
	}
	if len(closed) != 1 || closed[0] != "session_name" {
		t.Fatalf("lookups that scanned closed rows = %v, want only [session_name]: a closed alias, agent_name, template or common_name holder never blocks a name, so scanning closed rows for those keys is wasted work", closed)
	}
	for _, q := range store.queries {
		if key := metadataKeyOfQuery(q); key != "" && key != "session_name" && !q.Live {
			t.Errorf("%q lookup %+v was not read live; without closed rows it would be served from a cache that may not have seen a bead another process just created", key, q)
		}
	}
}

func TestNoLiveSessionNameCollisionsNeverReadsClosedRows(t *testing.T) {
	store := &listCountingStore{MemStore: beads.NewMemStore()}

	if !noLiveSessionNameCollisions(store, "fresh-name", "", "") {
		t.Fatal("noLiveSessionNameCollisions = false on an empty store, want true")
	}

	closed, looked := closedKeysOf(store.queries)
	for _, key := range []string{"session_name", "alias", "agent_name", "template", "common_name"} {
		if !looked[key] {
			t.Errorf("no %q lookup reached the store; the live-collision check must still cover every identity key", key)
		}
	}
	if len(closed) != 0 {
		t.Fatalf("lookups that scanned closed rows = %v, want none: this check only asks about LIVE beads and skips every closed row", closed)
	}
	for _, q := range store.queries {
		if key := metadataKeyOfQuery(q); key != "" && !q.Live {
			t.Errorf("%q lookup %+v was not read live; this check gates the legacy bypass and must see a bead another process just created", key, q)
		}
	}
}

// The freshness regression the review found: a bead another process creates
// straight in the backing store is invisible to a cache that has not seen it
// yet. The checks must still see it, as they did while every lookup asked for
// closed rows (which always reaches the backing store).
func TestEnsureSessionNameAvailableSeesLiveBeadsTheCacheHasNotSeen(t *testing.T) {
	for _, key := range []string{"alias", "agent_name", "template", "common_name"} {
		t.Run(key, func(t *testing.T) {
			backing := beads.NewMemStore()
			cache := beads.NewCachingStoreForTest(backing, nil)
			if err := cache.PrimeActive(); err != nil {
				t.Fatalf("PrimeActive: %v", err)
			}
			if _, err := backing.Create(beads.Bead{
				Type:     BeadType,
				Labels:   []string{LabelSession},
				Metadata: map[string]string{"session_name": "other", key: "taken"},
			}); err != nil {
				t.Fatalf("Create in backing store: %v", err)
			}

			if err := ensureSessionNameAvailable(cache, "taken"); !errors.Is(err, ErrSessionNameExists) {
				t.Fatalf("err = %v, want ErrSessionNameExists: a live bead holding %q as %s exists in the backing store", err, "taken", key)
			}
		})
	}
}

func TestNoLiveSessionNameCollisionsSeesLiveBeadsTheCacheHasNotSeen(t *testing.T) {
	backing := beads.NewMemStore()
	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	if _, err := backing.Create(beads.Bead{
		Type:     BeadType,
		Labels:   []string{LabelSession},
		Metadata: map[string]string{"session_name": "other", "alias": "taken"},
	}); err != nil {
		t.Fatalf("Create in backing store: %v", err)
	}

	if noLiveSessionNameCollisions(cache, "taken", "", "") {
		t.Fatal("noLiveSessionNameCollisions = true, want false: a live alias holder exists in the backing store")
	}
}

// The reduction above must not change any answer. These pin the closed-row
// rules the loop in ensureSessionNameAvailableForSelfAndOwner already had.
func TestEnsureSessionNameAvailableClosedRowRulesAreUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		meta    map[string]string
		closed  bool
		wantErr bool
	}{
		{"closed session_name holder keeps the name reserved", map[string]string{"session_name": "taken"}, true, true},
		{"open session_name holder blocks", map[string]string{"session_name": "taken"}, false, true},
		{"closed alias holder does not block", map[string]string{"session_name": "other", "alias": "taken"}, true, false},
		{"closed agent_name holder does not block", map[string]string{"session_name": "other", "agent_name": "taken"}, true, false},
		{"closed template holder does not block", map[string]string{"session_name": "other", "template": "taken"}, true, false},
		{"closed common_name holder does not block", map[string]string{"session_name": "other", "common_name": "taken"}, true, false},
		{"open alias holder blocks", map[string]string{"session_name": "other", "alias": "taken"}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			b, err := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: tc.meta})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tc.closed {
				if err := store.Close(b.ID); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}

			err = ensureSessionNameAvailable(store, "taken")
			if tc.wantErr && !errors.Is(err, ErrSessionNameExists) {
				t.Fatalf("err = %v, want ErrSessionNameExists", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

// When a closed configured-named holder releases the name, the error must name
// the OPEN bead that still blocks it (the session_name matches are read first,
// then the rest, each bead once).
func TestEnsureSessionNameAvailableNamesTheOpenBlockerWhenAClosedNamedHolderIsReleased(t *testing.T) {
	store := beads.NewMemStore()
	closedHolder, err := store.Create(beads.Bead{
		Type:     BeadType,
		Labels:   []string{LabelSession},
		Metadata: map[string]string{"session_name": "taken", "configured_named_session": "true"},
	})
	if err != nil {
		t.Fatalf("Create closed holder: %v", err)
	}
	if err := store.Close(closedHolder.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	openBlocker, err := store.Create(beads.Bead{
		Type:     BeadType,
		Labels:   []string{LabelSession},
		Metadata: map[string]string{"session_name": "other", "alias": "taken"},
	})
	if err != nil {
		t.Fatalf("Create open blocker: %v", err)
	}

	err = ensureSessionNameAvailable(store, "taken")
	if !errors.Is(err, ErrSessionNameExists) {
		t.Fatalf("err = %v, want ErrSessionNameExists from the open alias holder", err)
	}
	if !strings.Contains(err.Error(), openBlocker.ID) {
		t.Fatalf("err = %q, want it to name the open blocker %s", err.Error(), openBlocker.ID)
	}
}

func TestNoLiveSessionNameCollisionsClosedRowRulesAreUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		meta   map[string]string
		closed bool
		want   bool
	}{
		{"closed session_name holder is not a live collision", map[string]string{"session_name": "taken"}, true, true},
		{"open session_name holder collides", map[string]string{"session_name": "taken"}, false, false},
		{"closed alias holder is not a live collision", map[string]string{"session_name": "other", "alias": "taken"}, true, true},
		{"open alias holder collides", map[string]string{"session_name": "other", "alias": "taken"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStore()
			b, err := store.Create(beads.Bead{Type: BeadType, Labels: []string{LabelSession}, Metadata: tc.meta})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if tc.closed {
				if err := store.Close(b.ID); err != nil {
					t.Fatalf("Close: %v", err)
				}
			}

			if got := noLiveSessionNameCollisions(store, "taken", "", ""); got != tc.want {
				t.Fatalf("noLiveSessionNameCollisions = %v, want %v", got, tc.want)
			}
		})
	}
}
