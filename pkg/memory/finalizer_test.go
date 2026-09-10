package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JustinANelson/less-bad-ai/pkg/gitengine"
)

func TestFinalizerCreatesLinkedCodeAndMetadataCommits(t *testing.T) {
	root, base := memoryGitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "feature.go"), []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 15, 4, 5, 123, time.UTC)
	nowCalls := 0
	result, err := (Finalizer{Root: root, Now: func() time.Time { nowCalls++; return now }}).Finalize(context.Background(), FinalizeOptions{
		Base: base, Prompt: "Add feature support", WorkerSummary: "Added feature support.", ReviewerSummary: "No changes required.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if nowCalls != 1 {
		t.Fatalf("clock calls = %d, want 1", nowCalls)
	}
	if parent := runMemoryGit(t, root, "rev-parse", result.CodeCommit+"^"); parent != base {
		t.Fatalf("code commit parent = %s, want %s", parent, base)
	}
	if parent := runMemoryGit(t, root, "rev-parse", result.MetadataCommit+"^"); parent != result.CodeCommit {
		t.Fatalf("metadata commit parent = %s, want %s", parent, result.CodeCommit)
	}
	if !strings.Contains(result.TracePath, "20260910T150405Z_"+short(result.CodeCommit)) {
		t.Fatalf("trace path = %q", result.TracePath)
	}
	traceBytes := []byte(runMemoryGit(t, root, "show", result.MetadataCommit+":"+result.TracePath))
	var trace Trace
	if err := json.Unmarshal(traceBytes, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.CommitSHA != result.CodeCommit || trace.TraceID != "lbai-"+strconv.FormatInt(now.UnixNano(), 10) {
		t.Fatalf("trace does not identify code commit: %#v", trace)
	}
	if commandSucceeds(root, "git", "cat-file", "-e", result.CodeCommit+":"+result.TracePath) {
		t.Fatal("trace was included in the code commit")
	}
	message := runMemoryGit(t, root, "show", "-s", "--format=%B", result.CodeCommit)
	if !strings.Contains(message, "LBAI-Trace-ID: "+trace.TraceID) {
		t.Fatalf("code commit message omitted trace ID:\n%s", message)
	}
	if status := runMemoryGit(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("finalized repository is dirty:\n%s", status)
	}
}

func TestExecGitRunnerDoesNotMixWarningsIntoSuccessfulOutput(t *testing.T) {
	root, base := memoryGitFixture(t)
	runMemoryGit(t, root, "config", "core.autocrlf", "true")
	runMemoryGit(t, root, "config", "core.safecrlf", "warn")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := (ExecGitRunner{}).Run(context.Background(), root, "diff", "--name-only", base, "--")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "base.txt" {
		t.Fatalf("git stdout = %q, want only the changed path", got)
	}
}

type failMetadataCommitRunner struct {
	commits int
}

func (r *failMetadataCommitRunner) Run(ctx context.Context, root string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "commit" {
		r.commits++
		if r.commits == 2 {
			return nil, errors.New("scripted metadata commit failure")
		}
	}
	return (ExecGitRunner{}).Run(ctx, root, args...)
}

func TestMetadataCommitFailureCanRollBackEntireTransaction(t *testing.T) {
	root, base := memoryGitFixture(t)
	engine := gitengine.New(root)
	engine.NewID = func() string { return "memory-failure" }
	if _, err := engine.Begin(context.Background(), gitengine.BeginOptions{Prompt: "add feature"}); err != nil {
		t.Fatal(err)
	}
	feature := filepath.Join(root, "feature.go")
	if err := os.WriteFile(feature, []byte("package feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := engine.CaptureCreatedFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner := &failMetadataCommitRunner{}
	_, err := (Finalizer{Root: root, Now: func() time.Time { return time.Date(2026, 9, 10, 15, 4, 5, 0, time.UTC) }, Git: runner}).Finalize(context.Background(), FinalizeOptions{Base: base, Prompt: "add feature"})
	if err == nil || !strings.Contains(err.Error(), "scripted metadata commit failure") {
		t.Fatalf("Finalize error = %v", err)
	}
	if head := runMemoryGit(t, root, "rev-parse", "HEAD"); head == base {
		t.Fatal("test did not fail after the code commit")
	}
	state, err := engine.Undo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != gitengine.StatusRolledBack {
		t.Fatalf("rollback status = %s", state.Status)
	}
	if head := runMemoryGit(t, root, "rev-parse", "HEAD"); head != base {
		t.Fatalf("HEAD after rollback = %s, want %s", head, base)
	}
	if status := runMemoryGit(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("repository is dirty after rollback:\n%s", status)
	}
	for _, path := range []string{"feature.go", "ARCHITECTURE.md", "AI_CONTEXT.md", filepath.Join("docs", "decisions", "LOG.md")} {
		if _, err := os.Stat(filepath.Join(root, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated path remains after rollback: %s (%v)", path, err)
		}
	}
}

func memoryGitFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test User"}} {
		runMemoryGit(t, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".lbai/state.json\n.lbai/snapshots/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runMemoryGit(t, root, "add", ".gitignore", "base.txt")
	runMemoryGit(t, root, "commit", "-m", "initial")
	return root, runMemoryGit(t, root, "rev-parse", "HEAD")
}

func runMemoryGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

func commandSucceeds(root, name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	return cmd.Run() == nil
}
