package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// wakeBudgetHoldHarness runs one budget-limited start pass over a started
// session (which wins the only slot because it was woken longest ago) and a
// set of other candidates that the budget then defers. Session names are unique
// per test: the deferral table is process-wide.
type wakeBudgetHoldHarness struct {
	t     *testing.T
	store *beads.MemStore
	clk   *clock.Fake
	cfg   *config.City
	sp    *runtime.Fake
}

func newWakeBudgetHoldHarness(t *testing.T) *wakeBudgetHoldHarness {
	t.Helper()
	budget := 1
	return &wakeBudgetHoldHarness{
		t:     t,
		store: beads.NewMemStore(),
		clk:   &clock.Fake{Time: time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC)},
		cfg:   &config.City{Daemon: config.DaemonConfig{MaxWakesPerTick: &budget}},
		sp:    runtime.NewFake(),
	}
}

func (h *wakeBudgetHoldHarness) tp(name string) TemplateParams {
	return TemplateParams{
		Command:      "claude",
		SessionName:  name,
		TemplateName: name,
		ResolvedProvider: &config.ResolvedProvider{
			Name:          "claude",
			Command:       "claude",
			PromptMode:    "arg",
			ResumeFlag:    "--resume",
			ResumeStyle:   "flag",
			SessionIDFlag: "--session-id",
		},
	}
}

// bead creates a session bead in state=creating with pending_create_claim, and
// the given extra metadata.
func (h *wakeBudgetHoldHarness) bead(name string, extra map[string]string) beads.Bead {
	h.t.Helper()
	meta := map[string]string{
		"session_name":         name,
		"agent_name":           name,
		"template":             name,
		"state":                "creating",
		"generation":           "1",
		"instance_token":       "test-token",
		"live_hash":            runtime.LiveFingerprint(runtime.Config{Command: "test-cmd"}),
		"pending_create_claim": "true",
	}
	for k, v := range extra {
		meta[k] = v
	}
	b, err := h.store.Create(beads.Bead{
		Title:    name,
		Type:     sessionBeadType,
		Labels:   []string{sessionBeadLabel},
		Metadata: meta,
	})
	if err != nil {
		h.t.Fatalf("Create(%s): %v", name, err)
	}
	return b
}

// startPass runs executePlannedStarts over the beads, in the order given.
func (h *wakeBudgetHoldHarness) startPass(sessions ...beads.Bead) int {
	h.t.Helper()
	desired := map[string]TemplateParams{}
	var candidates []startCandidate
	for i := range sessions {
		sess := sessions[i]
		name := sess.Metadata["session_name"]
		tp := h.tp(name)
		desired[name] = tp
		candidates = append(candidates, startCandidate{session: &sess, tp: tp, order: i})
	}
	return executePlannedStarts(
		context.Background(),
		candidates,
		h.cfg,
		desired,
		h.sp,
		h.store,
		"",
		h.clk,
		events.Discard,
		5*time.Second,
		ioDiscard{},
		ioDiscard{},
	)
}

// The 10/10 shape: a crew create has been in the start queue 11 minutes without
// ever being attempted, because MaxWakesPerTick keeps spending the budget on
// sessions that were woken longer ago. It is waiting for its turn, not dead:
// rolling it back (or reaping it) hands the replacement a fresh CreatedAt, which
// is the fairness key of a never-woken session, i.e. the back of the queue.
func TestWakeBudgetDeferral_KeepsNeverStartedCreateFromLeaseExpiry(t *testing.T) {
	h := newWakeBudgetHoldHarness(t)
	queuedSince := h.clk.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	ahead := h.bead("hold-a-ahead", map[string]string{"last_woke_at": "2020-01-01T00:00:00Z"})
	waiting := h.bead("hold-a-crew", map[string]string{"pending_create_started_at": queuedSince})

	const startupTimeout = 5 * time.Second
	if !pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Fatal("precondition: an 11 min old never-started create with no deferral on record must read as lease-expired")
	}

	if woken := h.startPass(ahead, waiting); woken != 1 {
		t.Fatalf("woken = %d, want 1 (the budget)", woken)
	}
	if !h.sp.IsRunning("hold-a-ahead") || h.sp.IsRunning("hold-a-crew") {
		t.Fatalf("setup: the longest-waiting started session must take the slot and the never-started create must be deferred (ahead=%v crew=%v)",
			h.sp.IsRunning("hold-a-ahead"), h.sp.IsRunning("hold-a-crew"))
	}

	if pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Error("a create the wake budget deferred this tick reads as lease-expired: the reconciler would roll it back and its replacement would join the back of the queue")
	}
	if pendingCreateNeverStartedExpired(waiting, h.clk) {
		t.Error("pendingCreateNeverStartedExpired is true for a create the wake budget deferred this tick")
	}
	// The stuck-creating reaper, the heal path and the pool-slot sweep share
	// this predicate; none of them may close the bead from under the queue.
	if pendingCreateNeverStartedLeaseExpired(waiting, h.clk) {
		t.Error("pendingCreateNeverStartedLeaseExpired is true for a create the wake budget deferred this tick (reaper/heal/sweep would close it)")
	}
	if !pendingCreateLeaseActive(waiting, h.clk, startupTimeout) {
		t.Error("pendingCreateLeaseActive is false for a create the wake budget deferred this tick")
	}
}

