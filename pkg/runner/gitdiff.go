package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type GitDiff struct{ Root, Base string }

func (g GitDiff) Diff(ctx context.Context) (string, error) {
	out, err := g.git(ctx, "read transaction diff", "diff", g.Base, "--")
	return out, err
}

// Files returns the sorted, deduplicated set of paths changed relative to
// Base, including files created but not yet tracked by Git.
func (g GitDiff) Files(ctx context.Context) ([]string, error) {
	tracked, err := g.git(ctx, "list changed files", "diff", "--name-only", g.Base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := g.git(ctx, "list untracked files", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, out := range []string{tracked, untracked} {
		for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				set[filepath.ToSlash(line)] = true
			}
		}
	}
	files := make([]string, 0, len(set))
	for f := range set {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}

func (g GitDiff) git(ctx context.Context, op string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		details := strings.TrimSpace(strings.TrimSpace(string(b)) + "\n" + stderr.String())
		return "", fmt.Errorf("%s: %w: %s", op, err, details)
	}
	return string(b), nil
}
