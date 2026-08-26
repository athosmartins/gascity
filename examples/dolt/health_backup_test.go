package dolt_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// health_backup_test.go — regression coverage for ga-soxi9: `gc dolt health`
// used to report backup freshness by globbing "$GC_CITY_PATH"/migration-backup-*,
// an entirely different subsystem (gc dolt rollback's own pre-migration
// snapshots), and never asked Dolt about its actual configured backup
// remotes. "Backups: none found" and "Backups: Xh ago" were both measuring
// data unrelated to whether `dolt backup sync` (mol-dog-backup.sh) was
// actually working. These tests exercise the fixed per-database check
// against a stubbed `dolt` binary that mimics `dolt backup -v`'s real
// output shape (confirmed against the real command on a live city).

type backupJSON struct {
	Name                string `json:"name"`
	State               string `json:"state"`
	AgeSec              int    `json:"age_sec"`
	Path                string `json:"path"`
	SizeBytes           int64  `json:"size_bytes"`
	RemoteCount         int    `json:"remote_count"`
	VerifiedRemoteCount int    `json:"verified_remote_count"`
	Stale               bool   `json:"stale"`
}

type healthJSON struct {
	Backups []backupJSON `json:"backups"`
}

// backupDoltStub returns a fake `dolt` script implementing only
// `dolt --user <u> backup -v`, keyed by the CWD it is invoked from (the real
// health script does `cd "$d" && dolt --user ... backup -v`, so the stub's
// own $PWD tells it which fake database is being queried).
//
// It DELIBERATELY fails with dolt's real observed error text when invoked
// WITHOUT --user — reproducing the exact bug found live on 2026-08-26: this
// script exports DOLT_CLI_PASSWORD (even as "") for the SQL probe, and every
// subsequent `dolt` call inherits it; `dolt backup -v` with no --user then
// hard-fails ("Failed to parse credentials: When a password is provided, a
// user must also be provided"), not a timeout. Since the fixed script always
// passes --user, every scenario built on this stub implicitly re-proves that
// fix holds — if a future edit ever dropped --user, every one of those
// scenarios would flip to state=query-failed instead of its expected state.
func backupDoltStub(byCwdSuffix map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("case \"$*\" in\n")
	b.WriteString("  *'backup -v'*) ;;\n")
	b.WriteString("  *) exit 0 ;;\n")
	b.WriteString("esac\n")
	b.WriteString("case \" $* \" in\n")
	b.WriteString("  *' --user '*) ;;\n")
	b.WriteString("  *) echo 'Failed to parse credentials: When a password is provided, a user must also be provided. Use the --user flag to provide a username' >&2; exit 1 ;;\n")
	b.WriteString("esac\n")
	b.WriteString("cwd=\"$(pwd)\"\n")
	b.WriteString("case \"$cwd\" in\n")
	for suffix, output := range byCwdSuffix {
		b.WriteString("  *'" + suffix + "') printf '%s\\n' '" + output + "'; exit 0 ;;\n")
	}
	b.WriteString("esac\n")
	b.WriteString("exit 0\n")
	return b.String()
}

