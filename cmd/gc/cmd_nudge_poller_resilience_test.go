package main

// Resilience of the legacy per-session `gc nudge poll` process (ga-55jx16q).
//
// Measured on the live city (31/10): a crew with a wait-idle nudge PENDING for
// hours had NO poller process. The poller had been spawned 5 s after the
// enqueue and then died. The historical poller logs hold its last words:
//
//	gc nudge poll: getting session: getting bead "...": [mysql] packets.go:58 unexpected EOF
//	gc nudge poll: getting session: ... dolt circuit breaker is open ... failing fast
//
// i.e. ONE failed read (a Dolt blip) was fatal, and nothing re-armed the poller.
// A read that failed is a third state ("unknown"), never "the session is gone"
// and never "the queue is empty". These tests pin that rule for every read the
// poller depends on.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

var errPollBlip = errors.New(`getting bead "ga-x": exit status 1: Error: failed to open database: dolt circuit breaker is open: server appears down, failing fast (cooldown 5s)`)

// breakNudgeQueueState makes the persisted queue unreadable (not empty).
func breakNudgeQueueState(t *testing.T, dir string) {
	t.Helper()
	statePath := nudgequeue.StatePath(dir)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(statePath, []byte("{not-valid-json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func queueOneNudge(t *testing.T, dir string, now time.Time) {
	t.Helper()
	item := newQueuedNudgeWithOptions("worker", "queued follow-up", "session", now.Add(-time.Minute), queuedNudgeOptions{
		ID:        "n-resilience",
		SessionID: "gc-1",
	})
	if err := enqueueQueuedNudge(dir, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
}

func TestDecideNudgePollRound(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	now := time.Now()

	newTarget := func(dir string) nudgeTarget {
		return nudgeTarget{cityPath: dir, agent: config.Agent{Name: "worker"}, sessionID: "gc-1"}
	}
	idle := now.Add(-time.Minute)
	running := worker.LiveObservation{Running: true, LastActivity: &idle}

	t.Run("observe failed with items queued: wait, never exit", func(t *testing.T) {
		dir := t.TempDir()
		queueOneNudge(t, dir, now)
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{}, errPollBlip, time.Time{}, time.Time{}, now)
		if d.action != nudgePollWait {
			t.Fatalf("action = %v, want wait: a failed observation is unknown, not fatal", d.action)
		}
		if d.unknownSince.IsZero() {
			t.Fatal("unknownSince not stamped on the first failed round")
		}
	})

	t.Run("observe failed and queue VERIFIED empty: exit 0", func(t *testing.T) {
		dir := t.TempDir()
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{}, errPollBlip, time.Time{}, time.Time{}, now)
		if d.action != nudgePollExit || d.exitCode != 0 {
			t.Fatalf("decision = %+v, want exit 0: nothing queued means nothing to lose", d)
		}
		if d.reason == "" {
			t.Fatal("exit decision carries no reason to log")
		}
	})

	t.Run("observe failed and queue UNREADABLE: keep waiting inside the ceiling", func(t *testing.T) {
		dir := t.TempDir()
		breakNudgeQueueState(t, dir)
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{}, errPollBlip, time.Time{}, now.Add(-time.Minute), now)
		if d.action != nudgePollWait {
			t.Fatalf("action = %v, want wait: unreadable queue is not an empty queue", d.action)
		}
		if !d.unknownSince.Equal(now.Add(-time.Minute)) {
			t.Fatalf("unknownSince = %v, want the streak start preserved", d.unknownSince)
		}
	})

	t.Run("observe failed and queue UNREADABLE past the ceiling: loud exit 1", func(t *testing.T) {
		dir := t.TempDir()
		breakNudgeQueueState(t, dir)
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{}, errPollBlip, time.Time{}, now.Add(-defaultNudgePollUnknownCeiling-time.Second), now)
		if d.action != nudgePollExit || d.exitCode != 1 {
			t.Fatalf("decision = %+v, want exit 1 once nothing could be proven for the whole ceiling", d)
		}
		if d.reason == "" {
			t.Fatal("exit 1 carries no reason to log")
		}
	})

	t.Run("session observed running: deliver and clear both streaks", func(t *testing.T) {
		dir := t.TempDir()
		queueOneNudge(t, dir, now)
		d := decideNudgePollRound(newTarget(dir), running, nil, now.Add(-time.Minute), now.Add(-time.Minute), now)
		if d.action != nudgePollDeliver {
			t.Fatalf("action = %v, want deliver", d.action)
		}
		if !d.missingSince.IsZero() || !d.unknownSince.IsZero() {
			t.Fatalf("streaks not cleared: %+v", d)
		}
	})

	t.Run("session not running with items queued: wait inside the start grace", func(t *testing.T) {
		dir := t.TempDir()
		queueOneNudge(t, dir, now)
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{Running: false}, nil, time.Time{}, time.Time{}, now)
		if d.action != nudgePollWait || d.missingSince.IsZero() {
			t.Fatalf("decision = %+v, want wait with missingSince stamped", d)
		}
	})

	t.Run("session not running, grace over: exit 0", func(t *testing.T) {
		dir := t.TempDir()
		queueOneNudge(t, dir, now)
		d := decideNudgePollRound(newTarget(dir), worker.LiveObservation{Running: false}, nil, now.Add(-defaultNudgePollStartGrace-time.Second), time.Time{}, now)
		if d.action != nudgePollExit || d.exitCode != 0 {
			t.Fatalf("decision = %+v, want exit 0", d)
		}
	})
}

