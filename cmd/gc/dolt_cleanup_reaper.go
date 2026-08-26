package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// DoltProcInfo describes a live `dolt sql-server` process candidate.
//
// PID is the OS pid; Argv is the raw command line split on NUL boundaries
// (typically read from /proc/<pid>/cmdline). Ports lists the TCP ports the
// process is listening on, used to cross-reference against active per-rig
// dolt servers so the reaper never touches a production server. RSSBytes is
// the best-effort resident set size used for operator cleanup summaries.
// StartTimeTicks is /proc/<pid>/stat field 22 and lets force-mode revalidation
// detect PID reuse before sending a signal. StartIdentity is a portable
// fallback populated by ps-based discovery on hosts without /proc.
type DoltProcInfo struct {
	PID            int
	Argv           []string
	Ports          []int
	RSSBytes       int64
	StartTimeTicks uint64
	StartIdentity  string
}

// reapClassification is the per-process decision produced by classifyDoltProcess.
//
// Action is "reap" or "protect". For reap, ConfigPath carries the test-config
// path that matched the allowlist. For protect, Reason explains why so the
// operator-facing report can echo it (e.g. "active rig dolt server (rig: beads)").
type reapClassification struct {
	Action     string
	Reason     string
	ConfigPath string
}

// ReapTarget is a single PID slated for SIGTERM+SIGKILL during the reap stage.
type ReapTarget struct {
	PID            int
	ConfigPath     string
	RSSBytes       int64
	StartTimeTicks uint64
	StartIdentity  string
}

// ProtectedProcess is a single PID that the reaper refused to kill, with the
// reason recorded so the report can show operators why nothing was done.
type ProtectedProcess struct {
	PID    int
	Reason string
}

// ReapPlan is the outcome of planOrphanReap. Reap is the orphan list; Protected
// covers production-side rigs and unknown processes that fall outside the
// test-config-path allowlist (e.g. an active benchmark).
type ReapPlan struct {
	Reap      []ReapTarget
	Protected []ProtectedProcess
}

