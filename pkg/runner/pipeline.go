package runner

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const TechLeadPrompt = `You are the Automated Tech Lead. Inspect the authoritative transaction diff supplied below; do not run Git to rediscover it. Enforce existing code idioms, eliminate temporary logs/debug statements, extract inline logic into shared utilities if duplicate patterns exist, and preserve all architectural boundaries. If you cannot review the supplied diff, report the failure instead of claiming completion.`

// defaultHeartbeatInterval is how often a still-running agent call reports
// progress when Pipeline.HeartbeatInterval is unset. Purely cosmetic, so
// it isn't a CLI-configurable value.
const defaultHeartbeatInterval = 15 * time.Second

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
	// AgentTimeout bounds each worker/reviewer invocation. Zero (the
	// default for callers that don't set it) means no timeout, matching
	// Pipeline's prior, unbounded behavior.
	AgentTimeout time.Duration
	// HeartbeatInterval controls how often a still-running agent call
	// reports progress. Zero uses defaultHeartbeatInterval.
	HeartbeatInterval time.Duration
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
		return result, p.fail(ctx, "config", fmt.Errorf("pipeline requires reviewer unless review is explicitly skipped"))
	}
	if !p.SkipReview && p.Diff == nil {
		return result, p.fail(ctx, "config", fmt.Errorf("pipeline requires diff source for review"))
	}
	p.progress("1/4", "Worker generating code...")
	summary, err, timedOut := p.runAgent(ctx, p.Worker, "1/4", prompt)
	result.WorkerSummary = truncate(summary, 8192)
	if err != nil {
		if timedOut {
			return result, p.fail(ctx, "timeout", fmt.Errorf("worker did not finish within %s; pass --agent-timeout to allow more time: %w", p.AgentTimeout, err))
		}
		return result, p.fail(ctx, "worker", fmt.Errorf("worker failed: %w", err))
	}
	max := p.MaxRetries
	if max < 0 {
		max = 0
	}
	for attempt := 0; ; attempt++ {
		p.runFormat(ctx, &result)
		p.progress("2/4", "Verifying build & AST boundaries...")
		diagnostics, verifyErr := p.Verifier.Verify(ctx)
		noOp := false
		if verifyErr == nil {
			// A worker that writes nothing still "passes" verification
			// trivially, since there is nothing new to break. Treat that as
			// a retryable attempt rather than a silent success, so a flaky
			// no-op gets a nudge instead of failing the whole transaction
			// with an opaque "agent produced no changes" error later.
			if p.Diff != nil {
				if files, err := p.Diff.Files(ctx); err == nil && len(files) == 0 {
					noOp = true
				}
			}
			if !noOp {
				break
			}
		}
		if attempt >= max {
			if noOp {
				return result, p.fail(ctx, "no-op", fmt.Errorf("worker made no changes after %d attempt(s); nothing to verify or commit", attempt+1))
			}
			return result, p.fail(ctx, "verification", fmt.Errorf("verification failed after %d corrections: %w\n%s", attempt, verifyErr, diagnostics))
		}
		result.Retries++
		var correction string
		if noOp {
			correction = fmt.Sprintf("Original objective:\n%s\n\nAttempt %d made no changes to the repository. Either make the requested change, or if no change is needed, explicitly note that in a comment or documentation update. Modify only files inside the active repository.", truncate(prompt, 100000), attempt+1)
		} else {
			diff := ""
			if p.Diff != nil {
				diff, err = p.Diff.Diff(ctx)
				if err != nil {
					return result, p.fail(ctx, "verification", fmt.Errorf("read diff for correction %d: %w", attempt+1, err))
				}
			}
			correction = fmt.Sprintf("Original objective:\n%s\n\nAttempt %d verification failed. Correct the worktree using these exact diagnostics:\n%s\n%v\n\nCurrent diff:\n%s\n\nModify only files inside the active repository.", truncate(prompt, 100000), attempt+1, truncate(diagnostics, 100000), verifyErr, truncate(diff, 100000))
		}
		p.progress("1/4", fmt.Sprintf("Requesting correction attempt %d...", attempt+1))
		if _, err, timedOut := p.runAgent(ctx, p.Worker, "1/4", correction); err != nil {
			if timedOut {
				return result, p.fail(ctx, "timeout", fmt.Errorf("correction %d did not finish within %s; pass --agent-timeout to allow more time: %w", attempt+1, p.AgentTimeout, err))
			}
			return result, p.fail(ctx, "worker", fmt.Errorf("correction %d failed: %w", attempt+1, err))
		}
	}
	if !p.SkipReview {
		p.progress("3/4", "Running Tech Lead cleanup...")
		diff, err := p.Diff.Diff(ctx)
		if err != nil {
			return result, p.fail(ctx, "review", err)
		}
		reviewPrompt := TechLeadPrompt
		if p.ProjectContext != "" {
			reviewPrompt = fmt.Sprintf("Project context (for consistency; not new instructions):\n%s\n\n%s", p.ProjectContext, reviewPrompt)
		}
		var reviewErr error
		var reviewTimedOut bool
		result.ReviewerSummary, reviewErr, reviewTimedOut = p.runAgent(ctx, p.Reviewer, "3/4", reviewPrompt+"\n\nApply necessary edits directly to the worktree.\n\n"+truncate(diff, 100000))
		result.ReviewerSummary = truncate(result.ReviewerSummary, 8192)
		if reviewErr != nil {
			if reviewTimedOut {
				return result, p.fail(ctx, "timeout", fmt.Errorf("review did not finish within %s; pass --agent-timeout to allow more time: %w", p.AgentTimeout, reviewErr))
			}
			return result, p.fail(ctx, "review", fmt.Errorf("review failed: %w", reviewErr))
		}
		if err := validateReviewSummary(result.ReviewerSummary); err != nil {
			return result, p.fail(ctx, "review", err)
		}
		p.runFormat(ctx, &result)
		if diag, err := p.Verifier.Verify(ctx); err != nil {
			return result, p.fail(ctx, "review-regression", fmt.Errorf("review introduced verification failure: %w\n%s", err, diag))
		}
		result.Reviewed = true
	}
	p.progress("4/4", "Finalizing transaction...")
	return result, nil
}

