package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// newSuspendWaitTestSession creates a minimal session bead + fake provider
// pair, mirroring reconcilerTestEnv.createSessionBead/addDesired's fixture
// shape (session_reconciler_test.go), so workerSessionTargetRunningWithConfig
// resolves the bead the same way the reconciler does. running controls
// whether the fake provider is started for this session up front.
func newSuspendWaitTestSession(t *testing.T, name string, running bool) (beads.Store, *runtime.Fake, string) {
	t.Helper()
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	b, err := store.Create(beads.Bead{
		Title:  name,
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": name,
			"agent_name":   name,
			"template":     name,
			"state":        "active",
		},
	})
	if err != nil {
		t.Fatalf("creating test session bead: %v", err)
	}
	if running {
		if err := sp.Start(t.Context(), name, runtime.Config{Command: "test-cmd"}); err != nil {
			t.Fatalf("starting fake session: %v", err)
		}
	}
	return store, sp, b.ID
}

// TestWaitForSessionRuntimeStop_AlreadyStopped covers the common fast path:
// the runtime is already gone (e.g. the reconciler ticked before the CLI
// even started polling), so the wait must return true on its very first
// observation, without ever calling sleep.
func TestWaitForSessionRuntimeStop_AlreadyStopped(t *testing.T) {
	store, sp, sessionID := newSuspendWaitTestSession(t, "worker", false)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	sleepCalls := 0
	sleep := func(time.Duration) { sleepCalls++ }

	stopped := waitForSessionRuntimeStop("", store, sp, &config.City{}, sessionID, 5*time.Second, time.Second, clk, sleep)

	if !stopped {
		t.Fatal("waitForSessionRuntimeStop = false, want true for an already-stopped runtime")
	}
	if sleepCalls != 0 {
		t.Errorf("sleep called %d times, want 0 — an already-stopped runtime must not wait at all", sleepCalls)
	}
}

// TestWaitForSessionRuntimeStop_StopsDuringWait covers the case this bug
// (ga-me6wsb) is actually meant to fix: the runtime is still alive when the
// wait begins (the reconciler hasn't ticked yet) but stops shortly after —
// well within the bound. The wait must observe that and report true, instead
// of the old behavior of never checking at all.
func TestWaitForSessionRuntimeStop_StopsDuringWait(t *testing.T) {
	store, sp, sessionID := newSuspendWaitTestSession(t, "worker", true)
	clk := &clock.Fake{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	sleepCalls := 0
	sleep := func(d time.Duration) {
		sleepCalls++
		clk.Advance(d)
		if sleepCalls == 1 {
			// Simulate the reconciler's async drain completing between polls.
			if err := sp.Stop("worker"); err != nil {
				t.Fatalf("sp.Stop: %v", err)
			}
		}
	}

	stopped := waitForSessionRuntimeStop("", store, sp, &config.City{}, sessionID, 5*time.Second, time.Second, clk, sleep)

	if !stopped {
		t.Fatal("waitForSessionRuntimeStop = false, want true once the runtime stops mid-wait")
	}
	if sleepCalls != 1 {
		t.Errorf("sleep called %d times, want exactly 1 (stop happens on the first re-check)", sleepCalls)
	}
}

// TestWaitForSessionRuntimeStop_TimesOutStillRunning covers the honest-
// failure path: the runtime is still alive at the bound (e.g. the reconciler
// is deliberately protecting in-flight assigned work). The wait must give up
// and report false rather than block indefinitely or claim success.
func TestWaitForSessionRuntimeStop_TimesOutStillRunning(t *testing.T) {
	store, sp, sessionID := newSuspendWaitTestSession(t, "worker", true)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &clock.Fake{Time: start}
	maxWait := 2 * time.Second
	pollInterval := 500 * time.Millisecond
	sleepCalls := 0
	sleep := func(d time.Duration) {
		sleepCalls++
		clk.Advance(d)
	}

	stopped := waitForSessionRuntimeStop("", store, sp, &config.City{}, sessionID, maxWait, pollInterval, clk, sleep)

	if stopped {
		t.Fatal("waitForSessionRuntimeStop = true, want false — runtime never stopped within the bound")
	}
	if !sp.IsRunning("worker") {
		t.Fatal("test setup invariant broken: runtime should still be running")
	}
	if sleepCalls == 0 {
		t.Error("sleep never called — the wait must poll at least once before giving up")
	}
	if elapsed := clk.Now().Sub(start); elapsed < maxWait {
		t.Errorf("fake clock advanced by %v, want at least maxWait %v before giving up", elapsed, maxWait)
	}
}