// extractConfigPath pulls the --config <path> argument from a dolt sql-server
// argv. Supports both `--config foo` and `--config=foo` forms; returns empty
// when the flag is absent or has no value.
func extractConfigPath(argv []string) string {
	for i, arg := range argv {
		if arg == "--config" {
			if i+1 < len(argv) {
				return argv[i+1]
			}
			return ""
		}
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	return ""
}

// isTestConfigPath reports whether p matches the cleanup allowlist for test
// Dolt configs: Go test temp roots, plus known Gas City unit-test prefixes
// that use short socket-safe directories under os.TempDir().
func isTestConfigPath(p, homeDir, tempDir string) bool {
	if p == "" {
		return false
	}
	clean := filepath.Clean(p)
	if hasTestChildPrefix(clean, "/tmp", testConfigPathPrefixes()) {
		return true
	}
	if hasTestChildPrefix(clean, tempDir, testConfigPathPrefixes()) {
		return true
	}
	if homeDir == "" {
		return false
	}
	return hasTestChildPrefix(clean, filepath.Join(homeDir, ".gotmp"), []string{"Test"})
}

func testConfigPathPrefixes() []string {
	return []string{
		"Test",
		// Legacy pre-owner-PID cmd/gc test roots. Current cmd/gc roots use
		// the gct<PID>-* prefix and are handled by stale-root owner PID logic.
		"gctest-",
		"gc-state-runtime-builtin-",
		"gc-state-mutation-builtin-",
		"gc-supervisor-city-",
		"gc-reload-invalid-",
		"gc-rename-",
		"gcit-",
		"gc-int-env-",
	}
}

func hasTestChildPrefix(cleanPath, root string, prefixes []string) bool {
	if root == "" {
		return false
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "." || cleanRoot == string(filepath.Separator) {
		return false
	}
	rootPrefix := cleanRoot + string(filepath.Separator)
	if !strings.HasPrefix(cleanPath, rootPrefix) {
		return false
	}
	child := strings.TrimPrefix(cleanPath, rootPrefix)
	for _, prefix := range prefixes {
		if strings.HasPrefix(child, prefix) {
			return true
		}
	}
	return false
}

// underTempRoot reports whether configPath sits anywhere under root — same
// root-safety checks as hasTestChildPrefix (a "." or "/" root never matches
// anything, so a caller passing an unset tempDir can't accidentally match
// every path on the filesystem), but with NO prefix requirement: any child
// path counts, not just ones matching a known allowlisted name pattern.
// Used only by the ga-b5l0v age-gated fallback below — deliberately never by
// isTestConfigPath, which stays exact-prefix-only for its existing callers
// (discoverActiveTestRootsFromPS's "is this arg a plausible test root at
// all" question deliberately keeps the tighter, more conservative check).
func underTempRoot(configPath, root string) bool {
	if root == "" {
		return false
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "." || cleanRoot == string(filepath.Separator) {
		return false
	}
	cleanPath := filepath.Clean(configPath)
	rootPrefix := cleanRoot + string(filepath.Separator)
	return strings.HasPrefix(cleanPath, rootPrefix)
}

// orphanAgeThresholdSeconds is how long a dolt sql-server process must have
// been running — with a --config path under a system temp root but NOT
// matching the specific-prefix allowlist above — before the ga-b5l0v
// fallback below will reap it. The allowlist only recognizes the gc
// binary's OWN Go-test-harness temp-dir conventions (gctest-*,
// gc-state-runtime-builtin-*, etc.); it was never meant to, and does not,
// recognize other tools' throwaway workspaces — e.g. an agent session's
// scratchpad (/tmp/claude-<uid>/.../scratchpad/...) or other ad-hoc /tmp
// roots. Two hours is comfortably above every observed legitimate run in
// this codebase (the largest selftest suite here takes ~12-15 minutes) and
// well under the 4-hour reap-sweep interval, so a slow-but-legitimate run
// is never caught mid-flight even if a sweep lands awkwardly — while still
// being short enough that a genuine orphan (ga-b5l0v: one sat for 2+ days)
// gets caught within the next sweep or two of going idle, not another
// multi-day wait.
const orphanAgeThresholdSeconds = 2 * 60 * 60

func configUnderActiveTestRoot(configPath string, activeTestRoots []string) bool {
	if configPath == "" {
		return false
	}
	cleanConfig := filepath.Clean(configPath)
	for _, root := range activeTestRoots {
		cleanRoot := filepath.Clean(root)
		if cleanRoot == "." || cleanRoot == string(filepath.Separator) {
			continue
		}
		if cleanConfig == cleanRoot || strings.HasPrefix(cleanConfig, cleanRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// classifyDoltProcess applies the architect's reaper decision rules (§4.3) to a
// single dolt sql-server process. Order matters:
//
//  1. Any port match against rigPortByPort → protected (active rig server),
//     even if the cmdline says it's a test path (defense in depth).
//  2. Else extract --config path; matches /tmp/Test*, os.TempDir()/Test*,
//     known Gas City temp prefixes → reap.
//  3. Else protect if the config sits under an active test root.
//  4. Else reap if the config is under a system temp root (broader than
//     step 2's specific prefixes) AND the process has been running longer
//     than orphanAgeThresholdSeconds (ga-b5l0v — see that constant's doc).
//     Both conditions required, same as steps 1-3 above never having relied
//     on a single signal alone: a bare "under /tmp" match isn't sufficient
//     on its own (a genuinely in-progress run, not yet old enough, must
//     stay protected), and age alone isn't checked without also confirming
//     the path is somewhere throwaway in the first place.
//  5. Else protect with a reason that echoes the actual config path so
//     operators can decide whether to kill it manually (architect Open Q 0).
func classifyDoltProcess(p DoltProcInfo, rigPortByPort map[int]string, homeDir, tempDir string, activeTestRoots []string, now time.Time) reapClassification {
	for _, port := range p.Ports {
		if name, ok := rigPortByPort[port]; ok {
			return reapClassification{
				Action: "protect",
				Reason: fmt.Sprintf("active rig dolt server (rig: %s, port: %d)", name, port),
			}
		}
	}

	cfgPath := extractConfigPath(p.Argv)
	if cfgPath == "" {
		return reapClassification{
			Action: "protect",
			Reason: "no --config path detected; refusing to kill an unidentified dolt server",
		}
	}
	if configUnderActiveTestRoot(cfgPath, activeTestRoots) {
		return reapClassification{
			Action:     "protect",
			Reason:     fmt.Sprintf("config %q is under an active test root", cfgPath),
			ConfigPath: cfgPath,
		}
	}
	if isTestConfigPath(cfgPath, homeDir, tempDir) {
		return reapClassification{Action: "reap", ConfigPath: cfgPath}
	}
	if underTempRoot(cfgPath, "/tmp") || underTempRoot(cfgPath, tempDir) {
		if age, ok := processAgeSeconds(p.StartTimeTicks, p.StartIdentity, now); ok && age >= orphanAgeThresholdSeconds {
			return reapClassification{Action: "reap", ConfigPath: cfgPath}
		}
	}
	return reapClassification{
		Action: "protect",
		Reason: fmt.Sprintf("config %q not on test-config-path allowlist; kill manually if not wanted", cfgPath),
		// ConfigPath echoed so the human-readable layout (Wireframe 4) can
		// render the tree-style annotation alongside the port and reason.
		ConfigPath: cfgPath,
	}
}

// planOrphanReap classifies each dolt sql-server process and partitions them
// into reap targets vs protected processes. Order is preserved so the report
// renders deterministically.
func planOrphanReap(procs []DoltProcInfo, rigPortByPort map[int]string, homeDir, tempDir string, activeTestRoots []string, now time.Time) ReapPlan {
	plan := ReapPlan{}
	for _, p := range procs {
		c := classifyDoltProcess(p, rigPortByPort, homeDir, tempDir, activeTestRoots, now)
		switch c.Action {
		case "reap":
			plan.Reap = append(plan.Reap, ReapTarget{
				PID:            p.PID,
				ConfigPath:     c.ConfigPath,
				RSSBytes:       p.RSSBytes,
				StartTimeTicks: p.StartTimeTicks,
				StartIdentity:  p.StartIdentity,
			})
		default:
			plan.Protected = append(plan.Protected, ProtectedProcess{PID: p.PID, Reason: c.Reason})
		}
	}
	return plan
}
