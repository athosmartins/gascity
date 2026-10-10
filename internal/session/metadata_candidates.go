package session

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// ExactMetadataSessionCandidates returns session beads matching any exact
// metadata filter. Each filter must contain exactly one key/value pair; empty
// filters are ignored. Results are deduplicated by bead ID in query order.
func ExactMetadataSessionCandidates(store beads.Store, includeClosed bool, filters ...map[string]string) ([]beads.Bead, error) {
	return exactMetadataSessionCandidates(store, includeClosed, "", false, filters...)
}

// ExactMetadataSessionCandidatesWithStatus returns session beads matching any
// exact metadata filter and the requested bead status.
func ExactMetadataSessionCandidatesWithStatus(store beads.Store, status string, filters ...map[string]string) ([]beads.Bead, error) {
	return exactMetadataSessionCandidates(store, false, strings.TrimSpace(status), false, filters...)
}

// liveExactMetadataSessionCandidates is ExactMetadataSessionCandidates read
// live: it bypasses any cache in front of the store, so a bead another process
// created a moment ago is visible. The identity-availability checks need that:
// a query that includes closed rows always reaches the backing store, so
// dropping closed rows from one of them must not also drop its freshness
// (ga-1g2if1).
func liveExactMetadataSessionCandidates(store beads.Store, includeClosed bool, filters ...map[string]string) ([]beads.Bead, error) {
	return exactMetadataSessionCandidates(store, includeClosed, "", true, filters...)
}

func exactMetadataSessionCandidates(store beads.Store, includeClosed bool, status string, live bool, filters ...map[string]string) ([]beads.Bead, error) {
	if store == nil {
		return nil, nil
	}
	seenQueries := make(map[string]bool, len(filters))
	seenBeads := make(map[string]bool)
	candidates := make([]beads.Bead, 0, len(filters))
	for _, filter := range filters {
		if len(filter) != 1 {
			continue
		}
		var key, value string
		for k, v := range filter {
			key = strings.TrimSpace(k)
			value = strings.TrimSpace(v)
		}
		if key == "" || value == "" {
			continue
		}
		queryKey := key + "\x00" + value
		if seenQueries[queryKey] {
			continue
		}
		seenQueries[queryKey] = true
		// An exact-value lookup matches a handful of beads at most: mark it
		// Selective so NativeDoltStore can read a status-filtered one as a
		// bounded probe (ga-1g2if1). The hint is inert when closed rows are
		// included; the result is identical either way.
		query := beads.ListQuery{
			Metadata:  map[string]string{key: value},
			Selective: true,
			Live:      live,
		}
		if status != "" {
			query.Status = status
		} else {
			query.IncludeClosed = includeClosed
		}
		items, err := store.List(query)
		if err != nil {
			return nil, err
		}
		for _, b := range items {
			if seenBeads[b.ID] || !IsSessionBeadOrRepairable(b) {
				continue
			}
			RepairEmptyType(store, &b)
			seenBeads[b.ID] = true
			candidates = append(candidates, b)
		}
	}
	return candidates, nil
}
