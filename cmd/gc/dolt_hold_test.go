package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// holdTestCity returns a temp city and the path its hold file resolves to,
// with every env override that could redirect the layout cleared.
func holdTestCity(t *testing.T) (cityPath, holdPath string) {
	t.Helper()
	t.Setenv("GC_PACK_STATE_DIR", "")
	t.Setenv("GC_CITY_RUNTIME_DIR", "")
	t.Setenv("GC_DOLT_HOLD_FILE", "")
	cityPath = t.TempDir()
	holdPath, err := managedDoltHoldPath(cityPath)
	if err != nil {
		t.Fatalf("managedDoltHoldPath(%q): %v", cityPath, err)
	}
	return cityPath, holdPath
}

func writeHoldFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stubManagedDoltHoldClock(t *testing.T, now time.Time) {
	t.Helper()
	orig := managedDoltHoldNow
	t.Cleanup(func() { managedDoltHoldNow = orig })
	managedDoltHoldNow = func() time.Time { return now }
}

// shellHoldBlocks runs examples/bd/assets/scripts/dolt-hold.sh against the
// hold file at holdPath and returns what the shell side decides. The shell
// reads the real clock, so tests that compare it with Go must not stub the Go
// clock.
func shellHoldBlocks(t *testing.T, holdPath string) (blocks bool, message string) {
	t.Helper()
	helper := filepath.Join(repoRootForLint(t), "examples", "bd", "assets", "scripts", "dolt-hold.sh")
	script := `. "$1"; if dolt_hold_blocks; then printf 'BLOCK\n%s' "$DOLT_HOLD_MESSAGE"; else printf 'FREE\n%s' "$DOLT_HOLD_MESSAGE"; fi`
	cmd := exec.Command("sh", "-c", script, "sh", helper)
	// LC_ALL=C: launchd runs the scripts without a locale, and [:space:] must
	// not depend on the locale of whoever runs the tests.
	cmd.Env = append(os.Environ(), "GC_DOLT_HOLD_FILE="+holdPath, "PACK_STATE_DIR=", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running dolt-hold.sh: %v\n%s", err, out)
	}
	verdict, msg, _ := strings.Cut(string(out), "\n")
	switch verdict {
	case "BLOCK":
		return true, msg
	case "FREE":
		return false, msg
	default:
		t.Fatalf("dolt-hold.sh printed %q, want BLOCK/FREE", out)
		return false, ""
	}
}

