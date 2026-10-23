package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// ga-q2i6b7 (gt-wvey3): a rig agent runs from its agent home under the city,
// which is not inside any bound rig path. gc bd used to fall through to the
// city store there, while gc hook resolved the same identity to the rig store.

type sessionRigFixture struct {
	cityDir    string
	frontend   string
	backend    string
	agentHome  string // witness home: under the city, inside no rig
	otherHome  string // another dir under the city, inside no rig
	cfg        *config.City
	configToml string
}

func newSessionRigFixture(t *testing.T) sessionRigFixture {
	t.Helper()
	disableManagedDoltRecoveryForTest(t)
	cityDir := filepath.Join(t.TempDir(), "city")
	f := sessionRigFixture{
		cityDir:   cityDir,
		frontend:  filepath.Join(cityDir, "frontend"),
		backend:   filepath.Join(cityDir, "backend"),
		agentHome: filepath.Join(cityDir, ".gc", "agents", "frontend", "witness"),
		otherHome: filepath.Join(cityDir, ".gc", "agents", "dogs", "dog-1"),
		configToml: `[workspace]
name = "demo"
prefix = "hq"

[[rigs]]
name = "frontend"
path = "frontend"
prefix = "fr"

[[rigs]]
name = "backend"
path = "backend"
prefix = "be"
`,
		cfg: &config.City{
			Workspace: config.Workspace{Name: "demo", Prefix: "hq"},
			Rigs: []config.Rig{
				{Name: "frontend", Path: "frontend", Prefix: "fr"},
				{Name: "backend", Path: "backend", Prefix: "be"},
			},
		},
	}
	for _, dir := range []string{
		filepath.Join(cityDir, ".beads"),
		filepath.Join(f.frontend, ".beads"),
		filepath.Join(f.backend, ".beads"),
		f.agentHome,
		f.otherHome,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(f.configToml), 0o644); err != nil {
		t.Fatal(err)
	}
	probe := bdBeadExists
	bdBeadExists = func(string, execStoreTarget, string) bool { return false }
	t.Cleanup(func() { bdBeadExists = probe })
	return f
}

// sessionEnv exports what template_resolve.go gives a rig-scoped agent.
func (f sessionRigFixture) sessionEnv(t *testing.T, rig, rigRoot string) {
	t.Helper()
	t.Setenv("GC_RIG", rig)
	t.Setenv("GC_RIG_ROOT", rigRoot)
	t.Setenv("BEADS_DIR", filepath.Join(rigRoot, ".beads"))
	t.Setenv("GC_BEADS_SCOPE_ROOT", rigRoot)
}

func TestResolveBdScopeTargetUsesSessionRigOutsideAnyRigPath(t *testing.T) {
	f := newSessionRigFixture(t)
	f.sessionEnv(t, "frontend", f.frontend)
	setCwd(t, f.agentHome)

	var stderr bytes.Buffer
	got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"mol", "wisp", "some-formula"}, true, &stderr)
	if err != nil {
		t.Fatalf("resolveBdScopeTargetForSession() error = %v", err)
	}
	want := execStoreTarget{ScopeRoot: f.frontend, ScopeKind: "rig", Prefix: "fr", RigName: "frontend"}
	if got != want {
		t.Fatalf("target = %#v, want %#v", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// The same identity must land on the same store whether it asks gc hook or
// gc bd (Achado 2): the hook side is agentCommandDir.
func TestResolveBdScopeTargetAgreesWithHookStoreForRigAgent(t *testing.T) {
	f := newSessionRigFixture(t)
	f.sessionEnv(t, "frontend", f.frontend)
	setCwd(t, f.agentHome)

	witness := &config.Agent{Name: "witness", Dir: "frontend"}
	hookDir := agentCommandDir(f.cityDir, witness, f.cfg.Rigs)

	got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("resolveBdScopeTargetForSession() error = %v", err)
	}
	if !samePath(got.ScopeRoot, hookDir) {
		t.Fatalf("gc bd scope root = %q, gc hook store dir = %q; same identity must resolve to one store", got.ScopeRoot, hookDir)
	}
}

func TestResolveBdScopeTargetSessionRigDoesNotOverrideExplicitChoices(t *testing.T) {
	f := newSessionRigFixture(t)
	f.sessionEnv(t, "frontend", f.frontend)
	setCwd(t, f.agentHome)

	t.Run("explicit --rig", func(t *testing.T) {
		got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "backend", []string{"list"}, true, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		if got.RigName != "backend" || !samePath(got.ScopeRoot, f.backend) {
			t.Fatalf("target = %#v, want backend rig", got)
		}
	})

	t.Run("existing bead of another rig", func(t *testing.T) {
		orig := bdBeadExists
		bdBeadExists = func(_ string, target execStoreTarget, beadID string) bool {
			return target.RigName == "backend" && beadID == "be-123"
		}
		defer func() { bdBeadExists = orig }()
		got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"show", "be-123"}, true, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		if got.RigName != "backend" || !samePath(got.ScopeRoot, f.backend) {
			t.Fatalf("target = %#v, want backend rig (bead prefix wins over session rig)", got)
		}
	})

	t.Run("explicit --city", func(t *testing.T) {
		got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, false, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		if got.ScopeKind != "city" || !samePath(got.ScopeRoot, f.cityDir) {
			t.Fatalf("target = %#v, want city scope when the caller named a city", got)
		}
	})
}

