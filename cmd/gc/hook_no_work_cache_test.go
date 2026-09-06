package main

import (
	"fmt"
	"testing"
	"time"
)

func TestHookNoWorkCacheKeyEmptyWhenNoIdentity(t *testing.T) {
	if got := hookNoWorkCacheKey("", "", "/some/dir"); got != "" {
		t.Fatalf("hookNoWorkCacheKey(no agent/session) = %q, want empty (caching must be skipped, not collide)", got)
	}
}

func TestHookNoWorkCacheKeyNonEmptyWithAgentOnly(t *testing.T) {
	if got := hookNoWorkCacheKey("gastown.dog", "", ""); got == "" {
		t.Fatal("hookNoWorkCacheKey(agent only) = empty, want non-empty key")
	}
}

// TestHookNoWorkCacheKeyDiffersPerSession guards the critical safety
// property: two sessions of the same pool template (e.g. gastown.dog-2,
// gastown.dog-3) share an identical work_query string and agent identity
// but must never share a cache entry, because each resolves a different
// actual answer from its own $GC_SESSION_NAME. A collision here would let
// one session's "no work" answer for another session — a missed-dispatch
// bug, not just staleness.
func TestHookNoWorkCacheKeyDiffersPerSession(t *testing.T) {
	k1 := hookNoWorkCacheKey("gastown.dog", "gastown.dog-2", "/work")
	k2 := hookNoWorkCacheKey("gastown.dog", "gastown.dog-3", "/work")
	if k1 == "" || k2 == "" {
		t.Fatalf("expected non-empty keys, got %q and %q", k1, k2)
	}
	if k1 == k2 {
		t.Fatalf("hookNoWorkCacheKey collided across sessions of the same agent template: %q == %q", k1, k2)
	}
}

func TestHookNoWorkCacheKeyDiffersPerDir(t *testing.T) {
	k1 := hookNoWorkCacheKey("worker", "worker-1", "/rig/a")
	k2 := hookNoWorkCacheKey("worker", "worker-1", "/rig/b")
	if k1 == k2 {
		t.Fatalf("hookNoWorkCacheKey collided across different work dirs: %q == %q", k1, k2)
	}
}

func TestHookNoWorkCacheKeyStableForSameInputs(t *testing.T) {
	k1 := hookNoWorkCacheKey("worker", "worker-1", "/rig/a")
	k2 := hookNoWorkCacheKey("worker", "worker-1", "/rig/a")
	if k1 != k2 {
		t.Fatalf("hookNoWorkCacheKey not deterministic: %q != %q", k1, k2)
	}
}

// TestWrapWorkQueryRunnerWithNoWorkCacheSkipsRepeatedNegative is the
// ga-i3rx regression test: before this change, every gc hook invocation
// re-ran the full work_query (and every bd subprocess inside it)
// regardless of how recently the same agent/session/dir had already come
// back empty. This fails against the pre-fix code because the bare
// runner has no memory between calls — calls is 2 after two invocations,
// not 1.
func TestWrapWorkQueryRunnerWithNoWorkCacheSkipsRepeatedNegative(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = time.Minute
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return "", nil // no work
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	out1, err1 := wrapped("bd ready", "/work")
	if err1 != nil || out1 != "" {
		t.Fatalf("first call = (%q, %v), want (\"\", nil)", out1, err1)
	}
	if calls != 1 {
		t.Fatalf("calls after first invocation = %d, want 1", calls)
	}

	out2, err2 := wrapped("bd ready", "/work")
	if err2 != nil || out2 != "" {
		t.Fatalf("second call = (%q, %v), want (\"\", nil)", out2, err2)
	}
	if calls != 1 {
		t.Fatalf("calls after second invocation = %d, want 1 (cache hit must not invoke the underlying runner)", calls)
	}
}

// TestWrapWorkQueryRunnerWithNoWorkCacheExpiresAfterTTL guards against the
// cache becoming a correctness hazard: once the TTL elapses, the next
// check must run for real again rather than trusting a negative forever.
func TestWrapWorkQueryRunnerWithNoWorkCacheExpiresAfterTTL(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = 20 * time.Millisecond
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return "", nil
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	if _, err := wrapped("bd ready", "/work"); err != nil {
		t.Fatalf("first call error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after first invocation = %d, want 1", calls)
	}

	time.Sleep(60 * time.Millisecond)

	if _, err := wrapped("bd ready", "/work"); err != nil {
		t.Fatalf("post-TTL call error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls after TTL expiry = %d, want 2 (stale cache must not be trusted)", calls)
	}
}

