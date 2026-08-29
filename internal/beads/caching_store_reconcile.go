package beads

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"
)

// cacheLatencyWindowSize is the size of the rolling window of bd-list
// durations the reconciler uses for adaptive cadence decisions. Doubles
// as the hysteresis count for demotion.
//
// Rationale (designer §3 hysteresis): the window is asymmetric — a single
// slow scan can promote (P95 over the high-water mark immediately when
// the window fills), but demotion requires N consecutive calm cycles.
// At MEDIUM cadence (60 s) ten cycles is roughly ten minutes of sustained
// low-latency before we trust the easing.
const cacheLatencyWindowSize = 10

// cacheLatencyHighWaterMark is the P95 threshold above which the
// reconciler asks for MEDIUM cadence. Set to cacheReconcileIntervalSmall/4
// (= 7.5 s) per architect §3.2 — a single bd list call taking more than
// a quarter of the small cadence is evidence of sustained backend
// pressure.
const cacheLatencyHighWaterMark = cacheReconcileIntervalSmall / 4

func (c *CachingStore) reconcileLoop(ctx context.Context, stagger time.Duration) {
	if stagger > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(stagger):
		}
	}

	timer := time.NewTimer(cacheReconcilePollInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if c.nextReconcileDelay(time.Now()) == 0 && c.reconciling.CompareAndSwap(false, true) {
			c.runReconciliation()
			c.reconciling.Store(false)
		}

		next := c.nextReconcileDelay(time.Now())
		if next <= 0 || next > cacheReconcilePollInterval {
			next = cacheReconcilePollInterval
		}
		timer.Reset(next)
	}
}

func (c *CachingStore) adaptiveIntervalLocked() time.Duration {
	return effectiveCadence(len(c.beads), c.latencyDriverActive)
}

// effectiveCadence composes the bead-count cadence and the latency
// cadence. The result is the slower of the two — either input pushing
// to MEDIUM keeps the cadence at MEDIUM. LARGE is only reachable via
// bead count (>=5000) per architect scope.
func effectiveCadence(beadCount int, latencyDriverActive bool) time.Duration {
	bead := beadCountCadence(beadCount)
	latency := cacheReconcileIntervalSmall
	if latencyDriverActive {
		latency = cacheReconcileIntervalMedium
	}
	if latency > bead {
		return latency
	}
	return bead
}

// beadCountCadence returns the cadence demanded by the bead-count input
// alone. Preserved from the original adaptiveIntervalLocked so the
// classification stays in one place.
func beadCountCadence(total int) time.Duration {
	switch {
	case total >= 5000:
		return cacheReconcileIntervalLarge
	case total >= 1000:
		return cacheReconcileIntervalMedium
	default:
		return cacheReconcileIntervalSmall
	}
}

// recordReconcileLatencyLocked appends a bd-list duration sample to the
// rolling latency window, dropping the oldest sample once the window is
// full. Caller must hold c.mu (write lock).
func (c *CachingStore) recordReconcileLatencyLocked(d time.Duration) {
	if len(c.latencyWindow) < cacheLatencyWindowSize {
		c.latencyWindow = append(c.latencyWindow, d)
		return
	}
	c.latencyWindow = append(c.latencyWindow[1:], d)
}

// latencyP95Locked returns the nearest-rank P95 of the latency window
// and reports whether the window contains enough samples to be
// meaningful (full to cacheLatencyWindowSize). Caller must hold c.mu.
//
// Nearest-rank P95 index = ceil(0.95 * N) - 1. For N=10 this equals
// len(sorted)-1 (the max), which is why the prior implementation
// happened to be correct at the current window size — but the formula
// generalizes so the function stays P95 if cacheLatencyWindowSize is
// raised later.
func (c *CachingStore) latencyP95Locked() (time.Duration, bool) {
	if len(c.latencyWindow) < cacheLatencyWindowSize {
		return 0, false
	}
	sorted := make([]time.Duration, len(c.latencyWindow))
	copy(sorted, c.latencyWindow)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	return sorted[idx], true
}

