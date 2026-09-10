package gitengine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBeginAndUndoRemoveCreatedFiles(t *testing.T) {
	root := gitFixture(t)
	e := New(root)
	e.Now = func() time.Time { return time.Unix(1700000000, 0) }
	e.NewID = func() string { return "test" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "change it"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Head == "" || plan.SnapshotRef != "refs/lbai/snapshots/1700000000-test" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	created := filepath.Join(root, "generated.txt")
	if err := os.WriteFile(created, []byte("agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.CaptureCreatedFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := e.Undo(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusRolledBack {
		t.Fatalf("status = %s", state.Status)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("created file remains: %v", err)
	}
}

func TestDirtyWorktreeIsRestored(t *testing.T) {
	root := gitFixture(t)
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(root, "user-note.txt")
	if err := os.WriteFile(untracked, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "dirty" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Dirty || plan.State.StashRef == "" {
		t.Fatalf("dirty state not captured: %#v", plan)
	}
	if err := e.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(tracked)
	if string(b) != "user change" {
		t.Fatalf("tracked content = %q", b)
	}
	b, _ = os.ReadFile(untracked)
	if string(b) != "keep me" {
		t.Fatalf("untracked content = %q", b)
	}
}

func TestDryRunDoesNotWriteStateOrRefs(t *testing.T) {
	root := gitFixture(t)
	e := New(root)
	e.NewID = func() string { return "dry" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "noop", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote state: %v", err)
	}
	cmd := exec.Command("git", "show-ref", plan.SnapshotRef)
	cmd.Dir = root
	if err := cmd.Run(); err == nil {
		t.Fatal("dry run wrote snapshot ref")
	}
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	commands := [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}}
	for _, args := range commands {
		runGit(t, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "initial")
	return root
}
func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}
