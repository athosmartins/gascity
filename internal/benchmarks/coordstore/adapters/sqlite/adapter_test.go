package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/benchmarks/coordstore"
)

func TestOpenUsesProductionSQLiteSettings(t *testing.T) {
	ctx := context.Background()
	a := openTestAdapter(t, ctx, coordstore.Config{DataDir: t.TempDir()})

	if got := a.readDB.Stats().MaxOpenConnections; got != 8 {
		t.Fatalf("read pool max open connections = %d, want 8", got)
	}
	if got := a.writeDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("write pool max open connections = %d, want 1", got)
	}

	if got := queryStringPragma(t, ctx, a.writeDB, "journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %q, want wal", got)
	}
	if got := queryIntPragma(t, ctx, a.writeDB, "synchronous"); got != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL)", got)
	}
	if got := queryIntPragma(t, ctx, a.writeDB, "wal_autocheckpoint"); got != 1000 {
		t.Fatalf("wal_autocheckpoint = %d, want 1000", got)
	}
}

func TestPoolEightConcurrentAccessHasNoErrors(t *testing.T) {
	ctx := context.Background()
	a := openTestAdapter(t, ctx, coordstore.Config{DataDir: t.TempDir()})

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				r, err := a.Create(ctx, coordstore.Record{
					Title:    fmt.Sprintf("worker-%d-%d", worker, i),
					Status:   "open",
					Type:     "task",
					Assignee: fmt.Sprintf("agent-%d", worker),
				})
				if err != nil {
					errs <- fmt.Errorf("create: %w", err)
					return
				}
				if _, err := a.Get(ctx, r.ID); err != nil {
					errs <- fmt.Errorf("get: %w", err)
					return
				}
				if _, err := a.FilterScan(ctx, coordstore.Query{Assignee: r.Assignee, Limit: 5}); err != nil {
					errs <- fmt.Errorf("filter scan: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPurgeTerminalRemovesOnlyOldTerminalMainRecords(t *testing.T) {
	ctx := context.Background()
	a := openTestAdapter(t, ctx, coordstore.Config{DataDir: t.TempDir()})
	old := time.Now().Add(-5 * time.Hour)
	recent := time.Now()
	cutoff := time.Now().Add(-4 * time.Hour)

	oldClosed := mustCreateRecord(t, ctx, a, coordstore.Record{
		ID:        "old-closed",
		Title:     "old closed",
		Status:    "closed",
		Type:      "task",
		CreatedAt: old,
		Labels:    []string{"purge-me"},
		Metadata:  map[string]string{"scope": "terminal"},
	})
	oldCancelled := mustCreateRecord(t, ctx, a, coordstore.Record{
		ID:        "old-cancelled",
		Title:     "old cancelled",
		Status:    "cancelled",
		Type:      "task",
		CreatedAt: old,
	})
	recentClosed := mustCreateRecord(t, ctx, a, coordstore.Record{
		ID:        "recent-closed",
		Title:     "recent closed",
		Status:    "closed",
		Type:      "task",
		CreatedAt: recent,
	})
	oldOpen := mustCreateRecord(t, ctx, a, coordstore.Record{
		ID:        "old-open",
		Title:     "old open",
		Status:    "open",
		Type:      "task",
		CreatedAt: old,
	})
	if err := a.DepAdd(ctx, oldOpen.ID, oldClosed.ID, "blocks"); err != nil {
		t.Fatalf("add dep: %v", err)
	}

	purged, err := a.PurgeTerminal(ctx, cutoff)
	if err != nil {
		t.Fatalf("PurgeTerminal: %v", err)
	}
	if purged != 2 {
		t.Fatalf("PurgeTerminal purged %d records, want 2", purged)
	}

	for _, id := range []string{oldClosed.ID, oldCancelled.ID} {
		if _, err := a.Get(ctx, id); !errors.Is(err, coordstore.ErrNotFound) {
			t.Fatalf("Get(%q) error = %v, want ErrNotFound", id, err)
		}
	}
	for _, id := range []string{recentClosed.ID, oldOpen.ID} {
		if _, err := a.Get(ctx, id); err != nil {
			t.Fatalf("Get(%q): %v", id, err)
		}
	}
	deps, err := a.DepList(ctx, oldOpen.ID, "down")
	if err != nil {
		t.Fatalf("DepList: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("deps after purging old terminal target = %v, want none", deps)
	}
}

func TestRetentionSweepStartsFromConfig(t *testing.T) {
	ctx := context.Background()
	a := openTestAdapter(t, ctx, coordstore.Config{
		DataDir: t.TempDir(),
		Extra: map[string]string{
			"retention_period":         "1ms",
			"retention_sweep_interval": "5ms",
		},
	})

	r := mustCreateRecord(t, ctx, a, coordstore.Record{
		ID:        "sweep-me",
		Title:     "sweep me",
		Status:    "closed",
		Type:      "task",
		CreatedAt: time.Now().Add(-time.Hour),
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		_, err := a.Get(ctx, r.ID)
		if errors.Is(err, coordstore.ErrNotFound) {
			return
		}
		if err != nil {
			t.Fatalf("Get(%q): %v", r.ID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("retention sweep did not purge %q before deadline", r.ID)
}

func TestSQLiteWALAutoCheckpointBoundsLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := openTestAdapter(t, ctx, coordstore.Config{DataDir: dir})

	for i := 0; i < 1200; i++ {
		mustCreateRecord(t, ctx, a, coordstore.Record{
			Title:  fmt.Sprintf("wal-%d", i),
			Status: "open",
			Type:   "task",
		})
	}
	if _, err := a.writeDB.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatalf("wal checkpoint: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "store.db-wal"))
	if err != nil {
		t.Fatalf("stat wal file: %v", err)
	}
	const maxWALSize = 8 << 20
	if info.Size() > maxWALSize {
		t.Fatalf("wal size = %d bytes, want <= %d", info.Size(), maxWALSize)
	}
}

func openTestAdapter(t *testing.T, ctx context.Context, cfg coordstore.Config) *Adapter {
	t.Helper()
	a := New()
	if err := a.Open(ctx, cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return a
}

func mustCreateRecord(t *testing.T, ctx context.Context, a *Adapter, r coordstore.Record) coordstore.Record {
	t.Helper()
	created, err := a.Create(ctx, r)
	if err != nil {
		t.Fatalf("Create(%q): %v", r.ID, err)
	}
	return created
}

func queryIntPragma(t *testing.T, ctx context.Context, db *sql.DB, name string) int {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return got
}

func queryStringPragma(t *testing.T, ctx context.Context, db *sql.DB, name string) string {
	t.Helper()
	var got string
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return got
}
