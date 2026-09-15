package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/mail"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// ga-h9xpwb: a mail message bead assigned to a session's identity kept the
// drain-ack close gate from ever reporting "no assigned work", because reading
// a message never transitions its status — mail.read is a metadata flag, not a
// status close. Inbox/hook listings only surface UNREAD mail, so nothing in the
// session's own loop can archive a message once it has been read. A session
// that read a message and then drained could therefore never pass the close
// gate: the pool controller respawned it onto the same stale, already-read
// message every wake cycle — 99 wake/drain cycles over 3 days on one worker.
//
// Fix: hasNonSessionNonOwnDrainStepWork (the close-gate-only work filter) now
// also skips issue_type="message" beads, via the same hookClaimCandidateIsMessage
// predicate hook-claim already uses to keep mail out of assignment paths.
//
// STATUS (2026-09-15, disk-pressure incident mid-verification, resume after
// 08:00): whether this fix is still NEEDED on the current upstream/main tip is
// an open question — see the WIP note on TestDrainAckCloseGateIgnoresOpenReadMessage
// below. Do not treat this file as a finished, verified test suite until that
// note is resolved and `go test ./cmd/gc/... -run '(CloseGate|AwakeChain).*Message'`
// has actually been run green.

func closeGateMessageTestConfig() *config.City {
	return &config.City{Agents: []config.Agent{{Name: "worker"}}}
}

func closeGateMessageTestSessionInfo(t *testing.T, sessionName string) sessionpkg.Info {
	t.Helper()
	return sessiontest.InfoFromMeta(t, map[string]string{
		"session_name": sessionName,
		"template":     "worker",
	})
}

// mustCreateOpenMessage seeds an open, ephemeral mail message assigned to
// assignee — the wisp-tier shape a real mail bead has in production (ga-wisp-*
// ids live in hq.wisps). read controls whether mail.read is stamped: the fix
// must treat both states identically, since the awake-work chain that wakes a
// sleeping session for genuinely NEW mail (sessionHasAwakeAssignedWorkForReachableStore)
// is a structurally separate call chain that never reaches the close gate's
// message exclusion (see TestAwakeChainNeverCountsMailEitherWay below).
func mustCreateOpenMessage(t *testing.T, store beads.Store, assignee string, read bool) beads.Bead {
	t.Helper()
	metadata := map[string]string{}
	if read {
		metadata[mail.ReadMetadataKey] = "true"
	}
	msg, err := store.Create(beads.Bead{
		Title:     "mail message",
		Type:      "message",
		Assignee:  assignee,
		Ephemeral: true,
		Metadata:  metadata,
	})
	if err != nil {
		t.Fatalf("create message bead: %v", err)
	}
	return msg
}

// TestDrainAckCloseGateIgnoresOpenReadMessage is the test that must FAIL before
// the fix and PASS after it: a session draining with only an open, already-read
// message assigned to it must be allowed to close.
//
// INCOMPLETE — WIP, left mid-investigation (ga-h9xpwb, engine build/test
// suspended by the Mayor for a disk-pressure incident, resume after 08:00):
// PR #4831 ("fix(session): exclude mail message beads from work-release
// enumeration", ra-59207) landed on upstream/main AFTER the e21a3e5a3 baseline
// this bug was originally diagnosed against, and it already added an
// unconditional issue_type="message" filter (excludeMailMessageBeads) inside
// wa.OpenAssignedTo — the same function this test's close-gate call goes
// through. TestDrainAckCloseGateIgnoresOpenUnreadMessage below independently
// confirms (verified TWICE in isolation, on the unpatched tree) that an
// UNREAD message already returns closeGate=false with NO fix applied. It is
// therefore an open question whether this READ case is still actually broken
// on the current tip, or whether #4831 already fixed it too and ga-h9xpwb is
// stale. The one run of this exact test against the unpatched tree that got
// far enough to matter aborted on an assertion I had added myself (asserting
// sessionHasOpenAssignedWorkForConfigInfo still returns true for a read
// message) — NOT on the closeGate assertion below — meaning I do not yet know
// this test's real pre-fix closeGate value. A follow-up run (with go test
// available again) that logs both values before asserting is required before
// trusting this test either way. Do not delete this test or "fix" the
// assertion by guessing; run it and read the log first.
func TestDrainAckCloseGateIgnoresOpenReadMessage(t *testing.T) {
	const sessionName = "wa-worker-adhoc-test-read"
	store := beads.NewMemStore()
	info := closeGateMessageTestSessionInfo(t, sessionName)
	mustCreateOpenMessage(t, store, sessionName, true)

	has, hasErr := sessionHasOpenAssignedWorkForConfigInfo("", closeGateMessageTestConfig(), store, nil, info)
	t.Logf("general assigned-work chain (pre-assert, for diagnosis): has=%v err=%v", has, hasErr)

	closeGate, err := sessionHasOpenAssignedWorkForReachableStoreForCloseGate("", closeGateMessageTestConfig(), store, nil, info)
	if err != nil {
		t.Fatalf("sessionHasOpenAssignedWorkForReachableStoreForCloseGate: %v", err)
	}
	if closeGate {
		t.Fatal("drain-ack close gate counted an open, already-read mail message as assigned work; " +
			"this is the ga-h9xpwb wake/drain loop: the session bead can never close")
	}
}

