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
	prompts []string
}

func (f *fakeAgent) Run(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompts = append(f.prompts, prompt)
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

func (fakeDiff) Diff(context.Context) (string, error) { return "diff", nil }

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
	if !strings.Contains(worker.prompts[1], "Current diff:\ndiff") || !strings.Contains(worker.prompts[1], "Modify only files inside the active repository") {
		t.Fatalf("correction prompt omitted repository context: %q", worker.prompts[1])
	}
}
func TestPipelineRollsBackAfterExhaustion(t *testing.T) {
	rolled := false
	p := Pipeline{Worker: &fakeAgent{}, Verifier: &fakeVerifier{failures: 10}, MaxRetries: 1, Rollback: func(context.Context) error { rolled = true; return nil }}
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
		Worker: cancellingAgent{}, Verifier: &fakeVerifier{},
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

type recordingProcessRunner struct {
	dir, executable string
	args            []string
}

func (r *recordingProcessRunner) Run(_ context.Context, dir, executable string, args ...string) (string, error) {
	r.dir, r.executable, r.args = dir, executable, append([]string(nil), args...)
	return "build output", nil
}

func TestCommandVerifierUsesInjectedProcessRunner(t *testing.T) {
	runner := &recordingProcessRunner{}
	verifier := CommandVerifier{Root: "repo", Build: Command{Executable: "go", Args: []string{"test", "./..."}}, Runner: runner}
	output, err := verifier.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if output != "build output" || runner.dir != "repo" || runner.executable != "go" || strings.Join(runner.args, " ") != "test ./..." {
		t.Fatalf("unexpected invocation: %#v output=%q", runner, output)
	}
}
