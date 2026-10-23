package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The shell lifecycle script is the one door every start/recover walks
// through (gc dolt start, gc dolt restart, the supervisor's provider ops, the
// city's watchdogs), so it carries the hold check too (ga-3risy2). These tests
// run the real script with a minimal environment: under a hold it must exit
// before it takes the start lock, touches the pid file or looks for a server.
//
// Deliberately NOT tested here: what the script does once the hold is gone.
// That path pgrep's for dolt servers and would start one; the release logic is
// covered by the dolt-hold.sh table in TestReadManagedDoltHoldStates.
func TestGCBeadsBdRefusesStartAndRecoverUnderHold(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script := filepath.Join(repoRootForLint(t), "examples", "bd", "assets", "scripts", "gc-beads-bd.sh")

	for _, op := range []string{"start", "recover"} {
		t.Run(op, func(t *testing.T) {
			cityPath := t.TempDir()
			stateDir := filepath.Join(cityPath, "state")
			if err := os.MkdirAll(stateDir, 0o755); err != nil {
				t.Fatal(err)
			}
			holdFile := filepath.Join(stateDir, "dolt-hold")
			if err := os.WriteFile(holdFile, []byte("owner=mayor\nreason=hq table swap\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("sh", script, op)
			cmd.Env = []string{
				"PATH=/usr/bin:/bin",
				"HOME=" + cityPath,
				"GC_CITY_PATH=" + cityPath,
				"GC_PACK_STATE_DIR=" + stateDir,
			}
			out, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("gc-beads-bd %s under a hold: err = %v, want exit 1 (exit 2 would read as success to the Go caller)\n%s", op, err, out)
			}
			for _, want := range []string{"refused", "on hold", "mayor", "hq table swap", "remove the file"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("gc-beads-bd %s output %q lacks %q", op, out, want)
				}
			}
			// Nothing past the gate ran: no start lock, no pid file, no state.
			for _, name := range []string{"dolt.lock", "dolt.pid", "dolt-provider-state.json", "dolt-config.yaml"} {
				if _, statErr := os.Stat(filepath.Join(stateDir, name)); !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("%s exists after a refused %s (stat err = %v)", name, op, statErr)
				}
			}
		})
	}
}

