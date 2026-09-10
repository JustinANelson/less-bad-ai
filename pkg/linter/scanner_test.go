package linter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDetectsBoundaryViolationsAcrossLanguages(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/ui/card.go":   "package ui\nimport \"database/sql\"\n",
		"src/ui/Card.java": "package ui;\nimport com.mongodb.client.MongoCollection;\n",
		"src/ui/card.ts":   "import client from 'network/client';\n",
	}
	var paths []string
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, rel)
	}
	cfg := DefaultConfig()
	cfg.Boundaries = []Boundary{{Name: "UI Layer", PathPattern: "src/ui/**", ForbiddenImports: []string{`^database/sql$`, `^com\.mongodb\.`, `^network/`}, Hint: "Use a service boundary."}}
	diagnostics, err := Scan(root, paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 3 {
		t.Fatalf("got %d diagnostics: %#v", len(diagnostics), diagnostics)
	}
}

func TestAllowedImportsActAsWhitelist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ui", "x.go")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("package ui\nimport \"project/data\"\n"), 0o644)
	cfg := DefaultConfig()
	cfg.Boundaries = []Boundary{{Name: "UI", PathPattern: "ui/**", AllowedImports: []string{`^fmt$`}}}
	d, err := Scan(root, []string{"ui/x.go"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 1 {
		t.Fatalf("expected whitelist violation, got %#v", d)
	}
}