// TestDrainAckCloseGateIgnoresOpenUnreadMessage confirms the exclusion is
// unconditional on issue_type="message", not gated on mail.read.
func TestDrainAckCloseGateIgnoresOpenUnreadMessage(t *testing.T) {
	const sessionName = "wa-worker-adhoc-test-unread"
	store := beads.NewMemStore()
	info := closeGateMessageTestSessionInfo(t, sessionName)
	mustCreateOpenMessage(t, store, sessionName, false)

	closeGate, err := sessionHasOpenAssignedWorkForReachableStoreForCloseGate("", closeGateMessageTestConfig(), store, nil, info)
	if err != nil {
		t.Fatalf("sessionHasOpenAssignedWorkForReachableStoreForCloseGate: %v", err)
	}
	if closeGate {
		t.Fatal("drain-ack close gate counted an open, unread mail message as assigned work")
	}
}

// TestDrainAckCloseGateStillBlocksOnRealOpenWork is the regression pin: the fix
// excludes mail, not work. A real open work bead assigned to the session must
// still keep the close gate from closing it.
func TestDrainAckCloseGateStillBlocksOnRealOpenWork(t *testing.T) {
	const sessionName = "wa-worker-adhoc-test-realwork"
	store := beads.NewMemStore()
	info := closeGateMessageTestSessionInfo(t, sessionName)
	if _, err := store.Create(beads.Bead{
		Title:    "real work",
		Type:     "task",
		Assignee: sessionName,
	}); err != nil {
		t.Fatalf("create work bead: %v", err)
	}

	closeGate, err := sessionHasOpenAssignedWorkForReachableStoreForCloseGate("", closeGateMessageTestConfig(), store, nil, info)
	if err != nil {
		t.Fatalf("sessionHasOpenAssignedWorkForReachableStoreForCloseGate: %v", err)
	}
	if !closeGate {
		t.Fatal("drain-ack close gate stopped seeing real open work; the fix must exclude only mail messages")
	}
}

// TestAwakeChainNeverCountsMailEitherWay was meant to pin the other half of
// the Mayor's scope check (does wake-by-new-mail depend on the same function
// this fix touches?) by asserting an unread message keeps a sleeping session
// awake. That assumption was WRONG, caught by running this test against the
// UNPATCHED tree, where it failed: beads.go's readyExcludeTypes already lists
// "message": true (pre-existing, unrelated to ga-h9xpwb), so
// IsReadyCandidateForTier rejects every message bead unconditionally and
// sessionHasReadyAssignedWorkForTier — the only thing
// sessionHasAwakeAssignedWorkForReachableStore calls — never returns one, mail
// read or unread. So this reconciler chain was NEVER what wakes a session for
// new mail; that must happen through some other, not-yet-identified mechanism
// (a mail-delivery-time trigger, most likely) outside this bug's scope.
//
// What this test asserts instead is the narrower, verified-true fact that
// actually answers the Mayor's question: hasNonSessionNonOwnDrainStepWork (the
// function this fix edits) is never reached from this chain, so the fix
// cannot change this chain's answer either way — confirmed by construction
// (see hasNonSessionNonOwnDrainStepWork's own doc comment and its single call
// site), not by this test alone.
func TestAwakeChainNeverCountsMailEitherWay(t *testing.T) {
	const sessionName = "wa-worker-adhoc-test-wake"
	store := beads.NewMemStore()
	info := closeGateMessageTestSessionInfo(t, sessionName)
	mustCreateOpenMessage(t, store, sessionName, false)

	awake, err := sessionHasAwakeAssignedWorkForReachableStore("", closeGateMessageTestConfig(), store, nil, info)
	if err != nil {
		t.Fatalf("sessionHasAwakeAssignedWorkForReachableStore: %v", err)
	}
	if awake {
		t.Fatal("an unread mail message now counts as awake-work; readyExcludeTypes[\"message\"] must have changed — " +
			"if that is intentional, this bug's actual wake-for-new-mail mechanism needs re-investigation")
	}
}
