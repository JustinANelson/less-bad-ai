package gitengine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Commander interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type ExecCommander struct{}

func (ExecCommander) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Engine struct {
	Dir   string
	Git   Commander
	Now   func() time.Time
	NewID func() string
}

type BeginOptions struct {
	Prompt string
	DryRun bool
}

type Plan struct {
	Root        string
	Head        string
	SnapshotRef string
	Dirty       bool
	State       State
}

func New(dir string) *Engine {
	return &Engine{Dir: dir, Git: ExecCommander{}, Now: time.Now, NewID: randomID}
}

func randomID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (e *Engine) Root(ctx context.Context) (string, error) {
	out, err := e.git(ctx, e.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("current directory is not a Git repository: %w", err)
	}
	root, err := filepath.Abs(strings.TrimSpace(string(out)))
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	return filepath.Clean(root), nil
}

func (e *Engine) Begin(ctx context.Context, opts BeginOptions) (Plan, error) {
	root, err := e.Root(ctx)
	if err != nil {
		return Plan{}, err
	}
	previous, stateErr := LoadState(root)
	if stateErr == nil {
		if previous.Status == StatusInProgress || previous.Status == StatusRecoveryRequired {
			return Plan{}, fmt.Errorf("transaction %s is not finished (status %s)", previous.TransactionID, previous.Status)
		}
	} else if !errors.Is(stateErr, ErrNoTransaction) {
		return Plan{}, fmt.Errorf("inspect previous transaction: %w", stateErr)
	}
	headOut, err := e.git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return Plan{}, fmt.Errorf("repository must have an initial commit: %w", err)
	}
	head := strings.TrimSpace(string(headOut))
	if !opts.DryRun {
		if err := e.ensureRuntimeExcluded(ctx, root); err != nil {
			return Plan{}, err
		}
	}
	statusOut, err := e.git(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return Plan{}, err
	}
	dirty := len(bytes.TrimSpace(statusOut)) > 0
	untrackedOut, err := e.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Plan{}, fmt.Errorf("list pre-transaction untracked files: %w", err)
	}
	now := e.now().UTC()
	id := e.newID()
	snapshot := fmt.Sprintf("refs/lbai/snapshots/%d-%s", now.Unix(), id)
	state := State{
		Version: stateVersion, TransactionID: id, Repository: root,
		LastCleanHead: head, SnapshotRef: snapshot, Timestamp: now,
		Status: StatusInProgress, Phase: PhaseSnapshotCreated, Prompt: opts.Prompt,
		PreservedUntracked: nulSeparatedPaths(untrackedOut),
	}
	if dirty {
		state.StashRef = fmt.Sprintf("refs/lbai/stash/%s", id)
	}
	plan := Plan{Root: root, Head: head, SnapshotRef: snapshot, Dirty: dirty, State: state}
	if opts.DryRun {
		return plan, nil
	}
	if _, err := e.git(ctx, root, "update-ref", snapshot, head, ""); err != nil {
		return Plan{}, fmt.Errorf("create snapshot ref: %w", err)
	}
	if err := saveState(root, state); err != nil {
		cleanupErr := e.deleteRef(ctx, root, snapshot)
		return Plan{}, joinOperationError("save snapshot state", err, cleanupErr)
	}
	if dirty {
		state.Phase = PhaseStashing
		if err := saveState(root, state); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "", false, "")
			cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
			return Plan{}, joinOperationError("save stashing state", err, cleanupErr)
		}
		if _, err := e.git(ctx, root, "stash", "push", "--include-untracked", "--message", "lbai "+id); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "", false, "")
			cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
			return Plan{}, joinOperationError("stash existing changes", err, cleanupErr)
		}
		stashSHA, err := e.git(ctx, root, "rev-parse", "refs/stash")
		if err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "stash@{0}", true, "")
			cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
			return Plan{}, joinOperationError("resolve saved changes", err, cleanupErr)
		}
		if _, err := e.git(ctx, root, "update-ref", state.StashRef, strings.TrimSpace(string(stashSHA)), ""); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "stash@{0}", true, "")
			cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
			return Plan{}, joinOperationError("retain saved changes", err, cleanupErr)
		}
		if _, err := e.git(ctx, root, "stash", "drop", "stash@{0}"); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, state.StashRef, true, state.StashRef)
			cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
			return Plan{}, joinOperationError("detach saved changes from stash stack", err, cleanupErr)
		}
		plan.State = state
	}
	state.Phase = PhaseReady
	plan.State = state
	if err := saveState(root, state); err != nil {
		changesRef := ""
		if dirty {
			changesRef = state.StashRef
		}
		cleanupErr := e.abortBegin(ctx, root, snapshot, changesRef, false, state.StashRef)
		cleanupErr = errors.Join(cleanupErr, e.recordBeginFailure(root, &state, cleanupErr))
		return Plan{}, joinOperationError("save transaction state", err, cleanupErr)
	}
	return plan, nil
}

