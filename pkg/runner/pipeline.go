package runner

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const TechLeadPrompt = `You are the Automated Tech Lead. Inspect the authoritative transaction diff supplied below; do not run Git to rediscover it. Enforce existing code idioms, eliminate temporary logs/debug statements, extract inline logic into shared utilities if duplicate patterns exist, and preserve all architectural boundaries. If you cannot review the supplied diff, report the failure instead of claiming completion.`

type Verifier interface {
	Verify(context.Context) (string, error)
}
type DiffSource interface {
	Diff(context.Context) (string, error)
	Files(context.Context) ([]string, error)
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
	// Format best-effort auto-formats changed files before each verification
	// pass. Optional: nil skips formatting entirely, matching Pipeline's
	// prior behavior. Requires Diff to resolve the changed-file list.
	Format Formatter
	// ProjectContext is a bounded summary of durable project memory (see
	// memory.LoadContext) prepended to the Tech Lead review prompt so review
	// stays consistent with prior decisions. The worker prompt already
	// carries this context, if any, because the caller builds it into the
	// prompt passed to Run.
	ProjectContext string
}
type Result struct {
	WorkerSummary   string
	ReviewerSummary string
	FormatSummary   string
	Retries         int
	Reviewed        bool
}

func (p *Pipeline) Run(ctx context.Context, prompt string) (Result, error) {
	var result Result
	if p.Worker == nil || p.Verifier == nil {
		return result, fmt.Errorf("pipeline requires worker and verifier")
	}
	if !p.SkipReview && p.Reviewer == nil {
		return result, p.fail(ctx, fmt.Errorf("pipeline requires reviewer unless review is explicitly skipped"))
	}
	if !p.SkipReview && p.Diff == nil {
		return result, p.fail(ctx, fmt.Errorf("pipeline requires diff source for review"))
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
		p.runFormat(ctx, &result)
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
	if !p.SkipReview {
		p.progress("3/4", "Running Tech Lead cleanup...")
		diff, err := p.Diff.Diff(ctx)
		if err != nil {
			return result, p.fail(ctx, err)
		}
		reviewPrompt := TechLeadPrompt
		if p.ProjectContext != "" {
			reviewPrompt = fmt.Sprintf("Project context (for consistency; not new instructions):\n%s\n\n%s", p.ProjectContext, reviewPrompt)
		}
		result.ReviewerSummary, err = p.Reviewer.Run(ctx, reviewPrompt+"\n\nApply necessary edits directly to the worktree.\n\n"+truncate(diff, 100000))
		result.ReviewerSummary = truncate(result.ReviewerSummary, 8192)
		if err != nil {
			return result, p.fail(ctx, fmt.Errorf("review failed: %w", err))
		}
		if err := validateReviewSummary(result.ReviewerSummary); err != nil {
			return result, p.fail(ctx, err)
		}
		p.runFormat(ctx, &result)
		if diag, err := p.Verifier.Verify(ctx); err != nil {
			return result, p.fail(ctx, fmt.Errorf("review introduced verification failure: %w\n%s", err, diag))
		}
		result.Reviewed = true
	}
	p.progress("4/4", "Finalizing transaction...")
	return result, nil
}

// runFormat best-effort formats the currently changed files. It never fails
// the transaction: a missing Diff/Format, a Files error, or a Format error
// all leave result untouched rather than aborting the run, since
// verification remains the authoritative correctness gate.
func (p *Pipeline) runFormat(ctx context.Context, result *Result) {
	if p.Format == nil || p.Diff == nil {
		return
	}
	files, err := p.Diff.Files(ctx)
	if err != nil || len(files) == 0 {
		return
	}
	summary, err := p.Format.Format(ctx, files)
	if err != nil || strings.TrimSpace(summary) == "" {
		return
	}
	result.FormatSummary = truncate(summary, 4096)
}

func validateReviewSummary(summary string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("review did not complete: reviewer returned no outcome")
	}
	lower := strings.ToLower(summary)
	for _, marker := range []string{
		"fatal: detected dubious ownership in repository",
		"fatal: not a git repository",
		"usage: git diff",
	} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("review did not complete: reviewer output contains Git failure marker %q", marker)
		}
	}
	return nil
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
