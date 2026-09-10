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
		Status: StatusInProgress, Prompt: opts.Prompt,
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
	if dirty {
		if _, err := e.git(ctx, root, "stash", "push", "--include-untracked", "--message", "lbai "+id); err != nil {
			e.deleteRef(ctx, root, snapshot)
			return Plan{}, fmt.Errorf("stash existing changes: %w", err)
		}
		stashSHA, err := e.git(ctx, root, "rev-parse", "refs/stash")
		if err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "stash@{0}", true, "")
			return Plan{}, joinOperationError("resolve saved changes", err, cleanupErr)
		}
		if _, err := e.git(ctx, root, "update-ref", state.StashRef, strings.TrimSpace(string(stashSHA)), ""); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, "stash@{0}", true, "")
			return Plan{}, joinOperationError("retain saved changes", err, cleanupErr)
		}
		if _, err := e.git(ctx, root, "stash", "drop", "stash@{0}"); err != nil {
			cleanupErr := e.abortBegin(ctx, root, snapshot, state.StashRef, true, state.StashRef)
			return Plan{}, joinOperationError("detach saved changes from stash stack", err, cleanupErr)
		}
		plan.State = state
	}
	if err := saveState(root, state); err != nil {
		changesRef := ""
		if dirty {
			changesRef = state.StashRef
		}
		cleanupErr := e.abortBegin(ctx, root, snapshot, changesRef, false, state.StashRef)
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
	state.Status = StatusComplete
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
	state.Status, state.Error = StatusRolledBack, ""
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
