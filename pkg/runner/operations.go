package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type FileOperation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
}

type FileOperationApplier interface {
	Apply(context.Context, []FileOperation) error
}

type RootFileOperationApplier struct {
	Root string
}

func (a RootFileOperationApplier) Apply(ctx context.Context, operations []FileOperation) error {
	root, err := os.OpenRoot(a.Root)
	if err != nil {
		return fmt.Errorf("open operation root %s: %w", a.Root, err)
	}
	defer root.Close()

	normalized := make([]FileOperation, len(operations))
	seen := make(map[string]struct{}, len(operations))
	for i, operation := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := validateOperation(operation)
		if err != nil {
			return fmt.Errorf("file operation %d: %w", i+1, err)
		}
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("file operation %d: duplicate path %q", i+1, operation.Path)
		}
		seen[key] = struct{}{}
		operation.Path = path
		normalized[i] = operation
	}
	for _, operation := range normalized {
		if err := rejectSymlinkComponents(root, operation.Path); err != nil {
			return fmt.Errorf("validate %s: %w", operation.Path, err)
		}
	}

	for _, operation := range normalized {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch operation.Operation {
		case "write":
			parent := filepath.Dir(operation.Path)
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("create parent for %s: %w", operation.Path, err)
				}
			}
			if info, err := root.Lstat(operation.Path); err == nil && info.IsDir() {
				return fmt.Errorf("write %s: target is a directory", operation.Path)
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("inspect %s: %w", operation.Path, err)
			}
			if err := root.WriteFile(operation.Path, []byte(operation.Content), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", operation.Path, err)
			}
		case "delete":
			info, err := root.Lstat(operation.Path)
			if err != nil {
				return fmt.Errorf("inspect %s for deletion: %w", operation.Path, err)
			}
			if info.IsDir() {
				return fmt.Errorf("delete %s: directory deletion is not allowed", operation.Path)
			}
			if err := root.Remove(operation.Path); err != nil {
				return fmt.Errorf("delete %s: %w", operation.Path, err)
			}
		}
	}
	return nil
}

func rejectSymlinkComponents(root *os.Root, path string) error {
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if current == "" {
			current = component
		} else {
			current = filepath.Join(current, component)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed in operation paths")
		}
	}
	return nil
}

func validateOperation(operation FileOperation) (string, error) {
	if operation.Operation != "write" && operation.Operation != "delete" {
		return "", fmt.Errorf("unsupported operation %q", operation.Operation)
	}
	if strings.TrimSpace(operation.Path) == "" || filepath.IsAbs(operation.Path) || filepath.VolumeName(operation.Path) != "" {
		return "", fmt.Errorf("invalid repository-relative path %q", operation.Path)
	}
	path := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(operation.Path, "\\", "/")))
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository: %q", operation.Path)
	}
	slash := filepath.ToSlash(path)
	lower := strings.ToLower(slash)
	if lower == ".git" || strings.HasPrefix(lower, ".git/") || lower == ".lbai/state.json" || lower == ".lbai/snapshots" || strings.HasPrefix(lower, ".lbai/snapshots/") {
		return "", fmt.Errorf("path %q is protected transaction metadata", operation.Path)
	}
	if operation.Operation == "delete" && operation.Content != "" {
		return "", fmt.Errorf("delete operation for %q must not include content", operation.Path)
	}
	return path, nil
}
