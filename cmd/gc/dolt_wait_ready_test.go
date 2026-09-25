package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWaitForManagedDoltReady_NoDoltCLISpawnOnEmptyPassword is the regression
// test for ga-0y849e (dog investigation ga-xyhl9d): waitForManagedDoltReady
// and confirmManagedDoltStillReady poll the server in a hot loop (up to the
// caller's timeout, 500ms between attempts, called on every single bd/gc bd
// invocation via gc-beads-bd.sh's ensure-ready op). Before the fix, each
// poll went through managedDoltQueryProbe, which forks a `dolt` CLI
// subprocess whenever GC_DOLT_PASSWORD is empty (this city's default for a
// local managed server) — measured at ~120 short-lived `dolt` processes in
// ~80s correlated with a Dolt server death under load. This test proves the
// wait-ready hot loop makes zero `dolt` CLI subprocess spawns, regardless of
// GC_DOLT_PASSWORD, by asserting nothing was ever written to a fake `dolt`
// binary's invocation log while a stubbed direct probe reports success.
func TestWaitForManagedDoltReady_NoDoltCLISpawnOnEmptyPassword(t *testing.T) {
	binDir := t.TempDir()
	invocationFile := filepath.Join(t.TempDir(), "dolt-invocation.txt")
	fakeDolt := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$INVOCATION_FILE\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(fakeDolt), 0o755); err != nil {
		t.Fatalf("WriteFile(fake dolt): %v", err)
	}
	t.Setenv("INVOCATION_FILE", invocationFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The town's default managed-local-dolt shape: no password configured.
	t.Setenv("GC_DOLT_PASSWORD", "")

	// Stub the pooled/direct probe so the test needs no live MySQL-protocol
	// server — mirrors how existing dolt_recover_managed_test.go tests stub
	// managedDoltQueryProbeDirectFn. Exercising this seam is exactly what
	// production wiring should use for every poll of this loop.
	oldProbe := managedDoltWaitReadyProbeFn
	t.Cleanup(func() { managedDoltWaitReadyProbeFn = oldProbe })
	calls := 0
	managedDoltWaitReadyProbeFn = func(_, _, _ string) error {
		calls++
		return nil
	}

	// A bare listener stands in for the managed Dolt server for the TCP
	// reachability check; the probe itself is stubbed above so nothing needs
	// to speak the MySQL wire protocol.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			conn.Close() //nolint:errcheck
		}
	}()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	report, err := waitForManagedDoltReady("", "127.0.0.1", portStr, "root", os.Getpid(), 5*time.Second, false)
	if err != nil {
		t.Fatalf("waitForManagedDoltReady() error = %v", err)
	}
	if !report.Ready {
		t.Fatalf("waitForManagedDoltReady() = %+v, want Ready=true", report)
	}
	if calls == 0 {
		t.Fatalf("managedDoltWaitReadyProbeFn was never called — test did not exercise the wait-ready poll loop")
	}

	if data, statErr := os.ReadFile(invocationFile); statErr == nil {
		t.Fatalf("dolt CLI was invoked %d time(s) from the wait-ready hot loop with GC_DOLT_PASSWORD empty — this is the ga-0y849e burst (invocations: %s)", strings.Count(string(data), "\n"), string(data))
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("ReadFile(invocationFile): %v", statErr)
	}
}

// TestConfirmManagedDoltStillReady_NoDoltCLISpawnOnEmptyPassword covers the
// second probe call inside waitForManagedDoltReady's success path
// (confirmManagedDoltStillReady), which doubles the CLI-spawn count per
// successful poll before the fix.
func TestConfirmManagedDoltStillReady_NoDoltCLISpawnOnEmptyPassword(t *testing.T) {
	binDir := t.TempDir()
	invocationFile := filepath.Join(t.TempDir(), "dolt-invocation.txt")
	fakeDolt := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$INVOCATION_FILE\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(fakeDolt), 0o755); err != nil {
		t.Fatalf("WriteFile(fake dolt): %v", err)
	}
	t.Setenv("INVOCATION_FILE", invocationFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_DOLT_PASSWORD", "")

	oldProbe := managedDoltWaitReadyProbeFn
	t.Cleanup(func() { managedDoltWaitReadyProbeFn = oldProbe })
	managedDoltWaitReadyProbeFn = func(_, _, _ string) error { return nil }

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			conn.Close() //nolint:errcheck
		}
	}()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(port): %v", err)
	}

	stable, err := confirmManagedDoltStillReady("", "127.0.0.1", strconv.Itoa(port), "root", os.Getpid(), false, 0)
	if err != nil {
		t.Fatalf("confirmManagedDoltStillReady() error = %v", err)
	}
	if !stable {
		t.Fatalf("confirmManagedDoltStillReady() = false, want true")
	}

	if _, statErr := os.Stat(invocationFile); !os.IsNotExist(statErr) {
		t.Fatalf("dolt CLI was invoked from confirmManagedDoltStillReady with GC_DOLT_PASSWORD empty — this is half of the ga-0y849e burst (each successful poll paid for it twice)")
	}
}