func TestReadManagedDoltHoldStates(t *testing.T) {
	// Real clock on purpose: the shell reader is run on every case and has no
	// way to be told a fake time. Offsets are an hour, far from any boundary
	// except the "exactly now" case, which is expired on both sides at any
	// sub-second instant.
	now := time.Now().Truncate(time.Second)
	future := now.Add(time.Hour).Unix()
	past := now.Add(-time.Hour).Unix()

	cases := []struct {
		name       string
		content    string // ignored when absent
		absent     bool
		wantState  managedDoltHoldState
		wantBlocks bool
	}{
		{name: "no file", absent: true, wantState: managedDoltHoldNone, wantBlocks: false},
		{name: "bare touch holds until removed", content: "", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "owner and reason only", content: "owner=mayor\nreason=table swap\n", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "future expiry still holds", content: fmt.Sprintf("expires_at=%d\n", future), wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "expiry exactly now is released", content: fmt.Sprintf("expires_at=%d\n", now.Unix()), wantState: managedDoltHoldExpired, wantBlocks: false},
		{name: "past expiry is released", content: fmt.Sprintf("expires_at=%d\n", past), wantState: managedDoltHoldExpired, wantBlocks: false},
		{name: "comments blanks and unknown keys are ignored", content: fmt.Sprintf("# placed by hand\n\n  mode = swap \nnot a pair\nexpires_at = %d\n", future), wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "first expires_at wins (future then past)", content: fmt.Sprintf("expires_at=%d\nexpires_at=%d\n", future, past), wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "first expires_at wins (past then future)", content: fmt.Sprintf("expires_at=%d\nexpires_at=%d\n", past, future), wantState: managedDoltHoldExpired, wantBlocks: false},
		{name: "blank expires_at cannot be read, blocks", content: "expires_at=\n", wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "text expires_at cannot be read, blocks", content: "expires_at=tomorrow\n", wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "signed expires_at cannot be read, blocks", content: fmt.Sprintf("expires_at=+%d\n", future), wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "negative expires_at cannot be read, blocks", content: "expires_at=-5\n", wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "zero expires_at cannot be read, blocks", content: "expires_at=0\n", wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "overflowing expires_at cannot be read, blocks", content: "expires_at=99999999999999999999999\n", wantState: managedDoltHoldUnreadable, wantBlocks: true},
		// A hand-written "forever" (the MaxInt64 idiom). time.Unix adds
		// 62135596800 to the seconds, so from 9223371974719179008 up a
		// time.Time wraps to the year -292277022657 and reads as long expired.
		// The shell compares int64 seconds and holds; Go has to as well.
		{name: "MaxInt64 expires_at is a forever hold", content: "expires_at=9223372036854775807\n", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "near-MaxInt64 expires_at is a forever hold", content: "expires_at=9223372036854775000\n", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "last second before the time.Time wrap still holds", content: "expires_at=9223371974719179007\n", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "first second after the time.Time wrap still holds", content: "expires_at=9223371974719179008\n", wantState: managedDoltHoldActive, wantBlocks: true},
		{name: "very large expires_at still holds", content: "expires_at=9000000000000000000\n", wantState: managedDoltHoldActive, wantBlocks: true},
		// strings.TrimSpace trims Unicode spaces the shell keeps. A past
		// expiry hidden behind one must not read as "expired" on one side only.
		{name: "past expiry with a trailing no-break space cannot be read, blocks", content: fmt.Sprintf("expires_at=%d\u00a0\n", past), wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "past expiry with a leading no-break space cannot be read, blocks", content: fmt.Sprintf("expires_at=\u00a0%d\n", past), wantState: managedDoltHoldUnreadable, wantBlocks: true},
		{name: "key with a trailing no-break space is not expires_at", content: fmt.Sprintf("expires_at\u00a0=%d\n", past), wantState: managedDoltHoldActive, wantBlocks: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityPath, holdPath := holdTestCity(t)
			if !tc.absent {
				writeHoldFile(t, holdPath, tc.content)
			}
			if shellBlocks, msg := shellHoldBlocks(t, holdPath); shellBlocks != tc.wantBlocks {
				t.Fatalf("dolt-hold.sh blocks = %v (%q), want %v: shell and Go readers disagree", shellBlocks, msg, tc.wantBlocks)
			}
			got := readManagedDoltHold(cityPath)
			if got.State != tc.wantState {
				t.Fatalf("state = %v, want %v (hold=%+v)", got.State, tc.wantState, got)
			}
			if got.Blocks() != tc.wantBlocks {
				t.Fatalf("Blocks() = %v, want %v", got.Blocks(), tc.wantBlocks)
			}
			if got.State == managedDoltHoldUnreadable && got.Err == nil {
				t.Fatalf("unreadable hold carries no Err: %+v", got)
			}
		})
	}
}

func TestReadManagedDoltHoldCarriesOwnerReasonAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	stubManagedDoltHoldClock(t, now)
	cityPath, holdPath := holdTestCity(t)
	expires := now.Add(2 * time.Hour)
	writeHoldFile(t, holdPath, fmt.Sprintf("owner=mayor\nreason=hq table swap\nexpires_at=%d\n", expires.Unix()))

	got := readManagedDoltHold(cityPath)
	if got.Owner != "mayor" || got.Reason != "hq table swap" || got.ExpiresUnix != expires.Unix() {
		t.Fatalf("hold = %+v, want owner=mayor reason=%q expires=%v", got, "hq table swap", expires)
	}
	msg := got.Describe()
	for _, want := range []string{holdPath, "mayor", "hq table swap", "2026-10-21T05:00:00Z", "remove the file"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Describe() = %q, missing %q", msg, want)
		}
	}
}