func (e *Engine) CaptureCreatedFiles(ctx context.Context) error {
	root, err := e.Root(ctx)
	if err != nil {
		return err
	}
	state, err := LoadState(root)
	if err != nil {
		return err
	}
	out, err := e.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	state.CreatedFiles = transactionOwnedPaths(nulSeparatedPaths(out), state.PreservedUntracked, state.CreatedFiles)
	return saveState(root, state)
}

func (e *Engine) Complete(ctx context.Context) error {
	root, err := e.Root(ctx)
	if err != nil {
		return err
	}
	state, err := LoadState(root)
	if err != nil {
		return err
	}
	untrackedOut, err := e.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("record transaction-created files: %w", err)
	}
	state.CreatedFiles = transactionOwnedPaths(nulSeparatedPaths(untrackedOut), state.PreservedUntracked, state.CreatedFiles)
	if err := e.restoreStash(ctx, root, &state); err != nil {
		state.Status, state.Error = StatusRecoveryRequired, err.Error()
		_ = saveState(root, state)
		return err
	}
	state.Status, state.Phase = StatusComplete, ""
	return saveState(root, state)
}

func (e *Engine) Undo(ctx context.Context, hard bool) (State, error) {
	root, err := e.Root(ctx)
	if err != nil {
		return State{}, err
	}
	state, err := LoadState(root)
	if err != nil {
		return State{}, err
	}
	if filepath.Clean(state.Repository) != root {
		return State{}, fmt.Errorf("state belongs to repository %s, not %s", state.Repository, root)
	}
	if _, err := e.git(ctx, root, "cat-file", "-e", state.LastCleanHead+"^{commit}"); err != nil {
		return State{}, fmt.Errorf("pre-run commit is unavailable: %w", err)
	}
	if state.StashRef != "" {
		if err := e.prepareStashRecovery(ctx, root, &state); err != nil {
			state.Status, state.Error = StatusRecoveryRequired, err.Error()
			_ = saveState(root, state)
			return state, err
		}
	}
	// Discover untracked files immediately before resetting an active run. This
	// is essential on failure paths, which can roll back before
	// CaptureCreatedFiles is called. Once a run is complete, newly appearing
	// untracked files belong to the user and must not be added to the cleanup set.
	if state.Status == StatusInProgress || state.Status == StatusRecoveryRequired {
		untrackedOut, err := e.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return State{}, fmt.Errorf("list transaction-created files: %w", err)
		}
		state.CreatedFiles = transactionOwnedPaths(nulSeparatedPaths(untrackedOut), state.PreservedUntracked, state.CreatedFiles)
	}
	if _, err := e.git(ctx, root, "reset", "--hard", state.LastCleanHead); err != nil {
		return State{}, fmt.Errorf("reset to pre-run commit: %w", err)
	}
	if hard {
		if _, err := e.git(ctx, root, "clean", "-fd"); err != nil {
			return State{}, err
		}
	} else if err := removeCreated(root, state.CreatedFiles); err != nil {
		return State{}, err
	}
	if err := e.restoreStash(ctx, root, &state); err != nil {
		state.Status, state.Error = StatusRecoveryRequired, err.Error()
		_ = saveState(root, state)
		return state, err
	}
	state.Status, state.Phase, state.Error = StatusRolledBack, "", ""
	if err := saveState(root, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (e *Engine) restoreStash(ctx context.Context, root string, state *State) error {
	if state.StashRef == "" {
		return nil
	}
	if _, err := e.git(ctx, root, "stash", "apply", "--index", state.StashRef); err != nil {
		return fmt.Errorf("restore pre-run changes from %s: %w", state.StashRef, err)
	}
	if err := e.deleteRef(ctx, root, state.StashRef); err != nil {
		return err
	}
	state.StashRef = ""
	return nil
}

func (e *Engine) prepareStashRecovery(ctx context.Context, root string, state *State) error {
	if state.StashRef == "" {
		return nil
	}
	_, customErr := e.git(ctx, root, "rev-parse", "--verify", "--quiet", state.StashRef)
	stackRef, stackSHA, err := e.findTransactionStash(ctx, root, state.TransactionID)
	if err != nil {
		return err
	}
	if customErr == nil {
		if stackRef != "" {
			if _, err := e.git(ctx, root, "stash", "drop", stackRef); err != nil {
				return fmt.Errorf("detach recovered stash from stack: %w", err)
			}
		}
		state.Phase = PhaseReady
		return saveState(root, *state)
	}
	if stackRef == "" && (state.Phase == PhaseSnapshotCreated || state.Phase == PhaseStashing) {
		status, err := e.git(ctx, root, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return fmt.Errorf("inspect interrupted stash worktree: %w", err)
		}
		if len(bytes.TrimSpace(status)) == 0 {
			return fmt.Errorf("transaction %s has no saved stash and a clean worktree; manual recovery is required", state.TransactionID)
		}
		if _, err := e.git(ctx, root, "stash", "push", "--include-untracked", "--message", "lbai recovery "+state.TransactionID); err != nil {
			return fmt.Errorf("capture changes from interrupted stash: %w", err)
		}
		stackRef, stackSHA, err = e.findTransactionStash(ctx, root, state.TransactionID)
		if err != nil {
			return err
		}
	}
	if stackRef == "" || stackSHA == "" {
		return fmt.Errorf("saved changes for transaction %s are unavailable", state.TransactionID)
	}
	if _, err := e.git(ctx, root, "update-ref", state.StashRef, stackSHA, ""); err != nil {
		return fmt.Errorf("retain recovered stash at %s: %w", state.StashRef, err)
	}
	if _, err := e.git(ctx, root, "stash", "drop", stackRef); err != nil {
		return fmt.Errorf("detach recovered stash from stack: %w", err)
	}
	state.Phase = PhaseReady
	if err := saveState(root, *state); err != nil {
		return fmt.Errorf("save recovered stash state: %w", err)
	}
	return nil
}

func (e *Engine) findTransactionStash(ctx context.Context, root, transactionID string) (string, string, error) {
	out, err := e.git(ctx, root, "stash", "list", "--format=%gd%x09%H%x09%gs")
	if err != nil {
		return "", "", fmt.Errorf("list Git stashes: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(strings.TrimSuffix(line, "\r"), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		subject := strings.TrimSpace(parts[2])
		if strings.HasSuffix(subject, ": lbai "+transactionID) || strings.HasSuffix(subject, ": lbai recovery "+transactionID) || subject == "lbai "+transactionID || subject == "lbai recovery "+transactionID {
			return parts[0], parts[1], nil
		}
	}
	return "", "", nil
}

func removeCreated(root string, paths []string) error {
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, rel := range paths {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return fmt.Errorf("unsafe transaction-created path %q", rel)
		}
		path := filepath.Join(root, clean)
		within, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(within, "..") {
			return fmt.Errorf("path escapes repository: %q", rel)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", rel, err)
		}
	}
	return nil
}

func (e *Engine) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if e.Git == nil {
		e.Git = ExecCommander{}
	}
	return e.Git.Run(ctx, dir, "git", args...)
}
func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
func (e *Engine) newID() string {
	if e.NewID != nil {
		return e.NewID()
	}
	return randomID()
}
func (e *Engine) deleteRef(ctx context.Context, root, ref string) error {
	_, err := e.git(ctx, root, "update-ref", "-d", ref)
	return err
}

