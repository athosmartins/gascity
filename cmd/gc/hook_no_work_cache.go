package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
)

// defaultHookNoWorkCacheTTL bounds how long a "no ready work" gc hook
// result may be reused without re-running the work_query. gc hook runs as
// a fresh subprocess on every check — the controller's own spawn-decision
// polling and a live session's Stop-hook both re-invoke it repeatedly, and
// there is no long-lived process to hold an in-memory cache across those
// invocations (see internal/runtime/tmux/state_cache.go for the in-process
// equivalent, which does NOT help here for that reason).
//
// A single work_query can fire many bd subprocesses — one per candidate
// tier, one per federated rig store for cross-store-eligible agents — each
// opening its own fresh Dolt connection with a ~70ms CPU floor. The
// dominant Dolt CPU cost is poll FREQUENCY times that per-connection
// floor, not query cost (ga-i3rx, memory dolt-cpu-root-poll-freq-not-
// ftmci-layer). A cache hit skips the work_query, and every bd invocation
// inside it, entirely — while never delaying a positive result (see
// wrapWorkQueryRunnerWithNoWorkCache).
//
// Kept short relative to this city's existing daemon intervals (30s+):
// a few seconds of extra latency on a "no work" answer is far below what
// agents already tolerate elsewhere, and the propulsion-principle "no
// waiting" contract is about acting instantly on work that IS found, not
// about the polling cadence used to look for it.
const defaultHookNoWorkCacheTTL = 3 * time.Second

// hookNoWorkCacheTTL is a package-level var (not a const use-site) so tests
// can override it directly, matching the hookWorkQueryTimeout pattern
// already used in cmd_hook.go.
var hookNoWorkCacheTTL = hookNoWorkCacheTTLFromEnv()

// hookNoWorkCacheTTLFromEnv reads GC_HOOK_NO_WORK_CACHE_TTL and parses it
// as a duration, mirroring internal/runtime/tmux/state_cache.go's
// cacheTTLFromEnv so this cache is tunable (including disabled via "0" or
// a negative value) without a rebuild. Returns defaultHookNoWorkCacheTTL
// if the env var is unset, empty, or cannot be parsed.
func hookNoWorkCacheTTLFromEnv() time.Duration {
	v := os.Getenv("GC_HOOK_NO_WORK_CACHE_TTL")
	if v == "" {
		return defaultHookNoWorkCacheTTL
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if strings.TrimSpace(v) == v {
		if ms, err := strconv.Atoi(v); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	log.Printf("gc hook: invalid GC_HOOK_NO_WORK_CACHE_TTL=%q, using default %v", v, defaultHookNoWorkCacheTTL)
	return defaultHookNoWorkCacheTTL
}

// hookNoWorkCacheEntry is the on-disk shape of one cache entry.
type hookNoWorkCacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
}

// hookNoWorkCacheKey derives a stable, filesystem-safe cache key from the
// resolved agent/session/dir identity a hook check ran against.
//
// Session identity is required, not just the work_query text: multiple
// sessions of the same pool template (e.g. gastown.dog-2, gastown.dog-3)
// share an identical work_query string but resolve different actual
// results because the query reads $GC_SESSION_NAME/$GC_SESSION_ID/$GC_ALIAS
// at runtime. Keying on the query text alone would let one session's
// cached "no work" answer stand in for another session entirely — a
// correctness bug (missed dispatch), not just a staleness one. Returns ""
// when there isn't enough identity to key on safely; callers must treat
// that as "skip caching" rather than risk a collision.
func hookNoWorkCacheKey(agent, session, dir string) string {
	agent = strings.TrimSpace(agent)
	session = strings.TrimSpace(session)
	dir = strings.TrimSpace(dir)
	if agent == "" && session == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(agent + "\x1f" + session + "\x1f" + dir))
	return hex.EncodeToString(sum[:16])
}

// hookNoWorkCachePath returns the on-disk path for one cache entry, nested
// under the canonical per-city runtime root (citylayout.RuntimeDataDir) —
// the same root used by every other piece of mutable runtime state in this
// codebase (dolt-state.json, control-dispatcher traces, etc.).
func hookNoWorkCachePath(cityPath, key string) string {
	return filepath.Join(citylayout.RuntimeDataDir(cityPath), "hook-cache", key+".json")
}

// hookNoWorkCacheFresh reports whether a cached "no work" entry exists and
// is still within TTL. Any read or parse failure is treated as a cache
// miss — this cache is a pure optimization and must never become a
// correctness dependency (a corrupt or unreadable file just means the next
// check runs for real, same as a cold cache).
func hookNoWorkCacheFresh(path string) bool {
	if hookNoWorkCacheTTL <= 0 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var entry hookNoWorkCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return false
	}
	return time.Since(entry.CheckedAt) < hookNoWorkCacheTTL
}

// writeHookNoWorkCache records that this key was just checked and had no
// ready work. Best-effort and silent on failure (including a disabled TTL)
// — a cache write must never turn into a hook failure.
func writeHookNoWorkCache(path string) {
	if hookNoWorkCacheTTL <= 0 {
		return
	}
	data, err := json.Marshal(hookNoWorkCacheEntry{CheckedAt: time.Now()})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Write-to-temp-then-rename keeps a concurrent reader from ever
	// observing a partially-written cache file (rename is atomic on the
	// same filesystem, which filepath.Dir(path) guarantees here).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// clearHookNoWorkCache removes a cached "no work" entry. Called whenever a
// real check finds work or errors, so a stale negative can never outlive a
// positive: the next check of any kind runs for real rather than trusting
// an answer that is now known to be wrong.
func clearHookNoWorkCache(path string) {
	_ = os.Remove(path)
}

// wrapWorkQueryRunnerWithNoWorkCache wraps runner with the file-based
// no-work cache described above. A cache hit returns ("", nil) without
// invoking runner at all — doHook already treats an empty runner result as
// "no ready work" (see TestHookNoWork) — so none of the work_query's bd
// subprocesses run on a hit.
//
// Only a negative (no ready work) result is ever cached. Any positive
// result or error clears the entry immediately: a session that actually
// has work, or just hit a real error, is never told "no work" from a
// stale cache, and neither is ever itself deferred by this cache — only
// repeated NEGATIVE answers are ever short-circuited.
func wrapWorkQueryRunnerWithNoWorkCache(cityPath, agent, session, dir string, runner WorkQueryRunner) WorkQueryRunner {
	key := hookNoWorkCacheKey(agent, session, dir)
	if key == "" {
		return runner
	}
	path := hookNoWorkCachePath(cityPath, key)
	return func(command, runDir string) (string, error) {
		if hookNoWorkCacheFresh(path) {
			return "", nil
		}
		out, err := runner(command, runDir)
		if err != nil {
			clearHookNoWorkCache(path)
			return out, err
		}
		ready := filterUnreadyHookCandidates(normalizeWorkQueryOutput(strings.TrimSpace(out)), time.Now())
		if workQueryHasReadyWork(ready) {
			clearHookNoWorkCache(path)
		} else {
			writeHookNoWorkCache(path)
		}
		return out, nil
	}
}
