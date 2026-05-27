package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestOpenCityStoreAtUsesBboltBackendForCityScope(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bboltStore, ok := store.(*beads.BboltStore)
	if !ok {
		t.Fatalf("openCityStoreAt returned %T, want *beads.BboltStore", store)
	}
	defer func() {
		if err := bboltStore.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	created, err := store.Create(beads.Bead{Title: "city bbolt bead"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "gc-1" {
		t.Fatalf("created ID = %q, want gc-1", created.ID)
	}
	if _, err := os.Stat(bboltCityStorePath(cityDir)); err != nil {
		t.Fatalf("stat bbolt store: %v", err)
	}
}

func TestOpenCityStoreAtIgnoresBboltBackendForFileProvider(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "file")

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	if _, ok := store.(*beads.FileStore); !ok {
		t.Fatalf("openCityStoreAt returned %T, want *beads.FileStore", store)
	}
	if _, err := os.Stat(bboltCityStorePath(cityDir)); !os.IsNotExist(err) {
		t.Fatalf("bbolt store should not be created for file provider, stat err = %v", err)
	}
}

func TestStartBeadsLifecycleBboltCreatesStoreAndSkipsManagedDolt(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")
	cfg, err := loadCityConfig(cityDir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}

	var stderr bytes.Buffer
	if err := startBeadsLifecycle(cityDir, "demo", cfg, &stderr); err != nil {
		t.Fatalf("startBeadsLifecycle: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "coord-store: using bbolt backend "+bboltCityStorePath(cityDir)) {
		t.Fatalf("stderr = %q, want bbolt backend line with store path", got)
	}
	if _, err := os.Stat(bboltCityStorePath(cityDir)); err != nil {
		t.Fatalf("stat bbolt store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cityDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("managed dolt .beads directory should not be created, stat err = %v", err)
	}
}

func TestStartBeadsLifecycleRejectsUnknownBeadsBackend(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "boltdb", "")
	cfg, err := loadCityConfig(cityDir, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}

	err = startBeadsLifecycle(cityDir, "demo", cfg, &bytes.Buffer{})
	if err == nil {
		t.Fatal("startBeadsLifecycle = nil, want backend validation error")
	}
	for _, want := range []string{
		`bead store: unrecognized backend value "boltdb"`,
		`valid values for [beads] backend are: "" (dolt, default), "dolt", or "bbolt"`,
		"run `gc doctor`",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestOpenCityStoreAtRejectsUnknownBeadsBackend(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "boltdb", "")

	_, err := openCityStoreAt(cityDir)
	if err == nil {
		t.Fatal("openCityStoreAt = nil error, want backend validation error")
	}
	if !strings.Contains(err.Error(), `bead store: unrecognized backend value "boltdb"`) {
		t.Fatalf("error = %q, want unknown backend message", err)
	}
}

func writeBboltBackendTestCity(t *testing.T, backend, provider string) string {
	t.Helper()
	cityDir := t.TempDir()
	var toml strings.Builder
	toml.WriteString("[workspace]\nname = \"demo\"\nprefix = \"gc\"\n\n[beads]\n")
	if provider != "" {
		toml.WriteString("provider = \"" + provider + "\"\n")
	}
	if backend != "" {
		toml.WriteString("backend = \"" + backend + "\"\n")
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(toml.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return cityDir
}
