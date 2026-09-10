package runner

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadConfigDiscoversGoProjectAndCodex(t *testing.T) {
	root := t.TempDir()
	writeConfigFixture(t, root, "go.mod", "module example.com/project\n")
	lookup := lookupOnly("codex", "go")
	cfg, err := discoverConfig(root, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Worker.Command, []string{"codex", "exec", "--sandbox", "workspace-write", "--ephemeral"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("worker command = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(cfg.Reviewer, cfg.Worker) {
		t.Fatalf("reviewer = %#v, want discovered worker %#v", cfg.Reviewer, cfg.Worker)
	}
	wantChecks := []CheckConfig{
		{Name: "test", Executable: "go", Args: []string{"test", "./..."}},
		{Name: "vet", Executable: "go", Args: []string{"vet", "./..."}},
	}
	if !reflect.DeepEqual(cfg.Checks, wantChecks) {
		t.Fatalf("checks = %#v, want %#v", cfg.Checks, wantChecks)
	}
}

func TestDiscoverConfigUsesProviderPriorityAndRequiresAgent(t *testing.T) {
	root := t.TempDir()
	cfg, err := discoverConfig(root, lookupOnly("claude", "aider"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Worker.Command[0] != "claude" {
		t.Fatalf("worker = %#v", cfg.Worker)
	}
	if _, err := discoverConfig(root, lookupOnly()); err == nil || !strings.Contains(err.Error(), "no supported coding agent") {
		t.Fatalf("missing-agent error = %v", err)
	}
}

func TestDiscoverChecksUsesNodeLockfileAndScripts(t *testing.T) {
	root := t.TempDir()
	writeConfigFixture(t, root, "package.json", `{"scripts":{"test":"vitest","build":"vite build"}}`)
	writeConfigFixture(t, root, "pnpm-lock.yaml", "lockfileVersion: 9\n")
	checks := discoverChecks(root, lookupOnly("pnpm", "npm"))
	want := []CheckConfig{
		{Name: "build", Executable: "pnpm", Args: []string{"build"}},
		{Name: "test", Executable: "pnpm", Args: []string{"test"}},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("checks = %#v, want %#v", checks, want)
	}
}

func TestDiscoverChecksUsesNPMRunForPackageScripts(t *testing.T) {
	root := t.TempDir()
	writeConfigFixture(t, root, "package.json", `{"scripts":{"build":"vite build","lint":"eslint ."}}`)
	checks := discoverChecks(root, lookupOnly("npm"))
	want := []CheckConfig{
		{Name: "build", Executable: "npm", Args: []string{"run", "build"}},
		{Name: "lint", Executable: "npm", Args: []string{"run", "lint"}},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("checks = %#v, want %#v", checks, want)
	}
}

func TestConfigRejectsInvalidCheckGraph(t *testing.T) {
	base := Config{Worker: AgentConfig{Type: "command", Command: []string{"agent"}}}
	for _, checks := range [][]CheckConfig{
		{{Name: "duplicate", Executable: "go"}, {Name: "duplicate", Executable: "go"}},
		{{Name: "test", Executable: "go", DependsOn: []string{"missing"}}},
		{{Name: "one", Executable: "go", DependsOn: []string{"two"}}, {Name: "two", Executable: "go", DependsOn: []string{"one"}}},
		{{Name: "architecture", Executable: "custom-lint"}},
	} {
		cfg := base
		cfg.Checks = checks
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate accepted %#v", checks)
		}
	}
}

func TestLoadConfigRejectsRemovedBuildTable(t *testing.T) {
	root := t.TempDir()
	writeConfigFixture(t, root, filepath.Join(".lbai", "config.toml"), `[worker]
type = "command"
command = ["agent"]

[build]
executable = "go"
args = ["test", "./..."]
`)
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("LoadConfig accepted removed [build] configuration")
	}
}

func TestWriteConfigRoundTripsAndRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		Worker:   AgentConfig{Type: "command", Command: []string{"codex", "exec"}},
		Reviewer: AgentConfig{Command: []string{}},
		Checks: []CheckConfig{
			{Name: "test", Executable: "go", Args: []string{"test", "./..."}},
			{Name: "integration", Executable: "go", Args: []string{"test", "-tags=integration", "./..."}, DependsOn: []string{"test"}},
		},
	}
	path, err := WriteConfig(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("loaded = %#v, want %#v", loaded, cfg)
	}
	if _, err := WriteConfig(root, cfg); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error = %v", err)
	}
	if path != filepath.Join(root, ".lbai", "config.toml") {
		t.Fatalf("path = %q", path)
	}
}

func lookupOnly(names ...string) executableLookup {
	available := make(map[string]bool, len(names))
	for _, name := range names {
		available[name] = true
	}
	return func(name string) (string, error) {
		if available[name] {
			return name, nil
		}
		return "", errors.New("not found")
	}
}

func writeConfigFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