// Describe prints a date only where one exists: a far-future expires_at that
// time.Time cannot hold (it wraps negative) is printed as raw seconds, never as
// a year -292277022657.
func TestManagedDoltHoldDescribeFarFutureExpiry(t *testing.T) {
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	stubManagedDoltHoldClock(t, now)
	cityPath, holdPath := holdTestCity(t)
	writeHoldFile(t, holdPath, "expires_at=9223372036854775807\n")

	got := readManagedDoltHold(cityPath)
	if got.State != managedDoltHoldActive || got.ExpiresUnix != 9223372036854775807 {
		t.Fatalf("hold = %+v, want active with ExpiresUnix=MaxInt64", got)
	}
	msg := got.Describe()
	if !strings.Contains(msg, "expires at unix 9223372036854775807") || strings.Contains(msg, "-292277") {
		t.Fatalf("Describe() = %q, want the raw seconds and no wrapped year", msg)
	}
}

// A hold that cannot be READ must block exactly like one that can: the
// action it guards is the destructive one, so "could not tell" is not "no hold".
func TestReadManagedDoltHoldUnreadableBlocks(t *testing.T) {
	t.Run("directory where the file should be", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		if err := os.MkdirAll(holdPath, 0o755); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh let a directory-in-place-of-file through: %q", msg)
		}
	})
	t.Run("a file where the parent directory should be", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// ENOTDIR is not ENOENT: nothing says "no hold" here.
		parent := filepath.Dir(holdPath)
		if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh read a file-where-the-directory-should-be as \"no hold\": %q", msg)
		}
	})
	t.Run("symlink whose target is missing", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// os.Open follows the link and reports ENOENT, which on its own would
		// read as "no hold". But something IS at the hold path: whoever put it
		// there meant a hold, and the shell reader blocks on it too.
		if err := os.MkdirAll(filepath.Dir(holdPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(filepath.Dir(holdPath), "no-such-target"), holdPath); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh let a dangling symlink through: %q", msg)
		}
	})
	t.Run("parent directory is a symlink whose target is missing", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// Same class one level up: the parent is not ABSENT (a link sits
		// there), it just leads nowhere, so nothing can say "no hold".
		parent := filepath.Dir(holdPath)
		if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(filepath.Dir(parent), "no-such-dir"), parent); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh let a dangling parent symlink through: %q", msg)
		}
	})
	t.Run("an ancestor above the parent directory is not searchable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory modes")
		}
		cityPath, holdPath := holdTestCity(t)
		// The parent (.../packs/dolt) cannot be stat'd because the directory
		// above it (.../packs) has no search permission: os.Open says EACCES,
		// not ENOENT, so nothing says "no hold". [ -e parent ] is false in the
		// shell for the same reason, and the shell reader must tell the two
		// apart instead of reading it as "the parent is gone".
		writeHoldFile(t, holdPath, "owner=x\n")
		packs := filepath.Dir(filepath.Dir(holdPath))
		if err := os.Chmod(packs, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(packs, 0o755) })
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh read a hold behind an unsearchable ancestor as \"no hold\": %q", msg)
		}
	})
	t.Run("a file where an ancestor above the parent directory should be", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// ENOTDIR two levels up: the immediate parent does not exist as far as
		// [ -e ] can tell, yet the path is not "gone" — a regular file sits
		// where a directory should be.
		packs := filepath.Dir(filepath.Dir(holdPath))
		if err := os.MkdirAll(filepath.Dir(packs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(packs, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh read a file-above-the-parent as \"no hold\": %q", msg)
		}
	})
	t.Run("an ancestor above the parent directory is a symlink whose target is missing", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// Same class as the dangling parent link, one level further up: the
		// path cannot be walked, and ENOENT here is the link's, not the
		// parent's. Something IS on the path, so nothing can say "no hold".
		packs := filepath.Dir(filepath.Dir(holdPath))
		if err := os.MkdirAll(filepath.Dir(packs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(filepath.Dir(packs), "no-such-dir"), packs); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh let a dangling ancestor symlink through: %q", msg)
		}
	})
	t.Run("an ancestor above the parent is a symlink that resolves, the rest is absent", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// The control for the case above: a link that works is not a reason to
		// hold. The directory under it really is not there, so nobody can have
		// put a hold in it, and both readers must say "no hold".
		packs := filepath.Dir(filepath.Dir(holdPath))
		target := filepath.Join(t.TempDir(), "real-packs")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(packs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, packs); err != nil {
			t.Fatal(err)
		}
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldNone || got.Blocks() {
			t.Fatalf("hold = %+v, want none", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); blocks {
			t.Fatalf("dolt-hold.sh blocked behind a working ancestor symlink with nothing under it: %q", msg)
		}
	})
	t.Run("parent directory absent", func(t *testing.T) {
		cityPath, holdPath := holdTestCity(t)
		// The one ENOENT that does mean "no hold": nobody can have put a
		// hold under a directory that does not exist.
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldNone || got.Blocks() {
			t.Fatalf("hold = %+v, want none", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); blocks {
			t.Fatalf("dolt-hold.sh blocked on an absent parent: %q", msg)
		}
	})
	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any file")
		}
		cityPath, holdPath := holdTestCity(t)
		writeHoldFile(t, holdPath, "owner=x\n")
		if err := os.Chmod(holdPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(holdPath, 0o644) })
		got := readManagedDoltHold(cityPath)
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
		if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
			t.Fatalf("dolt-hold.sh let an unreadable file through: %q", msg)
		}
	})
	t.Run("city path that does not resolve", func(t *testing.T) {
		t.Setenv("GC_PACK_STATE_DIR", "")
		t.Setenv("GC_CITY_RUNTIME_DIR", "")
		t.Setenv("GC_DOLT_HOLD_FILE", "")
		got := readManagedDoltHold("")
		if got.State != managedDoltHoldUnreadable || !got.Blocks() {
			t.Fatalf("hold = %+v, want unreadable+blocking", got)
		}
	})
}