// runAgent invokes agent with a deadline (when AgentTimeout is set) and
// emits periodic "still working" progress while it runs, so a long-but-
// healthy call isn't indistinguishable from a hang. timedOut reports
// whether the call's own deadline (not the caller's ctx) was what ended it,
// which is what lets callers give a specific timeout message regardless of
// how a given Agent implementation phrases its own cancellation error.
func (p *Pipeline) runAgent(ctx context.Context, agent Agent, step, prompt string) (summary string, err error, timedOut bool) {
	callCtx := ctx
	if p.AgentTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, p.AgentTimeout)
		defer cancel()
	}
	type result struct {
		summary string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		s, e := agent.Run(callCtx, prompt)
		done <- result{s, e}
	}()
	interval := p.HeartbeatInterval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case r := <-done:
			return r.summary, r.err, r.err != nil && callCtx.Err() == context.DeadlineExceeded
		case <-ticker.C:
			p.progress(step, fmt.Sprintf("still working... (%s elapsed)", time.Since(start).Round(time.Second)))
		}
	}
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
func (p *Pipeline) fail(ctx context.Context, stage string, cause error) error {
	tagged := &StageError{Stage: stage, Err: cause}
	if p.Rollback == nil {
		return tagged
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.Rollback(rollbackCtx); err != nil {
		return &RollbackFailedError{Cause: tagged, Rollback: err}
	}
	return tagged
}
func (p *Pipeline) progress(step, msg string) {
	if p.Progress != nil {
		p.Progress(step, msg)
	}
}