// Controls: the hold must not turn a genuinely dead create into an immortal one.
func TestWakeBudgetDeferral_OnlyProtectsTheSessionThatWasDeferred(t *testing.T) {
	h := newWakeBudgetHoldHarness(t)
	queuedSince := h.clk.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	ahead := h.bead("hold-b-ahead", map[string]string{"last_woke_at": "2020-01-01T00:00:00Z"})
	waiting := h.bead("hold-b-crew", map[string]string{"pending_create_started_at": queuedSince})
	// Same age, same shape, never offered to a start pass: nothing deferred it.
	dead := h.bead("hold-b-dead", map[string]string{"pending_create_started_at": queuedSince})
	// Started once, long ago, and deferred now: a started attempt that went stale
	// is the old "attempt stale" rule, not a queue wait.
	stale := h.bead("hold-b-stale", map[string]string{
		"last_woke_at":              h.clk.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
		"pending_create_started_at": h.clk.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
	})

	h.startPass(ahead, waiting, stale)

	const startupTimeout = 5 * time.Second
	if !pendingCreateLeaseExpiredForRollback(dead, h.clk, startupTimeout) {
		t.Error("a never-started create the wake budget did NOT defer must still roll back after 10 min (dead creates keep their old fate)")
	}
	if !pendingCreateLeaseExpiredForRollback(stale, h.clk, startupTimeout) {
		t.Error("a create that already had its start attempt (last_woke_at set) and went stale must still roll back even when deferred now")
	}
	if pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Error("the deferred never-started create must be held (setup check for this control test)")
	}
}

// The hold renews every tick the session is still deferred and lapses when it
// is not: a create that stops being a candidate is dead again, as before.
func TestWakeBudgetDeferral_HoldLapsesWhenTheDeferralStops(t *testing.T) {
	h := newWakeBudgetHoldHarness(t)
	queuedSince := h.clk.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	ahead := h.bead("hold-c-ahead", map[string]string{"last_woke_at": "2020-01-01T00:00:00Z"})
	waiting := h.bead("hold-c-crew", map[string]string{"pending_create_started_at": queuedSince})
	h.startPass(ahead, waiting)

	const startupTimeout = 5 * time.Second
	if pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Fatal("deferred this tick: must be held")
	}
	h.clk.Time = h.clk.Time.Add(4 * time.Minute)
	if pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Error("4 min after the last deferral the hold must still stand (ticks are seconds apart; a gap this short is a slow tick, not a dead create)")
	}
	h.clk.Time = h.clk.Time.Add(2 * time.Minute)
	if !pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Error("6 min after the last deferral the hold must have lapsed: nothing is queueing this create any more, so it rolls back like any dead create")
	}

	// Deferred again => held again, from the new deferral.
	h.startPass(ahead, waiting)
	if pendingCreateLeaseExpiredForRollback(waiting, h.clk, startupTimeout) {
		t.Error("deferred again: must be held again")
	}
}

// The deferral that matters is the one in the wave-level early exit too: a
// wave the budget never reached at all (an earlier wave spent it) defers every
// candidate in it.
func TestWakeBudgetDeferral_WholeWaveSkippedByBudgetIsHeld(t *testing.T) {
	h := newWakeBudgetHoldHarness(t)
	h.cfg.Agents = []config.Agent{
		{Name: "hold-d-base"},
		{Name: "hold-d-crew", DependsOn: []string{"hold-d-base"}},
	}
	queuedSince := h.clk.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	base := h.bead("hold-d-base", map[string]string{"last_woke_at": "2020-01-01T00:00:00Z"})
	crew := h.bead("hold-d-crew", map[string]string{"pending_create_started_at": queuedSince})

	if woken := h.startPass(base, crew); woken != 1 {
		t.Fatalf("woken = %d, want 1: the budget goes to wave 0", woken)
	}
	if h.sp.IsRunning("hold-d-crew") {
		t.Fatal("setup: the wave-1 crew must not have started (budget spent in wave 0)")
	}

	const startupTimeout = 5 * time.Second
	if pendingCreateLeaseExpiredForRollback(crew, h.clk, startupTimeout) {
		t.Error("a create whose whole wave the budget skipped reads as lease-expired: it would be rolled back although it was never given a chance")
	}
}
