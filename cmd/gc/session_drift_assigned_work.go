package main

import (
	"sort"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// sessionHasLiveAssignedWorkForConfigDrift is the "does this session still own
// live work?" probe for the config-drift drain. It is
// sessionHasOpenAssignedWorkForReachableStore plus ONE extra case: a session
// whose agent resolves to the CITY store (storeRef == "") is also searched in
// every attached rig store.
//
// Why (ga-gn35963): the reachable-store probe answers from the agent's
// configured store. assignedWorkRoutingStoreRefForAgent resolves to a rig only
// when the agent has `dir` or a `work_dir` INSIDE a rig (gt-69j2v). A pool
// worker whose work_dir lives outside every rig — wa-worker-remote
// (work_dir = ".gc/agents/wa-worker-remote"), dog-remote, kimi-worker-remote —
// resolves to "" and the probe looked ONLY in the city store, while the bead it
// claimed lives in the rig store (wa-p7ag21.14.2.3 in the WA store, assignee
// wa-worker-remote-1). The probe read "no work", so a scripts-only drift
// (.gc/scripts content hash) drained a wake_mode=fresh worker mid-task and
// orphaned the in_progress bead; measured twice on one session in
// ~/.gc/supervisor.log. wa-worker-adhoc-* (work_dir inside the rig) logged
// "Skipping config-drift drain ...: live assigned work found" for the same
// drift.
//
// storeRef == "" means one of two different things the config cannot tell
// apart: a genuinely city-store agent, or "work_dir is outside every rig, store
// not derivable from config". City-scoped agents are cross-store eligible on
// purpose (agentIsCrossStoreEligible, vp-kvp). Three states, none of them
// guessed: a hit in any store is "has work" (defer the drain); a clean scan of
// every store is "has none"; a store that cannot be read is "can't know" and the
// error is returned, which the drift site turns into "skip the drain this tick".
//
// A session whose rig IS resolved (storeRef != "") keeps the existing
// single-rig answer: this widens only the unresolved case, not rig-scoped
// workers. The other callers of sessionHasOpenAssignedWorkForReachableStore
// (drain-ack close gate, max-age restart, drained-slot close, closeSessionBead
// guard) are intentionally unchanged; they share the blind spot and are tracked
// as a follow-up on ga-gn35963.
func sessionHasLiveAssignedWorkForConfigDrift(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	rigStores map[string]beads.Store,
	session beads.Bead,
) (bool, error) {
	has, err := sessionHasOpenAssignedWorkForReachableStore(cityPath, cfg, store, rigStores, session)
	if err != nil || has {
		return has, err
	}
	storeRef, ok := assignedWorkStoreRefForSession(cityPath, cfg, session)
	if !ok || storeRef != "" {
		// !ok: the reachable probe already searched every store.
		// storeRef != "": the session's rig is known; keep the single-rig answer.
		return false, nil
	}
	// The city store was just searched; search each attached rig store, in a
	// stable order so the first unreadable store is reported deterministically.
	refs := make([]string, 0, len(rigStores))
	for ref := range rigStores {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	identifiers := sessionAssignmentIdentifiersForConfig(session, cfg)
	for _, ref := range refs {
		if has, err := sessionHasOpenAssignedWorkInStoreByIdentifiers(rigStores[ref], identifiers); err != nil || has {
			return has, err
		}
	}
	return false, nil
}
