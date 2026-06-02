package beads

import "encoding/json"

// MarshalEventBead marshals a bead for bead-change events with an explicit
// dependency snapshot. Empty dependency lists are significant for cache
// consumers, so this must not rely on Bead.Dependencies' omitempty tag.
func MarshalEventBead(b Bead) ([]byte, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	deps, err := json.Marshal(eventDependencySnapshot(b.Dependencies))
	if err != nil {
		return nil, err
	}
	fields["dependencies"] = deps
	return json.Marshal(fields)
}

// MarshalEventBeadEnvelope marshals the hook-style bead event payload shape.
func MarshalEventBeadEnvelope(b Bead) ([]byte, error) {
	beadPayload, err := MarshalEventBead(b)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"bead": beadPayload})
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func eventDependencySnapshot(deps []Dep) []Dep {
	if len(deps) == 0 {
		return []Dep{}
	}
	return cloneDeps(deps)
}