func TestResolveBdScopeTargetCityScopedSessionStaysOnCity(t *testing.T) {
	f := newSessionRigFixture(t)
	// A city agent exports GC_RIG="" on purpose (template_resolve.go).
	t.Setenv("GC_RIG", "")
	t.Setenv("GC_RIG_ROOT", "")
	t.Setenv("GC_BEADS_SCOPE_ROOT", f.cityDir)
	setCwd(t, f.otherHome)

	got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ScopeKind != "city" || !samePath(got.ScopeRoot, f.cityDir) {
		t.Fatalf("target = %#v, want city scope", got)
	}
}

func TestResolveBdScopeTargetTrustsBareGCRigWithoutScopeRoot(t *testing.T) {
	f := newSessionRigFixture(t)
	t.Setenv("GC_RIG", "backend")
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
	setCwd(t, f.otherHome)

	got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got.RigName != "backend" || !samePath(got.ScopeRoot, f.backend) {
		t.Fatalf("target = %#v, want backend rig", got)
	}
}

// A stale GC_RIG (tmux-server env that outlived its session, a rig that was
// removed, a scope root that points elsewhere) must not break gc bd: warn and
// resolve the way the pre-session code did.
func TestResolveBdScopeTargetStaleSessionRigWarnsAndFallsBack(t *testing.T) {
	tests := []struct {
		name      string
		rig       string
		scopeRoot func(f sessionRigFixture) string
		wantWarn  string
	}{
		{
			name:      "rig not in this city",
			rig:       "ghost",
			scopeRoot: func(f sessionRigFixture) string { return f.cityDir },
			wantWarn:  `GC_RIG="ghost" is not a rig of this city`,
		},
		{
			name:      "scope root names another store",
			rig:       "frontend",
			scopeRoot: func(f sessionRigFixture) string { return f.backend },
			wantWarn:  "treating the session environment as stale",
		},
		{
			name:      "city scope root with a rig name",
			rig:       "frontend",
			scopeRoot: func(f sessionRigFixture) string { return f.cityDir },
			wantWarn:  "treating the session environment as stale",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSessionRigFixture(t)
			t.Setenv("GC_RIG", tt.rig)
			t.Setenv("GC_BEADS_SCOPE_ROOT", tt.scopeRoot(f))
			setCwd(t, f.otherHome)

			var stderr bytes.Buffer
			got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &stderr)
			if err != nil {
				t.Fatalf("stale env must not be a hard failure: %v", err)
			}
			if got.ScopeKind != "city" || !samePath(got.ScopeRoot, f.cityDir) {
				t.Fatalf("target = %#v, want the pre-session fallback (city)", got)
			}
			if !strings.Contains(stderr.String(), tt.wantWarn) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tt.wantWarn)
			}
		})
	}
}

func TestResolveBdScopeTargetUnboundSessionRigWarnsAndFallsBack(t *testing.T) {
	f := newSessionRigFixture(t)
	f.cfg.Rigs = append(f.cfg.Rigs, config.Rig{Name: "floating", Prefix: "fl"})
	t.Setenv("GC_RIG", "floating")
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
	setCwd(t, f.otherHome)

	var stderr bytes.Buffer
	got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if got.ScopeKind != "city" {
		t.Fatalf("target = %#v, want city fallback for an unbound session rig", got)
	}
	if !strings.Contains(stderr.String(), "has no path binding") {
		t.Fatalf("stderr = %q, want the unbound-rig warning", stderr.String())
	}
}

