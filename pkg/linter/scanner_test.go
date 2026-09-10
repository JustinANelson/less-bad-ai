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

func TestScanParsesDependencyManifests(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": `module example.com/app

// example.com/commented v1.0.0
require (
	example.com/allowed v1.0.0
	example.com/blocked v1.2.3 // indirect
)
`,
		"package.json": `{
  "description": "blocked-description-only",
  "peerDependencies": {"blocked-peer": "^1.0.0"},
  "optionalDependencies": {"blocked-optional": "^2.0.0"}
}`,
		"pom.xml": `<project>
  <description>blocked-description-only</description>
  <dependencies>
    <dependency><groupId>com.example</groupId><artifactId>blocked-artifact</artifactId></dependency>
  </dependencies>
</project>`,
	}
	var paths []string
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, rel)
	}
	cfg := DefaultConfig()
	cfg.ForbiddenDependencies = []ForbiddenDependency{
		{Name: "example.com/blocked"},
		{Name: "blocked-peer"},
		{Name: "blocked-optional"},
		{Name: "com.example:blocked-artifact"},
		{Name: "example.com/commented"},
		{Name: "blocked-description-only"},
	}
	diagnostics, err := Scan(root, paths, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 4 {
		t.Fatalf("got %d diagnostics, want 4: %#v", len(diagnostics), diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Offender == "example.com/commented" || diagnostic.Offender == "blocked-description-only" {
			t.Fatalf("non-dependency text produced a diagnostic: %#v", diagnostic)
		}
	}
}

func TestScanRejectsMalformedManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pom.xml")
	if err := os.WriteFile(path, []byte(`<project><dependency>`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ForbiddenDependencies = []ForbiddenDependency{{Name: "blocked"}}
	if _, err := Scan(root, []string{"pom.xml"}, cfg); err == nil {
		t.Fatal("Scan accepted malformed pom.xml")
	}
}

func TestJavaScriptImportsIgnoreCommentsAndOrdinaryStrings(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "imports.ts")
	source := `
const examples = "require('string-only') and import('also-string-only')";
const url = "https://example.com/module";
// require('comment-only')
/* import blocked from 'block-comment-only'; */
import client from 'real-static';
const lazy = import('real-dynamic');
const legacy = require('real-require');
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	imports, err := ExtractImports(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Import{
		{Name: "real-static", Line: 6},
		{Name: "real-dynamic", Line: 7},
		{Name: "real-require", Line: 8},
	}
	if len(imports) != len(want) {
		t.Fatalf("imports = %#v, want %#v", imports, want)
	}
	for i := range want {
		if imports[i] != want[i] {
			t.Fatalf("imports = %#v, want %#v", imports, want)
		}
	}
}

func TestRegressionsIgnoresLegacyViolationsAndLineMovement(t *testing.T) {
	legacy := Diagnostic{Path: "pkg/service.go", Line: 4, Rule: "direction", Violation: "forbidden", Offender: "app/cmd/tool", Severity: "error", Hint: "old hint"}
	newViolation := Diagnostic{Path: "pkg/service.go", Line: 9, Rule: "direction", Violation: "forbidden", Offender: "app/cmd/new", Severity: "error"}
	current := []Diagnostic{
		{Path: legacy.Path, Line: 20, Rule: legacy.Rule, Violation: legacy.Violation, Offender: legacy.Offender, Severity: legacy.Severity, Hint: "new hint"},
		newViolation,
	}
	got := Regressions(current, []Diagnostic{legacy})
	if len(got) != 1 || got[0] != newViolation {
		t.Fatalf("regressions = %#v, want %#v", got, []Diagnostic{newViolation})
	}
}

func TestScanSkipsDeletedPaths(t *testing.T) {
	diagnostics, err := Scan(t.TempDir(), []string{"deleted.go"}, DefaultConfig())
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("Scan deleted path = %#v, %v", diagnostics, err)
	}
}
