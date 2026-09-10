package gitengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const stateVersion = 1

var ErrNoTransaction = errors.New("no less-bad-ai transaction found")

type Status string

const (
	StatusInProgress       Status = "in_progress"
	StatusComplete         Status = "complete"
	StatusRolledBack       Status = "rolled_back"
	StatusRecoveryRequired Status = "recovery_required"
)

type State struct {
	Version       int       `json:"version"`
	TransactionID string    `json:"transaction_id"`
	Repository    string    `json:"repository"`
	LastCleanHead string    `json:"last_clean_head"`
	SnapshotRef   string    `json:"snapshot_ref"`
	StashRef      string    `json:"stash_ref,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	Status        Status    `json:"status"`
	Prompt        string    `json:"prompt"`
	// PreservedUntracked records user-owned untracked files that existed before
	// the transaction. Undo must never mistake them for agent-created files after
	// a successful run restores the user's dirty worktree.
	PreservedUntracked []string `json:"preserved_untracked,omitempty"`
	CreatedFiles       []string `json:"created_files,omitempty"`
	Error              string   `json:"error,omitempty"`
}

func statePath(root string) string { return filepath.Join(root, ".lbai", "state.json") }

func LoadState(root string) (State, error) {
	b, err := os.ReadFile(statePath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrNoTransaction
		}
		return State{}, fmt.Errorf("read transaction state: %w", err)
	}
	var state State
	if err := json.Unmarshal(b, &state); err != nil {
		return State{}, fmt.Errorf("decode transaction state: %w", err)
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported transaction state version %d", state.Version)
	}
	return state, nil
}

func saveState(root string, state State) error {
	dir := filepath.Dir(statePath(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transaction state: %w", err)
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, "state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect temporary state: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(tmpName, statePath(root)); err != nil {
		// Windows cannot atomically replace an existing file.
		if removeErr := os.Remove(statePath(root)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace transaction state: %w", err)
		}
		if err := os.Rename(tmpName, statePath(root)); err != nil {
			return fmt.Errorf("replace transaction state: %w", err)
		}
	}
	return nil
}