func TestShouldKeepNudgePollerAliveKeepsRunningWhenQueueUnreadable(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	breakNudgeQueueState(t, dir)
	now := time.Now()
	target := nudgeTarget{cityPath: dir, agent: config.Agent{Name: "worker"}, sessionID: "gc-1"}

	if !shouldKeepNudgePollerAlive(target, time.Time{}, now) {
		t.Fatal("shouldKeepNudgePollerAlive = false on an unreadable queue: that is 'unknown', and unknown must not end the poller")
	}
	if !shouldKeepNudgePollerAlive(target, now.Add(-defaultNudgePollStartGrace/2), now) {
		t.Fatal("shouldKeepNudgePollerAlive = false inside the grace with an unreadable queue")
	}
	if shouldKeepNudgePollerAlive(target, now.Add(-defaultNudgePollStartGrace-time.Second), now) {
		t.Fatal("shouldKeepNudgePollerAlive = true after the grace: the session has been gone too long to wait on")
	}
}

func TestShouldKeepNudgePollerAliveStopsOnVerifiedEmptyQueue(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	target := nudgeTarget{cityPath: dir, agent: config.Agent{Name: "worker"}, sessionID: "gc-1"}
	if shouldKeepNudgePollerAlive(target, time.Time{}, time.Now()) {
		t.Fatal("shouldKeepNudgePollerAlive = true with a verified-empty queue, want false")
	}
}

func TestNudgePollErrorBackoff(t *testing.T) {
	interval := 2 * time.Second
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
		{4, defaultNudgePollErrorBackoffMax},
		{10, defaultNudgePollErrorBackoffMax},
		{500, defaultNudgePollErrorBackoffMax},
	}
	for _, tc := range cases {
		if got := nudgePollErrorBackoff(interval, tc.streak); got != tc.want {
			t.Errorf("nudgePollErrorBackoff(%v, %d) = %v, want %v", interval, tc.streak, got, tc.want)
		}
	}
	if got := nudgePollErrorBackoff(time.Minute, 1); got != time.Minute {
		t.Errorf("a configured interval above the cap must be respected, got %v", got)
	}
}

func stubNudgePollSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	prev := nudgePollSleep
	nudgePollSleep = func(d time.Duration) { slept = append(slept, d) }
	t.Cleanup(func() { nudgePollSleep = prev })
	return &slept
}

