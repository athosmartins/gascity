package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// liveCrewSessionBead builds an open, non-pool session bead indexed by the
// snapshot under agentName so FindLiveSessionBeadByAgentName can resolve it.
func liveCrewSessionBead(id, agentName, template string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": agentName,
			"agent_name":   agentName,
			"template":     template,
			"state":        string(session.StateActive),
		},
	}
}

// TestSelectOrPlanPoolSessionBead_SingleIdentityReusesLiveSession is the FIX B
// regression (ga-i67t): for an identity-bound crew agent (digo) that already
// has a live session, the spawn path must REUSE that session instead of
// planning a fresh create (which would auto-suffix a duplicate digo-1 sharing
// the same crew/<identity>/* branch namespace and work_dir).
func TestSelectOrPlanPoolSessionBead_SingleIdentityReusesLiveSession(t *testing.T) {
	cfgAgent := config.Agent{Name: "digo"} // uncapped crew → single-identity
	existing := liveCrewSessionBead("gc-digo-live", cfgAgent.QualifiedName(), "digo")
	store := beads.NewMemStore()
	created, err := store.Create(existing)
	if err != nil {
		t.Fatalf("seeding live session bead: %v", err)
	}

	bp := &agentBuildParams{
		cityPath:     t.TempDir(),
		beadStore:    store,
		city:         &config.City{Agents: []config.Agent{cfgAgent}},
		agents:       []config.Agent{cfgAgent},
		sessionBeads: newSessionBeadSnapshot([]beads.Bead{created}),
	}

	bead, _, plan, err := selectOrPlanPoolSessionBead(bp, &cfgAgent, "digo", nil, map[string]bool{}, map[int]bool{})
	if err != nil {
		t.Fatalf("selectOrPlanPoolSessionBead: %v", err)
	}
	if plan != nil {
		t.Fatalf("expected REUSE (nil plan) for single-identity agent with a live session; got fresh create plan %#v", plan)
	}
	if bead.ID != created.ID {
		t.Fatalf("expected reuse of live session %q; got bead %q", created.ID, bead.ID)
	}
}

// TestSelectOrPlanPoolSessionBead_PoolAgentNotGuarded is the FIX B counterpart:
// an expansion pool agent (dog — min/max set) is NOT subject to the
// single-identity guard, so it still plans a fresh create for new demand even
// when sibling pool sessions are live. This proves dog/polecat keep
// multi-instance behavior.
func TestSelectOrPlanPoolSessionBead_PoolAgentNotGuarded(t *testing.T) {
	cfgAgent := config.Agent{Name: "dog", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)}
	store := beads.NewMemStore()

	bp := &agentBuildParams{
		cityPath:     t.TempDir(),
		beadStore:    store,
		city:         &config.City{Agents: []config.Agent{cfgAgent}},
		agents:       []config.Agent{cfgAgent},
		sessionBeads: newSessionBeadSnapshot(nil),
	}
	bp.configurePoolSessionCreateFairShare(nil)

	bead, _, plan, err := selectOrPlanPoolSessionBead(bp, &cfgAgent, "dog", nil, map[string]bool{}, map[int]bool{})
	if err != nil {
		t.Fatalf("selectOrPlanPoolSessionBead: %v", err)
	}
	if plan == nil {
		t.Fatalf("expected a fresh create plan for expansion pool agent; got reuse bead %#v", bead)
	}
}
