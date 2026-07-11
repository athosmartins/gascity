package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// assertEvent fails the test unless want appears in events.
func assertEvent(t *testing.T, events []string, want string) {
	t.Helper()
	for _, e := range events {
		if e == want {
			return
		}
	}
	t.Fatalf("events = %v, want %s", events, want)
}

// sleepPastSecondBoundary blocks until the wall clock crosses into the next
// whole second, so a row created (or reconciled) before the call has a strictly
// smaller second-resolution timestamp than one after it. Used to place a
// "settled" row in a second strictly below the reconcile watermark, so the
// second-boundary re-hydration cohort (ga-ftmci Fix 2) provably does not include
// it.
func sleepPastSecondBoundary() {
	now := time.Now()
	next := now.Truncate(time.Second).Add(time.Second)
	time.Sleep(next.Sub(now) + 5*time.Millisecond)
}

// depBundlingStore is a test backing whose List/Get bundle each bead's
// dependencies into the returned bead's .Dependencies field and which reports
// listIncludesCompleteDependencies()==true — mirroring the PRODUCTION
// NativeDoltStore path (the OPPOSITE branch from MemStore, which fetches deps
// per-ID). It can also override a bead's returned description to simulate a
// content write that does not advance updated_at (same-second write). Every
// List query is recorded for query-shape assertions.
type depBundlingStore struct {
	*MemStore
	mu        sync.Mutex
	queries   []ListQuery
	forceDesc map[string]string
}

func newDepBundlingStore() *depBundlingStore {
	return &depBundlingStore{MemStore: NewMemStore(), forceDesc: map[string]string{}}
}

func (s *depBundlingStore) listIncludesCompleteDependencies() bool { return true }

func (s *depBundlingStore) List(query ListQuery) ([]Bead, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	beads, err := s.MemStore.List(query)
	if err != nil {
		return beads, err
	}
	for i := range beads {
		beads[i] = s.decorate(beads[i])
	}
	return beads, nil
}

func (s *depBundlingStore) Get(id string) (Bead, error) {
	b, err := s.MemStore.Get(id)
	if err != nil {
		return b, err
	}
	return s.decorate(b), nil
}

// decorate bundles fresh deps (from the separate dep table) into .Dependencies
// and applies any forced-description override. It never touches UpdatedAt, so a
// forced description simulates a same-second content write.
func (s *depBundlingStore) decorate(b Bead) Bead {
	deps, _ := s.MemStore.DepList(b.ID, "down")
	b.Dependencies = deps
	s.mu.Lock()
	if d, ok := s.forceDesc[b.ID]; ok {
		b.Description = d
	}
	s.mu.Unlock()
	return b
}

func (s *depBundlingStore) setForceDesc(id, desc string) {
	s.mu.Lock()
	s.forceDesc[id] = desc
	s.mu.Unlock()
}

func (s *depBundlingStore) recordedQueries() []ListQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ListQuery, len(s.queries))
	copy(out, s.queries)
	return out
}

func (s *depBundlingStore) resetQueries() {
	s.mu.Lock()
	s.queries = nil
	s.mu.Unlock()
}

// droppingListStore wraps a Store and silently omits selected bead IDs from
// List results, simulating a cleanly parsed but incomplete List under backend
// stress.
type droppingListStore struct {
	Store
	dropFromList map[string]struct{}
	getOverride  map[string]Bead
	getErr       map[string]error
}

