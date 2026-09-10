package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingProcessRunner struct {
	mu              sync.Mutex
	dir, executable string
	args            []string
	calls           []string
}

func (r *recordingProcessRunner) Run(_ context.Context, dir, executable string, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dir, r.executable, r.args = dir, executable, append([]string(nil), args...)
	r.calls = append(r.calls, executable+" "+strings.Join(args, " "))
	return "check output", nil
}

func TestGraphVerifierRunsIndependentChecksConcurrentlyAndOrdersOutput(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	check := func(name string) VerificationCheck {
		return VerificationCheck{Name: name, Run: func(context.Context) (string, error) {
			started <- name
			<-release
			return name + " output\n", nil
		}}
	}
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := (GraphVerifier{Checks: []VerificationCheck{check("zeta"), check("alpha")}}).Verify(context.Background())
		done <- result{output: output, err: err}
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent checks did not start concurrently")
		}
	}
	unblock()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if strings.Index(got.output, "[alpha]") > strings.Index(got.output, "[zeta]") {
		t.Fatalf("output is not deterministic:\n%s", got.output)
	}
}

func TestGraphVerifierHonorsDependencies(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(name string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, name)
			return "", nil
		}
	}
	checks := []VerificationCheck{
		{Name: "package", DependsOn: []string{"test"}, Run: record("package")},
		{Name: "compile", Run: record("compile")},
		{Name: "test", DependsOn: []string{"compile"}, Run: record("test")},
	}
	if _, err := (GraphVerifier{Checks: checks}).Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "compile,test,package" {
		t.Fatalf("calls = %q", got)
	}
}

func TestGraphVerifierStopsDependentsAfterFailure(t *testing.T) {
	dependentRan := false
	checks := []VerificationCheck{
		{Name: "compile", Run: func(context.Context) (string, error) { return "compiler output", errors.New("failed") }},
		{Name: "test", DependsOn: []string{"compile"}, Run: func(context.Context) (string, error) { dependentRan = true; return "", nil }},
	}
	output, err := (GraphVerifier{Checks: checks}).Verify(context.Background())
	if err == nil || !strings.Contains(err.Error(), `check "compile"`) || !strings.Contains(output, "compiler output") {
		t.Fatalf("Verify output=%q error=%v", output, err)
	}
	if dependentRan {
		t.Fatal("dependent check ran after dependency failure")
	}
}

func TestCommandVerificationChecksUsesInjectedRunner(t *testing.T) {
	process := &recordingProcessRunner{}
	checks := CommandVerificationChecks("repo", []CheckConfig{{Name: "test", Executable: "go", Args: []string{"test", "./..."}}}, process)
	if _, err := (GraphVerifier{Checks: checks}).Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if process.dir != "repo" || process.executable != "go" || strings.Join(process.args, " ") != "test ./..." {
		t.Fatalf("unexpected invocation: %#v", process)
	}
}

func TestAutoVerificationCheckDiscoversManifestCreatedAfterConstruction(t *testing.T) {
	root := t.TempDir()
	process := &recordingProcessRunner{}
	check := AutoVerificationCheck(root, process)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := check.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sort.Strings(process.calls)
	if process.dir != root || !reflect.DeepEqual(process.calls, []string{"go test ./...", "go vet ./..."}) {
		t.Fatalf("unexpected invocations: %#v", process)
	}
}