func TestManagedDoltHoldPathEnvOverride(t *testing.T) {
	cityPath, defaultPath := holdTestCity(t)
	override := filepath.Join(t.TempDir(), "elsewhere", "my-hold")
	t.Setenv("GC_DOLT_HOLD_FILE", override)
	got, err := managedDoltHoldPath(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != normalizePathForCompare(override) || got == defaultPath {
		t.Fatalf("managedDoltHoldPath = %q, want the override %q (default would be %q)", got, override, defaultPath)
	}
	writeHoldFile(t, override, "")
	if hold := readManagedDoltHold(cityPath); !hold.Blocks() {
		t.Fatalf("hold at the override path does not block: %+v", hold)
	}
}

// The gate must refuse while held WITHOUT spending its cooldown window: the
// first real outage after the release must still get its recovery.
func TestRecoverManagedBDCommandAllowedRefusesUnderHold(t *testing.T) {
	origNow := recoverManagedBDCommandGateNow
	origCooldown := recoverManagedBDCommandCooldown
	t.Cleanup(func() {
		recoverManagedBDCommandGateNow = origNow
		recoverManagedBDCommandCooldown = origCooldown
	})
	recoverManagedBDCommandCooldown = func() time.Duration { return 30 * time.Second }
	fixedNow := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	recoverManagedBDCommandGateNow = func() time.Time { return fixedNow }

	cityPath, holdPath := holdTestCity(t)
	writeHoldFile(t, holdPath, "owner=mayor\n")

	for i := 0; i < 3; i++ {
		if recoverManagedBDCommandAllowed(cityPath) {
			t.Fatalf("call %d: gate opened while the managed dolt is on hold", i+1)
		}
	}
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(layout.PackStateDir, recoverManagedBDCommandGateFileName)
	if _, err := os.Stat(stamp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused call touched the cooldown stamp %s (stat err = %v)", stamp, err)
	}

	if err := os.Remove(holdPath); err != nil {
		t.Fatal(err)
	}
	if !recoverManagedBDCommandAllowed(cityPath) {
		t.Fatal("gate stayed closed right after the hold was released: the refused calls spent the cooldown")
	}
}

func TestBdCommandRunnerWithManagedRetryDoesNotRecoverUnderHold(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")

	origRunner := beadsExecCommandRunnerWithEnv
	origRecover := recoverManagedBDCommand
	t.Cleanup(func() {
		beadsExecCommandRunnerWithEnv = origRunner
		recoverManagedBDCommand = origRecover
	})
	var recoverCalls, runCalls int32
	recoverManagedBDCommand = func(string) error {
		atomic.AddInt32(&recoverCalls, 1)
		return nil
	}
	beadsExecCommandRunnerWithEnv = func(map[string]string) beads.CommandRunner {
		return func(string, string, ...string) ([]byte, error) {
			atomic.AddInt32(&runCalls, 1)
			return nil, fmt.Errorf("connection refused")
		}
	}

	cityPath, holdPath := holdTestCity(t)
	writeHoldFile(t, holdPath, "owner=mayor\n")
	runner := bdCommandRunnerWithManagedRetry(cityPath, func(string) map[string]string {
		return map[string]string{"GC_DOLT_PORT": "3307"}
	})
	if _, err := runner(t.TempDir(), "bd", "list", "--json"); err == nil {
		t.Fatal("runner returned nil error although every bd attempt failed")
	}
	if got := atomic.LoadInt32(&recoverCalls); got != 0 {
		t.Fatalf("recoverManagedBDCommand called %d times while held, want 0", got)
	}
	if got := atomic.LoadInt32(&runCalls); got != 2 {
		t.Fatalf("bd ran %d times, want 2 (the plain retry still happens, it just does not recover)", got)
	}

	// Released: the same failure triggers recovery again.
	if err := os.Remove(holdPath); err != nil {
		t.Fatal(err)
	}
	_, _ = runner(t.TempDir(), "bd", "list", "--json")
	if got := atomic.LoadInt32(&recoverCalls); got != 1 {
		t.Fatalf("recoverManagedBDCommand called %d times after the release, want 1", got)
	}
}

func holdTickRuntime(t *testing.T, healthCalls *int, stderr io.Writer) *CityRuntime {
	t.Helper()
	return &CityRuntime{
		cityPath:  "/tmp/test-city",
		logPrefix: "gc test",
		stderr:    stderr,
		managedDoltHealth: func(string) error {
			*healthCalls++
			return nil
		},
		managedDoltOwned: func(string) (bool, error) { return true, nil },
		managedDoltPort:  func(string) string { return "" },
	}
}

func TestEnsureManagedDoltPublishedForTickSkipsHealthUnderHold(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_PACK_STATE_DIR", "")
	t.Setenv("GC_CITY_RUNTIME_DIR", "")
	holdFile := filepath.Join(t.TempDir(), "dolt-hold")
	t.Setenv("GC_DOLT_HOLD_FILE", holdFile)
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	stubManagedDoltHoldClock(t, now)
	managedDoltHoldNoticeLast.Delete(normalizePathForCompare("/tmp/test-city"))

	var stderr bytes.Buffer
	healthCalls := 0
	cr := holdTickRuntime(t, &healthCalls, &stderr)

	// No hold: the preflight runs, as before.
	cr.ensureManagedDoltPublishedForTick()
	if healthCalls != 1 {
		t.Fatalf("healthCalls = %d without a hold, want 1", healthCalls)
	}

	// Held: no health preflight, one log line no matter how many ticks.
	writeHoldFile(t, holdFile, "owner=mayor\nreason=hq swap\n")
	for i := 0; i < 5; i++ {
		cr.ensureManagedDoltPublishedForTick()
	}
	if healthCalls != 1 {
		t.Fatalf("healthCalls = %d after 5 held ticks, want still 1", healthCalls)
	}
	if n := strings.Count(stderr.String(), "managed dolt not started/recovered"); n != 1 {
		t.Fatalf("hold notice logged %d times in 5 ticks, want 1; stderr=%q", n, stderr.String())
	}
	if !strings.Contains(stderr.String(), "hq swap") {
		t.Fatalf("hold notice lacks the reason: %q", stderr.String())
	}

	// The notice repeats once the interval passes, so a long hold stays visible.
	stubManagedDoltHoldClock(t, now.Add(managedDoltHoldNoticeInterval+time.Second))
	cr.ensureManagedDoltPublishedForTick()
	if n := strings.Count(stderr.String(), "managed dolt not started/recovered"); n != 2 {
		t.Fatalf("hold notice logged %d times after the interval, want 2", n)
	}

	// An unreadable hold file holds too.
	writeHoldFile(t, holdFile, "expires_at=soon\n")
	cr.ensureManagedDoltPublishedForTick()
	if healthCalls != 1 {
		t.Fatalf("healthCalls = %d with an unreadable hold, want 1 (blocked)", healthCalls)
	}

	// Released (file gone): the next tick recovers.
	if err := os.Remove(holdFile); err != nil {
		t.Fatal(err)
	}
	cr.ensureManagedDoltPublishedForTick()
	if healthCalls != 2 {
		t.Fatalf("healthCalls = %d after release, want 2", healthCalls)
	}
}

func TestEnsureManagedDoltPublishedForTickRecoversWhenHoldExpired(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_PACK_STATE_DIR", "")
	t.Setenv("GC_CITY_RUNTIME_DIR", "")
	holdFile := filepath.Join(t.TempDir(), "dolt-hold")
	t.Setenv("GC_DOLT_HOLD_FILE", holdFile)
	now := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)
	stubManagedDoltHoldClock(t, now)
	writeHoldFile(t, holdFile, fmt.Sprintf("expires_at=%d\n", now.Add(-time.Minute).Unix()))

	healthCalls := 0
	cr := holdTickRuntime(t, &healthCalls, io.Discard)
	cr.ensureManagedDoltPublishedForTick()
	if healthCalls != 1 {
		t.Fatalf("healthCalls = %d with an expired hold, want 1 (expiry releases the hold)", healthCalls)
	}
}

