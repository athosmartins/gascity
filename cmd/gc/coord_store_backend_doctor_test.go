package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
)

func TestCoordStoreBackendCheckReportsDefaultDolt(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "", "")
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)

	result := newCoordStoreBackendCheck(cityDir, cfg).Run(&doctor.CheckContext{Verbose: true})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "managed Dolt coord-store backend") {
		t.Fatalf("message = %q, want managed Dolt backend", result.Message)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "raw backend: (empty)") {
		t.Fatalf("details = %q, want raw empty backend", details)
	}
	if !strings.Contains(details, "bbolt path: "+bboltCityStorePath(cityDir)) {
		t.Fatalf("details = %q, want bbolt path", details)
	}
}

func TestCoordStoreBackendCheckReportsExplicitDolt(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "dolt", "")
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)

	result := newCoordStoreBackendCheck(cityDir, cfg).Run(&doctor.CheckContext{})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "managed Dolt coord-store backend") {
		t.Fatalf("message = %q, want managed Dolt backend", result.Message)
	}
}

func TestCoordStoreBackendCheckReportsBboltAbsentAsOK(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)

	result := newCoordStoreBackendCheck(cityDir, cfg).Run(&doctor.CheckContext{Verbose: true})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "store will be created on gc start") {
		t.Fatalf("message = %q, want creation-on-start message", result.Message)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "raw backend: bbolt") || !strings.Contains(details, bboltCityStorePath(cityDir)) {
		t.Fatalf("details = %q, want raw bbolt and path", details)
	}
}

func TestCoordStoreBackendCheckReportsBboltPresent(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)
	path := bboltCityStorePath(cityDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := newCoordStoreBackendCheck(cityDir, cfg).Run(&doctor.CheckContext{})
	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want ok: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "using bbolt coord-store backend at "+path) {
		t.Fatalf("message = %q, want present bbolt path", result.Message)
	}
}

func TestCoordStoreBackendCheckRejectsUnknownBackend(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "boltdb", "")
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)

	result := newCoordStoreBackendCheck(cityDir, cfg).Run(&doctor.CheckContext{Verbose: true})
	if result.Status != doctor.StatusError {
		t.Fatalf("status = %v, want error", result.Status)
	}
	if !strings.Contains(result.Message, `unrecognized backend value "boltdb"`) {
		t.Fatalf("message = %q, want invalid backend", result.Message)
	}
	if !strings.Contains(result.FixHint, `"dolt"`) || !strings.Contains(result.FixHint, "gc doctor") {
		t.Fatalf("fix hint = %q, want valid values and doctor hint", result.FixHint)
	}
}

func TestCoordStoreBackendCheckWarmupEligibleFalse(t *testing.T) {
	if newCoordStoreBackendCheck(t.TempDir(), &config.City{}).WarmupEligible() {
		t.Fatal("WarmupEligible() = true, want false")
	}
}

func TestDoctorBboltBackendSkipsCityTopologyButKeepsRigBackup(t *testing.T) {
	clearInheritedBeadsEnv(t)
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")
	rigDir := filepath.Join(cityDir, "managed")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"
prefix = "gc"

[beads]
backend = "bbolt"

[[rigs]]
name = "managed"
path = "managed"
prefix = "ma"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := contract.EnsureCanonicalMetadata(fsys.OSFS{}, filepath.Join(rigDir, ".beads", "metadata.json"), contract.MetadataState{
		Database:     "dolt",
		Backend:      "dolt",
		DoltMode:     "server",
		DoltDatabase: "managed",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)
	resolveRigPaths(cityDir, cfg.Rigs)

	oldBackupCheck := newDoctorDoltBackupCheck
	registeredBackups := 0
	newDoctorDoltBackupCheck = func(cityPath string, rig config.Rig, dataDir string) *doctor.DoltBackupCheck {
		registeredBackups++
		return doctor.NewDoltBackupCheck(cityPath, rig, dataDir)
	}
	t.Cleanup(func() { newDoctorDoltBackupCheck = oldBackupCheck })

	checks := buildDoctorChecks(cityDir, cfg, nil, buildDoctorChecksOpts{
		ControllerRunning:    true,
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
	})
	names := doctorCheckNames(checks)
	if !containsDoctorCheck(names, "coord-store-backend") {
		t.Fatalf("checks = %v, want coord-store-backend", names)
	}
	if containsDoctorCheck(names, "dolt-topology") || containsDoctorCheck(names, "dolt-drift") {
		t.Fatalf("checks = %v, want no city Dolt topology/drift under bbolt", names)
	}
	if registeredBackups != 1 {
		t.Fatalf("registered rig backup checks = %d, want 1", registeredBackups)
	}
}

func TestDoctorSkipsDoltChecksForBboltBackend(t *testing.T) {
	cityDir := writeBboltBackendTestCity(t, "bbolt", "")
	if !doctorSkipsDoltChecks(cityDir) {
		t.Fatal("doctorSkipsDoltChecks() = false, want true for bbolt backend")
	}
	cfg := loadCoordStoreBackendTestConfig(t, cityDir)
	if !managedDoltOpsCheckSkip(cityDir, cfg, nil) {
		t.Fatal("managedDoltOpsCheckSkip() = false, want true for bbolt backend")
	}
}

func loadCoordStoreBackendTestConfig(t *testing.T, cityDir string) *config.City {
	t.Helper()
	cfg, err := loadCityConfig(cityDir, io.Discard)
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}
	return cfg
}

func doctorCheckNames(checks []doctor.Check) []string {
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		names = append(names, check.Name())
	}
	return names
}

func containsDoctorCheck(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}
