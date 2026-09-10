package linter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDiscoversSafeGoBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Archetype.Name != "automatic-go" || len(cfg.Boundaries) != 2 {
		t.Fatalf("discovered config = %#v", cfg)
	}
	for _, boundary := range cfg.Boundaries {
		if !strings.Contains(boundary.ForbiddenImports[0], `example\.com/project/cmd`) {
			t.Fatalf("boundary does not target module command packages: %#v", boundary)
		}
	}
	path := filepath.Join(root, "pkg", "service.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package service\nimport _ \"example.com/project/cmd/server\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := Scan(root, []string{"pkg/service.go"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Rule != "library-command-direction" {
		t.Fatalf("automatic diagnostics = %#v", diagnostics)
	}
}

func TestLoadConfigExplicitRulesTakePrecedence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lbai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `[archetype]
name = "custom"
version = "1"
allowed_extensions = [".go"]
`
	if err := os.WriteFile(filepath.Join(root, ".lbai", "rules.toml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Archetype.Name != "custom" || len(cfg.Boundaries) != 0 {
		t.Fatalf("explicit config = %#v", cfg)
	}
}

func TestLoadConfigRejectsUnknownRuleFields(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lbai"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := `[archetype]
name = "custom"
version = "1"
allowed_extension = [".go"]
`
	if err := os.WriteFile(filepath.Join(root, ".lbai", "rules.toml"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("LoadConfig accepted an unknown rules field")
	}
}
