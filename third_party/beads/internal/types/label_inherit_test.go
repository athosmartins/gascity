package types

import (
	"reflect"
	"testing"
)

func TestIsInheritableLabel(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		// State families: never inherited.
		{"gate:failed", false},
		{"gate:fix-attempt:2", false},
		{"gate:needs-human:technical", false},
		{"gate-sha-failed:48d62592411a65c77aca2ef29224faa651664593:code", false},
		{"pilot:reclaim-count:escalated-at-3", false},
		{"pilot:refused-reason:umbrella-sliced-owned-by-live-crew", false},
		{"lifecycle:label-conflict", false},
		{"story:needs-human", false},

		// Classification and look-alikes: still inherited.
		{"lane:small", true},
		{"ctx:ready", true},
		{"story:needs-refino", true},
		{"story:unrefined", true},
		{"gates:review", true},
		{"mygate:failed", true},
		{"Gate:failed", true},
		{"gate", true},
		{"pilot", true},
		{"lifecycle", true},
		{"waiting-on:external", true},
		{"", true},
	}
	for _, tc := range cases {
		if got := IsInheritableLabel(tc.label); got != tc.want {
			t.Errorf("IsInheritableLabel(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

func TestFilterInheritableLabels(t *testing.T) {
	in := []string{"lane:small", "gate:failed", "ctx:ready", "pilot:held", "story:needs-human"}
	got := FilterInheritableLabels(in)
	want := []string{"lane:small", "ctx:ready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterInheritableLabels = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(in, []string{"lane:small", "gate:failed", "ctx:ready", "pilot:held", "story:needs-human"}) {
		t.Fatalf("input was mutated: %v", in)
	}

	if got := FilterInheritableLabels([]string{"gate:failed"}); got != nil {
		t.Fatalf("all-state input = %v, want nil", got)
	}
	if got := FilterInheritableLabels(nil); got != nil {
		t.Fatalf("nil input = %v, want nil", got)
	}
}
