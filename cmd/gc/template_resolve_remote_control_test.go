package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// ga-mhyz2: named crews (mila-wa, thies-wa, digo-wa, ...) should name their
// Claude Code Remote Control session after the agent instead of the
// auto-generated "host-adjective-animal" title, so they're identifiable in the
// human's mobile Claude Code app. We achieve this with the
// CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX env var (safe no-op when Remote
// Control is off) rather than claude's --remote-control launch flag (which can
// hard-fail boot when Remote Control is unavailable).

func remoteControlTestParams(t *testing.T, cityPath string) *agentBuildParams {
	t.Helper()
	return &agentBuildParams{
		cityName:  "city",
		cityPath:  cityPath,
		workspace: &config.Workspace{Provider: "claude"},
		providers: map[string]config.ProviderSpec{
			"claude":          config.BuiltinProviderAlias("claude"),
			"claude-headless": config.BuiltinProviderAlias("claude"),
			"test":            {Command: "echo", PromptMode: "none"},
		},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}
}

const remoteControlPrefixEnvKey = "CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX"

func TestResolveTemplateNamesRemoteControlSessionForSingleSessionClaudeCrew(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := remoteControlTestParams(t, cityPath)

	// Single-session claude-family crew (mirrors the live mila-wa / thies-wa /
	// digo-wa agent.toml: max_active_sessions = 1).
	agent := &config.Agent{
		Name:              "mila-wa",
		Provider:          "claude",
		MaxActiveSessions: intPtr(1),
		MinActiveSessions: intPtr(1),
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if got := tp.Env[remoteControlPrefixEnvKey]; got != "mila-wa" {
		t.Fatalf("%s = %q, want %q", remoteControlPrefixEnvKey, got, "mila-wa")
	}
}

func TestResolveTemplateSkipsRemoteControlNameForMultiSessionPool(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := remoteControlTestParams(t, cityPath)

	// Multi-session pool (dog / polecat / reviewer shape: max_active_sessions
	// != 1). These are headless pool builders and must keep their
	// auto-generated names — never prefix them.
	agent := &config.Agent{
		Name:              "dog",
		Provider:          "claude-headless",
		MaxActiveSessions: intPtr(3),
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if got, set := tp.Env[remoteControlPrefixEnvKey]; set {
		t.Fatalf("%s set to %q for a multi-session pool agent; want unset", remoteControlPrefixEnvKey, got)
	}
}

func TestResolveTemplateSkipsRemoteControlNameForNonClaudeProvider(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := remoteControlTestParams(t, cityPath)
	params.workspace = &config.Workspace{Provider: "test"}

	// Single-session, but a non-claude provider that does not understand the
	// claude Remote Control env var.
	agent := &config.Agent{
		Name:              "runner",
		Provider:          "test",
		MaxActiveSessions: intPtr(1),
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if got, set := tp.Env[remoteControlPrefixEnvKey]; set {
		t.Fatalf("%s set to %q for a non-claude provider; want unset", remoteControlPrefixEnvKey, got)
	}
}
