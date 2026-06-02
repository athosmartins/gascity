package beads

import (
	"encoding/json"
	"testing"
)

func TestMarshalEventBeadWritesExplicitEmptyDependencies(t *testing.T) {
	t.Parallel()

	payload, err := MarshalEventBead(Bead{ID: "gc-empty", Title: "empty deps", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("MarshalEventBead: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("Unmarshal(payload): %v", err)
	}
	deps, ok := fields["dependencies"]
	if !ok {
		t.Fatalf("payload = %s, want explicit dependencies field", payload)
	}
	if string(deps) != "[]" {
		t.Fatalf("dependencies = %s, want []", deps)
	}
}

func TestMarshalEventBeadEnvelopeWritesExplicitDependencies(t *testing.T) {
	t.Parallel()

	payload, err := MarshalEventBeadEnvelope(Bead{
		ID:     "gc-blocked",
		Title:  "blocked",
		Status: "open",
		Type:   "task",
		Dependencies: []Dep{{
			IssueID:     "gc-blocked",
			DependsOnID: "gc-blocker",
			Type:        "blocks",
		}},
	})
	if err != nil {
		t.Fatalf("MarshalEventBeadEnvelope: %v", err)
	}

	var decoded struct {
		Bead struct {
			Dependencies []Dep `json:"dependencies"`
		} `json:"bead"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal(payload): %v", err)
	}
	if len(decoded.Bead.Dependencies) != 1 || decoded.Bead.Dependencies[0].DependsOnID != "gc-blocker" {
		t.Fatalf("dependencies = %#v, want gc-blocker snapshot", decoded.Bead.Dependencies)
	}
}