func (e *Engine) ensureRuntimeExcluded(ctx context.Context, root string) error {
	out, err := e.git(ctx, root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("locate Git exclude file: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Git exclude file %s: %w", path, err)
	}
	existing := make(map[string]struct{})
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		existing[strings.TrimSpace(line)] = struct{}{}
	}
	wanted := []string{"/.lbai/state.json", "/.lbai/snapshots/"}
	var missing []string
	for _, pattern := range wanted {
		if _, ok := existing[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Git exclude directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open Git exclude file %s: %w", path, err)
	}
	defer f.Close()
	prefix := ""
	if len(b) > 0 && b[len(b)-1] != '\n' {
		prefix = "\n"
	}
	if _, err := fmt.Fprintf(f, "%s# less-bad-ai runtime recovery data\n%s\n", prefix, strings.Join(missing, "\n")); err != nil {
		return fmt.Errorf("update Git exclude file %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync Git exclude file %s: %w", path, err)
	}
	return nil
}

func (e *Engine) recordBeginFailure(root string, state *State, cleanupErr error) error {
	if cleanupErr != nil {
		state.Status = StatusRecoveryRequired
		state.Error = cleanupErr.Error()
	} else {
		state.Status = StatusRolledBack
		state.Phase = ""
		state.StashRef = ""
		state.Error = ""
	}
	return saveState(root, *state)
}