// healthCmd builds the health script invocation. GC_DOLT_DATA_DIR (not
// DOLT_DATA_DIR) is the correct override: runtime.sh's own sourced logic
// unconditionally recomputes DOLT_DATA_DIR from GC_DOLT_DATA_DIR (or, absent
// that, from GC_CITY_PATH/.beads/dolt or a managed-state fallback) — setting
// DOLT_DATA_DIR directly in the environment is silently clobbered before the
// health script's own `data_dir="$DOLT_DATA_DIR"` line ever reads it
// (confirmed live via `sh -x` while building this test: the env var this
// test originally set was overwritten with a real, unrelated path from this
// machine's live city before the script's own logic ran at all).
//
// GC_DOLT_PORT=1 mirrors status_test.go's own documented pattern
// (writeFakeBeadsBdProbe's caller comment): runtime.sh's port resolution
// accepts any syntactically valid port as an operator-seed fallback when no
// managed-state file resolves one, so this only has to satisfy sourcing —
// the backup loop under test does not depend on server_reachable at all.
func healthCmd(t *testing.T, dataDir, fakeBin string) *exec.Cmd {
	t.Helper()
	root := repoRoot(t)
	cityPath := t.TempDir()
	cmd := exec.Command("sh", filepath.Join(root, healthScript), "--json")
	cmd.Env = append(filteredEnv("GC_CITY_PATH", "GC_PACK_DIR", "GC_DOLT_HOST", "GC_DOLT_PORT",
		"GC_DOLT_USER", "GC_DOLT_PASSWORD", "GC_HEALTH_SKIP_ZOMBIE_SCAN", "GC_DOLT_DATA_DIR", "PATH"),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_HOST=127.0.0.1",
		"GC_DOLT_PORT=1",
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
		"GC_HEALTH_SKIP_ZOMBIE_SCAN=1",
		"GC_DOLT_DATA_DIR="+dataDir,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	return cmd
}

// runHealthJSON runs the health script against a fake data_dir (one
// subdirectory per fake database, each containing a `.dolt` marker so the
// script's own `[ -d "$d/.dolt" ]` gate accepts it) and returns the parsed
// backups array. The server is left unreachable (closed port) since the
// backup loop under test does not depend on server_reachable.
func runHealthJSON(t *testing.T, dataDir, fakeBin string) healthJSON {
	t.Helper()
	cmd := healthCmd(t, dataDir, fakeBin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health.sh --json failed: %v\n%s", err, out)
	}
	var parsed healthJSON
	if jsonErr := json.Unmarshal(out, &parsed); jsonErr != nil {
		t.Fatalf("could not parse health.sh JSON output: %v\nraw:\n%s", jsonErr, out)
	}
	return parsed
}

func findBackup(t *testing.T, report healthJSON, name string) backupJSON {
	t.Helper()
	for _, b := range report.Backups {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("no backup entry for database %q in report: %+v", name, report.Backups)
	return backupJSON{}
}

func makeFakeDB(t *testing.T, dataDir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dataDir, name, ".dolt"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
}

// TestHealthScriptBackupStates covers the three states the fixed
// per-database check can report on a SUCCESSFUL query: none (zero remotes
// registered), ok (a file:// remote with a real, timestamped manifest), and
// unverified via its two distinct causes (a file:// remote whose manifest is
// missing, and a remote using a scheme — e.g. s3:// — this check cannot stat
// locally). Before the fix, every one of these produced the identical
// "Backups: none found" or "Backups: Xh ago" city-wide line regardless of
// per-database reality.
//
// Every scenario here runs through backupDoltStub, which requires --user to
// be present or it fails the way real dolt does — so a passing "ok" result
// for dbok also re-proves the --user fix (ga-soxi9's second bug) holds; see
// TestHealthScriptBackupQueryFailureIsDistinctFromNone for the case where
// the query itself fails.
func TestHealthScriptBackupStates(t *testing.T) {
	dataDir := t.TempDir()
	fakeBin := t.TempDir()

	makeFakeDB(t, dataDir, "dbnone")
	makeFakeDB(t, dataDir, "dbok")
	makeFakeDB(t, dataDir, "dbmissing")
	makeFakeDB(t, dataDir, "dbnonfile")

	okBackupDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(okBackupDir, "manifest"), []byte("fake-manifest"), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	freshMtime := time.Now().Add(-100 * time.Second)
	if err := os.Chtimes(filepath.Join(okBackupDir, "manifest"), freshMtime, freshMtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	missingBackupDir := filepath.Join(t.TempDir(), "never-synced") // no manifest inside

	// dbnone has no entry at all: the stub's cwd case-block falls through
	// with zero output, matching "dolt backup -v ran fine and reported no
	// remotes" exactly (as opposed to an empty line, which would be a
	// different, less faithful simulation).
	stub := backupDoltStub(map[string]string{
		"/dbok":      "dbok-backup file://" + okBackupDir + " {}",
		"/dbmissing": "dbmissing-backup file://" + missingBackupDir + " {}",
		"/dbnonfile": "dbnonfile-backup s3://some-bucket/some/path {}",
	})
	writeExecutable(t, filepath.Join(fakeBin, "dolt"), stub)

	report := runHealthJSON(t, dataDir, fakeBin)

	if got := findBackup(t, report, "dbnone"); got.State != "none" {
		t.Errorf("dbnone: state = %q, want \"none\" (zero remotes registered must never read as ok or unverified)", got.State)
	}

	ok := findBackup(t, report, "dbok")
	if ok.State != "ok" {
		t.Fatalf("dbok: state = %q, want \"ok\"; full: %+v", ok.State, ok)
	}
	if ok.Path != okBackupDir {
		t.Errorf("dbok: path = %q, want %q", ok.Path, okBackupDir)
	}
	if ok.AgeSec < 90 || ok.AgeSec > 200 {
		t.Errorf("dbok: age_sec = %d, want ~100 (matching the manifest mtime set 100s in the past)", ok.AgeSec)
	}
	if ok.Stale {
		t.Errorf("dbok: stale = true, want false (100s is well under the 1800s threshold)")
	}
	if ok.RemoteCount != 1 || ok.VerifiedRemoteCount != 1 {
		t.Errorf("dbok: remote_count=%d verified_remote_count=%d, want 1/1", ok.RemoteCount, ok.VerifiedRemoteCount)
	}

	missing := findBackup(t, report, "dbmissing")
	if missing.State != "unverified" {
		t.Errorf("dbmissing: state = %q, want \"unverified\" (a registered file:// remote with NO manifest on disk is a real risk, not a healthy \"ok\" — and must not be silently reported as \"none\" either, since the remote IS registered)", missing.State)
	}
	if missing.RemoteCount != 1 || missing.VerifiedRemoteCount != 0 {
		t.Errorf("dbmissing: remote_count=%d verified_remote_count=%d, want 1/0", missing.RemoteCount, missing.VerifiedRemoteCount)
	}

	nonfile := findBackup(t, report, "dbnonfile")
	if nonfile.State != "unverified" {
		t.Errorf("dbnonfile: state = %q, want \"unverified\" (a non-file:// remote scheme cannot be stat'd locally — this is \"don't know\", not \"ok\" and not \"none\")", nonfile.State)
	}
	if nonfile.RemoteCount != 1 || nonfile.VerifiedRemoteCount != 0 {
		t.Errorf("dbnonfile: remote_count=%d verified_remote_count=%d, want 1/0", nonfile.RemoteCount, nonfile.VerifiedRemoteCount)
	}
}

// TestHealthScriptBackupStaleFlagging verifies the existing 30-minute
// staleness threshold (unchanged by ga-soxi9 — that tuning is a separate,
// already-filed concern, ga-165vq) now applies to the REAL manifest
// timestamp instead of the wrong migration-backup-* one.
func TestHealthScriptBackupStaleFlagging(t *testing.T) {
	dataDir := t.TempDir()
	fakeBin := t.TempDir()
	makeFakeDB(t, dataDir, "dbstale")

	staleBackupDir := t.TempDir()
	manifestPath := filepath.Join(staleBackupDir, "manifest")
	if err := os.WriteFile(manifestPath, []byte("fake-manifest"), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	staleMtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(manifestPath, staleMtime, staleMtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	stub := backupDoltStub(map[string]string{
		"/dbstale": "dbstale-backup file://" + staleBackupDir + " {}",
	})
	writeExecutable(t, filepath.Join(fakeBin, "dolt"), stub)

	report := runHealthJSON(t, dataDir, fakeBin)
	got := findBackup(t, report, "dbstale")
	if got.State != "ok" {
		t.Fatalf("dbstale: state = %q, want \"ok\" (a stale-but-present backup is still a resolvable, verified backup — staleness is a separate flag)", got.State)
	}
	if !got.Stale {
		t.Errorf("dbstale: stale = false, want true (manifest is 2h old, over the 1800s/30min threshold)")
	}
}

// TestHealthScriptBackupQueryFailureIsDistinctFromNone reproduces the shape
// (not the specific --user cause, covered above) of the sharpest bug found
// while building this fix: a `dolt backup -v` call that fails outright — for
// ANY reason: a timeout, a transient lock, a credential error — must never
// render identically to "ran fine and found zero remotes". Collapsing those
// two into the same "none registered" state is exactly the false-high-alarm
// shape ga-soxi9 itself is about, just one level deeper (at the query step
// instead of the per-remote resolution step).
func TestHealthScriptBackupQueryFailureIsDistinctFromNone(t *testing.T) {
	dataDir := t.TempDir()
	fakeBin := t.TempDir()
	makeFakeDB(t, dataDir, "dbqueryfail")

	// Unconditionally fails `backup -v` regardless of args or cwd —
	// simulates ANY real-world failure of the query itself, decoupled from
	// the specific --user cause already covered by every scenario in
	// TestHealthScriptBackupStates.
	writeExecutable(t, filepath.Join(fakeBin, "dolt"), `#!/bin/sh
case "$*" in
  *'backup -v'*) exit 7 ;;
esac
exit 0
`)

	report := runHealthJSON(t, dataDir, fakeBin)
	got := findBackup(t, report, "dbqueryfail")
	if got.State == "none" {
		t.Fatalf("dbqueryfail: state = \"none\" — a FAILED query must never render identically to a CONFIRMED-empty one (this is the exact silent-collapse class ga-soxi9 is about)")
	}
	if got.State != "query-failed" {
		t.Errorf("dbqueryfail: state = %q, want \"query-failed\"", got.State)
	}
}

// TestHealthScriptBackupIgnoresMigrationBackupDirs is the direct regression
// guard for ga-soxi9's root cause: the OLD check derived city-wide
// "Backups: ..." freshness from `ls -1d "$GC_CITY_PATH"/migration-backup-*` —
// a naming convention that belongs to `gc dolt rollback`'s pre-migration
// snapshots (commands/rollback/run.sh), not to any database's actual `dolt
// backup` remotes. A migration-backup-* directory, however fresh, must have
// zero influence on what this check reports.
func TestHealthScriptBackupIgnoresMigrationBackupDirs(t *testing.T) {
	dataDir := t.TempDir()
	fakeBin := t.TempDir()
	makeFakeDB(t, dataDir, "dbnone")
	writeExecutable(t, filepath.Join(fakeBin, "dolt"), backupDoltStub(map[string]string{}))

	root := repoRoot(t)
	cityPath := t.TempDir()
	// A brand-new (0-second-old) migration-backup dir — maximally "fresh" by
	// the OLD, wrong metric. If any of it leaks into the backup report,
	// dbnone would stop showing state=none.
	if err := os.MkdirAll(filepath.Join(cityPath, "migration-backup-20260826T060000Z"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cmd := exec.Command("sh", filepath.Join(root, healthScript), "--json")
	cmd.Env = append(filteredEnv("GC_CITY_PATH", "GC_PACK_DIR", "GC_DOLT_HOST", "GC_DOLT_PORT",
		"GC_DOLT_USER", "GC_DOLT_PASSWORD", "GC_HEALTH_SKIP_ZOMBIE_SCAN", "GC_DOLT_DATA_DIR", "PATH"),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_HOST=127.0.0.1",
		"GC_DOLT_PORT=1",
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
		"GC_HEALTH_SKIP_ZOMBIE_SCAN=1",
		"GC_DOLT_DATA_DIR="+dataDir,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("health.sh --json failed: %v\n%s", err, out)
	}
	var report healthJSON
	if jsonErr := json.Unmarshal(out, &report); jsonErr != nil {
		t.Fatalf("could not parse JSON: %v\nraw:\n%s", jsonErr, out)
	}
	got := findBackup(t, report, "dbnone")
	if got.State != "none" {
		t.Errorf("dbnone: state = %q, want \"none\" — a fresh migration-backup-* directory at the city root must not make an unrelated database's backup report look healthy", got.State)
	}
}
