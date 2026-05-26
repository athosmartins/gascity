package beads_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionCallersDoNotSelectStorageTiers(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}

	for _, dir := range []string{"cmd", "internal"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				rel, err := filepath.Rel(repoRoot, path)
				if err != nil {
					return err
				}
				if rel == filepath.Join("internal", "beads") || strings.HasPrefix(rel, filepath.Join("internal", "beads")+string(filepath.Separator)) {
					return filepath.SkipDir
				}
				if rel == filepath.Join("internal", "benchmarks") || strings.HasPrefix(rel, filepath.Join("internal", "benchmarks")+string(filepath.Separator)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(src)
			for _, forbidden := range []string{
				"beads.TierMode",
				"beads.TierIssues",
				"beads.TierWisps",
				"beads.TierBoth",
				"beads.WithEphemeral",
				"beads.WithBothTiers",
			} {
				if strings.Contains(text, forbidden) {
					rel, err := filepath.Rel(repoRoot, path)
					if err != nil {
						return err
					}
					t.Errorf("%s names %s; use beads.HandlesFor(store).Cached/Live readers instead", rel, forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
