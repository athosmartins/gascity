package checks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
)

func TestProjectIdentityCheck_ClassifyOK(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "ok", "same", "same")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {id: "same", ok: true, reachable: true},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK; result=%+v", result.Status, result)
	}
	if len(result.Details) != 0 {
		t.Fatalf("details = %v, want none", result.Details)
	}
}

func TestProjectIdentityCheck_ClassifyMigrationFixable_NoL1(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "legacy", "", "legacy-id")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {id: "legacy-id", ok: true, reachable: true},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)})
	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want Warning; result=%+v", result.Status, result)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "L1 absent") {
		t.Fatalf("details = %v, want L1 absent message", result.Details)
	}
}

func TestProjectIdentityCheck_ClassifyL2DriftFixable(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "l2", "canonical", "wrong-l2")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {id: "canonical", ok: true, reachable: true},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)})
	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want Warning; result=%+v", result.Status, result)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "L2 cache differs from L1") {
		t.Fatalf("details = %v, want L2 drift message", result.Details)
	}
}

func TestProjectIdentityCheck_ClassifyL3DriftUnfixable(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "l3", "canonical", "canonical")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {id: "database", ok: true, reachable: true},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)})
	if result.Status != doctor.StatusError {
		t.Fatalf("status = %v, want Error; result=%+v", result.Status, result)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "L3 dolt stamp differs from L1") {
		t.Fatalf("details = %v, want L3 drift message", result.Details)
	}
}

func TestProjectIdentityCheck_ClassifyL3Unverifiable(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "unverifiable", "canonical", "canonical")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {reachable: false},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root), Verbose: true})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want OK; result=%+v", result.Status, result)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "dolt unavailable") {
		t.Fatalf("details = %v, want dolt unavailable detail", result.Details)
	}
}

func TestProjectIdentityCheck_FixRepairsL2OnlyNotL3(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "fix", "canonical", "wrong-l2")
	states := map[string]projectIdentityL3State{
		scope.Root: {id: "canonical", ok: true, reachable: true},
	}
	check := newProjectIdentityTestCheck(scope, states)

	if err := check.Fix(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	assertProjectIdentityMetadataID(t, scope.Root, "canonical")
	if states[scope.Root].id != "canonical" {
		t.Fatalf("L3 state changed to %q, want unchanged canonical", states[scope.Root].id)
	}
}

func TestProjectIdentityCheck_FixRefusesL3UnderAnyOutcome(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "refuse-l3", "canonical", "canonical")
	states := map[string]projectIdentityL3State{
		scope.Root: {id: "database", ok: true, reachable: true},
	}
	check := newProjectIdentityTestCheck(scope, states)

	if err := check.Fix(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)}); err != nil {
		t.Fatalf("Fix: %v", err)
	}
	assertProjectIdentityMetadataID(t, scope.Root, "canonical")
	if states[scope.Root].id != "database" {
		t.Fatalf("L3 state changed to %q, want unchanged database", states[scope.Root].id)
	}
}

func TestProjectIdentityCheck_MultiScopeAggregation(t *testing.T) {
	city := t.TempDir()
	okScope := newProjectIdentityScopeFixtureAt(t, filepath.Join(city, "ok"), "ok", "same", "same")
	l2Scope := newProjectIdentityScopeFixtureAt(t, filepath.Join(city, "l2"), "l2", "canonical", "wrong-l2")
	l3Scope := newProjectIdentityScopeFixtureAt(t, filepath.Join(city, "l3"), "l3", "canonical", "canonical")
	check := &ProjectIdentityCheck{
		fs: fsys.OSFS{},
		resolveScopes: func(string) ([]projectIdentityScope, error) {
			return []projectIdentityScope{okScope, l2Scope, l3Scope}, nil
		},
		readL3: func(_ string, scope projectIdentityScope) (string, bool, bool, error) {
			switch scope.Root {
			case okScope.Root:
				return "same", true, true, nil
			case l2Scope.Root:
				return "canonical", true, true, nil
			case l3Scope.Root:
				return "database", true, true, nil
			default:
				return "", false, false, nil
			}
		},
	}

	result := check.Run(&doctor.CheckContext{CityPath: city})
	if result.Status != doctor.StatusError {
		t.Fatalf("status = %v, want Error; result=%+v", result.Status, result)
	}
	if len(result.Details) < 2 {
		t.Fatalf("details = %v, want L3 and L2 details", result.Details)
	}
	if !strings.Contains(result.Details[0], l3Scope.Root) {
		t.Fatalf("first detail = %q, want L3 scope first", result.Details[0])
	}
}

func TestProjectIdentityCheck_FixHintMentionsCommandName(t *testing.T) {
	scope := newProjectIdentityScopeFixture(t, "hint", "canonical", "canonical")
	check := newProjectIdentityTestCheck(scope, map[string]projectIdentityL3State{
		scope.Root: {id: "database", ok: true, reachable: true},
	})

	result := check.Run(&doctor.CheckContext{CityPath: filepath.Dir(scope.Root)})
	if !strings.Contains(result.FixHint, "gc bd doctor --reseed-identity") {
		t.Fatalf("FixHint = %q, want command name", result.FixHint)
	}
}

type projectIdentityL3State struct {
	id        string
	ok        bool
	reachable bool
}

func newProjectIdentityTestCheck(scope projectIdentityScope, states map[string]projectIdentityL3State) *ProjectIdentityCheck {
	return &ProjectIdentityCheck{
		fs: fsys.OSFS{},
		resolveScopes: func(string) ([]projectIdentityScope, error) {
			return []projectIdentityScope{scope}, nil
		},
		readL3: func(_ string, scope projectIdentityScope) (string, bool, bool, error) {
			state := states[scope.Root]
			return state.id, state.ok, state.reachable, nil
		},
	}
}

func newProjectIdentityScopeFixture(t *testing.T, name, l1, l2 string) projectIdentityScope {
	t.Helper()
	city := t.TempDir()
	return newProjectIdentityScopeFixtureAt(t, filepath.Join(city, name), name, l1, l2)
}

func newProjectIdentityScopeFixtureAt(t *testing.T, root, name, l1, l2 string) projectIdentityScope {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if l1 != "" {
		if err := contract.WriteProjectIdentity(fsys.OSFS{}, root, l1); err != nil {
			t.Fatalf("WriteProjectIdentity: %v", err)
		}
	}
	meta := map[string]any{"backend": "dolt", "database": "dolt", "dolt_database": "hq"}
	if l2 != "" {
		meta["project_id"] = l2
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(root, ".beads", "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return projectIdentityScope{Root: root, Kind: "rig", Name: name}
}

func assertProjectIdentityMetadataID(t *testing.T, scopeRoot, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(scopeRoot, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(meta["project_id"].(string)); got != want {
		t.Fatalf("metadata project_id = %q, want %q", got, want)
	}
}

var _ = context.Background