// TestWrapWorkQueryRunnerWithNoWorkCacheNeverDelaysPositiveResult is the
// core safety property: a session that actually has ready work must never
// be told "no work" by this cache, even if an earlier check (moments ago,
// well within TTL) also found work. Only negative results are ever cached.
func TestWrapWorkQueryRunnerWithNoWorkCacheNeverDelaysPositiveResult(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = time.Minute
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return `[{"id":"hw-1"}]`, nil
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	for i := 1; i <= 3; i++ {
		out, err := wrapped("bd ready", "/work")
		if err != nil {
			t.Fatalf("call %d error = %v", i, err)
		}
		if out != `[{"id":"hw-1"}]` {
			t.Fatalf("call %d output = %q, want real work every time", i, out)
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (a positive result must never be served from cache)", calls)
	}
}

// TestWrapWorkQueryRunnerWithNoWorkCacheNeverCachesError guards the same
// property for errors: a transient failure must never be memoized as "no
// work" (or as anything cacheable at all), so every call re-checks.
func TestWrapWorkQueryRunnerWithNoWorkCacheNeverCachesError(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = time.Minute
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return "", fmt.Errorf("transient store error")
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	for i := 1; i <= 2; i++ {
		if _, err := wrapped("bd ready", "/work"); err == nil {
			t.Fatalf("call %d error = nil, want transient store error", i)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (an error must never be cached)", calls)
	}
}

// TestWrapWorkQueryRunnerWithNoWorkCacheRecoversAfterErrorThenWork checks
// the transition sequence a real session can hit: error, then a real
// negative, then a real positive — each must be reflected immediately, and
// only the negative may ever be skipped on a repeat.
func TestWrapWorkQueryRunnerWithNoWorkCacheRecoversAfterErrorThenWork(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = time.Minute
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	step := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		step++
		switch step {
		case 1:
			return "", fmt.Errorf("transient")
		case 2, 3:
			return "", nil // negative, then a cache-eligible repeat
		default:
			return `[{"id":"hw-1"}]`, nil // now real work must never be hidden
		}
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	if _, err := wrapped("bd ready", "/work"); err == nil {
		t.Fatal("step 1: want error surfaced")
	}
	if step != 1 {
		t.Fatalf("step after call 1 = %d, want 1", step)
	}

	if out, err := wrapped("bd ready", "/work"); err != nil || out != "" {
		t.Fatalf("step 2: got (%q, %v), want (\"\", nil)", out, err)
	}
	if step != 2 {
		t.Fatalf("step after call 2 = %d, want 2", step)
	}

	// Repeat within TTL: must be served from cache, underlying runner not called.
	if out, err := wrapped("bd ready", "/work"); err != nil || out != "" {
		t.Fatalf("step 3 (cache hit): got (%q, %v), want (\"\", nil)", out, err)
	}
	if step != 2 {
		t.Fatalf("step after cache-hit call = %d, want still 2 (underlying runner must not have been invoked)", step)
	}
}

func TestWrapWorkQueryRunnerWithNoWorkCacheNoIdentityAlwaysDelegates(t *testing.T) {
	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return "", nil
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "", "", "/work", base)

	for i := 0; i < 2; i++ {
		if _, err := wrapped("bd ready", "/work"); err != nil {
			t.Fatalf("call %d error = %v", i, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (no agent/session identity means caching must be skipped entirely)", calls)
	}
}

func TestHookNoWorkCacheDisabledByNonPositiveTTL(t *testing.T) {
	oldTTL := hookNoWorkCacheTTL
	hookNoWorkCacheTTL = 0
	t.Cleanup(func() { hookNoWorkCacheTTL = oldTTL })

	cityPath := t.TempDir()
	calls := 0
	base := WorkQueryRunner(func(string, string) (string, error) {
		calls++
		return "", nil
	})
	wrapped := wrapWorkQueryRunnerWithNoWorkCache(cityPath, "worker", "worker-1", "/work", base)

	for i := 0; i < 2; i++ {
		if _, err := wrapped("bd ready", "/work"); err != nil {
			t.Fatalf("call %d error = %v", i, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (TTL<=0 must disable caching, e.g. as an emergency kill switch)", calls)
	}
}
