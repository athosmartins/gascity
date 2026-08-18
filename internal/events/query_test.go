package events

import (
	"testing"
	"time"
)

// --- matchesFilter unit tests ---

func TestMatchesFilter_Subject(t *testing.T) {
	e := Event{Type: BeadCreated, Actor: "actor-a", Subject: "gc-42"}

	if !matchesFilter(e, Filter{Subject: "gc-42"}) {
		t.Error("expected match on Subject gc-42")
	}
	if matchesFilter(e, Filter{Subject: "gc-99"}) {
		t.Error("expected no match on Subject gc-99")
	}
	if !matchesFilter(e, Filter{}) {
		t.Error("empty filter should match everything")
	}
}

// TestMatchesFilter_Type_CommaList reproduces ga-gye3f: `gc events
// --type=session.woke,session.stopped,session.crashed` silently matched
// zero events because the Type predicate compared the event's single Type
// against the whole comma-joined filter string with ==. A comma-list filter
// must OR-match, the same convention `bd list --status open,in_progress`
// and `--exclude-type=convoy,epic` already use elsewhere in this CLI
// family.
func TestMatchesFilter_Type_CommaList(t *testing.T) {
	woke := Event{Type: SessionWoke}
	stopped := Event{Type: SessionStopped}
	created := Event{Type: BeadCreated}

	list := Filter{Type: "session.woke,session.stopped,session.crashed"}
	if !matchesFilter(woke, list) {
		t.Error("comma-list filter should match session.woke (one of the listed types)")
	}
	if !matchesFilter(stopped, list) {
		t.Error("comma-list filter should match session.stopped (one of the listed types)")
	}
	if matchesFilter(created, list) {
		t.Error("comma-list filter should not match bead.created (not one of the listed types)")
	}

	// A single (non-comma) Type filter must keep behaving exactly as
	// before — this is not a behavior change for the common case.
	single := Filter{Type: "session.woke"}
	if !matchesFilter(woke, single) {
		t.Error("single-type filter should still match its exact type")
	}
	if matchesFilter(stopped, single) {
		t.Error("single-type filter should still reject a different type")
	}
}

func TestTypeMatches(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		filter    string
		want      bool
	}{
		{"empty filter matches anything", "session.woke", "", true},
		{"single type match", "session.woke", "session.woke", true},
		{"single type no match", "session.woke", "session.stopped", false},
		{"comma list first element", "session.woke", "session.woke,session.stopped,session.crashed", true},
		{"comma list middle element", "session.stopped", "session.woke,session.stopped,session.crashed", true},
		{"comma list last element", "session.crashed", "session.woke,session.stopped,session.crashed", true},
		{"comma list no match", "bead.created", "session.woke,session.stopped,session.crashed", false},
		{"comma list tolerates whitespace", "session.stopped", "session.woke, session.stopped, session.crashed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TypeMatches(tc.eventType, tc.filter); got != tc.want {
				t.Errorf("TypeMatches(%q, %q) = %v, want %v", tc.eventType, tc.filter, got, tc.want)
			}
		})
	}
}

func TestMatchesFilter_Until(t *testing.T) {
	now := time.Now()
	e := Event{Type: BeadCreated, Ts: now}

	if !matchesFilter(e, Filter{Until: now.Add(time.Second)}) {
		t.Error("event before Until should match")
	}
	if !matchesFilter(e, Filter{Until: now}) {
		t.Error("event exactly at Until should match (not After)")
	}
	if matchesFilter(e, Filter{Until: now.Add(-time.Second)}) {
		t.Error("event after Until should not match")
	}
}

func TestFakeList_Limit(t *testing.T) {
	// Create a fake with 5 events and request only 3.
	f := NewFake()
	for i := 0; i < 5; i++ {
		f.Record(Event{Type: BeadCreated, Actor: "actor-a"})
	}

	got, err := f.List(Filter{Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("List(Limit:3) returned %d events, want 3", len(got))
	}
}

func TestMatchesFilter_SubjectFilter_ViaFake(t *testing.T) {
	f := NewFake()
	f.Record(Event{Type: BeadCreated, Subject: "gc-1"})
	f.Record(Event{Type: BeadCreated, Subject: "gc-2"})
	f.Record(Event{Type: BeadClosed, Subject: "gc-1"})

	got, err := f.List(Filter{Subject: "gc-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List(Subject:gc-1) returned %d events, want 2", len(got))
	}
}

func TestMatchesFilter_UntilFilter_ViaFake(t *testing.T) {
	cutoff := time.Now()
	past := cutoff.Add(-time.Minute)
	future := cutoff.Add(time.Minute)

	f := NewFake()
	f.Record(Event{Type: BeadCreated, Ts: past, Subject: "old"})
	f.Record(Event{Type: BeadCreated, Ts: future, Subject: "new"})

	got, err := f.List(Filter{Until: cutoff})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Subject != "old" {
		t.Errorf("List(Until:cutoff) = %v, want 1 old event", got)
	}
}

// --- CountByType ---

func TestCountByType_Empty(t *testing.T) {
	counts := CountByType(nil)
	if len(counts) != 0 {
		t.Errorf("CountByType(nil) = %v, want empty map", counts)
	}
}

func TestCountByType(t *testing.T) {
	evts := []Event{
		{Type: BeadCreated},
		{Type: BeadCreated},
		{Type: BeadClosed},
		{Type: SessionWoke},
	}
	counts := CountByType(evts)
	if counts[BeadCreated] != 2 {
		t.Errorf("CountByType[BeadCreated] = %d, want 2", counts[BeadCreated])
	}
	if counts[BeadClosed] != 1 {
		t.Errorf("CountByType[BeadClosed] = %d, want 1", counts[BeadClosed])
	}
	if counts[SessionWoke] != 1 {
		t.Errorf("CountByType[SessionWoke] = %d, want 1", counts[SessionWoke])
	}
}

// --- CountByActor ---

func TestCountByActor_Empty(t *testing.T) {
	counts := CountByActor(nil)
	if len(counts) != 0 {
		t.Errorf("CountByActor(nil) = %v, want empty map", counts)
	}
}

func TestCountByActor(t *testing.T) {
	evts := []Event{
		{Actor: "actor-a"},
		{Actor: "actor-a"},
		{Actor: "actor-b"},
	}
	counts := CountByActor(evts)
	if counts["actor-a"] != 2 {
		t.Errorf("CountByActor[actor-a] = %d, want 2", counts["actor-a"])
	}
	if counts["actor-b"] != 1 {
		t.Errorf("CountByActor[actor-b] = %d, want 1", counts["actor-b"])
	}
}

// --- CountBySubject ---

func TestCountBySubject_Empty(t *testing.T) {
	counts := CountBySubject(nil)
	if len(counts) != 0 {
		t.Errorf("CountBySubject(nil) = %v, want empty map", counts)
	}
}

func TestCountBySubject(t *testing.T) {
	evts := []Event{
		{Subject: "gc-1"},
		{Subject: "gc-1"},
		{Subject: "gc-2"},
		{Subject: ""},
	}
	counts := CountBySubject(evts)
	if counts["gc-1"] != 2 {
		t.Errorf("CountBySubject[gc-1] = %d, want 2", counts["gc-1"])
	}
	if counts["gc-2"] != 1 {
		t.Errorf("CountBySubject[gc-2] = %d, want 1", counts["gc-2"])
	}
	if counts[""] != 1 {
		t.Errorf("CountBySubject[\"\"] = %d, want 1 (no-subject events)", counts[""])
	}
}
