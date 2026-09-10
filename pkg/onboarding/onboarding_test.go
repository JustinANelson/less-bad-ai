package onboarding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinANelson/less-bad-ai/pkg/runner"
)

func TestSetupInitializesConfigAndBaseline(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/new\n")
	result, err := Setup(context.Background(), testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if !result.InitializedGit || !result.CreatedConfig || !result.CreatedBaseline || !result.ConfigCommitted {
		t.Fatalf("setup result = %#v", result)
	}
	if got := gitTest(t, root, "show", "HEAD:go.mod"); !strings.Contains(got, "example.com/new") {
		t.Fatalf("baseline omitted project: %q", got)
	}
	if got := gitTest(t, root, "show", "HEAD:.lbai/config.toml"); !strings.Contains(got, `command = ['git']`) && !strings.Contains(got, `command = ["git"]`) {
		t.Fatalf("baseline omitted configuration: %q", got)
	}
	if name := gitTest(t, root, "config", "--get", "user.name"); name != "LBAI Setup Test" {
		t.Fatalf("repository identity = %q", name)
	}

	again, err := Setup(context.Background(), testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if again.InitializedGit || again.CreatedConfig || again.CreatedBaseline {
		t.Fatalf("idempotent setup changed project: %#v", again)
	}
}

func TestSetupCommitsOnlyNewConfigInCleanExistingProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "project\n")
	initializeBaseline(t, root)
	result, err := Setup(context.Background(), testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedConfig || !result.ConfigCommitted || result.CreatedBaseline {
		t.Fatalf("setup result = %#v", result)
	}
	if status := gitTest(t, root, "status", "--short"); status != "" {
		t.Fatalf("setup left clean project dirty: %q", status)
	}
	if subject := gitTest(t, root, "show", "-s", "--format=%s", "HEAD"); subject != "chore: configure less-bad-ai" {
		t.Fatalf("setup commit = %q", subject)
	}
}

func TestSetupLeavesNewConfigUncommittedWithExistingWork(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "project\n")
	initializeBaseline(t, root)
	write(t, root, "README.md", "user change\n")
	result, err := Setup(context.Background(), testOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	if !result.CreatedConfig || result.ConfigCommitted {
		t.Fatalf("setup result = %#v", result)
	}
	status := gitTest(t, root, "status", "--short", "--untracked-files=all")
	if !strings.Contains(status, "README.md") || !strings.Contains(status, ".lbai/config.toml") {
		t.Fatalf("setup did not preserve dirty work and config: %q", status)
	}
	if subject := gitTest(t, root, "show", "-s", "--format=%s", "HEAD"); subject != "baseline" {
		t.Fatalf("setup committed existing work: %q", subject)
	}
}

func TestSetupRefusesSensitiveBaseline(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env", "TOKEN=do-not-commit\n")
	_, err := Setup(context.Background(), testOptions(root))
	if err == nil || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), "--allow-sensitive") {
		t.Fatalf("sensitive setup error = %v", err)
	}
	if commandSucceeds(context.Background(), "git", root, "rev-parse", "HEAD") {
		t.Fatal("setup committed a sensitive baseline")
	}
	if _, err := os.Stat(filepath.Join(root, ".lbai", "config.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup wrote config before safety check: %v", err)
	}
}

func TestSetupRefusesAlreadyStagedSensitiveBaseline(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env.production", "TOKEN=do-not-commit\n")
	gitTest(t, root, "init", "--quiet")
	gitTest(t, root, "add", ".env.production")
	_, err := Setup(context.Background(), testOptions(root))
	if err == nil || !strings.Contains(err.Error(), ".env.production") {
		t.Fatalf("staged sensitive setup error = %v", err)
	}
	if commandSucceeds(context.Background(), "git", root, "rev-parse", "HEAD") {
		t.Fatal("setup committed a staged sensitive baseline")
	}
}

func TestSetupAllowsExplicitSensitiveBaseline(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env", "TOKEN=explicitly-allowed\n")
	opts := testOptions(root)
	opts.AllowSensitive = true
	if _, err := Setup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, root, "show", "HEAD:.env"); !strings.Contains(got, "explicitly-allowed") {
		t.Fatalf("allowed baseline = %q", got)
	}
}

func TestDiagnoseReportsReadyProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/ready\n")
	if _, err := Setup(context.Background(), testOptions(root)); err != nil {
		t.Fatal(err)
	}
	result := Diagnose(context.Background(), testOptions(root))
	if result.HasFailures() {
		t.Fatalf("ready diagnosis has failures: %#v", result.Checks)
	}
	for _, name := range []string{"git", "repository", "baseline", "agent", "configuration", "identity", "architecture"} {
		if !hasCheck(result.Checks, name, Pass) {
			t.Fatalf("missing passing %s check: %#v", name, result.Checks)
		}
	}
}

func TestDiagnoseReportsActionableFreshDirectory(t *testing.T) {
	root := t.TempDir()
	result := Diagnose(context.Background(), testOptions(root))
	if !result.HasFailures() || !hasCheck(result.Checks, "repository", Fail) || !hasCheck(result.Checks, "baseline", Fail) {
		t.Fatalf("fresh diagnosis = %#v", result.Checks)
	}
}

func TestSetupAndDoctorRejectInvalidArchitectureRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.Join(".lbai", "rules.toml"), `[[boundaries]]
name = "broken"
path_pattern = "**"
forbidden_imports = ["["]
`)
	if _, err := Setup(context.Background(), testOptions(root)); err == nil || !strings.Contains(err.Error(), "architectural rules") {
		t.Fatalf("invalid-rules setup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup initialized Git before validating rules: %v", err)
	}
	result := Diagnose(context.Background(), testOptions(root))
	if !hasCheck(result.Checks, "architecture", Fail) {
		t.Fatalf("invalid-rules diagnosis = %#v", result.Checks)
	}
}

func testOptions(root string) Options {
	return Options{Dir: root, Discover: func(string) (runner.Config, error) {
		return runner.Config{Worker: runner.AgentConfig{Type: "command", Command: []string{"git"}}}, nil
	}, LookPath: exec.LookPath, GitName: "LBAI Setup Test", GitEmail: "setup@example.invalid"}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func initializeBaseline(t *testing.T, root string) {
	t.Helper()
	gitTest(t, root, "init", "--quiet")
	gitTest(t, root, "config", "user.name", "Existing User")
	gitTest(t, root, "config", "user.email", "existing@example.invalid")
	gitTest(t, root, "add", "--all")
	gitTest(t, root, "commit", "--quiet", "-m", "baseline")
}

func hasCheck(checks []Check, name string, status Status) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}
