package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoteControlSettingsArg_OverlaysOntoCitySettings verifies FIX A (ga-629k):
// when an agent opts out of Remote Control, the engine emits a SINGLE inline
// --settings JSON arg that overlays {"remoteControlAtStartup": false} onto the
// city's managed settings file (preserving hooks), instead of the plain
// file-path arg. This is the path a pool/system worker session takes.
func TestRemoteControlSettingsArg_OverlaysOntoCitySettings(t *testing.T) {
	cityPath := t.TempDir()
	gcDir := filepath.Join(cityPath, ".gc")
	if err := os.MkdirAll(gcDir, 0o755); err != nil {
		t.Fatalf("mkdir .gc: %v", err)
	}
	// Managed city settings with hooks + remote-control ON (inherited default).
	settings := map[string]any{
		"remoteControlAtStartup": true,
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"matcher": "startup"}},
		},
		"skipDangerousModePermissionPrompt": true,
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(gcDir, "settings.json"), data, 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	arg, err := RemoteControlSettingsArg(cityPath, "claude")
	if err != nil {
		t.Fatalf("RemoteControlSettingsArg: %v", err)
	}
	if !strings.HasPrefix(arg, "--settings ") {
		t.Fatalf("expected --settings arg, got %q", arg)
	}
	// Exactly one --settings flag (no double-flag ambiguity).
	if strings.Count(arg, "--settings") != 1 {
		t.Fatalf("expected exactly one --settings flag, got %q", arg)
	}
	// Extract and parse the inline JSON payload.
	payload := strings.TrimPrefix(arg, "--settings ")
	payload = strings.Trim(payload, "'\"")
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload is not valid JSON (%q): %v", payload, err)
	}
	if rc, ok := got["remoteControlAtStartup"].(bool); !ok || rc {
		t.Fatalf("expected remoteControlAtStartup=false, got %v", got["remoteControlAtStartup"])
	}
	// Managed hooks must be preserved in the overlay (workers still get
	// gc prime / nudge / handoff hooks).
	if _, ok := got["hooks"]; !ok {
		t.Fatalf("expected managed hooks preserved in overlay, got %v", got)
	}
	if v, ok := got["skipDangerousModePermissionPrompt"].(bool); !ok || !v {
		t.Fatalf("expected skipDangerousModePermissionPrompt preserved=true, got %v", got["skipDangerousModePermissionPrompt"])
	}
}

// TestRemoteControlSettingsArg_NoFileEmitsBareOverride verifies the override is
// still produced (just the single key) when no managed settings file exists.
func TestRemoteControlSettingsArg_NoFileEmitsBareOverride(t *testing.T) {
	cityPath := t.TempDir()
	arg, err := RemoteControlSettingsArg(cityPath, "claude")
	if err != nil {
		t.Fatalf("RemoteControlSettingsArg: %v", err)
	}
	payload := strings.TrimPrefix(arg, "--settings ")
	payload = strings.Trim(payload, "'\"")
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if rc, ok := got["remoteControlAtStartup"].(bool); !ok || rc {
		t.Fatalf("expected remoteControlAtStartup=false, got %v", got["remoteControlAtStartup"])
	}
}

// TestRemoteControlSettingsArg_NonClaudeNoOp verifies non-claude providers get
// an empty string so callers fall through to their default settings handling.
func TestRemoteControlSettingsArg_NonClaudeNoOp(t *testing.T) {
	arg, err := RemoteControlSettingsArg(t.TempDir(), "codex")
	if err != nil {
		t.Fatalf("RemoteControlSettingsArg: %v", err)
	}
	if arg != "" {
		t.Fatalf("expected empty arg for non-claude provider, got %q", arg)
	}
}

// TestIsSingleIdentitySession verifies FIX B (ga-i67t) agent classification:
// identity-bound crew agents are single-instance; expansion pools (dog/polecat)
// are NOT — they keep multi-instance behavior.
func TestIsSingleIdentitySession(t *testing.T) {
	intp := func(v int) *int { return &v }
	cases := []struct {
		name  string
		agent Agent
		want  bool
	}{
		{
			name:  "uncapped crew (digo) is single-identity",
			agent: Agent{Name: "digo"},
			want:  true,
		},
		{
			name:  "max=1 no pool controls is single-identity",
			agent: Agent{Name: "mayor", MaxActiveSessions: intp(1)},
			want:  true,
		},
		{
			name:  "dog pool (min set, max=3) is NOT single-identity",
			agent: Agent{Name: "dog", MinActiveSessions: intp(0), MaxActiveSessions: intp(3)},
			want:  false,
		},
		{
			name:  "polecat pool (min set, max=5) is NOT single-identity",
			agent: Agent{Name: "polecat", MinActiveSessions: intp(0), MaxActiveSessions: intp(5)},
			want:  false,
		},
		{
			name:  "scale_check agent is NOT single-identity",
			agent: Agent{Name: "scaler", ScaleCheck: "echo 2"},
			want:  false,
		},
		{
			name:  "namepool agent is NOT single-identity",
			agent: Agent{Name: "fleet", NamepoolNames: []string{"a", "b"}},
			want:  false,
		},
		{
			name:  "explicit max>1 with no other controls is NOT single-identity",
			agent: Agent{Name: "wide", MaxActiveSessions: intp(4)},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.agent.IsSingleIdentitySession(); got != tc.want {
				t.Fatalf("IsSingleIdentitySession()=%v, want %v", got, tc.want)
			}
		})
	}
}
