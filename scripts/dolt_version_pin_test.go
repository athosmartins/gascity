package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoltVersionPins(t *testing.T) {
	const doltVersion = "2.1.0"

	root := doltVersionPinRepoRoot(t)
	assertContains(t, filepath.Join(root, "deps.env"), "DOLT_VERSION="+doltVersion)
	assertContains(t, filepath.Join(root, "contrib/k8s/Dockerfile.base"), "ARG DOLT_VERSION="+doltVersion)
	assertContains(t, filepath.Join(root, "internal/doltversion/doltversion.go"), `ManagedMin = "`+doltVersion+`"`)
	assertContains(t, filepath.Join(root, ".github/scripts/install-dolt-archive.sh"), doltVersion+":linux-amd64")
	assertContains(t, filepath.Join(root, ".github/scripts/install-dolt-archive.sh"), doltVersion+":linux-arm64")
	assertContains(t, filepath.Join(root, ".github/scripts/install-dolt-archive.sh"), doltVersion+":darwin-amd64")
	assertContains(t, filepath.Join(root, ".github/scripts/install-dolt-archive.sh"), doltVersion+":darwin-arm64")

	workflowDir := filepath.Join(root, ".github/workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", workflowDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		path := filepath.Join(workflowDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if strings.Contains(string(content), "DOLT_VERSION:") &&
			!strings.Contains(string(content), `DOLT_VERSION: "`+doltVersion+`"`) {
			t.Fatalf("%s has DOLT_VERSION but is not pinned to %s", path, doltVersion)
		}
	}
}

func doltVersionPinRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root")
		}
		dir = parent
	}
}

func assertContains(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !strings.Contains(string(content), want) {
		t.Fatalf("%s does not contain %q", path, want)
	}
}
