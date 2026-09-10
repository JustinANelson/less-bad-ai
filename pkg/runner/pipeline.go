package runner

import (
	"context"
	"fmt"
	"time"
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
	result.WorkerSummary = truncate(summary, 8192)
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
		diff := ""
		if p.Diff != nil {
			diff, err = p.Diff.Diff(ctx)
			if err != nil {
				return result, p.fail(ctx, fmt.Errorf("read diff for correction %d: %w", attempt+1, err))
			}
		}
		correction := fmt.Sprintf("Original objective:\n%s\n\nAttempt %d verification failed. Correct the worktree using these exact diagnostics:\n%s\n%v\n\nCurrent diff:\n%s\n\nModify only files inside the active repository.", truncate(prompt, 100000), attempt+1, truncate(diagnostics, 100000), verifyErr, truncate(diff, 100000))
		if _, err := p.Worker.Run(ctx, correction); err != nil {
			return result, p.fail(ctx, fmt.Errorf("correction %d failed: %w", attempt+1, err))
		}
	}
	if !p.SkipReview && p.Reviewer != nil {
		p.progress("3/4", "Running Tech Lead cleanup...")
		if p.Diff == nil {
			return result, p.fail(ctx, fmt.Errorf("pipeline requires diff source for review"))
		}
		diff, err := p.Diff.Diff(ctx)
		if err != nil {
			return result, p.fail(ctx, err)
		}
		result.ReviewerSummary, err = p.Reviewer.Run(ctx, TechLeadPrompt+"\n\nApply necessary edits directly to the worktree.\n\n"+truncate(diff, 100000))
		result.ReviewerSummary = truncate(result.ReviewerSummary, 8192)
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
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.Rollback(rollbackCtx); err != nil {
		return fmt.Errorf("%v; automatic rollback also failed: %w", cause, err)
	}
	return cause
}
func (p *Pipeline) progress(step, msg string) {
	if p.Progress != nil {
		p.Progress(step, msg)
	}
}
