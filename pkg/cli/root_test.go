package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jnels/less-bad-ai/pkg/linter"
	"github.com/jnels/less-bad-ai/pkg/memory"
	"github.com/jnels/less-bad-ai/pkg/runner"
)

func TestScanRegressionsAllowsLegacyViolationButRejectsNewOne(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "--quiet")
	runGitTest(t, root, "config", "user.name", "LBAI Test")
	runGitTest(t, root, "config", "user.email", "lbai@example.invalid")
	path := filepath.Join(root, "pkg", "service.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "package service\nimport _ \"example.com/app/cmd/legacy\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "--quiet", "-m", "baseline")
	cfg := linter.DefaultConfig()
	cfg.Boundaries = []linter.Boundary{{Name: "direction", PathPattern: "pkg/**", ForbiddenImports: []string{`/cmd/`}}}
	if err := os.WriteFile(path, []byte("package service\nimport (\n_ \"example.com/app/cmd/legacy\"\n_ \"example.com/app/cmd/new\"\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diagnostics, suppressed, err := scanRegressions(context.Background(), root, "HEAD", []string{"pkg/service.go"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Offender != "example.com/app/cmd/new" {
		t.Fatalf("regressions = %#v", diagnostics)
	}
	if suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1", suppressed)
	}
}

func TestLintDefaultReportsUnchangedBaselineWithoutFailing(t *testing.T) {
	root := t.TempDir()
	runGitTest(t, root, "init", "--quiet")
	runGitTest(t, root, "config", "user.name", "LBAI Test")
	runGitTest(t, root, "config", "user.email", "lbai@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pkg", "service.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "package service\nimport _ \"example.com/app/cmd/legacy\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "--quiet", "-m", "baseline")
	if err := os.WriteFile(path, []byte("// retained violation\n"+legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := (&app{out: &output, err: io.Discard, dir: root}).lintCommand()
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No new architectural violations (1 unchanged baseline violation(s))") {
		t.Fatalf("lint output:\n%s", output.String())
	}
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func TestInitWritesDiscoveredConfiguration(t *testing.T) {
	root := t.TempDir()
	git := exec.Command("git", "init", "--quiet", root)
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	var output bytes.Buffer
	a := app{out: &output, err: io.Discard, dir: root, discover: func(gotRoot string) (runner.Config, error) {
		if gotRoot != root {
			t.Fatalf("discovery root = %q, want %q", gotRoot, root)
		}
		return runner.Config{
			Worker: runner.AgentConfig{Type: "command", Command: []string{"codex", "exec"}},
			Checks: []runner.CheckConfig{
				{Name: "test", Executable: "go", Args: []string{"test", "./..."}},
				{Name: "vet", Executable: "go", Args: []string{"vet", "./..."}},
			},
		}, nil
	}}
	cmd := a.initCommand()
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.LoadConfig(root); err != nil {
		t.Fatalf("load initialized config: %v", err)
	}
	if !strings.Contains(output.String(), "Worker: codex exec") || !strings.Contains(output.String(), "Check test: go test ./...") || !strings.Contains(output.String(), "Check vet: go vet ./...") {
		t.Fatalf("init output:\n%s", output.String())
	}
	if got := filepath.Join(root, ".lbai", "config.toml"); !strings.Contains(output.String(), got) {
		t.Fatalf("init did not report %q:\n%s", got, output.String())
	}
}

func TestServeReportsPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	a := app{out: io.Discard, err: io.Discard}
	err = a.serve(context.Background(), t.TempDir(), port, false)
	if err == nil || !strings.Contains(err.Error(), "listen on 127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("serve error = %v", err)
	}
}

func TestRunAcceptsAndJoinsUnquotedPromptWords(t *testing.T) {
	cmd := (&app{}).runCommand()
	args := []string{"make", "it", "work"}
	if err := cmd.Args(cmd, args); err != nil {
		t.Fatalf("run rejected prompt words: %v", err)
	}
	if got := strings.Join(args, " "); got != "make it work" {
		t.Fatalf("joined prompt = %q", got)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("run accepted an empty prompt")
	}
}

func TestRenderSummaryReportsModulesWithBoundedASCIIRows(t *testing.T) {
	result := memory.FinalizeResult{
		CodeCommit: "0123456789abcdef",
		TouchedFiles: []string{
			"pkg/zeta/z.go",
			"README.md",
			"pkg/alpha/a.go",
			"pkg/alpha/b.go",
		},
	}
	var output bytes.Buffer
	renderSummary(&output, result, 3, "Extracted helper\n\x1b[31mwith colored output", 72)
	text := output.String()
	for _, expected := range []string{"Modules touched", "pkg/alpha, pkg/zeta, root", "3 checked, 0 violations", "Committed (01234567)", "lbai undo"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary omitted %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("summary contains a terminal escape:\n%q", text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if len(line) != 72 {
			t.Fatalf("summary line has width %d, want 72: %q", len(line), line)
		}
	}
}

func TestRenderSummaryUsesSafeMinimumWidthAndFallbackReview(t *testing.T) {
	var output bytes.Buffer
	renderSummary(&output, memory.FinalizeResult{}, -1, "", 10)
	text := output.String()
	if !strings.Contains(text, "No changes requ") || !strings.Contains(text, "none") {
		t.Fatalf("fallback summary missing values:\n%s", text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if len(line) != 40 {
			t.Fatalf("minimum-width line has width %d: %q", len(line), line)
		}
	}
}