// updateCadenceStatsLocked refreshes the diagnostic cadence fields
// without mutating hysteresis state or emitting transition logs. Caller
// must hold c.mu.
func (c *CachingStore) updateCadenceStatsLocked() {
	p95, samplesEnough := c.latencyP95Locked()
	var p95ms float64
	if samplesEnough {
		p95ms = float64(p95.Milliseconds())
	}
	c.stats.CurrentReconcileInterval = effectiveCadence(len(c.beads), c.latencyDriverActive)
	c.stats.LatencyP95Ms = p95ms
	c.stats.CadenceDriver = cadenceDriver(len(c.beads), c.latencyDriverActive)
}

// recomputeCadenceLocked updates the latency-driver hysteresis state
// based on the current P95, recomposes the effective cadence, refreshes
// the diagnostic CacheStats fields, and emits a single transition log
// line on small↔medium changes. Caller must hold c.mu.
//
// Hysteresis is provided by the rolling window itself: a single slow
// scan can promote (P95 jumps the moment the window fills), but
// demotion requires the window to drain — N=cacheLatencyWindowSize
// low-latency cycles before P95 drops below the high-water mark again.
// One spike anywhere in that drain pushes P95 back up and re-arms the
// driver, preventing thrash.
func (c *CachingStore) recomputeCadenceLocked() {
	prev := c.stats.CurrentReconcileInterval
	hadPrev := prev != 0
	prevDriver := c.stats.CadenceDriver
	if prevDriver == "" {
		prevDriver = cadenceDriver(len(c.beads), c.latencyDriverActive)
	}

	p95, samplesEnough := c.latencyP95Locked()
	if samplesEnough {
		if c.latencyDriverActive {
			if p95 <= cacheLatencyHighWaterMark {
				c.latencyDriverActive = false
			}
		} else if p95 > cacheLatencyHighWaterMark {
			c.latencyDriverActive = true
		}
	}

	c.updateCadenceStatsLocked()
	next := c.stats.CurrentReconcileInterval
	driver := cadenceTransitionDriver(prevDriver, c.stats.CadenceDriver)

	if hadPrev && prev != next {
		switch {
		case prev == cacheReconcileIntervalSmall && next == cacheReconcileIntervalMedium:
			log.Printf("beads cache: cadence promoted small→medium driver=%s p95=%.0fms window=%d",
				driver, c.stats.LatencyP95Ms, cacheLatencyWindowSize)
		case prev == cacheReconcileIntervalMedium && next == cacheReconcileIntervalSmall:
			log.Printf("beads cache: cadence demoted medium→small driver=%s p95=%.0fms window=%d",
				driver, c.stats.LatencyP95Ms, cacheLatencyWindowSize)
		}
	}
}

// cadenceDriver classifies which input(s) are driving the current
// cadence. "default" means cadence is at SMALL with no pressure.
func cadenceDriver(beadCount int, latencyDriverActive bool) string {
	beadDrives := beadCountCadence(beadCount) > cacheReconcileIntervalSmall
	switch {
	case beadDrives && latencyDriverActive:
		return "both"
	case beadDrives:
		return "bead-count"
	case latencyDriverActive:
		return "latency"
	default:
		return "default"
	}
}

func cadenceTransitionDriver(prevDriver, nextDriver string) string {
	switch {
	case prevDriver == "both" || nextDriver == "both":
		return "both"
	case nextDriver != "" && nextDriver != "default":
		return nextDriver
	case prevDriver != "" && prevDriver != "default":
		return prevDriver
	default:
		return "default"
	}
}

func (c *CachingStore) nextReconcileDelay(now time.Time) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.syncFailures >= maxCacheSyncFailures && !c.stats.LastProblemAt.IsZero() {
		dueAt := c.stats.LastProblemAt.Add(cacheReconcileFailureBackoff)
		if !now.Before(dueAt) {
			return 0
		}
		return dueAt.Sub(now)
	}

	if c.state == cacheDegraded {
		return 0
	}

	if c.lastFreshAt.IsZero() {
		return 0
	}

	lastFullScanAt := c.stats.LastReconcileAt
	if lastFullScanAt.IsZero() {
		lastFullScanAt = c.lastFreshAt
	}
	dueAt := lastFullScanAt.Add(c.adaptiveIntervalLocked())
	if !now.Before(dueAt) {
		return 0
	}
	return dueAt.Sub(now)
}

