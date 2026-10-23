//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// os.Open on a FIFO blocks until somebody opens the other end, so a FIFO put
// at the hold path would hang the controller tick (and every other caller)
// inside the reader. The reader must answer without opening it: not a regular
// file → cannot tell → hold, which is also what the shell reader says.
func TestReadManagedDoltHoldFIFOBlocksWithoutHanging(t *testing.T) {
	cityPath, holdPath := holdTestCity(t)
	if err := os.MkdirAll(filepath.Dir(holdPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(holdPath, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s): %v", holdPath, err)
	}

	if blocks, msg := shellHoldBlocks(t, holdPath); !blocks {
		t.Fatalf("dolt-hold.sh let a FIFO-in-place-of-file through: %q", msg)
	}

	done := make(chan managedDoltHold, 1)
	go func() { done <- readManagedDoltHold(cityPath) }()
	select {
	case got := <-done:
		if got.State != managedDoltHoldUnreadable || !got.Blocks() || got.Err == nil {
			t.Fatalf("hold = %+v, want unreadable+blocking with an Err", got)
		}
	case <-time.After(5 * time.Second):
		// Release the goroutine stuck in open(2) so the test binary can exit.
		if w, err := os.OpenFile(holdPath, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		t.Fatal("readManagedDoltHold hung on a FIFO at the hold path: it opened a file it had not checked is regular")
	}
}
