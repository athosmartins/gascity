package main

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// gateReviewerTemplateName is the dedicated, cap-exempt template the quality
// gate spawns its independent reviewer sessions from (see
// agents/gate-reviewer/agent.toml and packs/town-deltas/assets/
// quality-gate-dispatcher.sh). Reviewer sessions are named
// "gate-reviewer-adhoc-<hash>" and drain when their review completes.
const gateReviewerTemplateName = "gate-reviewer"

// quality-gate verdict bead markers written by the dispatcher. A reviewer is
// "mid-review" exactly while at least one verdict bead is still
// type:quality-gate-verdict + verdict:pending and open. The dispatcher closes
// (or relabels verdict:TIMEOUT) any still-pending verdict bead when its own
// verdict timeout fires, so the pending set is self-clearing — protection here
// is therefore inherently bounded by that timeout, mirroring the dog-pool skip
// shape ("defer drain until the work completes; the next tick drains naturally").
const (
	qualityGateVerdictTypeLabel = "type:quality-gate-verdict"
	qualityGateVerdictPending   = "verdict:pending"
)

// sessionIsGateReviewerTemplate reports whether a session bead was spawned from
// the dedicated gate-reviewer template.
func sessionIsGateReviewerTemplate(session beads.Bead, cfg *config.City) bool {
	template := normalizedSessionTemplate(session, cfg)
	if template == "" {
		template = strings.TrimSpace(session.Metadata["template"])
	}
	if template == "" {
		template = strings.TrimSpace(session.Metadata["common_name"])
	}
	// Templates may be qualified (e.g. "city/gate-reviewer"); match the bare
	// final segment so qualification does not defeat the guard.
	if template == gateReviewerTemplateName {
		return true
	}
	if idx := strings.LastIndex(template, "/"); idx >= 0 {
		return template[idx+1:] == gateReviewerTemplateName
	}
	return false
}

// sessionIsReviewerDeliveringVerdict reports whether the session is a
// gate-reviewer that is actively mid-review — i.e. there is an open
// quality-gate verdict bead still marked verdict:pending in a store the
// reviewer's configured agent can reach.
//
// Why this exists separately from sessionHasOpenAssignedWorkForReachableStore:
// the quality-gate dispatcher does NOT assign the verdict bead to the reviewer
// session (the bead's assignee is empty; the bead ID is delivered to the
// reviewer via a nudge, and the dispatcher correlates session<->verdict by
// ordered arrays). So the assignee-keyed "live assigned work" probe used for
// the dog pool can never see a reviewer's work. This probe recognizes the same
// "live work in flight, defer the drain" condition via the verdict bead's
// labels instead of its assignee.
//
// Correlation precision: a verdict bead carries gate-run / reviewer-index
// labels but no session linkage the engine can see, so this cannot bind a
// pending verdict to one specific reviewer session. It therefore protects any
// gate-reviewer session while ANY pending verdict bead exists. The
// over-protection that implies (a reviewer whose own verdict is already in,
// kept alive a little longer because a sibling's verdict is still pending) is
// bounded by the dispatcher's verdict timeout, which terminalizes pending
// beads — at which point the next tick drains the reviewer normally.
func sessionIsReviewerDeliveringVerdict(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	rigStores map[string]beads.Store,
	session beads.Bead,
) (bool, error) {
	if !sessionIsGateReviewerTemplate(session, cfg) {
		return false, nil
	}

	// Resolve the same reachable store(s) the dog "live assigned work" path
	// uses, so the verdict probe and the assignee probe scan the same scope.
	storeRef, ok := assignedWorkStoreRefForSession(cityPath, cfg, session)
	switch {
	case !ok:
		// Template not resolvable to a configured agent — scan the local
		// store and every rig store (the same fallback as the assignee path).
		if has, err := storeHasPendingVerdictBead(store); err != nil || has {
			return has, err
		}
		for _, rs := range rigStores {
			if has, err := storeHasPendingVerdictBead(rs); err != nil || has {
				return has, err
			}
		}
		return false, nil
	case storeRef == "":
		return storeHasPendingVerdictBead(store)
	default:
		rigStore, ok := rigStores[storeRef]
		if !ok || rigStore == nil {
			// Mirror the assignee path: an unavailable configured rig store is
			// a real error, surfaced to the caller (which logs and skips the
			// drain this tick rather than draining blind).
			return false, fmt.Errorf("rig store %q unavailable for session %q", storeRef, session.Metadata["session_name"])
		}
		return storeHasPendingVerdictBead(rigStore)
	}
}

// storeHasPendingVerdictBead reports whether the store holds an open
// quality-gate verdict bead still labeled verdict:pending. Verdict beads are
// created --ephemeral (wisp tier), so the query unions both tiers.
func storeHasPendingVerdictBead(store beads.Store) (bool, error) {
	if store == nil {
		return false, nil
	}
	items, err := store.List(beads.ListQuery{
		Label:    qualityGateVerdictTypeLabel,
		TierMode: beads.TierBoth,
		Live:     true,
	})
	if err != nil {
		return false, err
	}
	for _, b := range items {
		if b.Status == "closed" {
			continue
		}
		if beadHasLabelValue(b, qualityGateVerdictPending) {
			return true, nil
		}
	}
	return false, nil
}

func beadHasLabelValue(b beads.Bead, want string) bool {
	for _, label := range b.Labels {
		if label == want {
			return true
		}
	}
	return false
}
