package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestRecordSessionAttachedConfigDriftDeferral_SkipsWriteWithinHalfTTL verifies
// that a second deferral with the same drift key, taken well within the
// false-negative TTL window, does NOT re-stamp the deferred_at timestamp.
//
// On parent (pre-fix), recordSessionAttachedConfigDriftDeferral always writes
// now() into deferred_at, producing a bead.updated event every reconcile tick
// (~1.4s) on every attached session bead with persistent drift. This test
// fails on parent and passes after the fix.
func TestRecordSessionAttachedConfigDriftDeferral_SkipsWriteWithinHalfTTL(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	const driftKey = "old-hash:new-hash"

	if err := recordSessionAttachedConfigDriftDeferral(session, env.store, env.clk, driftKey); err != nil {
		t.Fatalf("first record: %v", err)
	}
	first, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after first: %v", err)
	}
	firstStamp := first.Metadata[sessionAttachedConfigDriftDeferredAtMetadata]
	if firstStamp == "" {
		t.Fatal("first call must stamp deferred_at")
	}
	if first.Metadata[sessionAttachedConfigDriftDeferredKeyMetadata] != driftKey {
		t.Fatalf("first key = %q, want %q", first.Metadata[sessionAttachedConfigDriftDeferredKeyMetadata], driftKey)
	}

	// Advance the clock well within TTL/2 (TTL is 30s; advance 5s).
	env.clk.Time = env.clk.Time.Add(5 * time.Second)

	if err := recordSessionAttachedConfigDriftDeferral(first, env.store, env.clk, driftKey); err != nil {
		t.Fatalf("second record: %v", err)
	}
	second, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after second: %v", err)
	}
	secondStamp := second.Metadata[sessionAttachedConfigDriftDeferredAtMetadata]
	if secondStamp != firstStamp {
		t.Fatalf("deferred_at must not be re-stamped within TTL/2; got %q want unchanged %q",
			secondStamp, firstStamp)
	}
}

// TestRecordSessionAttachedConfigDriftDeferral_RewritesWhenKeyChanges verifies
// that a different drift key forces a rewrite even within the TTL window — the
// guard must only suppress writes for the same drift situation, not for
// genuinely new drift.
func TestRecordSessionAttachedConfigDriftDeferral_RewritesWhenKeyChanges(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")

	if err := recordSessionAttachedConfigDriftDeferral(session, env.store, env.clk, "key-A"); err != nil {
		t.Fatalf("first record: %v", err)
	}
	first, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after first: %v", err)
	}
	firstStamp := first.Metadata[sessionAttachedConfigDriftDeferredAtMetadata]

	env.clk.Time = env.clk.Time.Add(5 * time.Second)

	if err := recordSessionAttachedConfigDriftDeferral(first, env.store, env.clk, "key-B"); err != nil {
		t.Fatalf("second record: %v", err)
	}
	second, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after second: %v", err)
	}
	if second.Metadata[sessionAttachedConfigDriftDeferredKeyMetadata] != "key-B" {
		t.Fatalf("key after key-change call = %q, want key-B",
			second.Metadata[sessionAttachedConfigDriftDeferredKeyMetadata])
	}
	if second.Metadata[sessionAttachedConfigDriftDeferredAtMetadata] == firstStamp {
		t.Fatalf("deferred_at must be re-stamped on key change; got unchanged %q", firstStamp)
	}
}

// TestRecordSessionAttachedConfigDriftDeferral_RewritesAfterHalfTTL verifies
// that once the existing stamp is older than TTL/2, the next call refreshes
// it. This keeps the 30s false-negative TTL semantically intact: the
// deferral cannot be allowed to age past TTL just because the same key
// keeps being observed.
func TestRecordSessionAttachedConfigDriftDeferral_RewritesAfterHalfTTL(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	const driftKey = "old-hash:new-hash"

	if err := recordSessionAttachedConfigDriftDeferral(session, env.store, env.clk, driftKey); err != nil {
		t.Fatalf("first record: %v", err)
	}
	first, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after first: %v", err)
	}
	firstStamp := first.Metadata[sessionAttachedConfigDriftDeferredAtMetadata]

	// Advance past TTL/2 (TTL is 30s; advance 16s).
	env.clk.Time = env.clk.Time.Add(sessionAttachedConfigDriftFalseNegativeLimit/2 + time.Second)

	if err := recordSessionAttachedConfigDriftDeferral(first, env.store, env.clk, driftKey); err != nil {
		t.Fatalf("second record: %v", err)
	}
	second, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("get after second: %v", err)
	}
	if second.Metadata[sessionAttachedConfigDriftDeferredAtMetadata] == firstStamp {
		t.Fatalf("deferred_at must be refreshed past TTL/2; got unchanged %q", firstStamp)
	}
}

