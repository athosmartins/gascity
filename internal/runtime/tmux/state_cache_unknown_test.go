package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin ga-4ytmas: under heavy load the Darwin `ps` snapshot is
// killed at the fetch timeout, and the supervisor used to read that failure
// as "no session is running" (runtime-missing / orphaned) instead of "I could
// not look". They only use fields and helpers that exist before the fix, so
// they compile — and fail on their assertions — against the old code.

// useKilledPS puts a fake `ps` first on PATH that dies by SIGKILL, which is
// exactly what exec.CommandContext reports as "signal: killed" when the fetch
// deadline fires. It returns a file that gets one line per ps invocation.
func useKilledPS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "ps-calls")
	script := "#!/bin/sh\necho x >> " + calls + "\nkill -9 $$\n"
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ps: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func psCalls(t *testing.T, calls string) int {
	t.Helper()
	data, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read ps calls: %v", err)
	}
	return strings.Count(string(data), "\n")
}

// paneListFetcher is a tmuxFetcher whose tmux server reports one live pane
// whose foreground command ("node") only a process-table walk can attribute
// to the agent.
func paneListFetcher() *tmuxFetcher {
	exec := &fakeExecutor{out: "agent-1\t0\tnode\t4242\n"}
	return &tmuxFetcher{tm: &Tmux{cfg: DefaultConfig(), exec: exec}}
}

// A killed `ps` must not erase the tmux view: the pane list already proved the
// session exists, and IsRunning needs nothing from the process table.
func TestStateCache_ProcessSnapshotKilledStillReportsTmuxSessions(t *testing.T) {
	useKilledPS(t)
	cache := NewStateCache(paneListFetcher(), 10*time.Millisecond)
	cache.staleTTL = 50 * time.Millisecond

	if !cache.IsRunning("agent-1") {
		t.Fatal("first read: agent-1 is in tmux list-panes but IsRunning=false because ps was killed")
	}
	// Outlast staleTTL while every refresh keeps failing to run ps. Before the
	// fix the cache served last-known-good for 30s and then collapsed to
	// "nothing is running" — the runtime-missing path.
	time.Sleep(120 * time.Millisecond)
	if !cache.IsRunning("agent-1") {
		t.Fatal("after staleTTL: agent-1 flipped to not-running although tmux still lists it; a failed ps read was collapsed into 'absent'")
	}
}

// Unknown is not dead: when the process table could not be read, ProcessAlive
// must not report a pane whose command needs the table to attribute as dead.
func TestStateCache_ProcessAliveIsOptimisticWhenProcessTableUnreadable(t *testing.T) {
	useKilledPS(t)
	cache := NewStateCache(paneListFetcher(), 10*time.Millisecond)

	if !cache.ProcessAlive("agent-1", []string{"claude"}) {
		t.Fatal("ProcessAlive=false for a live tmux pane while ps was unreadable; unknown must not read as dead")
	}
}

// A process snapshot that keeps failing must not be re-spawned on every
// refresh: 2213 killed `ps` calls in 33 minutes were load on a host that was
// already the cause of the failures.
func TestTmuxFetcher_FailingProcessSnapshotIsNotRespawnedEveryRefresh(t *testing.T) {
	calls := useKilledPS(t)
	f := paneListFetcher()

	for i := 0; i < 6; i++ {
		if _, err := f.FetchState(context.Background()); err != nil {
			t.Fatalf("FetchState #%d returned an error although tmux list-panes succeeded: %v", i+1, err)
		}
	}
	if got := psCalls(t, calls); got > 1 {
		t.Fatalf("ps spawned %d times in 6 back-to-back fetches; a failing snapshot must back off (want <= 1)", got)
	}
}

// deadlineProbeFetcher records how much time each fetch was given.
type deadlineProbeFetcher struct {
	mu      sync.Mutex
	budgets []time.Duration
	err     error
	partial bool // answer with the pane list but an unreadable process table
}

func (f *deadlineProbeFetcher) FetchState(ctx context.Context) (runtimeStateSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		f.budgets = append(f.budgets, time.Until(dl))
	} else {
		f.budgets = append(f.budgets, 0)
	}
	if f.err != nil {
		return runtimeStateSnapshot{}, f.err
	}
	return runtimeStateSnapshot{
		Sessions:         map[string]sessionRuntimeState{"agent-1": {Running: true}},
		ProcessesUnknown: f.partial,
	}, nil
}

func (f *deadlineProbeFetcher) budget(i int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.budgets) {
		return -1
	}
	return f.budgets[i]
}

