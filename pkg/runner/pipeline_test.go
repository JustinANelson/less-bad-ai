package runner

import (
	"context"
	"errors"
	"testing"
)

type fakeAgent struct {
	calls int
	err   error
}

func (f *fakeAgent) Run(context.Context, string) (string, error) { f.calls++; return "summary", f.err }

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
