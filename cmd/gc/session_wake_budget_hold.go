package main

import (
	"strings"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
)

// wakeBudgetDeferralHold is how long a deferred_by_wake_budget verdict keeps a
// never-started pending create from being rolled back as "lease expired and no
// live runtime".
//
// A pending create that has never started (no last_woke_at) is rolled back after
// pendingCreateNeverStartedTimeout so a dead create does not hold its alias
// forever. Under load, starts take minutes and MaxWakesPerTick caps how many
// begin per tick, so a healthy create can sit in the start queue longer than that
// timeout without ever having been attempted. Rolling it back is the wrong answer:
// the replacement bead gets a fresh CreatedAt, which is the wake-fairness key for a
// never-woken session (wakeFairnessTime), so it joins the BACK of the queue and the
// wait starts over. On 10/10 an always-on crew (oracle-wa, min_active_sessions = 1)
// went through four such beads and stayed without a session for ~30 minutes.
//
// The hold is deliberately short and in memory only. It is renewed every tick the
// session is still deferred, so it lapses minutes after the session stops being
// deferred (started, or no longer a candidate) and a dead create is then rolled
// back exactly as before. A supervisor restart drops it, which only restores the
// old behaviour for one tick.
const wakeBudgetDeferralHold = 5 * time.Minute

// wakeBudgetDeferralPruneEvery bounds the cost of keeping the table small.
const wakeBudgetDeferralPruneEvery = time.Minute

var wakeBudgetDeferrals = struct {
	sync.Mutex
	at         map[string]time.Time
	lastPruned time.Time
}{at: map[string]time.Time{}}

func wakeBudgetClockNow(clk clock.Clock) time.Time {
	if clk != nil {
		return clk.Now()
	}
	return time.Now()
}

// markWakeBudgetDeferred records that sessionName was held back by the per-tick
// wake budget just now.
func markWakeBudgetDeferred(sessionName string, clk clock.Clock) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return
	}
	now := wakeBudgetClockNow(clk)
	wakeBudgetDeferrals.Lock()
	defer wakeBudgetDeferrals.Unlock()
	if now.Sub(wakeBudgetDeferrals.lastPruned) >= wakeBudgetDeferralPruneEvery {
		for name, at := range wakeBudgetDeferrals.at {
			if now.Sub(at) >= wakeBudgetDeferralHold {
				delete(wakeBudgetDeferrals.at, name)
			}
		}
		wakeBudgetDeferrals.lastPruned = now
	}
	wakeBudgetDeferrals.at[sessionName] = now
}

// wakeBudgetHoldsPendingCreate reports whether session is a never-started pending
// create that the wake budget deferred within wakeBudgetDeferralHold, i.e. one
// that is waiting its turn rather than dead.
func wakeBudgetHoldsPendingCreate(session beads.Bead, clk clock.Clock) bool {
	if strings.TrimSpace(session.Metadata["last_woke_at"]) != "" {
		return false
	}
	name := strings.TrimSpace(session.Metadata["session_name"])
	if name == "" {
		return false
	}
	now := wakeBudgetClockNow(clk)
	wakeBudgetDeferrals.Lock()
	defer wakeBudgetDeferrals.Unlock()
	at, ok := wakeBudgetDeferrals.at[name]
	return ok && now.Sub(at) < wakeBudgetDeferralHold
}