// The prelude of gc-beads-bd.sh carries a fallback dolt_hold_blocks for
// harnesses that ship the script without its sibling helper. Its comment
// promises it "can only block MORE than the helper, never less"; this holds it
// to that. Each case is a hold-file situation; wherever the real helper
// blocks, the fallback must block too. It may also block where the helper
// releases (an expired hold: the fallback does not read expiry), so the table
// pins only the direction that protects Dolt, plus the one case where both
// must agree on "no hold" so the fallback is not a blanket refusal.
func TestGCBeadsBdFallbackHoldReaderNeverBlocksLessThanHelper(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	script, err := os.ReadFile(filepath.Join(repoRootForLint(t), "examples", "bd", "assets", "scripts", "gc-beads-bd.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "\n    dolt_hold_blocks() {\n"
	i := strings.Index(string(script), start)
	if i < 0 {
		t.Fatal("fallback dolt_hold_blocks not found in gc-beads-bd.sh")
	}
	body := string(script)[i+1:]
	j := strings.Index(body, "\n    }\n")
	if j < 0 {
		t.Fatal("end of fallback dolt_hold_blocks not found in gc-beads-bd.sh")
	}
	fallback := body[:j+len("\n    }\n")]

	fallbackBlocks := func(t *testing.T, holdPath string) bool {
		t.Helper()
		cmd := exec.Command("sh", "-c", fallback+`if dolt_hold_blocks; then echo BLOCK; else echo FREE; fi`)
		// In production gc-beads-bd always has PACK_STATE_DIR by the time it
		// gets here (it mkdir -p's it first); only the "no path" case leaves
		// it empty.
		stateDir := ""
		if holdPath != "" {
			stateDir = filepath.Dir(holdPath)
		}
		cmd.Env = append(os.Environ(), "GC_DOLT_HOLD_FILE="+holdPath, "PACK_STATE_DIR="+stateDir)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("running the fallback: %v\n%s", runErr, out)
		}
		return strings.TrimSpace(string(out)) == "BLOCK"
	}

	type scenario struct {
		name     string
		setup    func(t *testing.T, holdPath string)
		skipAs0  bool
		wantFree bool // both readers must say "no hold"
		// wantBlock: both readers must hold. Without it a scenario where the
		// helper lets the path through would pass the "never blocks less"
		// check below for the wrong reason (nothing to be looser than).
		wantBlock bool
	}
	mkParent := func(t *testing.T, holdPath string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(holdPath), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scenarios := []scenario{
		{name: "parent directory absent", setup: func(t *testing.T, holdPath string) {}, wantFree: true},
		{name: "file absent", setup: mkParent, wantFree: true},
		{name: "empty file", setup: func(t *testing.T, holdPath string) { writeHoldFile(t, holdPath, "") }},
		{name: "unexpired hold", setup: func(t *testing.T, holdPath string) {
			writeHoldFile(t, holdPath, fmt.Sprintf("expires_at=%d\n", time.Now().Add(time.Hour).Unix()))
		}},
		{name: "expires_at not a number", setup: func(t *testing.T, holdPath string) { writeHoldFile(t, holdPath, "expires_at=soon\n") }},
		{name: "directory in place of the file", setup: func(t *testing.T, holdPath string) {
			if err := os.MkdirAll(holdPath, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink whose target is missing", setup: func(t *testing.T, holdPath string) {
			mkParent(t, holdPath)
			if err := os.Symlink(filepath.Join(filepath.Dir(holdPath), "no-such-target"), holdPath); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parent is a symlink whose target is missing", setup: func(t *testing.T, holdPath string) {
			parent := filepath.Dir(holdPath)
			if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(parent), "no-such-dir"), parent); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a file where the parent directory should be", setup: func(t *testing.T, holdPath string) {
			parent := filepath.Dir(holdPath)
			if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parent not searchable", skipAs0: true, setup: func(t *testing.T, holdPath string) {
			writeHoldFile(t, holdPath, "owner=x\n")
			parent := filepath.Dir(holdPath)
			if err := os.Chmod(parent, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		}},
		{name: "ancestor above the parent not searchable", skipAs0: true, wantBlock: true, setup: func(t *testing.T, holdPath string) {
			writeHoldFile(t, holdPath, "owner=x\n")
			packs := filepath.Dir(filepath.Dir(holdPath))
			if err := os.Chmod(packs, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(packs, 0o755) })
		}},
		{name: "a file where an ancestor above the parent should be", wantBlock: true, setup: func(t *testing.T, holdPath string) {
			packs := filepath.Dir(filepath.Dir(holdPath))
			if err := os.MkdirAll(filepath.Dir(packs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(packs, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "ancestor above the parent is a symlink whose target is missing", wantBlock: true, setup: func(t *testing.T, holdPath string) {
			packs := filepath.Dir(filepath.Dir(holdPath))
			if err := os.MkdirAll(filepath.Dir(packs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(packs), "no-such-dir"), packs); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "file not readable", skipAs0: true, setup: func(t *testing.T, holdPath string) {
			writeHoldFile(t, holdPath, "owner=x\n")
			if err := os.Chmod(holdPath, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(holdPath, 0o644) })
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			if sc.skipAs0 && os.Geteuid() == 0 {
				t.Skip("root ignores file modes")
			}
			_, holdPath := holdTestCity(t)
			sc.setup(t, holdPath)
			helper, msg := shellHoldBlocks(t, holdPath)
			fb := fallbackBlocks(t, holdPath)
			if sc.wantFree && (helper || fb) {
				t.Fatalf("no hold here, but helper blocks=%v (%q) fallback blocks=%v", helper, msg, fb)
			}
			if sc.wantBlock && (!helper || !fb) {
				t.Fatalf("this path must hold, but helper blocks=%v (%q) fallback blocks=%v", helper, msg, fb)
			}
			if helper && !fb {
				t.Fatalf("helper blocks (%q) but the fallback lets it through — \"never blocks less\" is false", msg)
			}
		})
	}

	t.Run("no path to look at", func(t *testing.T) {
		helper, msg := shellHoldBlocks(t, "")
		if !helper {
			t.Fatalf("helper should hold when it has no path to look at, got free (%q)", msg)
		}
		if !fallbackBlocks(t, "") {
			t.Fatal("helper holds when there is no path to look at, the fallback lets it through")
		}
	})
}
