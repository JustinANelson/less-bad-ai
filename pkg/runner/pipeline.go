package runner

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

const TechLeadPrompt = `You are the Automated Tech Lead. Inspect this diff. Enforce existing code idioms, eliminate temporary logs/debug statements, extract inline logic into shared utilities if duplicate patterns exist, and preserve all architectural boundaries.`

type Verifier interface {
	Verify(context.Context) (string, error)
}
type DiffSource interface {
	Diff(context.Context) (string, error)
}
type Rollback func(context.Context) error
type Progress func(step, message string)

type Pipeline struct {
	Worker     Agent
	Reviewer   Agent
	Verifier   Verifier
	Diff       DiffSource
	Rollback   Rollback
	Progress   Progress
	MaxRetries int
	SkipReview bool
}
type Result struct {
	WorkerSummary   string
	ReviewerSummary string
	Retries         int
}

func (p *Pipeline) Run(ctx context.Context, prompt string) (Result, error) {
	var result Result
	if p.Worker == nil || p.Verifier == nil {
		return result, fmt.Errorf("pipeline requires worker and verifier")
	}
	p.progress("1/4", "Worker generating code...")
	summary, err := p.Worker.Run(ctx, prompt)
	result.WorkerSummary = summary
	if err != nil {
		return result, p.fail(ctx, fmt.Errorf("worker failed: %w", err))
	}
	max := p.MaxRetries
	if max < 0 {
		max = 0
	}
	for attempt := 0; ; attempt++ {
		p.progress("2/4", "Verifying build & AST boundaries...")
		diagnostics, verifyErr := p.Verifier.Verify(ctx)
		if verifyErr == nil {
			break
		}
		if attempt >= max {
			return result, p.fail(ctx, fmt.Errorf("verification failed after %d corrections: %w\n%s", attempt, verifyErr, diagnostics))
		}
		result.Retries++
		correction := fmt.Sprintf("Original objective:\n%s\n\nAttempt %d verification failed. Correct the worktree using these exact diagnostics:\n%s\n%v", prompt, attempt+1, diagnostics, verifyErr)
		if _, err := p.Worker.Run(ctx, correction); err != nil {
			return result, p.fail(ctx, fmt.Errorf("correction %d failed: %w", attempt+1, err))
		}
	}
	if !p.SkipReview && p.Reviewer != nil {
		p.progress("3/4", "Running Tech Lead cleanup...")
		diff, err := p.Diff.Diff(ctx)
		if err != nil {
			return result, p.fail(ctx, err)
		}
		result.ReviewerSummary, err = p.Reviewer.Run(ctx, TechLeadPrompt+"\n\nApply necessary edits directly to the worktree.\n\n"+truncate(diff, 100000))
		if err != nil {
			return result, p.fail(ctx, fmt.Errorf("review failed: %w", err))
		}
		if diag, err := p.Verifier.Verify(ctx); err != nil {
			return result, p.fail(ctx, fmt.Errorf("review introduced verification failure: %w\n%s", err, diag))
		}
	}
	p.progress("4/4", "Finalizing transaction...")
	return result, nil
}
func (p *Pipeline) fail(ctx context.Context, cause error) error {
	if p.Rollback == nil {
		return cause
	}
	if err := p.Rollback(ctx); err != nil {
		return fmt.Errorf("%v; automatic rollback also failed: %w", cause, err)
	}
	return cause
}
func (p *Pipeline) progress(step, msg string) {
	if p.Progress != nil {
		p.Progress(step, msg)
	}
}

type CommandVerifier struct {
	Root  string
	Build Command
	Lint  func(context.Context) (string, error)
}

func (v CommandVerifier) Verify(ctx context.Context) (string, error) {
	var output strings.Builder
	if v.Build.Executable != "" {
		cmd := exec.CommandContext(ctx, v.Build.Executable, v.Build.Args...)
		cmd.Dir = v.Root
		b, err := cmd.CombinedOutput()
		output.Write(b)
		if err != nil {
			return output.String(), fmt.Errorf("build command failed: %w", err)
		}
	}
	if v.Lint != nil {
		s, err := v.Lint(ctx)
		output.WriteString(s)
		if err != nil {
			return output.String(), err
		}
	}
	return output.String(), nil
}
