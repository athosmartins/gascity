package coordstore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteCGOAdapterBuildTagAndImportBoundary(t *testing.T) {
	root := repoRoot(t)
	adapterPath := filepath.Join(root, "internal", "benchmarks", "coordstore", "adapters", "sqlite-cgo", "adapter.go")
	data, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatalf("read sqlite-cgo adapter: %v", err)
	}
	text := string(data)
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "//go:build cgo && sqlite_cgo" {
		t.Fatalf("sqlite-cgo adapter first line = %q, want //go:build cgo && sqlite_cgo", lines[0])
	}
	mattnImport := "github.com/" + "mattn/go-sqlite3"
	if !strings.Contains(text, `"`+mattnImport+`"`) {
		t.Fatalf("sqlite-cgo adapter must import mattn/go-sqlite3 behind its build tag")
	}

	err = filepath.WalkDir(filepath.Join(root, "internal", "benchmarks", "coordstore"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/adapters/sqlite-cgo/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), mattnImport) {
			t.Fatalf("mattn/go-sqlite3 import leaked outside sqlite-cgo build tag: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk coordstore files: %v", err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found from %s", dir)
		}
		dir = parent
	}
}
