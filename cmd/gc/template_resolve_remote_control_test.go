package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// resolveTemplateRemoteControlParams builds an agentBuildParams wired to a
// claude-family provider with a staged .gc/settings.json, so resolveTemplate
// emits a --settings arg whose remote-control treatment depends on the agent's
// RemoteControl field (FIX A / ga-629k).
func resolveTemplateRemoteControlParams(t *testing.T, cityPath string) *agentBuildParams {
	t.Helper()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	gcDir := filepath.Join(cityPath, ".gc")
	if err := os.MkdirAll(gcDir, 0o755); err != nil {
		t.Fatalf("mkdir .gc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gcDir, "settings.json"),
		[]byte(`{"remoteControlAtStartup": true, "skipDangerousModePermissionPrompt": true}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}
	return &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "claude"},
		providers:  map[string]config.ProviderSpec{"claude": {Command: "claude", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/claude", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}
}

// TestResolveTemplate_PoolAgentDisablesRemoteControl verifies FIX A: a pool /
// system agent with remote_control=false launches claude with an inline
// --settings override that turns remoteControlAtStartup off, so its sessions do
// not register for Remote Control (no mobile clutter, ga-629k).
func TestResolveTemplate_PoolAgentDisablesRemoteControl(t *testing.T) {
	cityPath := t.TempDir()
	params := resolveTemplateRemoteControlParams(t, cityPath)

	off := false
	agent := &config.Agent{Name: "dog", RemoteControl: &off}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if !strings.Contains(tp.Command, "remoteControlAtStartup") {
		t.Fatalf("pool agent command must carry inline remote-control override; got %q", tp.Command)
	}
	if !strings.Contains(tp.Command, "false") {
		t.Fatalf("pool agent override must set remoteControlAtStartup=false; got %q", tp.Command)
	}
	// Single --settings flag — no double-flag ambiguity.
	if strings.Count(tp.Command, "--settings") != 1 {
		t.Fatalf("expected exactly one --settings flag, got %q", tp.Command)
	}
}

// TestResolveTemplate_CrewAgentKeepsRemoteControl verifies the counterpart:
// the Mayor / named crew (remote_control unset) keep the plain file-path
// --settings arg and do NOT get the remote-control override, so they still
// register for Remote Control (operator can drive them from a phone).
func TestResolveTemplate_CrewAgentKeepsRemoteControl(t *testing.T) {
	cityPath := t.TempDir()
	params := resolveTemplateRemoteControlParams(t, cityPath)

	agent := &config.Agent{Name: "mayor"} // RemoteControl unset → inherit ON
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if strings.Contains(tp.Command, "remoteControlAtStartup") {
		t.Fatalf("crew agent must NOT carry the remote-control override; got %q", tp.Command)
	}
	// It should still pass the managed settings file path.
	if !strings.Contains(tp.Command, "--settings") {
		t.Fatalf("crew agent should still receive a --settings arg; got %q", tp.Command)
	}
	if !strings.Contains(tp.Command, filepath.Join(".gc", "settings.json")) {
		t.Fatalf("crew agent --settings should point at the managed settings file; got %q", tp.Command)
	}
}
