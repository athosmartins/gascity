package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// spawnTimingProvider decorates runtime.Fake to record the wall-clock
// time of every Start call (ga-ih4ma). Start is the first substantive
// call each async spawn goroutine makes after
// enqueuePreparedStartWaveForCity launches it (via runPreparedStartCandidate
// -> startPreparedStartCandidate -> the worker handle -> sp.Start), so the
// spacing between consecutive recorded Start timestamps is, modulo
// negligible goroutine-scheduling overhead, the spacing between the
// goroutine launches themselves — exactly what the ga-ih4ma stagger is
// supposed to control.
type spawnTimingProvider struct {
	*runtime.Fake

	mu    sync.Mutex
	times []time.Time
}

func newSpawnTimingProvider() *spawnTimingProvider {
	return &spawnTimingProvider{Fake: runtime.NewFake()}
}

func (p *spawnTimingProvider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	p.mu.Lock()
	p.times = append(p.times, time.Now())
	p.mu.Unlock()
	return p.Fake.Start(ctx, name, cfg)
}

// startTimesSorted returns the recorded Start call timestamps sorted
// ascending. Sorted rather than call-order so the assertions below don't
// depend on which goroutine the Go scheduler happens to run first when
// several become runnable close together.
func (p *spawnTimingProvider) startTimesSorted() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]time.Time, len(p.times))
	copy(out, p.times)
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// buildStaggerTestWave creates n asyncPreparedStart items backed by n
// distinct minimal session beads in store. Metadata deliberately omits
// session_key and last_woke_at so runPreparedStartCandidate's stale-key
// (staleKeyDetectDelay sleep) and startupRateLimitScreenDetected (a Peek
// call) branches are skipped entirely — this test measures spawn-launch
// spacing, not those unrelated sub-phases, and skipping them keeps the
// test fast and free of incidental timing noise. wg.Done fires once each
// item's spawn goroutine — including its async commit — has fully
// finished, giving the test a deterministic completion signal instead of
// a wall-clock guess.
func buildStaggerTestWave(t *testing.T, store beads.Store, n int, wg *sync.WaitGroup) []asyncPreparedStart {
	t.Helper()
	items := make([]asyncPreparedStart, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("stagger-agent-%d", i)
		session, err := store.Create(beads.Bead{
			ID:     fmt.Sprintf("gc-stagger-%d", i),
			Title:  name,
			Type:   sessionBeadType,
			Labels: []string{sessionBeadLabel},
			Metadata: map[string]string{
				"session_name": name,
				"template":     "worker",
			},
		})
		if err != nil {
			t.Fatalf("store.Create(%s): %v", name, err)
		}
		item := preparedStart{
			candidate: startCandidate{
				session: &session,
				tp: TemplateParams{
					Command:      "claude",
					SessionName:  name,
					TemplateName: "worker",
				},
			},
			cfg: runtime.Config{
				Command:      "claude",
				ProcessNames: []string{"claude"},
			},
		}
		wg.Add(1)
		items = append(items, asyncPreparedStart{item: item, done: wg.Done})
	}
	return items
}

// waitOrTimeout waits for wg or fails the test after timeout, so a
// regression that deadlocks the spawn loop (e.g. a stagger sleep that
// isn't context-aware) fails fast with a clear message instead of hanging
// the test run.
func waitOrTimeout(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for spawned goroutines to finish")
	}
}

