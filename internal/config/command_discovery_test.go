package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

func TestDiscoverPackCommands_BasicAndNested(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/status/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/status/help.md", "status help")
	writeTestFile(t, packDir, "commands/repo/sync/run.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d commands, want 2", len(got))
	}

	if got[0].Name != "repo/sync" {
		t.Fatalf("got first command %q, want %q", got[0].Name, "repo/sync")
	}
	if !reflect.DeepEqual(got[0].Command, []string{"repo", "sync"}) {
		t.Fatalf("repo/sync words = %#v, want %#v", got[0].Command, []string{"repo", "sync"})
	}

	if got[1].Name != "status" {
		t.Fatalf("got second command %q, want %q", got[1].Name, "status")
	}
	if !reflect.DeepEqual(got[1].Command, []string{"status"}) {
		t.Fatalf("status words = %#v, want %#v", got[1].Command, []string{"status"})
	}
	if got[1].HelpFile == "" {
		t.Fatal("status HelpFile = empty, want discovered help.md")
	}
}

func TestDiscoverPackCommands_ManifestOverride(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/repo-sync/command.toml", `
command = ["repo", "sync"]
description = "Sync the repo"
run = "../../shared/entry.sh"
`)
	writeTestFile(t, packDir, "shared/entry.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].Command, []string{"repo", "sync"}) {
		t.Fatalf("command words = %#v, want %#v", got[0].Command, []string{"repo", "sync"})
	}
	if got[0].Description != "Sync the repo" {
		t.Fatalf("description = %q, want %q", got[0].Description, "Sync the repo")
	}
	wantRun := filepath.Join(packDir, "shared", "entry.sh")
	if got[0].RunScript != wantRun {
		t.Fatalf("RunScript = %q, want %q", got[0].RunScript, wantRun)
	}
}

func TestDiscoverPackCommands_RejectsEscapingOrAbsoluteRunPaths(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	tests := []struct {
		name string
		run  string
	}{
		{name: "absolute", run: "/tmp/outside.sh"},
		{name: "escape", run: "../../../outside.sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, packDir, "commands/status/command.toml", "run = "+`"`+tt.run+`"`+"\n")
			writeTestFile(t, packDir, "commands/status/run.sh", "#!/bin/sh\nexit 0\n")

			_, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
			if err == nil {
				t.Fatal("DiscoverPackCommands error = nil, want containment error")
			}
		})
	}
}

func TestDiscoverPackCommands_SkipsHiddenAndUnderscoreDirs(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/status/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/.hidden/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/_private/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/repo/_internal/run.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if got[0].Name != "status" {
		t.Fatalf("got command %q, want %q", got[0].Name, "status")
	}
}

func TestDiscoverPackCommands_NoCommandsDir(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d commands, want 0", len(got))
	}
}

func TestDiscoverPackCommands_BadManifest(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/status/command.toml", "command = [")
	writeTestFile(t, packDir, "commands/status/run.sh", "#!/bin/sh\nexit 0\n")

	_, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err == nil {
		t.Fatal("DiscoverPackCommands error = nil, want manifest parse error")
	}
}

func TestDiscoverPackCommands_TreatsLeafAsTerminalAndIgnoresNestedRunSh(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/repo/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/repo/sync/run.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].Command, []string{"repo"}) {
		t.Fatalf("command words = %#v, want %#v", got[0].Command, []string{"repo"})
	}
}

func TestDiscoverPackCommands_AllowsVisibleAssetSubdirsUnderLeaf(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/deploy/run.sh", "#!/bin/sh\nexit 0\n")
	writeTestFile(t, packDir, "commands/deploy/templates/example.txt", "template")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].Command, []string{"deploy"}) {
		t.Fatalf("command words = %#v, want %#v", got[0].Command, []string{"deploy"})
	}
}

// TestDiscoverPackCommands_ManifestBindingOverride is a regression test for
// ga-9n9z7: town-deltas (and any other local pack) had no way to claim an
// EXISTING binding namespace for one of its own commands/*/run.sh entries —
// command.toml could redirect the verb path (see ManifestOverride above) but
// always registered under the importing pack's own default binding
// (stampDefaultBinding), so a same-named command in a differently-named pack
// could never override — e.g. a town-deltas command.toml declaring
// `command = ["dolt", "compact"]` still landed under `gc town-deltas ...`,
// never under `gc dolt compact`. This proves a pack can now declare an
// explicit `binding` in command.toml to target another pack's namespace —
// the discovery half of the override; cmd/gc's
// TestAddDiscoveredCommandsToRoot_LaterEntryOverridesEarlierLeaf proves the
// other half (that a later-registered same-binding/same-verb entry actually
// wins the CLI leaf instead of being silently dropped).
func TestDiscoverPackCommands_ManifestBindingOverride(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "town-deltas")

	writeTestFile(t, packDir, "commands/dolt-compact-override/command.toml", `
command = ["dolt", "compact"]
binding = "dolt"
description = "Patched compact"
run = "../../scripts/compact.sh"
`)
	writeTestFile(t, packDir, "scripts/compact.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "town-deltas")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if got[0].BindingName != "dolt" {
		t.Fatalf("BindingName = %q, want %q (explicit manifest binding must survive discovery, unstamped)", got[0].BindingName, "dolt")
	}
	// PackName still reflects the importing pack, independent of the binding
	// it targets — only the CLI namespace (BindingName) is redirected.
	if got[0].PackName != "town-deltas" {
		t.Fatalf("PackName = %q, want %q", got[0].PackName, "town-deltas")
	}
}

// TestDiscoverPackCommands_NoManifestBindingLeavesBindingNameEmpty guards the
// backward-compatible default: a command.toml without `binding` (the
// overwhelming majority of existing packs) must leave BindingName empty so
// stampDefaultBinding's `if out[i].BindingName == ""` still applies the
// importing pack's own name, exactly as before this change.
func TestDiscoverPackCommands_NoManifestBindingLeavesBindingNameEmpty(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "mypk")

	writeTestFile(t, packDir, "commands/status/run.sh", "#!/bin/sh\nexit 0\n")

	got, err := DiscoverPackCommands(fsys.OSFS{}, packDir, "mypk")
	if err != nil {
		t.Fatalf("DiscoverPackCommands: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1", len(got))
	}
	if got[0].BindingName != "" {
		t.Fatalf("BindingName = %q, want empty (no binding declared)", got[0].BindingName)
	}
}
