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
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		details := strings.TrimSpace(strings.TrimSpace(string(b)) + "\n" + stderr.String())
		return "", fmt.Errorf("read transaction diff: %w: %s", err, details)
	}
	return string(b), nil
}