// TestEnqueuePreparedStartWaveForCity_StaggerSpacesOutLaunches is the
// ga-ih4ma regression test. Against the pre-fix code — enqueuePreparedStartWaveForCity's
// `for i, reserved := range prepared { ... go func(...) }` loop with no
// sleep of any kind between iterations — this test fails: all N candidates'
// Start calls land within a couple of milliseconds of each other
// regardless of what session_spawn_stagger is set to, because nothing in
// the old code ever reads that config value. With the fix (a
// sessionSpawnStagger(cfg)-derived interruptibleSleep before every launch
// but the first), consecutive Start calls are spaced by approximately the
// configured stagger, so this test passes.
func TestEnqueuePreparedStartWaveForCity_StaggerSpacesOutLaunches(t *testing.T) {
	t.Setenv("GC_SESSION_SPAWN_STAGGER", "") // isolate from the ambient environment; env wins over config

	const n = 5
	const stagger = 60 * time.Millisecond

	store := beads.NewMemStore()
	sp := newSpawnTimingProvider()
	clk := &clock.Fake{Time: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	cfg := &config.City{Daemon: config.DaemonConfig{SessionSpawnStagger: stagger.String()}}

	var wg sync.WaitGroup
	prepared := buildStaggerTestWave(t, store, n, &wg)

	results := enqueuePreparedStartWaveForCity(
		context.Background(),
		prepared,
		"",
		sp,
		store,
		cfg,
		clk,
		events.Discard,
		5*time.Second,
		0,
		ioDiscard{}, ioDiscard{},
		nil,
		nil,
	)
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}
	for i, r := range results {
		if r.outcome != "start_enqueued" {
			t.Fatalf("results[%d].outcome = %q, want %q", i, r.outcome, "start_enqueued")
		}
	}

	waitOrTimeout(t, &wg, 5*time.Second)

	times := sp.startTimesSorted()
	if len(times) != n {
		t.Fatalf("recorded %d provider Start calls, want %d", len(times), n)
	}

	// Tolerance is half the configured stagger: generous enough to absorb
	// goroutine-scheduling jitter on a loaded machine, but far above the
	// sub-millisecond gaps the pre-fix code produces, so this threshold
	// cleanly separates "staggered" from "back-to-back."
	const tolerance = stagger / 2
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap < tolerance {
			t.Fatalf("gap between launch %d and %d = %s, want >= %s (stagger configured = %s): spawn goroutines are not being staggered (ga-ih4ma regression)",
				i-1, i, gap, tolerance, stagger)
		}
	}
	totalSpan := times[len(times)-1].Sub(times[0])
	minSpan := time.Duration(n-1) * tolerance
	if totalSpan < minSpan {
		t.Fatalf("total span across %d launches = %s, want >= %s", n, totalSpan, minSpan)
	}
}

// TestEnqueuePreparedStartWaveForCity_ZeroStaggerLaunchesBackToBack is the
// control case: session_spawn_stagger="0s" must reproduce the pre-ga-ih4ma
// behavior exactly (no serialization at all), so operators who explicitly
// disable the stagger are not silently opted into a slower wave. This
// passes both before and after the fix by construction on the old code
// (which never staggers) and by explicit opt-out on the new code; it
// exists to guard against a future change accidentally making stagger=0
// still delay launches.
func TestEnqueuePreparedStartWaveForCity_ZeroStaggerLaunchesBackToBack(t *testing.T) {
	t.Setenv("GC_SESSION_SPAWN_STAGGER", "")

	const n = 5

	store := beads.NewMemStore()
	sp := newSpawnTimingProvider()
	clk := &clock.Fake{Time: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	cfg := &config.City{Daemon: config.DaemonConfig{SessionSpawnStagger: "0s"}}

	var wg sync.WaitGroup
	prepared := buildStaggerTestWave(t, store, n, &wg)

	results := enqueuePreparedStartWaveForCity(
		context.Background(),
		prepared,
		"",
		sp,
		store,
		cfg,
		clk,
		events.Discard,
		5*time.Second,
		0,
		ioDiscard{}, ioDiscard{},
		nil,
		nil,
	)
	if len(results) != n {
		t.Fatalf("len(results) = %d, want %d", len(results), n)
	}

	waitOrTimeout(t, &wg, 5*time.Second)

	times := sp.startTimesSorted()
	if len(times) != n {
		t.Fatalf("recorded %d provider Start calls, want %d", len(times), n)
	}

	// Generous absolute bound (not stagger-relative, since stagger is 0):
	// n goroutine launches plus Fake.Start's own work should complete
	// within a couple hundred milliseconds even under scheduling pressure.
	// The stagger-enabled test above requires >=30ms between EVERY
	// consecutive pair; this test requires the TOTAL span across all n to
	// stay under this bound, which is the sharp behavioral difference
	// between "staggered" and "back-to-back."
	const maxBackToBackSpan = 250 * time.Millisecond
	totalSpan := times[len(times)-1].Sub(times[0])
	if totalSpan > maxBackToBackSpan {
		t.Fatalf("total span across %d back-to-back launches = %s, want < %s (session_spawn_stagger=\"0s\" should disable staggering entirely)",
			n, totalSpan, maxBackToBackSpan)
	}
}