func (c *CachingStore) runReconciliation() {
	start := time.Now()

	c.mu.RLock()
	startSeq := c.mutationSeq
	watermark := c.lastHydrationWatermark
	// Snapshot the cache atomically with startSeq so carry-forward content and
	// the "seq advanced during reconcile" guards agree on the same baseline.
	cachedSnap := make(map[string]Bead, len(c.beads))
	for id, b := range c.beads {
		cachedSnap[id] = cloneBead(b)
	}
	c.mu.RUnlock()

	bdStart := time.Now()
	freshByID, freshWatermark, err := c.gatherReconcileState(watermark, cachedSnap)
	bdLatency := time.Since(bdStart)
	if err != nil {
		c.mu.Lock()
		c.syncFailures++
		if (IsPartialResult(err) || c.syncFailures >= maxCacheSyncFailures) && (c.state == cacheLive || c.state == cachePartial) {
			c.state = cacheDegraded
			// Force a full re-hydrate on recovery: a degraded cache may have
			// drifted, so the incremental watermark can no longer be trusted.
			c.lastHydrationWatermark = time.Time{}
		}
		c.recordProblemLocked("reconcile cache", err)
		c.recordReconcileLatencyLocked(bdLatency)
		c.recomputeCadenceLocked()
		c.updateStatsLocked()
		c.mu.Unlock()
		return
	}

	confirmedClosed := c.recoverMissingFromList(freshByID)

	depMap, depsComplete, depErr := c.fetchDepsForBeads(freshByID)
	if depErr != nil {
		c.recordProblem("refresh dep cache during reconcile", depErr)
	}
	useFreshDeps := depsComplete && depErr == nil

	c.mu.Lock()
	now := time.Now()
	if c.mutationSeq != startSeq {
		var adds, removes, updates int64
		notifications := make([]cacheNotification, 0, len(freshByID))
		nextDepsComplete := useFreshDeps

		for id, freshBead := range freshByID {
			if c.deletedSeq[id] > startSeq || c.beadSeq[id] > startSeq {
				if _, exists := c.beads[id]; exists {
					if _, ok := c.deps[id]; !ok {
						nextDepsComplete = false
					}
				}
				continue
			}
			if _, keep := c.recentLocalBeadConflictLocked(id, freshBead, now, true); keep {
				if _, ok := c.deps[id]; !ok {
					nextDepsComplete = false
				}
				continue
			}
			freshDeps := c.depsForReconcileLocked(id, freshBead, depMap, useFreshDeps)

			old, exists := c.beads[id]
			switch {
			case !exists:
				adds++
				notifications = append(notifications, cacheNotification{
					eventType: "bead.created",
					bead:      cloneBead(freshBead),
				})
			case beadChanged(old, freshBead, true):
				updates++
				notifications = append(notifications, cacheNotification{
					eventType: "bead.updated",
					bead:      cloneBead(freshBead),
				})
			case depsChanged(c.deps[id], freshDeps):
				updates++
				notifications = append(notifications, cacheNotification{
					eventType: "bead.updated",
					bead:      cloneBead(freshBead),
				})
			}

			c.beads[id] = cloneBead(freshBead)
			c.deps[id] = cloneDeps(freshDeps)
			delete(c.dirty, id)
			delete(c.deletedSeq, id)
			if !recentLocalMutation(c.localBeadAt[id], now) {
				delete(c.beadSeq, id)
				delete(c.localBeadAt, id)
			}
		}

		for id, old := range c.beads {
			if _, exists := freshByID[id]; exists {
				continue
			}
			if c.deletedSeq[id] > startSeq || c.beadSeq[id] > startSeq {
				continue
			}
			if old.Status != "closed" && recentLocalMutation(c.localBeadAt[id], now) {
				continue
			}
			removes++
			if old.Status != "closed" {
				closed := cloneBead(old)
				closed.Status = "closed"
				if freshClosed, ok := confirmedClosed[id]; ok {
					closed = cloneBead(freshClosed)
				}
				notifications = append(notifications, cacheNotification{
					eventType: "bead.closed",
					bead:      closed,
				})
			}
			delete(c.beads, id)
			delete(c.deps, id)
			delete(c.dirty, id)
			delete(c.deletedSeq, id)
			delete(c.beadSeq, id)
			delete(c.localBeadAt, id)
		}

		c.syncFailures = 0
		c.depsComplete = nextDepsComplete
		c.primePartialErr = nil
		if c.state == cacheDegraded {
			c.state = cacheLive
		}
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		c.stats.LastReconcileAt = now
		c.lastHydrationWatermark = freshWatermark
		c.stats.LastReconcileMs = durMs
		c.stats.Adds += adds
		c.stats.Removes += removes
		c.stats.Updates += updates
		c.markFreshLocked(now)
		c.recordReconcileLatencyLocked(bdLatency)
		c.recomputeCadenceLocked()
		c.updateStatsLocked()
		logLine, emit := c.reconcileSuccessLogLocked(now, time.Since(start), adds, removes, updates)
		c.mu.Unlock()
		if emit {
			log.Print(logLine)
		}
		c.notifyChanges(notifications)
		return
	}

	var adds, removes, updates int64
	notifications := make([]cacheNotification, 0, len(freshByID))
	nextBeads := make(map[string]Bead, len(freshByID))
	nextDeps := make(map[string][]Dep, len(freshByID))
	nextDirty := make(map[string]struct{})
	nextBeadSeq := make(map[string]uint64)
	nextLocalBeadAt := make(map[string]time.Time)

	for id, freshBead := range freshByID {
		beadForCache := freshBead
		preservedRecentLocal := false
		if current, keep := c.recentLocalBeadConflictLocked(id, freshBead, now, true); keep {
			beadForCache = current
			preservedRecentLocal = true
			c.carryRecentLocalMutationLocked(id, nextDirty, nextBeadSeq, nextLocalBeadAt)
		}
		freshDeps := c.depsForReconcileLocked(id, freshBead, depMap, useFreshDeps)
		nextBeads[id] = cloneBead(beadForCache)
		nextDeps[id] = cloneDeps(freshDeps)

		old, exists := c.beads[id]
		switch {
		case !exists:
			adds++
			notifications = append(notifications, cacheNotification{
				eventType: "bead.created",
				bead:      cloneBead(beadForCache),
			})
		case !preservedRecentLocal && beadChanged(old, freshBead, true):
			updates++
			notifications = append(notifications, cacheNotification{
				eventType: "bead.updated",
				bead:      cloneBead(freshBead),
			})
		case !preservedRecentLocal && depsChanged(c.deps[id], freshDeps):
			updates++
			notifications = append(notifications, cacheNotification{
				eventType: "bead.updated",
				bead:      cloneBead(freshBead),
			})
		}
	}

	for id, old := range c.beads {
		if _, exists := freshByID[id]; !exists {
			if old.Status != "closed" && recentLocalMutation(c.localBeadAt[id], now) {
				nextBeads[id] = cloneBead(old)
				if deps, ok := c.deps[id]; ok {
					nextDeps[id] = cloneDeps(deps)
				}
				c.carryRecentLocalMutationLocked(id, nextDirty, nextBeadSeq, nextLocalBeadAt)
				continue
			}
			removes++
			if old.Status == "closed" {
				continue
			}
			closed := cloneBead(old)
			closed.Status = "closed"
			if freshClosed, ok := confirmedClosed[id]; ok {
				closed = cloneBead(freshClosed)
			}
			notifications = append(notifications, cacheNotification{
				eventType: "bead.closed",
				bead:      closed,
			})
		}
	}

	c.beads = nextBeads
	c.deps = nextDeps
	c.depsComplete = useFreshDeps
	c.dirty = nextDirty
	c.beadSeq = nextBeadSeq
	c.localBeadAt = nextLocalBeadAt
	c.deletedSeq = make(map[string]uint64)
	c.syncFailures = 0
	c.primePartialErr = nil
	if c.state == cacheDegraded {
		c.state = cacheLive
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	c.stats.LastReconcileAt = now
	c.lastHydrationWatermark = freshWatermark
	c.stats.LastReconcileMs = durMs
	c.stats.Adds += adds
	c.stats.Removes += removes
	c.stats.Updates += updates
	c.markFreshLocked(now)
	c.recordReconcileLatencyLocked(bdLatency)
	c.recomputeCadenceLocked()
	c.updateStatsLocked()
	logLine, emit := c.reconcileSuccessLogLocked(now, time.Since(start), adds, removes, updates)
	c.mu.Unlock()
	if emit {
		log.Print(logLine)
	}
	c.notifyChanges(notifications)
}

// reconcileHydrationQuery is the full-content reconcile projection: it skips
// labels and the three large LONGTEXT body columns (design/acceptance_criteria/
// notes) but retains every field gc's Bead and change-detection read. Shared by
// the boot full-hydrate and the incremental hydration scan so their projections
// cannot drift. The scope is non-closed (Status="" + IncludeClosed=false): a
// bead leaving the non-closed set is precisely the close signal, so this must
// never pin Status or set IncludeClosed.
func reconcileHydrationQuery() ListQuery {
	// ga-ftmci: SkipBody drops design/acceptance_criteria/notes from the scan.
	// The reconcile diff (beadChanged) never inspects them and gc's Bead does
	// not carry them, so streaming them was pure CPU/IO waste that drove Dolt
	// to sustained high CPU and stalled reviewer boots.
	return ListQuery{AllowScan: true, SkipLabels: true, SkipBody: true, TierMode: TierBoth}
}

// reconcileListWithRetry runs a reconcile List, retrying once on a bad pooled
// connection.
//
// gc-aov9u: the reconciler runs on the supervisor's long-lived Dolt pool. If an
// idle pooled connection was reaped server-side (Dolt 30s timeout) before the Go
// pool retired it, the first List inherits a dead socket and fails with "invalid
// connection". Retrying once pulls a fresh connection so a single stale socket
// does not flip the cache to degraded.
func (c *CachingStore) reconcileListWithRetry(query ListQuery) ([]Bead, error) {
	fresh, err := c.backing.List(query)
	if err != nil && IsBadConnError(err) {
		fresh, err = c.backing.List(query)
	}
	return fresh, err
}

// gatherReconcileState produces the freshByID map the reconcile diff consumes —
// the COMPLETE set of currently non-closed beads, each with full content — plus
// the DB-clock watermark (max updated_at over that set) for the next cycle.
//
// ga-ftmci incremental hydration. On the first reconcile after boot (zero
// watermark) it runs a single full-content scan, preserving the pre-incremental
// behavior. On later cycles it runs two queries:
//
//  1. a CHEAP COMPLETE scan (id/status/updated_at only — SkipDescription drops
//     the last streamed text column) that yields the exact non-closed ID set.
//     This is the load-bearing invariant: the ID set drives removal/close/delete
//     detection, so it must stay complete. A naive `updated_at > watermark`
//     filter on THIS scan would drop every unchanged open row and synthesize
//     bead.closed for the whole corpus.
//  2. a hydration scan bounded by `updated_at > watermark` that streams full
//     content only for rows changed since the last cycle.
//
// Unchanged rows carry their cached content forward. The ID set is never
// narrowed by the watermark, so no unchanged bead is mistaken for a close.
//
// Hydration selection is per-row (updated_at advanced, or status differs, or the
// row is new) rather than trusting the global watermark alone; any selected row
// the watermark scan does not return — a change on the DATETIME second boundary,
// or a new row — is fetched explicitly via Get so its content is never stale and
// it is never dropped from the complete set.
func (c *CachingStore) gatherReconcileState(watermark time.Time, cachedSnap map[string]Bead) (map[string]Bead, time.Time, error) {
	if watermark.IsZero() {
		fresh, err := c.reconcileListWithRetry(reconcileHydrationQuery())
		if err != nil {
			return nil, time.Time{}, err
		}
		freshByID := make(map[string]Bead, len(fresh))
		var maxUpdatedAt time.Time
		for _, b := range fresh {
			freshByID[b.ID] = cloneBead(b)
			if b.UpdatedAt.After(maxUpdatedAt) {
				maxUpdatedAt = b.UpdatedAt
			}
		}
		return freshByID, maxUpdatedAt, nil
	}

	// Query 1: cheap complete scan. Drives the ID set for close/delete detection.
	// It also carries fresh dependencies (nativeIssueFilterFromListQuery sets
	// IncludeDependencies), which we reuse below so carried-forward rows never
	// keep stale deps — a dep add/remove does NOT bump issues.updated_at (deps
	// live in a separate table), so such rows are never in needHydrate.
	idScanQuery := reconcileHydrationQuery()
	idScanQuery.SkipDescription = true
	idScan, err := c.reconcileListWithRetry(idScanQuery)
	if err != nil {
		return nil, time.Time{}, err
	}

	// watermarkSecond is the second-resolution floor of the watermark. Because
	// issues.updated_at is DATETIME (second resolution), a second content write
	// within the same wall-clock second as the previous frontier does NOT advance
	// updated_at, so an updated_at-only comparison would carry the stale content
	// forward forever. Rows at or after this second are re-hydrated every cycle so
	// a same-second content change is picked up on the next reconcile. The next
	// frontier advances past this second, so the re-hydrated cohort stays small
	// (the rows sharing the single most-recent second).
	watermarkSecond := watermark.Truncate(time.Second)

	idScanByID := make(map[string]Bead, len(idScan))
	needHydrate := make(map[string]struct{})
	var maxUpdatedAt time.Time
	for _, b := range idScan {
		idScanByID[b.ID] = b
		if b.UpdatedAt.After(maxUpdatedAt) {
			maxUpdatedAt = b.UpdatedAt
		}
		cached, ok := cachedSnap[b.ID]
		if !ok || b.UpdatedAt.After(cached.UpdatedAt) || b.Status != cached.Status ||
			!b.UpdatedAt.Before(watermarkSecond) {
			needHydrate[b.ID] = struct{}{}
		}
	}

	// Query 2: hydration scan for changed rows only. Skipped entirely when
	// nothing needs hydrating — the steady-state fast path that eliminates the
	// per-cycle full-content stream. The bound is `watermarkSecond - 1ns` so the
	// predicate is effectively `updated_at >= watermarkSecond`: it RETURNS the
	// same-second boundary cohort (whose second-resolution updated_at equals
	// watermarkSecond and would be excluded by a strict `> watermark`), while
	// still excluding genuinely older settled rows. On the real second-resolution
	// DB this formats (RFC3339) to the previous whole second, i.e. `updated_at >
	// prevSecond`, which is exactly `>= watermarkSecond`.
	hydratedByID := make(map[string]Bead)
	if len(needHydrate) > 0 {
		hydrateQuery := reconcileHydrationQuery()
		hydrateQuery.UpdatedAfter = watermarkSecond.Add(-time.Nanosecond)
		hydrated, herr := c.reconcileListWithRetry(hydrateQuery)
		if herr != nil {
			return nil, time.Time{}, herr
		}
		for _, b := range hydrated {
			if _, open := idScanByID[b.ID]; open {
				hydratedByID[b.ID] = cloneBead(b)
			}
		}
	}

	// Assemble freshByID over the COMPLETE non-closed ID set.
	freshByID := make(map[string]Bead, len(idScanByID))
	var fallbackIDs []string
	for id, scanned := range idScanByID {
		if hb, ok := hydratedByID[id]; ok {
			freshByID[id] = hb
			continue
		}
		if _, changed := needHydrate[id]; !changed {
			if cb, ok := cachedSnap[id]; ok {
				// Carry unchanged content forward, but refresh dependencies from
				// the complete scan: a dep add/remove does not bump updated_at, so
				// the cached bead's dep fields may be stale even though its scalar
				// content is current. Deriving deps from the carried cached bead
				// (the completeness-store path) would miss the change.
				carried := cloneBead(cb)
				carried.Dependencies = cloneDeps(scanned.Dependencies)
				carried.Needs = append([]string(nil), scanned.Needs...)
				freshByID[id] = carried
				continue
			}
		}
		fallbackIDs = append(fallbackIDs, id)
	}

	// A row that needs hydration but the watermark scan did not return (a change
	// or new row exactly on the second-resolution updated_at boundary) is fetched
	// explicitly. It must never be dropped from the complete set — that would
	// spuriously close it.
	for _, id := range fallbackIDs {
		b, gerr := c.backing.Get(id)
		if gerr == nil && b.ID == id {
			freshByID[id] = cloneBead(b)
			continue
		}
		if cb, ok := cachedSnap[id]; ok {
			// Could not confirm fresh content; keep the cached row so the diff
			// does not close a bead the complete scan just reported as open.
			freshByID[id] = cloneBead(cb)
		}
		// else: a brand-new row we could not hydrate this cycle — omit it; the
		// next reconcile re-observes it. Its absence closes nothing (not cached).
	}

	return freshByID, maxUpdatedAt, nil
}

// reconcileSuccessLogLocked composes the per-reconcile success log line
// and returns (line, true) when emission is permitted by the
// cacheReconcileSuccessLogWindow rate limiter, or ("", false) otherwise.
// Updates lastReconcileLogAt on emit. Caller must hold c.mu.
//
// Gap context: runReconciliation previously emitted no log line on
// successful cache refresh. Cadence transitions and errors were logged,
// but a reconciler ticking quietly with stale data produced no operator-
// visible signal. On a T7920 incident 2026-05-26 a stale cache went
// undetected for 2h 31m. This line gives the operator a heartbeat plus
// diff counts and bd-list duration without flooding the log.
func (c *CachingStore) reconcileSuccessLogLocked(now time.Time, elapsed time.Duration, adds, removes, updates int64) (string, bool) {
	if !c.lastReconcileLogAt.IsZero() && now.Sub(c.lastReconcileLogAt) < cacheReconcileSuccessLogWindow {
		return "", false
	}
	c.lastReconcileLogAt = now
	rig := c.idPrefix
	if rig == "" {
		rig = "(no-prefix)"
	}
	cadence := c.stats.CadenceDriver
	if cadence == "" {
		cadence = "default"
	}
	return fmt.Sprintf(
		"beads cache: reconciled rig=%s beads=%d adds=%d updates=%d removes=%d took=%s cadence=%s",
		rig, len(c.beads), adds, updates, removes, elapsed.Round(time.Millisecond), cadence,
	), true
}

func (c *CachingStore) depsForReconcileLocked(id string, freshBead Bead, depMap map[string][]Dep, useFreshDeps bool) []Dep {
	if useFreshDeps {
		return cloneDeps(depMap[id])
	}
	freshDeps := depsFromBeadFields(freshBead)
	if _, ok := c.backing.(*BdStore); ok {
		return freshDeps
	}
	if len(freshDeps) == 0 {
		if cachedDeps, ok := c.deps[id]; ok && len(cachedDeps) > 0 {
			return cloneDeps(cachedDeps)
		}
	}
	return freshDeps
}

// recoverMissingFromList re-fetches any cached active bead that didn't appear
// in freshByID and merges verified-alive ones back. This guards against
// cleanly incomplete List results: a List that drops an active bead must not
// synthesize a spurious bead.closed event for it.
//
// On ErrNotFound the bead is left absent so the diff path can emit
// bead.closed as before. When Get confirms a closed bead, the returned map
// carries that fresh row so the diff path can emit an authoritative close
// payload instead of a stale cached status flip. On any other error the cached
// entry is merged back conservatively, deferring the close to a later scan
// when the backing store's state is unambiguous. Callers must own freshByID
// and not access it concurrently while recovery is running.
func (c *CachingStore) recoverMissingFromList(freshByID map[string]Bead) map[string]Bead {
	c.mu.RLock()
	candidates := make(map[string]Bead)
	for id, b := range c.beads {
		if _, ok := freshByID[id]; ok {
			continue
		}
		if b.Status == "closed" {
			continue
		}
		candidates[id] = cloneBead(b)
	}
	c.mu.RUnlock()
	if len(candidates) == 0 {
		return nil
	}
	var confirmedClosed map[string]Bead
	var recoveredAlive int64
	var deferredClose int64
	for id, cached := range candidates {
		bead, err := c.backing.Get(id)
		switch {
		case err == nil:
			if bead.ID != id {
				c.recordProblem(
					"verify missing bead before close",
					fmt.Errorf("%s: backing returned bead %q", id, bead.ID),
				)
				freshByID[id] = cached
				deferredClose++
				continue
			}
			if bead.Status == "closed" {
				if confirmedClosed == nil {
					confirmedClosed = make(map[string]Bead)
				}
				confirmedClosed[id] = cloneBead(bead)
				continue
			}
			freshByID[id] = cloneBead(bead)
			recoveredAlive++
		case errors.Is(err, ErrNotFound):
			// Confirmed gone; let the diff path emit bead.closed.
		default:
			c.recordProblem(
				"verify missing bead before close",
				fmt.Errorf("%s: %w", id, err),
			)
			freshByID[id] = cached
			deferredClose++
		}
	}
	if recoveredAlive != 0 || deferredClose != 0 {
		c.mu.Lock()
		c.stats.ReconcileRecoveries += recoveredAlive
		c.stats.ReconcileCloseDeferrals += deferredClose
		c.mu.Unlock()
	}
	return confirmedClosed
}
