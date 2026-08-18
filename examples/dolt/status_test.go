// Package dolt_test also covers the dolt pack's status.sh script: it must
// always print a state line and use a distinct exit code for each of
// running / not running / indeterminate. See ga-dzado.
package dolt_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// statusScript is the on-disk path to the status command script, mirroring
// healthScript's convention (commands/<name>/run.sh under the pack root).
const statusScript = "commands/status/run.sh"

// writeFakeBeadsBdProbe installs a fake gc-beads-bd.sh at the exact path
// runtime.sh resolves GC_BEADS_BD_SCRIPT to ($GC_CITY_PATH/.gc/system/packs/
// bd/assets/scripts/gc-beads-bd.sh), so status.sh's probe call is driven by
// a script we control instead of a real Dolt server. exitCode is returned
// only when the first argument is "probe" (op_probe's own contract); any
// other operation exits 1, matching gc-beads-bd.sh's real dispatch default
// for an op this fake doesn't implement.
func writeFakeBeadsBdProbe(t *testing.T, cityPath string, exitCode int) {
	t.Helper()
	dir := filepath.Join(cityPath, ".gc", "system", "packs", "bd", "assets", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir fake gc-beads-bd dir: %v", err)
	}
	body := "#!/bin/sh\nif [ \"$1\" = probe ]; then exit " + strconv.Itoa(exitCode) + "; fi\nexit 1\n"
	writeExecutable(t, filepath.Join(dir, "gc-beads-bd.sh"), body)
}

func runStatusScript(t *testing.T, cityPath string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("sh", filepath.Join(root, statusScript))
	cmd.Env = append(filteredEnv("GC_CITY_PATH", "GC_PACK_DIR", "GC_DOLT_HOST", "GC_DOLT_PORT", "GC_DOLT_USER", "GC_DOLT_PASSWORD"),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_HOST=127.0.0.1",
		// Any syntactically valid port works: the fake gc-beads-bd.sh never
		// consults it. This only has to satisfy runtime.sh's port resolution
		// (resolve_dolt_port_or_die) so sourcing it doesn't die before
		// status.sh's own probe-handling logic runs.
		"GC_DOLT_PORT=1",
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("status.sh produced non-exit error: %v", err)
	}
	return exitErr.ExitCode()
}

// TestStatusScriptReportsRunning is the baseline: a probe that confirms the
// server is up must print "running" and exit 0.
func TestStatusScriptReportsRunning(t *testing.T) {
	cityPath := t.TempDir()
	writeFakeBeadsBdProbe(t, cityPath, 0)

	out, err := runStatusScript(t, cityPath)
	if got := exitCodeOf(t, err); got != 0 {
		t.Fatalf("status.sh exit code = %d, want 0\noutput: %q", got, out)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("status.sh output = %q, want it to contain %q", out, "running")
	}
}

// TestStatusScriptReportsNotRunning covers the confirmed-down case: probe
// exits 2 (gc-beads-bd.sh's own "not running" contract). status.sh must
// print a distinct "not running" line and exit 1.
func TestStatusScriptReportsNotRunning(t *testing.T) {
	cityPath := t.TempDir()
	writeFakeBeadsBdProbe(t, cityPath, 2)

	out, err := runStatusScript(t, cityPath)
	if got := exitCodeOf(t, err); got != 1 {
		t.Fatalf("status.sh exit code = %d, want 1\noutput: %q", got, out)
	}
	if !strings.Contains(out, "not running") {
		t.Fatalf("status.sh output = %q, want it to contain %q", out, "not running")
	}
}

// TestStatusScriptReportsUnknownOnIndeterminateProbe is the regression
// guard for ga-dzado: the actual bug measured live was `gc dolt status`
// exiting 0 with ZERO bytes of output, because the old script discarded the
// probe's exit code into a bare `set -e` tail call and never echoed
// anything on any path. This drives a probe exit code that is neither 0 nor
// 2 (op_probe's only two documented outcomes) -- e.g. a `die()` in
// gc-beads-bd.sh's prelude, or any other failure above the probe dispatch --
// and asserts the indeterminate case is reported distinctly from a
// confirmed-down result, not silently folded into it and not silently
// dropped.
func TestStatusScriptReportsUnknownOnIndeterminateProbe(t *testing.T) {
	// 17 is deliberately not 0, 1, or 2, so this cannot pass by accident
	// via a fix that merely special-cases "anything nonzero is code 1".
	const indeterminateExit = 17
	cityPath := t.TempDir()
	writeFakeBeadsBdProbe(t, cityPath, indeterminateExit)

	out, err := runStatusScript(t, cityPath)
	if strings.TrimSpace(out) == "" {
		t.Fatalf("status.sh printed nothing for an indeterminate probe result -- this is the exact ga-dzado bug (exit 0, zero bytes, \"healthy\" and \"could not check\" indistinguishable)")
	}
	got := exitCodeOf(t, err)
	if got == 0 {
		t.Fatalf("status.sh exited 0 for an indeterminate probe result; want nonzero\noutput: %q", out)
	}
	if got == 1 {
		t.Fatalf("status.sh exited 1 (its own \"not running\" code) for an indeterminate probe result; "+
			"this conflates \"confirmed down\" with \"could not determine\", which is the distinction "+
			"the bead exists to preserve\noutput: %q", out)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("status.sh output = %q, want it to contain %q", out, "UNKNOWN")
	}
}
