package types

import "strings"

// Labels a child never inherits from its parent.
//
// `bd create --parent` copies the parent's labels onto the new child. Most of
// them are classification (lane:small, ctx:*) and are worth carrying down. The
// families below are not: they record what an automation OBSERVED about one
// specific bead — a gate verdict, a retry counter, a refusal reason, a
// lifecycle flag. A child born with them reads as if it had already been
// through that process and failed it, so a brand-new slice shows up parked or
// escalated without ever having run.
//
// The match is on the label NAMESPACE (a literal prefix, case-sensitive), not
// a substring: "gates:review", "mygate:x" and a bare "pilot" are unrelated
// labels and keep being inherited.
var nonInheritableLabelPrefixes = []string{
	"gate:",            // gate verdicts and counters: gate:failed, gate:fix-attempt:N, gate:needs-human:*
	"gate-sha-failed:", // per-SHA gate failure memo
	"pilot:",           // pilot bookkeeping: reclaim/refusal counters, refused-reason
	"lifecycle:",       // lifecycle flags such as lifecycle:label-conflict
}

// nonInheritableLabels are single labels (not a family) with the same problem.
var nonInheritableLabels = map[string]struct{}{
	"story:needs-human": {},
}

// IsInheritableLabel reports whether a label may be copied from a parent onto a
// new child. It governs INHERITANCE only: a label the caller asks for by name
// is never filtered, so `--labels gate:queued` still works.
func IsInheritableLabel(label string) bool {
	if _, blocked := nonInheritableLabels[label]; blocked {
		return false
	}
	for _, prefix := range nonInheritableLabelPrefixes {
		if strings.HasPrefix(label, prefix) {
			return false
		}
	}
	return true
}

// FilterInheritableLabels returns the labels that may be inherited, in their
// original order. It never aliases the input; a nil result means none survive.
func FilterInheritableLabels(labels []string) []string {
	var kept []string
	for _, label := range labels {
		if IsInheritableLabel(label) {
			kept = append(kept, label)
		}
	}
	return kept
}