// TestSessionAttachedForConfigDrift_PinnedIdleSessionSpared is the regression
// test for ga-84rm: a pinned session with NO live tmux attachment (idle crew
// like Oracle/Mila that nobody has a terminal open on at the reconcile tick)
// must be treated as spared from config-drift handling. On parent (pre-fix)
// this returned false because only live attachment was honored — so the
// session fell through to a real config-drift drain decision on every
// config_revision bump. After the fix the durable pin override (pin_awake)
// spares it.
func TestSessionAttachedForConfigDrift_PinnedIdleSessionSpared(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("oracle", "oracle")
	// Pinned, but explicitly NOT attached: no terminal connected.
	env.setSessionMetadata(&session, map[string]string{"pin_awake": "true"})
	env.sp.SetAttached("oracle", false)

	spared, err := sessionAttachedForConfigDrift(session, env.sp, "", env.store, nil, env.cfg, "oracle")
	if err != nil {
		t.Fatalf("sessionAttachedForConfigDrift: %v", err)
	}
	if !spared {
		t.Fatal("pinned idle (unattached) session must be SPARED from config-drift drain; got spared=false")
	}
}

// TestSessionAttachedForConfigDrift_UnpinnedIdleSessionNotSpared is the
// counterpart guard: an UNpinned, unattached session must still be eligible for
// config-drift handling. This proves the pin guard is narrow (it does not
// blanket-spare every idle session) and that the original behavior is intact
// for non-pinned crew.
func TestSessionAttachedForConfigDrift_UnpinnedIdleSessionNotSpared(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("digo", "digo")
	env.sp.SetAttached("digo", false)

	spared, err := sessionAttachedForConfigDrift(session, env.sp, "", env.store, nil, env.cfg, "digo")
	if err != nil {
		t.Fatalf("sessionAttachedForConfigDrift: %v", err)
	}
	if spared {
		t.Fatal("unpinned, unattached session must NOT be spared by the pin guard; got spared=true")
	}
}

// TestSessionAttachedForConfigDrift_PinIsProviderIndependent verifies the pin
// guard holds even when the runtime provider is unavailable (nil). Pinning is a
// durable, bead-level signal and must not depend on a live provider probe.
func TestSessionAttachedForConfigDrift_PinIsProviderIndependent(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("mila", "mila")
	env.setSessionMetadata(&session, map[string]string{"pin_awake": "true"})

	spared, err := sessionAttachedForConfigDrift(session, nil, "", env.store, nil, env.cfg, "mila")
	if err != nil {
		t.Fatalf("sessionAttachedForConfigDrift: %v", err)
	}
	if !spared {
		t.Fatal("pinned session must be spared even with a nil provider; got spared=false")
	}
}

// TestSessionAttachedForConfigDrift_AssignedWorkSpared is the FIX C regression
// (ga-r471): an UNpinned, UNattached session that holds open assigned work
// (story:in-flight) must be SPARED from config-drift drain so a pool dog or
// crew actively building/reviewing is not killed mid-task by a config_revision
// bump. The drift applies once the work bead closes and the session is idle.
func TestSessionAttachedForConfigDrift_AssignedWorkSpared(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("digo", "digo")
	env.sp.SetAttached("digo", false) // not attached
	// Not pinned. Give it open assigned work (a non-session bead assigned to
	// the session's identity).
	if _, err := env.store.Create(beads.Bead{
		Title:    "build story",
		Type:     "task",
		Status:   "open",
		Assignee: "digo",
	}); err != nil {
		t.Fatalf("creating assigned work bead: %v", err)
	}

	spared, err := sessionAttachedForConfigDrift(session, env.sp, "", env.store, nil, env.cfg, "digo")
	if err != nil {
		t.Fatalf("sessionAttachedForConfigDrift: %v", err)
	}
	if !spared {
		t.Fatal("session with open assigned work must be SPARED from config-drift drain; got spared=false")
	}
}

// TestSessionAttachedForConfigDrift_NoAssignedWorkNotSpared is the FIX C
// counterpart: an unpinned, unattached session with NO assigned work must NOT
// be spared by the assigned-work guard — it remains eligible for config-drift
// handling. This proves the guard is narrow and the drain path still fires for
// idle sessions.
func TestSessionAttachedForConfigDrift_NoAssignedWorkNotSpared(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("digo", "digo")
	env.sp.SetAttached("digo", false)
	// No work bead assigned to "digo".

	spared, err := sessionAttachedForConfigDrift(session, env.sp, "", env.store, nil, env.cfg, "digo")
	if err != nil {
		t.Fatalf("sessionAttachedForConfigDrift: %v", err)
	}
	if spared {
		t.Fatal("session with no assigned work must NOT be spared by the assigned-work guard; got spared=true")
	}
}

// TestSessionPinnedAwake_TrimsAndMatches checks the canonical pin predicate
// matches the same normalization used elsewhere (trim + exact "true").
func TestSessionPinnedAwake_TrimsAndMatches(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"  true  ", true},
		{"false", false},
		{"", false},
		{"1", false},
	}
	for _, tc := range cases {
		b := beads.Bead{Metadata: map[string]string{"pin_awake": tc.val}}
		if got := sessionPinnedAwake(b); got != tc.want {
			t.Errorf("sessionPinnedAwake(pin_awake=%q) = %v, want %v", tc.val, got, tc.want)
		}
	}
}
