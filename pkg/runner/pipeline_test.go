package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAgent struct {
	calls   int
	err     error
	summary string
	prompts []string
}

func (f *fakeAgent) Run(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompts = append(f.prompts, prompt)
	if f.summary != "" {
		return f.summary, f.err
	}
	return "summary", f.err
}

type fakeVerifier struct {
	calls    int
	failures int
}

func (f *fakeVerifier) Verify(context.Context) (string, error) {
	f.calls++
	if f.calls <= f.failures {
		return "compile error", errors.New("failed")
	}
	return "", nil
}

type fakeDiff struct{}

func (fakeDiff) Diff(context.Context) (string, error)    { return "diff", nil }
func (fakeDiff) Files(context.Context) ([]string, error) { return []string{"main.go"}, nil }

func TestPipelineCorrectsAndReviews(t *testing.T) {
	worker := &fakeAgent{}
	reviewer := &fakeAgent{}
	verify := &fakeVerifier{failures: 1}
	p := Pipeline{Worker: worker, Reviewer: reviewer, Verifier: verify, Diff: fakeDiff{}, MaxRetries: 2}
	result, err := p.Run(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if result.Retries != 1 || worker.calls != 2 || reviewer.calls != 1 || verify.calls != 3 {
		t.Fatalf("unexpected execution: %#v worker=%d reviewer=%d verify=%d", result, worker.calls, reviewer.calls, verify.calls)
	}
	if !result.Reviewed {
		t.Fatal("successful review was not recorded")
	}
	if !strings.Contains(worker.prompts[1], "Current diff:\ndiff") || !strings.Contains(worker.prompts[1], "Modify only files inside the active repository") {
		t.Fatalf("correction prompt omitted repository context: %q", worker.prompts[1])
	}
}

// sequenceFilesDiff scripts Files() results per call, holding the last
// entry steady once the script runs out (mirroring sequenceVerifier below).
type sequenceFilesDiff struct {
	files [][]string
	calls int
}

func (d *sequenceFilesDiff) Diff(context.Context) (string, error) { return "diff", nil }

func (d *sequenceFilesDiff) Files(context.Context) ([]string, error) {
	index := d.calls
	d.calls++
	if index < len(d.files) {
		return d.files[index], nil
	}
	return d.files[len(d.files)-1], nil
}

func TestPipelineRetriesOnNoOpWorkerRun(t *testing.T) {
	worker := &fakeAgent{}
	diff := &sequenceFilesDiff{files: [][]string{{}, {"main.go"}}}
	p := Pipeline{Worker: worker, Verifier: &fakeVerifier{}, Diff: diff, SkipReview: true, MaxRetries: 1}
	result, err := p.Run(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if result.Retries != 1 || worker.calls != 2 {
		t.Fatalf("unexpected execution: retries=%d worker.calls=%d", result.Retries, worker.calls)
	}
	if !strings.Contains(worker.prompts[1], "made no changes") {
		t.Fatalf("correction prompt did not nudge the worker about a no-op attempt: %q", worker.prompts[1])
	}
}

func TestPipelineFailsAfterExhaustingNoOpRetries(t *testing.T) {
	rolled := false
	worker := &fakeAgent{}
	diff := &sequenceFilesDiff{files: [][]string{{}}}
	p := Pipeline{
		Worker: worker, Verifier: &fakeVerifier{}, Diff: diff, SkipReview: true, MaxRetries: 1,
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	_, err := p.Run(context.Background(), "work")
	if err == nil || !strings.Contains(err.Error(), "no changes") {
		t.Fatalf("Run error = %v, want a no-changes error", err)
	}
	if worker.calls != 2 {
		t.Fatalf("worker.calls = %d, want 2 (one initial attempt plus one correction)", worker.calls)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

func TestPipelineReviewerNoOpIsNotRetried(t *testing.T) {
	reviewer := &fakeAgent{}
	verify := &fakeVerifier{}
	p := Pipeline{Worker: &fakeAgent{}, Reviewer: reviewer, Verifier: verify, Diff: fakeDiff{}}
	result, err := p.Run(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reviewed || reviewer.calls != 1 {
		t.Fatalf("expected a single, successful review without a retry loop: reviewed=%v calls=%d", result.Reviewed, reviewer.calls)
	}
}

func TestPipelineReviewIncludesProjectContext(t *testing.T) {
	worker := &fakeAgent{}
	reviewer := &fakeAgent{}
	verify := &fakeVerifier{}
	p := Pipeline{Worker: worker, Reviewer: reviewer, Verifier: verify, Diff: fakeDiff{}, ProjectContext: "## Recent project decisions\n\n- used gofmt"}
	if _, err := p.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if len(reviewer.prompts) != 1 || !strings.Contains(reviewer.prompts[0], "used gofmt") {
		t.Fatalf("expected project context in review prompt, got %#v", reviewer.prompts)
	}
}

func TestPipelineReviewOmitsContextBlockWhenEmpty(t *testing.T) {
	worker := &fakeAgent{}
	reviewer := &fakeAgent{}
	verify := &fakeVerifier{}
	p := Pipeline{Worker: worker, Reviewer: reviewer, Verifier: verify, Diff: fakeDiff{}}
	if _, err := p.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if len(reviewer.prompts) != 1 || strings.Contains(reviewer.prompts[0], "Project context") {
		t.Fatalf("did not expect a project context block with no memory, got %#v", reviewer.prompts)
	}
}

type fakeFormatter struct {
	calls   int
	summary string
	err     error
}

func (f *fakeFormatter) Format(_ context.Context, files []string) (string, error) {
	f.calls++
	if len(files) == 0 {
		return "", nil
	}
	return f.summary, f.err
}

func TestPipelineFormatsBeforeEveryVerify(t *testing.T) {
	format := &fakeFormatter{summary: "gofmt: processed 1 file(s)"}
	verify := &fakeVerifier{failures: 1}
	p := Pipeline{Worker: &fakeAgent{}, Reviewer: &fakeAgent{}, Verifier: verify, Diff: fakeDiff{}, Format: format, MaxRetries: 1}
	result, err := p.Run(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	// One call per verify: initial attempt, the retried attempt, and the
	// post-review verify.
	if format.calls != 3 {
		t.Fatalf("format calls = %d, want 3", format.calls)
	}
	if result.FormatSummary != "gofmt: processed 1 file(s)" {
		t.Fatalf("FormatSummary = %q", result.FormatSummary)
	}
}

func TestPipelineFormatterFailureDoesNotFailRun(t *testing.T) {
	format := &fakeFormatter{err: errors.New("gofmt: syntax error")}
	p := Pipeline{Worker: &fakeAgent{}, Reviewer: &fakeAgent{}, Verifier: &fakeVerifier{}, Diff: fakeDiff{}, Format: format}
	result, err := p.Run(context.Background(), "work")
	if err != nil {
		t.Fatalf("formatter failure should not fail the run: %v", err)
	}
	if result.FormatSummary != "" {
		t.Fatalf("expected no format summary recorded on formatter error, got %q", result.FormatSummary)
	}
}

func TestPipelineSkipsFormatWhenNotConfigured(t *testing.T) {
	p := Pipeline{Worker: &fakeAgent{}, Reviewer: &fakeAgent{}, Verifier: &fakeVerifier{}, Diff: fakeDiff{}}
	if _, err := p.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineReviewDiffFailureRollsBack(t *testing.T) {
	rolled := false
	diffErr := errors.New("fatal: detected dubious ownership")
	p := Pipeline{
		Worker: &fakeAgent{}, Reviewer: &fakeAgent{}, Verifier: &fakeVerifier{},
		Diff:     errorDiff{err: diffErr},
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	if _, err := p.Run(context.Background(), "work"); !errors.Is(err, diffErr) {
		t.Fatalf("Run error = %v, want diff error", err)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

type errorDiff struct{ err error }

func (d errorDiff) Diff(context.Context) (string, error)    { return "", d.err }
func (d errorDiff) Files(context.Context) ([]string, error) { return nil, d.err }

func TestPipelineRejectsReviewerGitFailureOutput(t *testing.T) {
	rolled := false
	p := Pipeline{
		Worker: &fakeAgent{}, Reviewer: &fakeAgent{summary: "fatal: detected dubious ownership in repository at 'C:/project'"},
		Verifier: &fakeVerifier{}, Diff: fakeDiff{},
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	result, err := p.Run(context.Background(), "work")
	if err == nil || !strings.Contains(err.Error(), "review did not complete") {
		t.Fatalf("Run error = %v", err)
	}
	if result.Reviewed {
		t.Fatal("failed review was recorded as completed")
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

func TestPipelineRejectsEmptyReviewerOutcome(t *testing.T) {
	if err := validateReviewSummary(" \r\n\t"); err == nil || !strings.Contains(err.Error(), "no outcome") {
		t.Fatalf("validateReviewSummary error = %v", err)
	}
}

func TestPipelineRequiresReviewerUnlessExplicitlySkipped(t *testing.T) {
	rolled := false
	p := Pipeline{
		Worker: &fakeAgent{}, Verifier: &fakeVerifier{}, Diff: fakeDiff{},
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	if _, err := p.Run(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "requires reviewer") {
		t.Fatalf("Run error = %v", err)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}
func TestPipelineRollsBackAfterExhaustion(t *testing.T) {
	rolled := false
	p := Pipeline{Worker: &fakeAgent{}, Verifier: &fakeVerifier{failures: 10}, MaxRetries: 1, SkipReview: true, Rollback: func(context.Context) error { rolled = true; return nil }}
	if _, err := p.Run(context.Background(), "work"); err == nil {
		t.Fatal("expected failure")
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

func TestPipelineSkipsReview(t *testing.T) {
	reviewer := &fakeAgent{}
	verify := &fakeVerifier{}
	p := Pipeline{Worker: &fakeAgent{}, Reviewer: reviewer, Verifier: verify, Diff: fakeDiff{}, SkipReview: true}
	if _, err := p.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if reviewer.calls != 0 || verify.calls != 1 {
		t.Fatalf("reviewer calls=%d verifier calls=%d", reviewer.calls, verify.calls)
	}
}

func TestPipelineReviewFailureRollsBack(t *testing.T) {
	rolled := false
	p := Pipeline{
		Worker: &fakeAgent{}, Reviewer: &fakeAgent{err: errors.New("review failed")},
		Verifier: &fakeVerifier{}, Diff: fakeDiff{},
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	if _, err := p.Run(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "review failed") {
		t.Fatalf("Run error = %v", err)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

type sequenceVerifier struct {
	errors []error
	calls  int
}

func (v *sequenceVerifier) Verify(context.Context) (string, error) {
	index := v.calls
	v.calls++
	if index < len(v.errors) {
		return "diagnostic", v.errors[index]
	}
	return "", nil
}

func TestPipelineReviewRegressionRollsBack(t *testing.T) {
	rolled := false
	verify := &sequenceVerifier{errors: []error{nil, errors.New("regression")}}
	p := Pipeline{
		Worker: &fakeAgent{}, Reviewer: &fakeAgent{}, Verifier: verify, Diff: fakeDiff{},
		Rollback: func(context.Context) error { rolled = true; return nil },
	}
	if _, err := p.Run(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "review introduced") {
		t.Fatalf("Run error = %v", err)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}

type cancellingAgent struct{}

func (cancellingAgent) Run(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestPipelineCancellationUsesFreshRollbackContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rolled := false
	p := Pipeline{
		Worker: cancellingAgent{}, Verifier: &fakeVerifier{}, SkipReview: true,
		Rollback: func(ctx context.Context) error {
			rolled = true
			if err := ctx.Err(); err != nil {
				t.Fatalf("rollback context is already cancelled: %v", err)
			}
			return nil
		},
	}
	if _, err := p.Run(ctx, "work"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if !rolled {
		t.Fatal("rollback was not called")
	}
}