func TestResolveBdScopeTargetNotesWhenCwdRigDiffersFromSessionRig(t *testing.T) {
	f := newSessionRigFixture(t)
	f.sessionEnv(t, "frontend", f.frontend)

	t.Run("cwd in another rig", func(t *testing.T) {
		setCwd(t, f.backend)
		var stderr bytes.Buffer
		got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		if got.RigName != "frontend" {
			t.Fatalf("target = %#v, want the session rig", got)
		}
		for _, want := range []string{`cwd is in rig "backend"`, `bound to rig "frontend"`, "--rig backend"} {
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), want)
			}
		}
	})

	t.Run("cwd in the session rig", func(t *testing.T) {
		setCwd(t, f.frontend)
		var stderr bytes.Buffer
		got, err := resolveBdScopeTargetForSession(f.cfg, f.cityDir, "", []string{"list"}, true, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		if got.RigName != "frontend" {
			t.Fatalf("target = %#v, want frontend", got)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want no note when cwd and session agree", stderr.String())
		}
	})
}

// The end-to-end shape of the bug: `gc bd mol wisp` from a rig agent's home
// must reach bd with the rig store, not the city store.
func TestGcBdRoutesToSessionRigFromAgentHome(t *testing.T) {
	f := newSessionRigFixture(t)
	origCityFlag, origRigFlag := cityFlag, rigFlag
	defer func() { cityFlag, rigFlag = origCityFlag, origRigFlag }()
	cityFlag, rigFlag = "", ""

	f.sessionEnv(t, "frontend", f.frontend)
	t.Setenv("GC_CITY_PATH", f.cityDir)
	setCwd(t, f.agentHome)

	got := runFakeBd(t, []string{"mol", "wisp", "some-formula"})
	if got["args"] != "mol wisp some-formula" {
		t.Fatalf("args = %q", got["args"])
	}
	if got["GC_STORE_SCOPE"] != "rig" || !samePath(got["GC_STORE_ROOT"], f.frontend) {
		t.Fatalf("store = %s at %q, want rig at %q (bare gc bd fell through to the city store)", got["GC_STORE_SCOPE"], got["GC_STORE_ROOT"], f.frontend)
	}
	if !samePath(got["pwd"], f.frontend) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], f.frontend)
	}
	if got["GC_RIG"] != "frontend" {
		t.Fatalf("GC_RIG = %q, want frontend", got["GC_RIG"])
	}
	if !samePath(filepath.Dir(got["BEADS_DIR"]), f.frontend) {
		t.Fatalf("BEADS_DIR = %q, want it under %q", got["BEADS_DIR"], f.frontend)
	}
}

func TestGcBdExplicitCityFlagIgnoresSessionRig(t *testing.T) {
	f := newSessionRigFixture(t)
	origCityFlag, origRigFlag := cityFlag, rigFlag
	defer func() { cityFlag, rigFlag = origCityFlag, origRigFlag }()
	cityFlag, rigFlag = "", ""

	f.sessionEnv(t, "frontend", f.frontend)
	t.Setenv("GC_CITY_PATH", f.cityDir)
	setCwd(t, f.agentHome)

	got := runFakeBd(t, []string{"--city", f.cityDir, "context", "--json"})
	if got["GC_STORE_SCOPE"] != "city" || !samePath(got["GC_STORE_ROOT"], f.cityDir) {
		t.Fatalf("store = %s at %q, want city at %q when --city is explicit", got["GC_STORE_SCOPE"], got["GC_STORE_ROOT"], f.cityDir)
	}
}

// runFakeBd runs doBd against a stub bd that records what it was handed.
func runFakeBd(t *testing.T, args []string) map[string]string {
	t.Helper()
	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-capture.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
  printf 'GC_RIG=%s\n' "${GC_RIG:-}"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capture)

	var stdout, stderr bytes.Buffer
	if rc := doBd(args, &stdout, &stderr); rc != 0 {
		t.Fatalf("doBd(%v) = %d, want 0; stderr=%q", args, rc, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			got[key] = value
		}
	}
	return got
}
