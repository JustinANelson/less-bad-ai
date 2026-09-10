package gitengine

import (
	"context"
	"errors"
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

func TestUndoRestoresDirtyWorktreeAndRemovesOnlyTransactionFiles(t *testing.T) {
	root := gitFixture(t)
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(root, "user note.txt")
	if err := os.WriteFile(userFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "dirty-rollback" }
	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err != nil {
		t.Fatal(err)
	}
	agentFile := filepath.Join(root, "agent output.txt")
	if err := os.WriteFile(agentFile, []byte("remove me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, tracked, "user change")
	assertFileContent(t, userFile, "keep me")
	if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
		t.Fatalf("transaction-created file remains: %v", err)
	}
}

func TestUndoAfterCompletePreservesRestoredUntrackedFiles(t *testing.T) {
	root := gitFixture(t)
	userFile := filepath.Join(root, "user.txt")
	if err := os.WriteFile(userFile, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "complete-then-undo" }
	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err != nil {
		t.Fatal(err)
	}
	agentFile := filepath.Join(root, "agent.txt")
	if err := os.WriteFile(agentFile, []byte("agent data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	postRunFile := filepath.Join(root, "after-run.txt")
	if err := os.WriteFile(postRunFile, []byte("new user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, userFile, "user data")
	assertFileContent(t, postRunFile, "new user data")
	if _, err := os.Stat(agentFile); !os.IsNotExist(err) {
		t.Fatalf("transaction-created file remains: %v", err)
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
	excludePath := filepath.Join(root, ".git", "info", "exclude")
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dry-run-user.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "dry" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "noop", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote state: %v", err)
	}
	if plan.State.StashRef != "refs/lbai/stash/dry" {
		t.Fatalf("dry run did not plan stash ref: %#v", plan.State)
	}
	cmd := exec.Command("git", "show-ref", plan.SnapshotRef)
	cmd.Dir = root
	if err := cmd.Run(); err == nil {
		t.Fatal("dry run wrote snapshot ref")
	}
	assertFileContent(t, filepath.Join(root, "dry-run-user.txt"), "keep")
	excludeAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(excludeAfter) != string(excludeBefore) {
		t.Fatal("dry run modified Git's local exclude file")
	}
}

func TestBeginKeepsRuntimeJournalOutOfDirtyStash(t *testing.T) {
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "journal-exclude" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != PhaseReady || plan.State.Phase != PhaseReady {
		t.Fatalf("transaction phase = %q, plan phase = %q", state.Phase, plan.State.Phase)
	}
	if status := runGit(t, root, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("runtime state dirtied transaction worktree:\n%s", status)
	}
	if ignored := runGit(t, root, "check-ignore", ".lbai/state.json"); ignored != ".lbai/state.json" {
		t.Fatalf("state journal is not locally excluded: %q", ignored)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(root, "tracked.txt"), "user change")
}

func TestBeginDoesNotTreatLegacyUnignoredJournalAsUserChanges(t *testing.T) {
	root := gitFixture(t)
	previous := State{
		Version: stateVersion, TransactionID: "previous", Repository: root,
		LastCleanHead: runGit(t, root, "rev-parse", "HEAD"), SnapshotRef: "refs/lbai/snapshots/previous",
		Timestamp: time.Now().UTC(), Status: StatusComplete,
	}
	if err := saveState(root, previous); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.NewID = func() string { return "upgrade" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dirty || plan.State.StashRef != "" {
		t.Fatalf("legacy runtime journal was treated as user work: %#v", plan)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestUndoRecoversInterruptionBeforeStashPush(t *testing.T) {
	root := interruptedDirtyFixture(t, "before-stash", PhaseStashing)
	e := New(root)
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertInterruptedUserChanges(t, root)
	assertMissingRef(t, root, "refs/stash")
	assertMissingRef(t, root, "refs/lbai/stash/before-stash")
}

func TestUndoRecoversInterruptionAfterStashPush(t *testing.T) {
	root := interruptedDirtyFixture(t, "after-stash", PhaseStashing)
	runGit(t, root, "stash", "push", "--include-untracked", "--message", "lbai after-stash")
	if _, err := os.Stat(statePath(root)); err != nil {
		t.Fatalf("stash removed recovery journal: %v", err)
	}
	if _, err := New(root).Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertInterruptedUserChanges(t, root)
	assertMissingRef(t, root, "refs/stash")
	assertMissingRef(t, root, "refs/lbai/stash/after-stash")
}

func TestUndoRecoversInterruptionBeforeStashDrop(t *testing.T) {
	root := interruptedDirtyFixture(t, "before-drop", PhaseStashing)
	runGit(t, root, "stash", "push", "--include-untracked", "--message", "lbai before-drop")
	sha := runGit(t, root, "rev-parse", "refs/stash")
	runGit(t, root, "update-ref", "refs/lbai/stash/before-drop", sha, "")
	if _, err := New(root).Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertInterruptedUserChanges(t, root)
	assertMissingRef(t, root, "refs/stash")
	assertMissingRef(t, root, "refs/lbai/stash/before-drop")
}

func TestTransactionFromNestedDirectoryAtDetachedHead(t *testing.T) {
	root := gitFixture(t)
	head := runGit(t, root, "rev-parse", "HEAD")
	runGit(t, root, "checkout", "--detach", head)
	nested := filepath.Join(root, "path with spaces", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	e := New(nested)
	e.NewID = func() string { return "nested" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(plan.Root) != filepath.Clean(root) || plan.Head != head {
		t.Fatalf("unexpected nested plan: %#v", plan)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

func TestUndoDoesNotRequireSnapshotRefAndIsIdempotent(t *testing.T) {
	root := gitFixture(t)
	e := New(root)
	e.NewID = func() string { return "stale-snapshot" }
	plan, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "update-ref", "-d", plan.SnapshotRef)
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	postUndo := filepath.Join(root, "post-undo.txt")
	if err := os.WriteFile(postUndo, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Undo(context.Background(), false); err != nil {
		t.Fatalf("second undo: %v", err)
	}
	assertFileContent(t, postUndo, "keep")
}

func TestLoadStateRejectsCorruptState(t *testing.T) {
	root := gitFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".lbai"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(root); err == nil || !strings.Contains(err.Error(), "decode transaction state") {
		t.Fatalf("LoadState error = %v", err)
	}
	if _, err := New(root).Begin(context.Background(), BeginOptions{Prompt: "agent"}); err == nil || !strings.Contains(err.Error(), "inspect previous transaction") {
		t.Fatalf("Begin error = %v", err)
	}
}

func TestSaveStateAtomicallyReplacesExistingJournal(t *testing.T) {
	root := t.TempDir()
	state := State{Version: stateVersion, TransactionID: "atomic", Repository: root, LastCleanHead: strings.Repeat("a", 40), SnapshotRef: "refs/lbai/snapshots/atomic", Timestamp: time.Now().UTC(), Status: StatusInProgress, Phase: PhaseReady}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	state.Status, state.Phase = StatusComplete, ""
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != StatusComplete || loaded.TransactionID != state.TransactionID {
		t.Fatalf("loaded state = %#v", loaded)
	}
	temporary, err := filepath.Glob(filepath.Join(root, ".lbai", "state-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary state files remain: %v", temporary)
	}
}

func TestBeginRefusesTransactionThatRequiresRecovery(t *testing.T) {
	root := gitFixture(t)
	state := State{
		Version:       stateVersion,
		TransactionID: "needs-recovery",
		Repository:    root,
		LastCleanHead: runGit(t, root, "rev-parse", "HEAD"),
		SnapshotRef:   "refs/lbai/snapshots/needs-recovery",
		Timestamp:     time.Now().UTC(),
		Status:        StatusRecoveryRequired,
	}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}

	if _, err := New(root).Begin(context.Background(), BeginOptions{Prompt: "new work"}); err == nil || !strings.Contains(err.Error(), "recovery_required") {
		t.Fatalf("Begin error = %v", err)
	}
	got, err := LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.TransactionID != state.TransactionID || got.Status != StatusRecoveryRequired {
		t.Fatalf("recovery state was replaced: %#v", got)
	}
}

func TestBeginDoesNotOverwriteExistingSnapshotRef(t *testing.T) {
	root := gitFixture(t)
	e := New(root)
	e.Now = func() time.Time { return time.Unix(1700000000, 0) }
	e.NewID = func() string { return "collision" }
	ref := "refs/lbai/snapshots/1700000000-collision"
	head := runGit(t, root, "rev-parse", "HEAD")
	runGit(t, root, "update-ref", ref, head, "")

	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err == nil || !strings.Contains(err.Error(), "create snapshot ref") {
		t.Fatalf("Begin error = %v", err)
	}
	if got := runGit(t, root, "rev-parse", ref); got != head {
		t.Fatalf("snapshot ref moved from %s to %s", head, got)
	}
	if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
		t.Fatalf("failed begin wrote state: %v", err)
	}
}

func TestBeginStashRefCollisionRestoresDirtyWorktree(t *testing.T) {
	root := gitFixture(t)
	head := runGit(t, root, "rev-parse", "HEAD")
	runGit(t, root, "update-ref", "refs/lbai/stash/collision", head, "")
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(root, "user file.txt")
	if err := os.WriteFile(userFile, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	e.Now = func() time.Time { return time.Unix(1700000001, 0) }
	e.NewID = func() string { return "collision" }

	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err == nil || !strings.Contains(err.Error(), "retain saved changes") {
		t.Fatalf("Begin error = %v", err)
	}
	assertFileContent(t, tracked, "user change")
	assertFileContent(t, userFile, "user data")
	if got := runGit(t, root, "rev-parse", "refs/lbai/stash/collision"); got != head {
		t.Fatalf("existing stash ref moved from %s to %s", head, got)
	}
	assertMissingRef(t, root, "refs/lbai/snapshots/1700000001-collision")
	assertMissingRef(t, root, "refs/stash")
}

func TestBeginStateWriteFailureRemovesSnapshotRef(t *testing.T) {
	root := gitFixture(t)
	e := New(root)
	e.Now = func() time.Time { return time.Unix(1700000002, 0) }
	e.NewID = func() string { return "state-failure" }
	e.SaveState = func(string, State) error { return errors.New("injected state write failure") }

	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err == nil || !strings.Contains(err.Error(), "save snapshot state") {
		t.Fatalf("Begin error = %v", err)
	}
	assertMissingRef(t, root, "refs/lbai/snapshots/1700000002-state-failure")
}

func TestBeginRequiresInitialCommit(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	if _, err := New(root).Begin(context.Background(), BeginOptions{Prompt: "agent"}); err == nil || !strings.Contains(err.Error(), "initial commit") {
		t.Fatalf("Begin error = %v", err)
	}
	if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
		t.Fatalf("failed begin wrote state: %v", err)
	}
}

func TestUndoRejectsStateFromAnotherRepository(t *testing.T) {
	first := gitFixture(t)
	second := gitFixture(t)
	e := New(first)
	e.NewID = func() string { return "first" }
	if _, err := e.Begin(context.Background(), BeginOptions{Prompt: "agent"}); err != nil {
		t.Fatal(err)
	}
	stateBytes, err := os.ReadFile(statePath(first))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath(second)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath(second), stateBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before := runGit(t, second, "rev-parse", "HEAD")
	if _, err := New(second).Undo(context.Background(), false); err == nil || !strings.Contains(err.Error(), "state belongs to repository") {
		t.Fatalf("Undo error = %v", err)
	}
	after := runGit(t, second, "rev-parse", "HEAD")
	if after != before {
		t.Fatalf("second repository moved from %s to %s", before, after)
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

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s content = %q, want %q", path, b, want)
	}
}

func assertMissingRef(t *testing.T, root, ref string) {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = root
	if err := cmd.Run(); err == nil {
		t.Fatalf("Git ref still exists: %s", ref)
	}
}

func interruptedDirtyFixture(t *testing.T, id string, phase Phase) string {
	t.Helper()
	root := gitFixture(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("user tracked change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user note.txt"), []byte("user untracked change"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New(root)
	if err := e.ensureRuntimeExcluded(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	head := runGit(t, root, "rev-parse", "HEAD")
	snapshot := "refs/lbai/snapshots/" + id
	runGit(t, root, "update-ref", snapshot, head, "")
	state := State{
		Version: stateVersion, TransactionID: id, Repository: root,
		LastCleanHead: head, SnapshotRef: snapshot, StashRef: "refs/lbai/stash/" + id,
		Timestamp: time.Now().UTC(), Status: StatusInProgress, Phase: phase, Prompt: "agent",
		PreservedUntracked: []string{"user note.txt"},
	}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertInterruptedUserChanges(t *testing.T, root string) {
	t.Helper()
	assertFileContent(t, filepath.Join(root, "tracked.txt"), "user tracked change")
	assertFileContent(t, filepath.Join(root, "user note.txt"), "user untracked change")
	state, err := LoadState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusRolledBack || state.Phase != "" || state.StashRef != "" {
		t.Fatalf("unexpected recovered state: %#v", state)
	}
}
