package badger_test

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/benchmarks/coordstore"
	"github.com/gastownhall/gascity/internal/benchmarks/coordstore/adapters/badger"
)

func TestAdapterCorrectnessRecoveryAndStats(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	adapter := badger.New()
	if err := adapter.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if failures := coordstore.CorrectnessChecker(ctx, adapter); len(failures) > 0 {
		t.Fatalf("correctness failures: %v", failures)
	}
	if err := adapter.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	mainRecord, err := adapter.Create(ctx, coordstore.Record{
		ID:       "main-1",
		Title:    "durable main",
		Status:   "open",
		Type:     "task",
		Priority: 2,
		Labels:   []string{"durable"},
		Metadata: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Create main: %v", err)
	}
	ephRecord, err := adapter.Create(ctx, coordstore.Record{
		ID:        "eph-1",
		Title:     "durable ephemeral",
		Status:    "open",
		Type:      "message",
		Ephemeral: true,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create ephemeral: %v", err)
	}
	if err := adapter.DepAdd(ctx, mainRecord.ID, ephRecord.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	stats := adapter.Stats(ctx)
	if _, ok := stats["badger_lsm_size_bytes"]; !ok {
		t.Fatalf("Stats missing badger_lsm_size_bytes: %#v", stats)
	}
	if _, ok := stats["badger_vlog_size_bytes"]; !ok {
		t.Fatalf("Stats missing badger_vlog_size_bytes: %#v", stats)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := badger.New()
	if err := reopened.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close() //nolint:errcheck

	gotMain, err := reopened.Get(ctx, mainRecord.ID)
	if err != nil {
		t.Fatalf("Get main after reopen: %v", err)
	}
	if gotMain.Title != mainRecord.Title || gotMain.Metadata["k"] != "v" {
		t.Fatalf("main record mismatch after reopen: %#v", gotMain)
	}
	gotEph, err := reopened.Get(ctx, ephRecord.ID)
	if err != nil {
		t.Fatalf("Get ephemeral after reopen: %v", err)
	}
	if !gotEph.Ephemeral || gotEph.Title != ephRecord.Title {
		t.Fatalf("ephemeral record mismatch after reopen: %#v", gotEph)
	}
	deps, err := reopened.DepList(ctx, mainRecord.ID, "down")
	if err != nil {
		t.Fatalf("DepList after reopen: %v", err)
	}
	if len(deps) != 1 || deps[0].ToID != ephRecord.ID {
		t.Fatalf("deps mismatch after reopen: %#v", deps)
	}
}

func TestAdapterStatsIncludeGCEventTelemetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	adapter := badger.New()
	if err := adapter.Open(ctx, coordstore.Config{DataDir: dir}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer adapter.Close() //nolint:errcheck

	expired, err := adapter.Create(ctx, coordstore.Record{
		ID:        "expired",
		Title:     "expired wisp",
		Status:    "open",
		Type:      "message",
		Ephemeral: true,
		ExpiresAt: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	if _, err := adapter.Get(ctx, expired.ID); err != nil {
		t.Fatalf("Get expired before purge: %v", err)
	}

	if _, err := adapter.PurgeExpired(ctx); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	stats := adapter.Stats(ctx)
	for _, key := range []string{
		"badger_gc_events",
		"badger_gc_last_started_unix_nano",
		"badger_gc_last_duration_nanos",
		"badger_gc_last_freed_bytes",
		"badger_gc_total_freed_bytes",
	} {
		if _, ok := stats[key]; !ok {
			t.Fatalf("Stats missing %s: %#v", key, stats)
		}
	}
	if stats["badger_gc_events"] < 1 {
		t.Fatalf("badger_gc_events = %d, want >= 1", stats["badger_gc_events"])
	}
	if stats["badger_gc_last_started_unix_nano"] <= 0 {
		t.Fatalf("badger_gc_last_started_unix_nano = %d", stats["badger_gc_last_started_unix_nano"])
	}
}