func TestRunNudgePollLoopSurvivesTransientObserveErrorsAndStillDelivers(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "review the deploy logs", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store, fake, nil)
	info, err := mgr.Create(context.Background(), "worker", "Worker", "codex", dir, "codex", nil, session.ProviderResume{}, runtime.Config{WorkDir: dir})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Start(context.Background(), info.ID, "", runtime.Config{WorkDir: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	idleSince := time.Now().Add(-10 * time.Second)
	fake.Activity = map[string]time.Time{info.SessionName: idleSince}
	target := nudgeTarget{
		cityPath:    dir,
		agent:       config.Agent{Name: "worker"},
		sessionID:   info.ID,
		resolved:    &config.ResolvedProvider{Name: "codex"},
		sessionName: info.SessionName,
	}

	calls := 0
	prevObserve := nudgeObserveTarget
	nudgeObserveTarget = func(nudgeTarget, beads.Store, runtime.Provider) (worker.LiveObservation, error) {
		calls++
		switch calls {
		case 1, 2: // two Dolt blips in a row, exactly what killed the real poller
			return worker.LiveObservation{}, errPollBlip
		case 3:
			return worker.LiveObservation{Running: true, LastActivity: &idleSince}, nil
		default: // the session ends; the queue is empty by now
			return worker.LiveObservation{Running: false}, nil
		}
	}
	t.Cleanup(func() { nudgeObserveTarget = prevObserve })
	slept := stubNudgePollSleep(t)

	var stderr bytes.Buffer
	rc := runNudgePollLoop(target, store, fake, 2*time.Second, 3*time.Second, &stderr)

	if rc != 0 {
		t.Fatalf("runNudgePollLoop rc = %d (stderr %q), want 0: a transient observe error must not end the poller", rc, stderr.String())
	}
	if calls != 4 {
		t.Fatalf("observe calls = %d, want 4 (2 blips, deliver round, session gone): the loop must have kept going after the first error", calls)
	}
	nudges := 0
	for _, call := range fake.Calls {
		if call.Method == "Nudge" {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("nudge calls = %d, want 1: the queued reminder must still reach the session after the blips", nudges)
	}
	pending, inFlight, _, err := listQueuedNudges(dir, "worker", time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 0 || len(inFlight) != 0 {
		t.Fatalf("queue not drained: pending=%d inFlight=%d", len(pending), len(inFlight))
	}
	if len(*slept) < 2 || (*slept)[0] <= 2*time.Second {
		t.Fatalf("sleeps = %v, want the first wait after a failed round to back off beyond the 2s interval", *slept)
	}
	out := stderr.String()
	if got := strings.Count(out, "will retry"); got != 1 {
		t.Fatalf("'will retry' lines = %d, want exactly 1 per streak (no log spam). stderr:\n%s", got, out)
	}
	if !strings.Contains(out, "recovered after 2") {
		t.Fatalf("stderr lacks the 'recovered after 2' line:\n%s", out)
	}
	if !strings.Contains(out, "circuit breaker") {
		t.Fatalf("stderr does not carry the underlying error:\n%s", out)
	}
}

func TestRunNudgePollLoopWithUnreadableQueueAndFailingObserveGivesUpLoudly(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	breakNudgeQueueState(t, dir)
	target := nudgeTarget{cityPath: dir, agent: config.Agent{Name: "worker"}, sessionID: "gc-1", sessionName: "worker-1"}

	prevObserve := nudgeObserveTarget
	nudgeObserveTarget = func(nudgeTarget, beads.Store, runtime.Provider) (worker.LiveObservation, error) {
		return worker.LiveObservation{}, errPollBlip
	}
	t.Cleanup(func() { nudgeObserveTarget = prevObserve })

	// A fake clock: each sleep advances it, so the ceiling is reached without waiting.
	clock := time.Now()
	prevNow, prevSleep := nudgePollNow, nudgePollSleep
	nudgePollNow = func() time.Time { return clock }
	nudgePollSleep = func(d time.Duration) { clock = clock.Add(d) }
	t.Cleanup(func() { nudgePollNow, nudgePollSleep = prevNow, prevSleep })

	var stderr bytes.Buffer
	rc := runNudgePollLoop(target, nil, runtime.NewFake(), 2*time.Second, 3*time.Second, &stderr)
	if rc != 1 {
		t.Fatalf("rc = %d, want 1 once nothing could be observed or read for the whole ceiling", rc)
	}
	if !strings.Contains(stderr.String(), "giving up") {
		t.Fatalf("the give-up is not logged (the original death left no trace):\n%s", stderr.String())
	}
}

func TestRetryNudgePollStartup(t *testing.T) {
	t.Run("recovers after transient failures", func(t *testing.T) {
		slept := stubNudgePollSleep(t)
		n := 0
		var stderr bytes.Buffer
		err := retryNudgePollStartup("resolving the session", &stderr, func() error {
			n++
			if n < 3 {
				return errPollBlip
			}
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil after 2 transient failures", err)
		}
		if n != 3 {
			t.Fatalf("attempts = %d, want 3", n)
		}
		want := []time.Duration{time.Second, 2 * time.Second}
		if len(*slept) != len(want) || (*slept)[0] != want[0] || (*slept)[1] != want[1] {
			t.Fatalf("sleeps = %v, want %v", *slept, want)
		}
	})
	t.Run("gives up after the bounded attempts and says so", func(t *testing.T) {
		slept := stubNudgePollSleep(t)
		n := 0
		var stderr bytes.Buffer
		err := retryNudgePollStartup("resolving the session", &stderr, func() error {
			n++
			return errPollBlip
		})
		if err == nil || !errors.Is(err, errPollBlip) {
			t.Fatalf("err = %v, want the last underlying error", err)
		}
		if n != defaultNudgePollStartupAttempts {
			t.Fatalf("attempts = %d, want %d", n, defaultNudgePollStartupAttempts)
		}
		if len(*slept) != defaultNudgePollStartupAttempts-1 {
			t.Fatalf("sleeps = %d, want %d (no sleep after the last attempt)", len(*slept), defaultNudgePollStartupAttempts-1)
		}
	})
}

// ROOT CAUSE of the stuck wait-idle nudges (ga-55jx16q), reproduced with the real
// binary on 31/10: a poller started with exec.Cmd{Stdout/Stderr: io.Discard} is NOT
// on /dev/null. os/exec turns any non-*os.File writer into a pipe whose read end
// lives in the PARENT. `gc session nudge` is a short-lived CLI, so the moment it
// exits the poller's stdout/stderr are dead pipes, and the Go runtime kills a
// process on its first write to a broken fd 1 or 2 (SIGPIPE, rc -13 after 1.2 s;
// every gc start prints a pack-drift warning to stderr). It is a race between the
// spawner's lifetime and the poller's first write: a CLI that exits within a
// second loses it (the stranded crew nudges); a poller whose spawner lingered
// survives until its next write, and is a latent time bomb (a live one, parent
// launchd, fd 1/2 = PIPE, was seen 1 h 44 m old). The child's stdio must therefore
// be real files.
func TestEnsureNudgePollerGivesThePollerRealFilesNotParentOwnedPipes(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-gc")
	// The script reports what kind of file its own fd 1 and fd 2 are.
	body := "#!/bin/sh\n" +
		"if [ -p /dev/stderr ]; then echo 'stderr=pipe' >&2; else echo 'stderr=file' >&2; fi\n" +
		"if [ -p /dev/stdout ]; then echo 'stdout=pipe'; else echo 'stdout=file'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prevExe := nudgePollerExecutable
	nudgePollerExecutable = func() (string, error) { return script, nil }
	t.Cleanup(func() { nudgePollerExecutable = prevExe })

	if err := ensureNudgePoller(dir, "worker", "worker-1"); err != nil {
		t.Fatalf("ensureNudgePoller: %v", err)
	}

	logPath := nudgePollerLogPath(dir, "worker-1", "worker")
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil {
			got = string(data)
			if strings.Contains(got, "stderr=file") && strings.Contains(got, "stdout=file") {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("poller stdio is not a real file or never reached %s (got %q): a pipe owned by the exiting CLI kills the poller with SIGPIPE, and a poller that dies must leave its last words", logPath, got)
}

// If the log file cannot be opened the poller must still start, on /dev/null: a
// nil writer makes os/exec open the null device as a real fd. io.Discard would
// bring the pipes (and the SIGPIPE death) back.
func TestEnsureNudgePollerFallsBackToNullDeviceWhenLogCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-gc")
	marker := filepath.Join(t.TempDir(), "marker")
	body := "#!/bin/sh\n" +
		"if [ -p /dev/stderr ] || [ -p /dev/stdout ]; then echo pipe > '" + marker + "'; else echo file > '" + marker + "'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	prevExe := nudgePollerExecutable
	nudgePollerExecutable = func() (string, error) { return script, nil }
	t.Cleanup(func() { nudgePollerExecutable = prevExe })

	// Make the log path a directory so OpenFile(O_WRONLY) fails.
	if err := os.MkdirAll(nudgePollerLogPath(dir, "worker-2", "worker"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := ensureNudgePoller(dir, "worker", "worker-2"); err != nil {
		t.Fatalf("ensureNudgePoller must still start the poller when only the log is unavailable: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && len(data) > 0 {
			if got := strings.TrimSpace(string(data)); got != "file" {
				t.Fatalf("poller stdio kind = %q, want file (null device): never io.Discard", got)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("poller never ran")
}