// The 3s budget that was killed on every attempt must grow while the host
// stays too slow for it, instead of repeating the same doomed deadline.
func TestStateCache_FetchTimeoutGrowsWhileRefreshesKeepFailing(t *testing.T) {
	f := &deadlineProbeFetcher{err: errors.New("fetching Darwin process args snapshot: signal: killed")}
	cache := NewStateCache(f, time.Millisecond)

	for i := 0; i < 4; i++ {
		cache.IsRunning("agent-1")
		time.Sleep(3 * time.Millisecond) // let the 1ms TTL lapse
	}
	first, last := f.budget(0), f.budget(3)
	if first <= 0 || last <= 0 {
		t.Fatalf("fetcher did not see deadlines: first=%v last=%v", first, last)
	}
	if last <= first+time.Second {
		t.Fatalf("fetch budget did not grow under repeated failure: first=%v fourth=%v", first, last)
	}
}

// A healthy fetch walks the budget back down so one bad minute does not leave
// every later read waiting on a 20s deadline.
func TestStateCache_FetchTimeoutDecaysAfterSuccess(t *testing.T) {
	f := &deadlineProbeFetcher{err: errors.New("signal: killed")}
	cache := NewStateCache(f, time.Millisecond)
	for i := 0; i < 6; i++ {
		cache.IsRunning("agent-1")
		time.Sleep(3 * time.Millisecond)
	}
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	grown := f.budget(5)
	if grown < fetchTimeout+time.Second {
		t.Fatalf("test premise: budget should have grown while failing, got %v", grown)
	}
	for i := 0; i < 8; i++ {
		cache.IsRunning("agent-1")
		time.Sleep(3 * time.Millisecond)
	}
	if after := f.budget(len(f.budgets) - 1); after > fetchTimeout+500*time.Millisecond {
		t.Fatalf("budget stayed inflated after 8 successful fetches: grown=%v after=%v", grown, after)
	}
}

// A refresh that came back with the pane list but no process table is still
// degraded: the budget the next process-table attempt gets must grow, or a
// host that needs 5s for ps never gets the chance to give it.
func TestStateCache_FetchTimeoutGrowsWhileProcessTableStaysUnreadable(t *testing.T) {
	f := &deadlineProbeFetcher{partial: true}
	cache := NewStateCache(f, time.Millisecond)
	for i := 0; i < 4; i++ {
		cache.IsRunning("agent-1")
		time.Sleep(3 * time.Millisecond)
	}
	if first, last := f.budget(0), f.budget(3); last <= first+time.Second {
		t.Fatalf("budget did not grow while the process table stayed unreadable: first=%v fourth=%v", first, last)
	}
}

func TestFetchTimeoutFor(t *testing.T) {
	for _, tc := range []struct {
		streak int
		want   time.Duration
	}{
		{0, 3 * time.Second},
		{1, 6 * time.Second},
		{2, 12 * time.Second},
		{3, 20 * time.Second},
		{maxDegradedStreak, 20 * time.Second},
		{1000, 20 * time.Second},
	} {
		if got := fetchTimeoutFor(tc.streak); got != tc.want {
			t.Errorf("fetchTimeoutFor(%d) = %v, want %v", tc.streak, got, tc.want)
		}
	}
}

func TestProcessRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{5, 80 * time.Second},
		{6, 2 * time.Minute},
		{50, 2 * time.Minute},
	} {
		if got := processRetryDelay(tc.failures); got != tc.want {
			t.Errorf("processRetryDelay(%d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// The process table is left alone while it is in backoff, read again when the
// backoff ends, and a successful read puts every answer back to exact.
func TestTmuxFetcher_ProcessSnapshotBackoffAndRecovery(t *testing.T) {
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	reads := 0
	var readErr error = errors.New("fetching Darwin process args snapshot: signal: killed")
	f := paneListFetcher()
	f.now = clk.now
	f.processes = func(context.Context) (processSnapshot, error) {
		reads++
		if readErr != nil {
			return processSnapshot{}, readErr
		}
		return newProcessSnapshot([]processRuntimeState{{PID: "4242", PPID: "1", Command: "claude", Args: "claude"}}), nil
	}
	fetch := func() runtimeStateSnapshot {
		t.Helper()
		state, err := f.FetchState(context.Background())
		if err != nil {
			t.Fatalf("FetchState: %v", err)
		}
		if !state.Sessions["agent-1"].Running {
			t.Fatalf("agent-1 missing from a snapshot whose pane list succeeded: %+v", state.Sessions)
		}
		return state
	}

	if state := fetch(); !state.ProcessesUnknown || reads != 1 {
		t.Fatalf("first fetch: ProcessesUnknown=%v reads=%d, want true/1", state.ProcessesUnknown, reads)
	}
	clk.advance(processRetryBase - time.Second)
	if state := fetch(); !state.ProcessesUnknown || reads != 1 {
		t.Fatalf("inside backoff: ProcessesUnknown=%v reads=%d, want true/1 (the table must be left alone)", state.ProcessesUnknown, reads)
	}
	clk.advance(2 * time.Second) // past the first backoff; a second failure doubles it
	if state := fetch(); !state.ProcessesUnknown || reads != 2 {
		t.Fatalf("after backoff, still failing: ProcessesUnknown=%v reads=%d, want true/2", state.ProcessesUnknown, reads)
	}
	clk.advance(processRetryBase + time.Second) // 6s: inside the doubled (10s) backoff
	fetch()
	if reads != 2 {
		t.Fatalf("inside the doubled backoff: reads=%d, want 2", reads)
	}

	readErr = nil
	clk.advance(10 * time.Second)
	state := fetch()
	if state.ProcessesUnknown || reads != 3 {
		t.Fatalf("recovery: ProcessesUnknown=%v reads=%d, want false/3", state.ProcessesUnknown, reads)
	}
	if !state.processAlive("agent-1", []string{"claude"}) {
		t.Fatal("recovered snapshot should see the claude process under pane 4242")
	}
	if state.processAlive("agent-1", []string{"codex"}) {
		t.Fatal("recovered snapshot must answer from the process table again: no codex process exists")
	}

	// success cleared the failure count: the next failure backs off 5s, not 40s
	readErr = errors.New("signal: killed")
	fetch()
	clk.advance(processRetryBase + time.Second)
	readErr = nil
	if state := fetch(); state.ProcessesUnknown || reads != 5 {
		t.Fatalf("after a fresh failure the backoff should restart at %v: ProcessesUnknown=%v reads=%d, want false/5", processRetryBase, state.ProcessesUnknown, reads)
	}
}

// "Could not look" only stands in for the process table's answer; it never
// overrides a table that WAS read.
func TestRuntimeStateSnapshot_ProcessAliveUnknownIsOptimisticOnlyWhenUnread(t *testing.T) {
	sessions := map[string]sessionRuntimeState{
		"agent-1": {Running: true, Panes: []paneRuntimeState{{Command: "bash", PID: "100"}}},
	}
	read := runtimeStateSnapshot{
		Sessions:  sessions,
		Processes: newProcessSnapshot([]processRuntimeState{{PID: "100", PPID: "1", Command: "bash"}}),
	}
	if read.processAlive("agent-1", []string{"claude"}) {
		t.Fatal("a process table that was read and shows only an idle shell must still say the agent is dead")
	}
	unread := runtimeStateSnapshot{Sessions: sessions, ProcessesUnknown: true}
	if !unread.processAlive("agent-1", []string{"claude"}) {
		t.Fatal("an unread process table must not be reported as 'agent process is gone'")
	}
	if unread.processAlive("no-such-session", []string{"claude"}) {
		t.Fatal("an unread process table does not make a session tmux does not list alive")
	}
}

// A degraded-but-answered refresh still counts as a fetch: tmux's own answer
// is exact, so last-known-good never ages out and the cache is never uncertain.
func TestStateCache_PartialSnapshotNeverAgesOut(t *testing.T) {
	f := &deadlineProbeFetcher{partial: true}
	cache := NewStateCache(f, 5*time.Millisecond)
	cache.staleTTL = 30 * time.Millisecond
	for i := 0; i < 12; i++ {
		if !cache.IsRunning("agent-1") {
			t.Fatalf("read %d: agent-1 reads as not running although every refresh listed it", i)
		}
		if cache.Uncertain() {
			t.Fatalf("read %d: Uncertain=true although every refresh succeeded", i)
		}
		time.Sleep(8 * time.Millisecond)
	}
}

// Uncertain is the third state: "I could not look". It has to cover a cache
// whose last-known-good aged out while every refresh failed, not only a cache
// that never succeeded — otherwise the start path sees a confirmed-absent
// session and races tmux's own view ("fresh start after stale key: session
// already exists", ga-4ytmas).
func TestStateCache_UncertainOnceLastKnownGoodAgesOutUnderFailure(t *testing.T) {
	f := &mockFetcher{sessions: map[string]bool{"agent-1": true}}
	ttl := 20 * time.Millisecond
	cache := NewStateCache(f, ttl)
	cache.staleTTL = 60 * time.Millisecond

	if !cache.IsRunning("agent-1") {
		t.Fatal("expected agent-1 running initially")
	}
	f.setResult(nil, errors.New("tmux state fetch failed"))
	time.Sleep(ttl + 5*time.Millisecond)
	cache.IsRunning("agent-1") // refresh fails, last-known-good still within staleTTL
	if cache.Uncertain() {
		t.Fatal("Uncertain=true while last-known-good is still being served")
	}

	time.Sleep(80 * time.Millisecond)
	if cache.IsRunning("agent-1") {
		t.Fatal("test premise: after staleTTL the cache reports not-running")
	}
	if !cache.Uncertain() {
		t.Fatal("Uncertain=false although last-known-good aged out and the last refresh failed; callers cannot tell 'absent' from 'could not look'")
	}

	// And it clears the moment a fetch succeeds.
	f.setResult(map[string]bool{"agent-1": true}, nil)
	time.Sleep(ttl + 5*time.Millisecond)
	if !cache.IsRunning("agent-1") || cache.Uncertain() {
		t.Fatalf("expected running and certain after recovery, running=%v uncertain=%v", cache.IsRunning("agent-1"), cache.Uncertain())
	}
}
