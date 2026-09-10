package runner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type GitDiff struct{ Root, Base string }

func (g GitDiff) Diff(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", g.Base, "--")
	cmd.Dir = g.Root
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read transaction diff: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