// abortBegin restores user changes after a partially completed Begin. Refs are
// retained when restoration fails so manual recovery remains possible.
func (e *Engine) abortBegin(ctx context.Context, root, snapshot, changesRef string, dropTopStash bool, ownedStashRef string) error {
	if changesRef != "" {
		if _, err := e.git(ctx, root, "stash", "apply", "--index", changesRef); err != nil {
			return fmt.Errorf("restore saved changes from %s: %w", changesRef, err)
		}
	}
	var cleanupErrs []error
	if dropTopStash {
		if _, err := e.git(ctx, root, "stash", "drop", "stash@{0}"); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("drop temporary stash: %w", err))
		}
	}
	if ownedStashRef != "" {
		if err := e.deleteRef(ctx, root, ownedStashRef); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("delete temporary stash ref: %w", err))
		}
	}
	if err := e.deleteRef(ctx, root, snapshot); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Errorf("delete snapshot ref: %w", err))
	}
	return errors.Join(cleanupErrs...)
}

func joinOperationError(operation string, operationErr, cleanupErr error) error {
	if cleanupErr == nil {
		return fmt.Errorf("%s: %w", operation, operationErr)
	}
	return fmt.Errorf("%s: %w", operation, errors.Join(operationErr, fmt.Errorf("restore worktree after failed begin: %w", cleanupErr)))
}

func nulSeparatedPaths(b []byte) []string {
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			out = append(out, filepath.ToSlash(string(part)))
		}
	}
	return out
}

func transactionOwnedPaths(current, preserved, recorded []string) []string {
	preserve := make(map[string]struct{}, len(preserved))
	for _, path := range preserved {
		preserve[filepath.ToSlash(path)] = struct{}{}
	}
	owned := make(map[string]struct{}, len(current)+len(recorded))
	for _, paths := range [][]string{current, recorded} {
		for _, path := range paths {
			path = filepath.ToSlash(path)
			if _, userOwned := preserve[path]; !userOwned {
				owned[path] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(owned))
	for path := range owned {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
