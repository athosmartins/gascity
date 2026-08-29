package dolt_test

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestHealthScriptOrphanCheckDegradesWhenRigDiscoveryFails is the
// regression guard for ga-eu2x: when `gc rig list --json` fails (the
// real incident was a 5s bound racing 8-17s of Dolt load, but any
// failure/timeout is the same signal from the script's point of view),
// metadata_files() used to silently fall back to an HQ-only scan and the
// orphan check below then reported EVERY other database directory as a
// confirmed orphan — including live production databases whose only
// "crime" was living in a rig outside $GC_CITY_PATH/rigs (true for every
// externally-cloned rig; verified live against whatsapp_automation,
// gastown, dc, lexbh, marketing, property_scrapers). The health report
// must instead flag the scan as degraded, and the human-readable output
// must not present those directories as confirmed orphans — Mayor's
// 2026-07-31 comment on ga-eu2x identifies this report as the one
// remaining live trigger for a human/agent to run the destructive
// `gc dolt cleanup --force` (space form) by hand.
func TestHealthScriptOrphanCheckDegradesWhenRigDiscoveryFails(t *testing.T) {
	cityPath := t.TempDir()
	fakeBin := t.TempDir()
	dataDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, ".beads", "metadata.json"),
		[]byte(`{"dolt_database":"hq"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stand-ins for hq (referenced) plus two "production" databases that
	// are NOT referenced by the only metadata.json this city can see once
	// rig discovery fails — matching the real incident's shape.
	for _, name := range []string{"hq", "whatsapp_automation", "gastown"} {
		if err := os.MkdirAll(filepath.Join(dataDir, name, ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Fake gc: `gc rig list --json` always fails — reproduces the trigger
	// (a hard failure and a run_bounded timeout look identical to the
	// script: both leave rig_list_json unset and the `if rig_list_json=...`
	// branch false).
	writeExecutable(t, filepath.Join(fakeBin, "gc"), "#!/bin/sh\nexit 1\n")
	writeExecutable(t, filepath.Join(fakeBin, "dolt"), "#!/bin/sh\nexit 1\n")

	root := repoRoot(t)
	baseEnv := append(
		filteredEnv("GC_CITY_PATH", "GC_PACK_DIR", "GC_DOLT_HOST", "GC_DOLT_PORT",
			"GC_DOLT_USER", "GC_DOLT_PASSWORD", "GC_HEALTH_SKIP_ZOMBIE_SCAN",
			"GC_DOLT_DATA_DIR", "PATH"),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_HOST=127.0.0.1",
		"GC_DOLT_PORT="+strconv.Itoa(freeTCPPortForTest(t)),
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
		"GC_HEALTH_SKIP_ZOMBIE_SCAN=1",
		"GC_DOLT_DATA_DIR="+dataDir,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)

	jsonCmd := exec.Command("sh", filepath.Join(root, healthScript), "--json")
	jsonCmd.Env = baseEnv
	out, err := jsonCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health.sh --json failed: %v\n%s", err, out)
	}

	var report struct {
		OrphanCheckDegraded bool `json:"orphan_check_degraded"`
		Orphans             []struct {
			Name string `json:"name"`
		} `json:"orphans"`
	}
	if jsonErr := json.Unmarshal(out, &report); jsonErr != nil {
		t.Fatalf("health.sh --json returned invalid JSON: %v\n%s", jsonErr, out)
	}

	if !report.OrphanCheckDegraded {
		t.Errorf("orphan_check_degraded = false, want true: gc rig list failed, "+
			"so the orphan list cannot be trusted — got:\n%s", out)
	}

	// Human-readable mode is the actual live risk vector per Mayor's
	// analysis (a human/agent reads this and may act on it). It must
	// never present whatsapp_automation/gastown as confirmed orphans
	// without an equally prominent degraded warning.
	humanCmd := exec.Command("sh", filepath.Join(root, healthScript))
	humanCmd.Env = baseEnv
	humanOut, _ := humanCmd.CombinedOutput() // non-zero exit expected: server unreachable
	human := string(humanOut)

	if strings.Contains(human, "whatsapp_automation") && !strings.Contains(strings.ToLower(human), "degraded") {
		t.Errorf("human-readable output lists whatsapp_automation without a degraded warning:\n%s", human)
	}
	if strings.Contains(human, "Orphans: 2") {
		t.Errorf("human-readable output reports a confirmed orphan count from a degraded scan:\n%s", human)
	}
}

func freeTCPPortForTest(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}
