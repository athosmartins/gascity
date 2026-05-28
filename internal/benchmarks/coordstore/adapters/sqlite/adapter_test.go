package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/benchmarks/coordstore"
)

func TestOpenRecoversGeneratedIDSequence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first := New()
	if err := first.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("first open: %v", err)
	}
	created, err := first.Create(ctx, coordstore.Record{Title: "first", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.ID != "sq-1" {
		t.Fatalf("first generated ID = %q, want sq-1", created.ID)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	second := New()
	if err := second.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
	})
	next, err := second.Create(ctx, coordstore.Record{Title: "second", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if next.ID != "sq-2" {
		t.Fatalf("next generated ID = %q, want sq-2", next.ID)
	}
}

func TestOpenRecoversCustomGeneratedIDSequence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first := NewWithDriver(DefaultDriverName, DefaultPragmas, "custom")
	if err := first.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("first open: %v", err)
	}
	created, err := first.Create(ctx, coordstore.Record{Title: "first", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.ID != "custom-1" {
		t.Fatalf("first generated ID = %q, want custom-1", created.ID)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	second := NewWithDriver(DefaultDriverName, DefaultPragmas, "custom")
	if err := second.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
	})
	next, err := second.Create(ctx, coordstore.Record{Title: "second", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if next.ID != "custom-2" {
		t.Fatalf("next generated ID = %q, want custom-2", next.ID)
	}
}

// brokenPragmasForContrast documents the pre-fix configuration that caused
// the ga-4advr OOM at 32m22s under MemoryMax=8G. Kept as a negative
// baseline for TestWALUnboundedWithCheckpointerDisabled. Do not use in
// production code or any non-test file. See ga-qe54tg for the fix rationale.
const brokenPragmasForContrast = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA cache_size=-65536;
PRAGMA temp_store=MEMORY;
PRAGMA mmap_size=268435456;
PRAGMA wal_autocheckpoint=0;
`

// TestWALBoundedUnderSustainedWrites is the ga-2s6sz spike regression test:
// with FullSyncPragmas (wal_autocheckpoint=1000) and the background
// wal_checkpoint(TRUNCATE) loop enabled, the on-disk WAL file must not grow
// without bound under sustained writes. Before the spike the WAL was
// monotone for the lifetime of the process — see ga-tm9sg for the 8GB OOM
// that motivated the fix.
func TestWALBoundedUnderSustainedWrites(t *testing.T) {
	// Short interval so the goroutine fires several times in this test's
	// budget; the production default is 30s.
	t.Setenv("COORDSTORE_SQLITE_CHECKPOINT_INTERVAL", "200ms")

	dir := t.TempDir()
	ctx := context.Background()

	a := NewWithDriver(DefaultDriverName, FullSyncPragmas, "tst")
	if err := a.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	// Hammer writes — enough to exceed 1000 pages (~4MB) and trigger
	// wal_autocheckpoint on the writer side at least once. With FULL sync
	// each commit fsyncs, so this also exercises the goroutine + writer
	// contention path.
	const writes = 1500
	for i := 0; i < writes; i++ {
		_, err := a.Create(ctx, coordstore.Record{
			Title:    fmt.Sprintf("wal-bound-%d", i),
			Status:   "open",
			Type:     "task",
			Assignee: "test",
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	// Give the TRUNCATE goroutine room to fire after the last write.
	time.Sleep(500 * time.Millisecond)

	stats := a.Stats(ctx)
	wal, ok := stats["wal_size_bytes"]
	if !ok {
		t.Fatalf("stats missing wal_size_bytes; got %#v", stats)
	}
	// 16MiB is a generous ceiling: the WAL high-water mark on a healthy
	// run sits around 4-8MB between auto-checkpoints; ga-tm9sg's broken
	// configuration would already be tens of MB by this point. Tightening
	// this bound is a follow-up after the soak data lands.
	const ceiling = 16 << 20
	if wal > ceiling {
		t.Errorf("wal_size_bytes = %d, want <= %d (%dMB ceiling)", wal, ceiling, ceiling>>20)
	}
	t.Logf("wal_size_bytes after %d writes + 500ms drain: %d (%.2f MB)",
		writes, wal, float64(wal)/(1<<20))
}

// TestWALUnboundedWithCheckpointerDisabled documents the pre-fix failure
// mode: brokenPragmasForContrast (wal_autocheckpoint=0, mmap_size=256MB)
// with the background loop off leaves the same write volume with a WAL
// already larger than the bounded ceiling. DefaultPragmas is now the fixed
// configuration (wal_autocheckpoint=1000, mmap_size=0); this test uses the
// explicit broken-pragma constant to preserve the negative baseline without
// asserting against the production config. See ga-qe54tg for the OOM
// incident that motivated the fix.
func TestWALUnboundedWithCheckpointerDisabled(t *testing.T) {
	t.Setenv("COORDSTORE_SQLITE_CHECKPOINT_INTERVAL", "off")

	dir := t.TempDir()
	ctx := context.Background()

	a := NewWithDriver(DefaultDriverName, brokenPragmasForContrast, "tst")
	if err := a.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	const writes = 1500
	for i := 0; i < writes; i++ {
		_, err := a.Create(ctx, coordstore.Record{
			Title:    fmt.Sprintf("wal-unbound-%d", i),
			Status:   "open",
			Type:     "task",
			Assignee: "test",
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	stats := a.Stats(ctx)
	wal := stats["wal_size_bytes"]
	// With autocheckpoint=0 and no background TRUNCATE, the WAL should be
	// noticeably larger than the bounded-config ceiling. This is the
	// canary: if it ever drops back below 4MB, somebody quietly fixed the
	// default and the contrast test loses its meaning.
	const floor = 4 << 20
	if wal < floor {
		t.Errorf("wal_size_bytes = %d, expected > %d (%dMB) to demonstrate "+
			"the unbounded-WAL failure mode this contrast test exists to lock in",
			wal, floor, floor>>20)
	}
	t.Logf("wal_size_bytes after %d writes with checkpointer disabled: %d (%.2f MB)",
		writes, wal, float64(wal)/(1<<20))
}
