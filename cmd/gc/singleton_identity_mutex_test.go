package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// These tests cover the single-identity mutex for singleton-crew templates
// (ga-b41wn): a singleton-crew agent (max_active_sessions==1, no namepool)
// must never end up with two awake/active sessions, regardless of the
// activation path. ComputeAwakeSet is the single chokepoint every path funnels
// through (new/sling, wake, resume, reconciler-revive), so the mutex is
// enforced there. The bug manifested via the resume/wake/revive vector, where a
// second EXISTING bead (manual --resume, continuation_reset_pending) was
// reactivated alongside the live canonical session.

// TestSingletonMutex_ResumeDuplicateRefused is the core ga-b41wn repro: the
// canonical fresh+running session and a manual --resume duplicate are BOTH
// wake-eligible this tick. The mutex must keep the live canonical session and
// refuse the resume duplicate.
func TestSingletonMutex_ResumeDuplicateRefused(t *testing.T) {
	result := ComputeAwakeSet(AwakeInput{
		Agents: []AwakeAgent{{QualifiedName: "whatsapp_automation/digo-wa", SingletonIdentity: true}},
		SessionBeads: []AwakeSessionBead{
			// Fresh canonical session: active, running, pinned awake.
			{
				ID:          "sess-canonical",
				SessionName: "whatsapp_automation--digo-wa",
				Template:    "whatsapp_automation/digo-wa",
				State:       "active",
				Pinned:      true,
			},
			// The duplicate: manual --resume bead with continuation_reset_pending.
			{
				ID:                       "sess-resume",
				SessionName:              "whatsapp_automation--digo-wa--manual",
				Template:                 "whatsapp_automation/digo-wa",
				State:                    "asleep",
				ManualSession:            true,
				ContinuationResetPending: true,
			},
		},
		RunningSessions: map[string]bool{"whatsapp_automation--digo-wa": true},
		Now:             now,
	})

	assertAwake(t, result, "whatsapp_automation--digo-wa")
	assertAsleep(t, result, "whatsapp_automation--digo-wa--manual")
	assertReason(t, result, "whatsapp_automation--digo-wa--manual", "singleton-mutex")
}

// TestSingletonMutex_OnlyOneSurvivesEvenWhenBothHealthy guards the general
// invariant: at most one awake bead for a singleton-crew identity, even when
// both look equally healthy. The tie-break is deterministic (lowest ID).
func TestSingletonMutex_OnlyOneSurvivesEvenWhenBothHealthy(t *testing.T) {
	result := ComputeAwakeSet(AwakeInput{
		Agents: []AwakeAgent{{QualifiedName: "rig/crew", SingletonIdentity: true}},
		SessionBeads: []AwakeSessionBead{
			{ID: "aaa", SessionName: "rig--crew", Template: "rig/crew", State: "active", Pinned: true},
			{ID: "bbb", SessionName: "rig--crew--2", Template: "rig/crew", State: "active", Pinned: true},
		},
		Now: now,
	})

	awake := 0
	for _, name := range []string{"rig--crew", "rig--crew--2"} {
		if d, ok := result[name]; ok && d.ShouldWake {
			awake++
		}
	}
	if awake != 1 {
		t.Fatalf("singleton-crew identity has %d awake sessions, want exactly 1", awake)
	}
	// Lowest ID ("aaa") wins the deterministic tie-break.
	assertAwake(t, result, "rig--crew")
	assertAsleep(t, result, "rig--crew--2")
}

// TestSingletonMutex_AttachedSessionWins verifies refuse-or-reuse preference:
// an attached session is the survivor even against a non-manual canonical bead.
func TestSingletonMutex_AttachedSessionWins(t *testing.T) {
	result := ComputeAwakeSet(AwakeInput{
		Agents: []AwakeAgent{{QualifiedName: "rig/crew", SingletonIdentity: true}},
		SessionBeads: []AwakeSessionBead{
			// Non-manual canonical bead, pinned (would normally be preferred).
			{ID: "canon", SessionName: "rig--crew", Template: "rig/crew", State: "active", Pinned: true},
			// Manual bead, but the user is attached to it right now.
			{ID: "attached", SessionName: "rig--crew--manual", Template: "rig/crew", State: "active", ManualSession: true, Pinned: true},
		},
		AttachedSessions: map[string]bool{"rig--crew--manual": true},
		Now:              now,
	})

	assertAwake(t, result, "rig--crew--manual")
	assertReason(t, result, "rig--crew--manual", "attached")
	assertAsleep(t, result, "rig--crew")
	assertReason(t, result, "rig--crew", "singleton-mutex")
}

// TestSingletonMutex_SingleBeadUnaffected: one awake bead is never demoted.
func TestSingletonMutex_SingleBeadUnaffected(t *testing.T) {
	result := ComputeAwakeSet(AwakeInput{
		Agents: []AwakeAgent{{QualifiedName: "rig/crew", SingletonIdentity: true}},
		SessionBeads: []AwakeSessionBead{
			{ID: "only", SessionName: "rig--crew", Template: "rig/crew", State: "active", Pinned: true},
		},
		Now: now,
	})
	assertAwake(t, result, "rig--crew")
	assertReason(t, result, "rig--crew", "pin")
}

// TestSingletonMutex_NonSingletonAgentNotCapped is the regression guard: a
// multi-session agent (SingletonIdentity=false, e.g. a pool with max>1 or a
// namepool) must keep BOTH awake — the mutex only fires for singleton crews.
func TestSingletonMutex_NonSingletonAgentNotCapped(t *testing.T) {
	result := ComputeAwakeSet(AwakeInput{
		Agents: []AwakeAgent{{QualifiedName: "rig/pool", SingletonIdentity: false}},
		SessionBeads: []AwakeSessionBead{
			{ID: "p1", SessionName: "rig--pool-1", Template: "rig/pool", State: "active", Pinned: true},
			{ID: "p2", SessionName: "rig--pool-2", Template: "rig/pool", State: "active", Pinned: true},
		},
		Now: now,
	})
	assertAwake(t, result, "rig--pool-1")
	assertAwake(t, result, "rig--pool-2")
}

// TestIsSingletonIdentityAgent verifies the config-side predicate the bridge
// uses to populate AwakeAgent.SingletonIdentity. Only max_active_sessions==1
// with no namepool qualifies.
func TestIsSingletonIdentityAgent(t *testing.T) {
	cases := []struct {
		name  string
		agent *config.Agent
		want  bool
	}{
		{"crew max=1", &config.Agent{Name: "digo-wa", MaxActiveSessions: intPtr(1)}, true},
		{"named-session crew max=1", &config.Agent{Name: "refinery", MaxActiveSessions: intPtr(1), MinActiveSessions: intPtr(1)}, true},
		{"pool max=3", &config.Agent{Name: "polecat", MaxActiveSessions: intPtr(3)}, false},
		{"unlimited (nil)", &config.Agent{Name: "war-rig"}, false},
		{"max=0 disabled", &config.Agent{Name: "off", MaxActiveSessions: intPtr(0)}, false},
		{"namepool max=1 (distinct identities)", &config.Agent{Name: "furiosa", Namepool: "names", MaxActiveSessions: intPtr(1)}, false},
		{"namepool names max=1", &config.Agent{Name: "furiosa", NamepoolNames: []string{"a", "b"}, MaxActiveSessions: intPtr(1)}, false},
		{"nil agent", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSingletonIdentityAgent(tc.agent); got != tc.want {
				t.Errorf("isSingletonIdentityAgent(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