func (s *droppingListStore) List(query ListQuery) ([]Bead, error) {
	all, err := s.Store.List(query)
	if err != nil || len(s.dropFromList) == 0 {
		return all, err
	}
	filtered := make([]Bead, 0, len(all))
	for _, b := range all {
		if _, drop := s.dropFromList[b.ID]; drop {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered, nil
}

func (s *droppingListStore) Get(id string) (Bead, error) {
	if err, ok := s.getErr[id]; ok {
		return Bead{}, err
	}
	if b, ok := s.getOverride[id]; ok {
		return cloneBead(b), nil
	}
	return s.Store.Get(id)
}

func assertNotCached(t *testing.T, cache *CachingStore, id string) {
	t.Helper()
	cache.mu.RLock()
	_, ok := cache.beads[id]
	cache.mu.RUnlock()
	if ok {
		t.Fatalf("cache still has bead %q after confirmed close", id)
	}
}

// TestReconcileSkipsCloseWhenListDropsAliveBead reproduces the cache-thrash
// scenario where a cleanly incomplete List omits an alive bead. Before the
// fix, the reconciler would synthesize bead.closed every cycle and
// re-introduction via other paths would re-trigger it.
func TestReconcileSkipsCloseWhenListDropsAliveBead(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	survivor, err := mem.Create(Bead{Title: "Survivor"})
	if err != nil {
		t.Fatalf("Create survivor: %v", err)
	}
	dropped, err := mem.Create(Bead{Title: "Dropped by tolerant parser"})
	if err != nil {
		t.Fatalf("Create dropped: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	backing.dropFromList = map[string]struct{}{dropped.ID: {}}
	events = events[:0]

	cache.runReconciliation()

	for _, e := range events {
		if e == "bead.closed:"+dropped.ID {
			t.Fatalf("emitted bead.closed for an alive bead dropped by List; events = %v", events)
		}
	}

	got, err := cache.Get(dropped.ID)
	if err != nil {
		t.Fatalf("Get(dropped) after reconcile: %v", err)
	}
	if got.Status == "closed" {
		t.Fatalf("Get(dropped) returned status=closed; cache should still see it as alive")
	}
	if _, err := cache.Get(survivor.ID); err != nil {
		t.Fatalf("Get(survivor) after reconcile: %v", err)
	}
	stats := cache.Stats()
	if stats.ReconcileRecoveries != 1 {
		t.Fatalf("ReconcileRecoveries = %d, want 1", stats.ReconcileRecoveries)
	}
	if stats.ReconcileCloseDeferrals != 0 {
		t.Fatalf("ReconcileCloseDeferrals = %d, want 0", stats.ReconcileCloseDeferrals)
	}
}

// TestReconcileEmitsCloseWhenBackingConfirmsNotFound verifies that a genuine
// closure (List omits the bead AND backing.Get reports ErrNotFound) still
// produces a bead.closed event.
func TestReconcileEmitsCloseWhenBackingConfirmsNotFound(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	gone, err := mem.Create(Bead{Title: "Truly gone"})
	if err != nil {
		t.Fatalf("Create gone: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	backing.dropFromList = map[string]struct{}{gone.ID: {}}
	backing.getErr = map[string]error{
		gone.ID: fmt.Errorf("getting bead %q: %w", gone.ID, ErrNotFound),
	}
	events = events[:0]

	cache.runReconciliation()

	want := "bead.closed:" + gone.ID
	found := false
	for _, e := range events {
		if e == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("events = %v, want %s when backing confirmed not-found", events, want)
	}
	if _, err := cache.Get(gone.ID); err == nil {
		t.Fatalf("Get(gone) succeeded after confirmed close; cache should evict it")
	}
	assertNotCached(t, cache, gone.ID)
}

// TestReconcileEmitsCloseWhenGetReturnsClosed verifies that a real open-to-
// closed transition still emits bead.closed when the closed bead is absent
// from normal List results.
func TestReconcileEmitsCloseWhenGetReturnsClosed(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	closing, err := mem.Create(Bead{Title: "Closing"})
	if err != nil {
		t.Fatalf("Create closing: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if err := mem.Close(closing.ID); err != nil {
		t.Fatalf("Close backing bead: %v", err)
	}
	events = events[:0]

	cache.runReconciliation()

	want := "bead.closed:" + closing.ID
	found := false
	for _, e := range events {
		if e == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("events = %v, want %s when backing returned closed bead", events, want)
	}
	assertNotCached(t, cache, closing.ID)
}

// TestReconcileEmitsFreshClosePayloadWhenGetReturnsClosed pins the close
// recovery path that verifies a missing active-list row with backing.Get.
// The close notification must carry the fresh closed row, not a synthetic
// status flip built from stale cache contents.
func TestReconcileEmitsFreshClosePayloadWhenGetReturnsClosed(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	closing, err := mem.Create(Bead{Title: "Closing"})
	if err != nil {
		t.Fatalf("Create closing: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var closedPayload Bead
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, payload json.RawMessage) {
		if eventType != "bead.closed" || beadID != closing.ID {
			return
		}
		if err := json.Unmarshal(payload, &closedPayload); err != nil {
			t.Fatalf("unmarshal close payload: %v", err)
		}
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	status := "closed"
	if err := mem.Update(closing.ID, UpdateOpts{
		Status: &status,
		Metadata: map[string]string{
			"ci.verdict": "done",
			"gc.outcome": "pass",
		},
	}); err != nil {
		t.Fatalf("Close backing bead with metadata: %v", err)
	}

	cache.runReconciliation()

	if closedPayload.ID != closing.ID {
		t.Fatalf("closed payload ID = %q, want %q", closedPayload.ID, closing.ID)
	}
	if closedPayload.Metadata["ci.verdict"] != "done" || closedPayload.Metadata["gc.outcome"] != "pass" {
		t.Fatalf("closed payload metadata = %#v, want fresh backing close metadata", closedPayload.Metadata)
	}
	assertNotCached(t, cache, closing.ID)
}

// TestReconcileDefersCloseOnBackingError verifies that a transient backing
// failure (List omits the bead, Get returns a non-NotFound error) does NOT
// produce a bead.closed event — the close is deferred until a later scan.
func TestReconcileDefersCloseOnBackingError(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	uncertain, err := mem.Create(Bead{Title: "Uncertain"})
	if err != nil {
		t.Fatalf("Create uncertain: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	backing.dropFromList = map[string]struct{}{uncertain.ID: {}}
	backing.getErr = map[string]error{uncertain.ID: errors.New("dolt: connection reset")}
	events = events[:0]

	cache.runReconciliation()

	for _, e := range events {
		if e == "bead.closed:"+uncertain.ID {
			t.Fatalf("emitted bead.closed despite backing.Get error; events = %v", events)
		}
	}
	if _, err := cache.Get(uncertain.ID); err != nil {
		t.Fatalf("Get(uncertain) after reconcile: %v", err)
	}
	stats := cache.Stats()
	if stats.ReconcileRecoveries != 0 {
		t.Fatalf("ReconcileRecoveries = %d, want 0", stats.ReconcileRecoveries)
	}
	if stats.ReconcileCloseDeferrals != 1 {
		t.Fatalf("ReconcileCloseDeferrals = %d, want 1", stats.ReconcileCloseDeferrals)
	}
}

// TestReconcileDefersCloseWhenGetReturnsWrongID verifies recovery does not
// merge a successful but invalid Get result under the requested ID.
func TestReconcileDefersCloseWhenGetReturnsWrongID(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	uncertain, err := mem.Create(Bead{Title: "Uncertain"})
	if err != nil {
		t.Fatalf("Create uncertain: %v", err)
	}

	backing := &droppingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	backing.dropFromList = map[string]struct{}{uncertain.ID: {}}
	backing.getOverride = map[string]Bead{
		uncertain.ID: {ID: "wrong-id", Title: "Wrong bead", Status: "open"},
	}
	events = events[:0]

	cache.runReconciliation()

	for _, e := range events {
		if e == "bead.closed:"+uncertain.ID {
			t.Fatalf("emitted bead.closed despite wrong backing.Get ID; events = %v", events)
		}
	}
	got, err := cache.Get(uncertain.ID)
	if err != nil {
		t.Fatalf("Get(uncertain) after reconcile: %v", err)
	}
	if got.ID != uncertain.ID || got.Title != uncertain.Title {
		t.Fatalf("Get(uncertain) = %#v, want cached bead %#v", got, uncertain)
	}
	stats := cache.Stats()
	if stats.ReconcileRecoveries != 0 {
		t.Fatalf("ReconcileRecoveries = %d, want 0", stats.ReconcileRecoveries)
	}
	if stats.ReconcileCloseDeferrals != 1 {
		t.Fatalf("ReconcileCloseDeferrals = %d, want 1", stats.ReconcileCloseDeferrals)
	}
}

// recordingListStore records every List query it receives so a test can assert
// the exact query shape the reconciler uses to hydrate the cache. All other
// Store methods delegate to the embedded backing.
type recordingListStore struct {
	Store
	mu      sync.Mutex
	queries []ListQuery
}

func (s *recordingListStore) List(query ListQuery) ([]Bead, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return s.Store.List(query)
}

func (s *recordingListStore) reset() {
	s.mu.Lock()
	s.queries = nil
	s.mu.Unlock()
}

func (s *recordingListStore) snapshot() []ListQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ListQuery, len(s.queries))
	copy(out, s.queries)
	return out
}

// TestReconcileHydrationScopesToNonClosed pins the reconcile hydration scan to a
// non-closed scope at its construction site. The per-cycle full-table hydration
// must never request closed beads: on a production store that is ~97% closed,
// hydrating closed rows streams tens of thousands of rows every reconcile cycle
// per rig and is the dominant driver of chronic Dolt CPU. The scoping is
// load-bearing for that cost AND coupled to close detection (a bead leaving the
// non-closed set is precisely what signals a close). If a future edit flips
// IncludeClosed or pins Status on the reconcile query, the reconciler would
// hydrate closed rows and this test fails.
func TestReconcileHydrationScopesToNonClosed(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	if _, err := mem.Create(Bead{Title: "open work"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	backing := &recordingListStore{Store: mem}
	cache := NewCachingStoreForTest(backing, func(string, string, json.RawMessage) {})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	backing.reset()
	cache.runReconciliation()

	var scan *ListQuery
	for _, q := range backing.snapshot() {
		if q.AllowScan && q.SkipBody && q.TierMode == TierBoth {
			hit := q
			scan = &hit
			break
		}
	}
	if scan == nil {
		t.Fatalf("reconcile issued no full-table hydration scan; queries=%+v", backing.snapshot())
	}
	if scan.IncludeClosed {
		t.Fatalf("reconcile hydration set IncludeClosed=true — it would hydrate every closed row each cycle")
	}
	if scan.Status != "" {
		t.Fatalf("reconcile hydration pinned Status=%q; want empty so the scope is exactly non-closed", scan.Status)
	}
	// Matches encodes the scope the SQL builders translate to WHERE status !=
	// 'closed': it must reject a closed bead and accept an open one.
	if scan.Matches(Bead{ID: "c", Status: "closed"}) {
		t.Fatalf("reconcile hydration query Matches a closed bead; non-closed scoping regressed")
	}
	if !scan.Matches(Bead{ID: "o", Status: "open"}) {
		t.Fatalf("reconcile hydration query rejects an open bead; scoping is too narrow and would drop live work")
	}
}

// TestReconcileOpenOnlyHydrationDetectsCloseInMajorityClosedStore proves the
// safety property that makes non-closed hydration correct. The store is seeded
// mostly-closed to mirror production, and the test asserts both halves: closed
// rows are never hydrated into the cache (the optimization), AND a bead that was
// open last cycle and is now closed — therefore absent from the open-only scan —
// still emits bead.closed via the recoverMissingFromList → Get → confirmedClosed
// path (the guarantee). A handful of newly-missing beads per cycle take the Get
// path; there is no Get storm and no spurious close deferral.
func TestReconcileOpenOnlyHydrationDetectsCloseInMajorityClosedStore(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	closedIDs := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		b, err := mem.Create(Bead{Title: fmt.Sprintf("archived-%d", i)})
		if err != nil {
			t.Fatalf("Create archived-%d: %v", i, err)
		}
		if err := mem.Close(b.ID); err != nil {
			t.Fatalf("Close archived-%d: %v", i, err)
		}
		closedIDs = append(closedIDs, b.ID)
	}
	staysOpen, err := mem.Create(Bead{Title: "stays open"})
	if err != nil {
		t.Fatalf("Create staysOpen: %v", err)
	}
	willClose, err := mem.Create(Bead{Title: "will close"})
	if err != nil {
		t.Fatalf("Create willClose: %v", err)
	}

	var events []string
	cache := NewCachingStoreForTest(mem, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	// Optimization: only the two non-closed beads are hydrated; the 20 closed
	// rows never enter the cache.
	cache.mu.RLock()
	hydrated := len(cache.beads)
	_, hasStaysOpen := cache.beads[staysOpen.ID]
	_, hasWillClose := cache.beads[willClose.ID]
	cache.mu.RUnlock()
	if hydrated != 2 {
		t.Fatalf("cache hydrated %d beads; want 2 (closed rows must not be hydrated)", hydrated)
	}
	if !hasStaysOpen || !hasWillClose {
		t.Fatalf("cache missing an open bead after prime (staysOpen=%v willClose=%v)", hasStaysOpen, hasWillClose)
	}
	for _, id := range closedIDs {
		assertNotCached(t, cache, id)
	}

	// Guarantee: close one open bead. The open-only scan no longer returns it,
	// so close detection must run through recoverMissingFromList.
	events = events[:0]
	if err := mem.Close(willClose.ID); err != nil {
		t.Fatalf("Close willClose: %v", err)
	}
	cache.runReconciliation()

	want := "bead.closed:" + willClose.ID
	found := false
	for _, e := range events {
		if e == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("events = %v, want %s under open-only hydration", events, want)
	}
	assertNotCached(t, cache, willClose.ID)
	if _, err := cache.Get(staysOpen.ID); err != nil {
		t.Fatalf("Get(staysOpen) after reconcile: %v", err)
	}
	stats := cache.Stats()
	if stats.ReconcileCloseDeferrals != 0 {
		t.Fatalf("ReconcileCloseDeferrals = %d, want 0 (close was confirmed, not deferred)", stats.ReconcileCloseDeferrals)
	}
}

// TestReconcileIncrementalHydrationOnlyHydratesChangedRows is the corpus-safety
// gate for ga-ftmci incremental hydration. After a steady-state reconcile
// establishes the DB-clock watermark, exactly one open row's content is mutated
// (bumping updated_at) and a different row is closed. The test asserts, via the
// recordingListStore that captures every ListQuery, that:
//   (a) the reconcile issues an updated_at-scoped hydration scan (not a full
//       scan) that selects ONLY the bumped row — the unchanged row is not
//       re-hydrated; AND
//   (b) change detection is preserved end to end: the bumped row emits
//       bead.updated with fresh content, the closed row still emits bead.closed
//       (via the complete-scan absence → recoverMissingFromList path), and the
//       untouched row emits nothing.
// A naive `updated_at > watermark` filter on the reconcile scan would drop every
// unchanged open row from the diff and synthesize bead.closed for the whole
// corpus; the two-query shape (cheap complete scan for the ID set, watermark
// scan only for hydration) is what this test pins.
func TestReconcileIncrementalHydrationOnlyHydratesChangedRows(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	staysOpen, err := mem.Create(Bead{Title: "stays open"})
	if err != nil {
		t.Fatalf("Create staysOpen: %v", err)
	}
	// Put staysOpen in a second strictly below the watermark so the second-
	// boundary re-hydration cohort (Fix 2) provably excludes it — otherwise a
	// row created in the same second as the frontier is correctly re-hydrated.
	sleepPastSecondBoundary()
	willBump, err := mem.Create(Bead{Title: "will bump"})
	if err != nil {
		t.Fatalf("Create willBump: %v", err)
	}
	willClose, err := mem.Create(Bead{Title: "will close"})
	if err != nil {
		t.Fatalf("Create willClose: %v", err)
	}

	backing := &recordingListStore{Store: mem}
	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	// First reconcile establishes the hydration watermark (boot: full hydrate).
	cache.runReconciliation()

	backing.reset()
	events = events[:0]

	// Mutate exactly one open row's content (bumps updated_at strictly past the
	// watermark) and close another.
	time.Sleep(2 * time.Millisecond)
	newTitle := "will bump (edited)"
	if err := mem.Update(willBump.ID, UpdateOpts{Title: &newTitle}); err != nil {
		t.Fatalf("Update willBump: %v", err)
	}
	if err := mem.Close(willClose.ID); err != nil {
		t.Fatalf("Close willClose: %v", err)
	}

	cache.runReconciliation()

	// (a) An updated_at-scoped hydration scan AND a cheap complete scan were
	// issued. A naive full-scan reconcile issues neither and fails here.
	var hydrationQ *ListQuery
	var cheapCompleteQ *ListQuery
	for _, q := range backing.snapshot() {
		qq := q
		if !qq.UpdatedAfter.IsZero() {
			hydrationQ = &qq
		}
		if qq.SkipDescription && qq.UpdatedAfter.IsZero() && qq.AllowScan {
			cheapCompleteQ = &qq
		}
	}
	if hydrationQ == nil {
		t.Fatalf("reconcile issued no updated_at-scoped hydration scan; queries=%+v", backing.snapshot())
	}
	if cheapCompleteQ == nil {
		t.Fatalf("reconcile issued no cheap complete (SkipDescription) scan; queries=%+v", backing.snapshot())
	}

	// The hydration scan, evaluated against the store, returns ONLY the bumped
	// row: staysOpen (unchanged, updated_at <= watermark) and willClose (closed)
	// are excluded.
	hydratedRows, err := mem.List(*hydrationQ)
	if err != nil {
		t.Fatalf("evaluate hydration query: %v", err)
	}
	hydratedIDs := make(map[string]bool, len(hydratedRows))
	for _, b := range hydratedRows {
		hydratedIDs[b.ID] = true
	}
	if !hydratedIDs[willBump.ID] {
		t.Fatalf("hydration scan omitted the updated_at-bumped row %s; got %v", willBump.ID, hydratedIDs)
	}
	if hydratedIDs[staysOpen.ID] {
		t.Fatalf("hydration scan re-hydrated the unchanged row %s; incremental hydration regressed", staysOpen.ID)
	}

	// (b) Change detection preserved.
	assertEvent(t, events, "bead.updated:"+willBump.ID)
	assertEvent(t, events, "bead.closed:"+willClose.ID)
	for _, e := range events {
		if e == "bead.updated:"+staysOpen.ID {
			t.Fatalf("unchanged row %s emitted bead.updated; events=%v", staysOpen.ID, events)
		}
	}
	assertNotCached(t, cache, willClose.ID)
	if _, err := cache.Get(staysOpen.ID); err != nil {
		t.Fatalf("Get(staysOpen) after reconcile: %v", err)
	}
	got, err := cache.Get(willBump.ID)
	if err != nil {
		t.Fatalf("Get(willBump): %v", err)
	}
	if got.Title != newTitle {
		t.Fatalf("willBump title = %q, want %q (hydration did not refresh content)", got.Title, newTitle)
	}
}

// TestReconcileIncrementalHydrationDetectsDepOnlyChangeWithoutUpdatedAtBump pins
// the dep-only footgun the runbook flags: adding a dependency does NOT bump the
// source issue's updated_at (deps live in a separate table), so hydration
// selection keyed on updated_at will not content-hydrate the source. The
// reconcile must still detect the dependency change, because deps are refreshed
// for the COMPLETE open set every cycle (fetchDepsForBeads over freshByID),
// independent of the updated_at hydration decision.
func TestReconcileIncrementalHydrationDetectsDepOnlyChangeWithoutUpdatedAtBump(t *testing.T) {
	t.Parallel()

	mem := NewMemStore()
	src, err := mem.Create(Bead{Title: "dep source"})
	if err != nil {
		t.Fatalf("Create src: %v", err)
	}
	target, err := mem.Create(Bead{Title: "dep target"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	var events []string
	cache := NewCachingStoreForTest(mem, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cache.runReconciliation() // establish watermark
	events = events[:0]

	before, err := mem.Get(src.ID)
	if err != nil {
		t.Fatalf("Get src before dep: %v", err)
	}
	if err := mem.DepAdd(src.ID, target.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	after, err := mem.Get(src.ID)
	if err != nil {
		t.Fatalf("Get src after dep: %v", err)
	}
	// Precondition: the dep add did NOT bump updated_at (else the test no longer
	// exercises the footgun and would pass trivially via content hydration).
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("precondition failed: DepAdd bumped src UpdatedAt (%s → %s); test no longer covers the dep-only-no-bump case",
			before.UpdatedAt, after.UpdatedAt)
	}

	cache.runReconciliation()

	assertEvent(t, events, "bead.updated:"+src.ID)
	deps, err := cache.DepList(src.ID, "down")
	if err != nil {
		t.Fatalf("DepList(src): %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != target.ID {
		t.Fatalf("cache deps for src = %+v, want exactly one dep on %s", deps, target.ID)
	}
}

// TestReconcileIncrementalHydrationRefreshesDepsOnCompleteDepsBacking is the
// regression test for Fix 1 on the PRODUCTION native path
// (listIncludesCompleteDependencies()==true, so reconcile deps are derived from
// each bead's carried .Dependencies field — NOT the per-ID DepList path MemStore
// uses). A dep add does not bump issues.updated_at, so the source bead is
// carried forward unchanged. Its cached .Dependencies would be STALE unless the
// carry-forward path refreshes deps from the cheap complete scan (which already
// hydrates them). The source is placed a full second below the watermark so it
// is genuinely carried forward (not re-hydrated by the second-boundary cohort),
// isolating the carry-forward dep-overlay fix. FAILS before Fix 1 (no
// bead.updated; DepList stays empty); PASSES after.
func TestReconcileIncrementalHydrationRefreshesDepsOnCompleteDepsBacking(t *testing.T) {
	t.Parallel()

	backing := newDepBundlingStore()
	src, err := backing.Create(Bead{Title: "dep source"})
	if err != nil {
		t.Fatalf("Create src: %v", err)
	}
	target, err := backing.Create(Bead{Title: "dep target"})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	// Advance a full second, then create a pacer so the watermark second is
	// strictly greater than src's second: src is then carried forward, never in
	// the boundary re-hydration cohort.
	sleepPastSecondBoundary()
	if _, err := backing.Create(Bead{Title: "pacer"}); err != nil {
		t.Fatalf("Create pacer: %v", err)
	}

	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cache.runReconciliation() // establish watermark (> src's second)
	backing.resetQueries()
	events = events[:0]

	before, err := backing.Get(src.ID)
	if err != nil {
		t.Fatalf("Get src before dep: %v", err)
	}
	if err := backing.DepAdd(src.ID, target.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	after, err := backing.Get(src.ID)
	if err != nil {
		t.Fatalf("Get src after dep: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("precondition failed: DepAdd bumped src UpdatedAt (%s → %s)", before.UpdatedAt, after.UpdatedAt)
	}

	cache.runReconciliation()

	// src is carried forward (below the watermark second): the hydration scan
	// must NOT return it, proving the dep refresh came from the carry-forward
	// overlay, not from re-hydration.
	var hydrationQ *ListQuery
	for _, q := range backing.recordedQueries() {
		qq := q
		if !qq.UpdatedAfter.IsZero() {
			hydrationQ = &qq
		}
	}
	if hydrationQ == nil {
		t.Fatalf("no updated_at-scoped hydration scan issued")
	}
	if hydrationQ.Matches(after) {
		t.Fatalf("hydration scan would re-hydrate src (%s); test no longer isolates the carry-forward dep overlay", src.ID)
	}

	assertEvent(t, events, "bead.updated:"+src.ID)
	deps, err := cache.DepList(src.ID, "down")
	if err != nil {
		t.Fatalf("DepList(src): %v", err)
	}
	if len(deps) != 1 || deps[0].DependsOnID != target.ID {
		t.Fatalf("cache deps for carried-forward src = %+v, want exactly one dep on %s (stale carried deps = Fix 1 regression)", deps, target.ID)
	}
}

// TestReconcileIncrementalHydrationCatchesSameSecondContentWrite is the
// regression test for Fix 2: issues.updated_at is DATETIME (second resolution),
// so a content write in the same wall-clock second as the last observed change
// does NOT advance updated_at. Keyed purely on `updated_at.After(cached)`, that
// second write is carried-forward-stale permanently. The fix re-hydrates the
// boundary-second cohort each cycle. Here the frontier bead's description is
// changed WITHOUT touching updated_at (depBundlingStore.forceDesc); the reconcile
// must still pick up the new content. FAILS before Fix 2 (cache keeps "v1");
// PASSES after ("v2").
func TestReconcileIncrementalHydrationCatchesSameSecondContentWrite(t *testing.T) {
	t.Parallel()

	backing := newDepBundlingStore()
	x, err := backing.Create(Bead{Title: "frontier", Description: "v1"})
	if err != nil {
		t.Fatalf("Create x: %v", err)
	}

	var events []string
	cache := NewCachingStoreForTest(backing, func(eventType, beadID string, _ json.RawMessage) {
		events = append(events, eventType+":"+beadID)
	})
	if err := cache.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cache.runReconciliation() // establish watermark == x's second
	events = events[:0]

	got, err := cache.Get(x.ID)
	if err != nil {
		t.Fatalf("Get x after first reconcile: %v", err)
	}
	if got.Description != "v1" {
		t.Fatalf("precondition: cached description = %q, want v1", got.Description)
	}

	// Same-second content write: change the returned description WITHOUT bumping
	// updated_at (forceDesc leaves UpdatedAt untouched).
	backing.setForceDesc(x.ID, "v2")
	reGot, err := backing.Get(x.ID)
	if err != nil {
		t.Fatalf("Get x after forceDesc: %v", err)
	}
	if !reGot.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("precondition failed: forceDesc changed UpdatedAt (%s → %s); test no longer exercises the same-second race", got.UpdatedAt, reGot.UpdatedAt)
	}

	cache.runReconciliation()

	final, err := cache.Get(x.ID)
	if err != nil {
		t.Fatalf("Get x after second reconcile: %v", err)
	}
	if final.Description != "v2" {
		t.Fatalf("cache description = %q, want v2 — same-second content write was dropped (Fix 2 regression)", final.Description)
	}
	assertEvent(t, events, "bead.updated:"+x.ID)
}
