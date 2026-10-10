package main

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildCreateIssuePreservesLabels(t *testing.T) {
	labels := []string{"gc:wisp", "status:pending"}
	issue := buildCreateIssue(createIssueParams{
		Title:     "labelled create",
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    labels,
	})

	if !reflect.DeepEqual(issue.Labels, labels) {
		t.Fatalf("labels = %v, want %v", issue.Labels, labels)
	}

	labels[0] = "mutated"
	if issue.Labels[0] != "gc:wisp" {
		t.Fatalf("issue labels alias input slice: got %v", issue.Labels)
	}
}

func TestMergeCreateLabelsKeepsUserOrderAndDedupesInherited(t *testing.T) {
	got := mergeCreateLabels(
		[]string{"gc:wisp", "status:pending", "gc:wisp"},
		[]string{"status:pending", "owner:agent"},
	)
	want := []string{"gc:wisp", "status:pending", "owner:agent"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged labels = %v, want %v", got, want)
	}
}

// A child must not be born carrying its parent's gate/pilot/lifecycle state.
// The fixture is the exact label set a live umbrella (wa-2362s2) held when a
// new slice created under it came out with gate:failed, gate:needs-human and
// pilot:reclaim-count:escalated-at-3 without ever having been through the gate.
func TestMergeCreateLabelsDoesNotInheritAutomationStateLabels(t *testing.T) {
	got := mergeCreateLabels(
		[]string{"explicit-label"},
		[]string{
			"gate-sha-failed:48d62592411a65c77aca2ef29224faa651664593:code",
			"gate:failed",
			"gate:fix-attempt:2",
			"gate:needs-fix",
			"gate:needs-human",
			"gate:needs-human:technical",
			"lane:small",
			"lifecycle:label-conflict",
			"pilot:reclaim-count:escalated-at-3",
			"pilot:refusal-count:1",
			"pilot:refused-reason:umbrella-sliced-owned-by-live-crew",
			"story:needs-human",
		},
	)
	want := []string{"explicit-label", "lane:small"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged labels = %v, want %v (only non-state labels such as lane:small may be inherited)", got, want)
	}
}

// The filter is about what is INHERITED. A label the caller asks for by name
// is the caller's decision and must survive, state-looking or not.
func TestMergeCreateLabelsKeepsExplicitStateLabels(t *testing.T) {
	got := mergeCreateLabels(
		[]string{"gate:queued", "story:needs-human"},
		[]string{"gate:queued", "pilot:held", "lane:small"},
	)
	want := []string{"gate:queued", "story:needs-human", "lane:small"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged labels = %v, want %v", got, want)
	}
}

// Prefix families, not substrings: look-alikes and other namespaces keep
// being inherited exactly as before.
func TestMergeCreateLabelsStillInheritsNonStateLabels(t *testing.T) {
	inherited := []string{
		"lane:small",
		"ctx:ready",
		"story:needs-refino",
		"gates:review",
		"pilot",
		"Gate:failed",
		"mygate:failed",
		"gate",
		"lifecycle",
		"waiting-on:external",
	}
	got := mergeCreateLabels(nil, inherited)

	if !reflect.DeepEqual(got, inherited) {
		t.Fatalf("merged labels = %v, want %v", got, inherited)
	}
}