func TestHealthBeadsProviderDoesNotRecoverUnderHold(t *testing.T) {
	cityPath, holdPath := holdTestCity(t)
	writeMinimalCityToml(t, cityPath)
	opsFile := writeBreakerAwarePreflightFakes(t, cityPath, "unhealthy")
	cityKey := normalizePathForCompare(cityPath)
	t.Cleanup(func() { lastBeadsProviderRecover.Delete(cityKey) })
	writeHoldFile(t, holdPath, "owner=mayor\nreason=hq swap\n")

	err := healthBeadsProvider(cityPath)
	if !errors.Is(err, errManagedDoltHeld) {
		t.Fatalf("healthBeadsProvider() error = %v, want errManagedDoltHeld", err)
	}
	if !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("error %q hides the underlying health failure", err)
	}
	ops, readErr := os.ReadFile(opsFile)
	if readErr != nil {
		t.Fatalf("read provider ops: %v", readErr)
	}
	if got := strings.Fields(strings.TrimSpace(string(ops))); len(got) != 1 || got[0] != "health" {
		t.Fatalf("provider ops = %v, want only [health] (recover must not run under a hold)", got)
	}
	if _, loaded := lastBeadsProviderRecover.Load(cityKey); loaded {
		t.Fatal("a refused recover stamped lastBeadsProviderRecover: the cooldown would block the post-release recovery")
	}

	// Released: the next call recovers at once.
	if err := os.Remove(holdPath); err != nil {
		t.Fatal(err)
	}
	_ = healthBeadsProvider(cityPath)
	ops, readErr = os.ReadFile(opsFile)
	if readErr != nil {
		t.Fatalf("read provider ops: %v", readErr)
	}
	if _, r := countOps(strings.Fields(strings.TrimSpace(string(ops))), "health", "recover"); r != 1 {
		t.Fatalf("provider ops = %q, want exactly one recover after the release", ops)
	}
}

// Both low-level entry points refuse before they touch anything: recover would
// otherwise stop a running server first, start would spawn one.
func TestManagedDoltLifecycleEntryPointsRefuseUnderHold(t *testing.T) {
	cityPath, holdPath := holdTestCity(t)
	writeHoldFile(t, holdPath, "owner=mayor\n")
	layout, err := resolveManagedDoltRuntimeLayout(cityPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := startManagedDoltProcess(cityPath, "127.0.0.1", "3399", "root", "", 5*time.Second); !errors.Is(err, errManagedDoltHeld) {
		t.Fatalf("startManagedDoltProcess() error = %v, want errManagedDoltHeld", err)
	}
	if _, err := recoverManagedDoltProcess(cityPath, "127.0.0.1", "3399", "root", "", 5*time.Second); !errors.Is(err, errManagedDoltHeld) {
		t.Fatalf("recoverManagedDoltProcess() error = %v, want errManagedDoltHeld", err)
	}
	// Neither got as far as the lifecycle lock, the config file or the log.
	for _, p := range []string{layout.LockFile, layout.ConfigFile, layout.LogFile, layout.PIDFile} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s exists after a refused lifecycle call (stat err = %v)", p, err)
		}
	}
}
