package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const defaultVerificationParallelism = 4

// VerificationCheck is one deterministic node in the verification graph.
// Dependencies run successfully before the check becomes eligible.
type VerificationCheck struct {
	Name      string
	DependsOn []string
	Run       func(context.Context) (string, error)
}

type GraphVerifier struct {
	Checks      []VerificationCheck
	MaxParallel int
}

type checkResult struct {
	name   string
	output string
	err    error
}

func (v GraphVerifier) Verify(ctx context.Context) (string, error) {
	if err := validateVerificationChecks(v.Checks); err != nil {
		return "", err
	}
	remaining := make(map[string]VerificationCheck, len(v.Checks))
	completed := make(map[string]bool, len(v.Checks))
	for _, check := range v.Checks {
		remaining[check.Name] = check
	}
	parallelism := v.MaxParallel
	if parallelism <= 0 {
		parallelism = defaultVerificationParallelism
	}
	var output strings.Builder
	for len(remaining) > 0 {
		ready := readyChecks(remaining, completed)
		results := runCheckBatch(ctx, ready, parallelism)
		var failures []error
		for _, result := range results {
			if result.output != "" {
				fmt.Fprintf(&output, "[%s]\n%s", result.name, result.output)
				if !strings.HasSuffix(result.output, "\n") {
					output.WriteByte('\n')
				}
			}
			if result.err != nil {
				failures = append(failures, fmt.Errorf("check %q: %w", result.name, result.err))
				continue
			}
			completed[result.name] = true
			delete(remaining, result.name)
		}
		if len(failures) > 0 {
			return output.String(), errors.Join(failures...)
		}
	}
	return output.String(), nil
}

func readyChecks(remaining map[string]VerificationCheck, completed map[string]bool) []VerificationCheck {
	ready := make([]VerificationCheck, 0, len(remaining))
	for _, check := range remaining {
		eligible := true
		for _, dependency := range check.DependsOn {
			if !completed[dependency] {
				eligible = false
				break
			}
		}
		if eligible {
			ready = append(ready, check)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].Name < ready[j].Name })
	return ready
}

func runCheckBatch(ctx context.Context, checks []VerificationCheck, parallelism int) []checkResult {
	results := make([]checkResult, len(checks))
	semaphore := make(chan struct{}, parallelism)
	done := make(chan int, len(checks))
	for i, check := range checks {
		go func(i int, check VerificationCheck) {
			semaphore <- struct{}{}
			output, err := check.Run(ctx)
			<-semaphore
			results[i] = checkResult{name: check.Name, output: output, err: err}
			done <- i
		}(i, check)
	}
	for range checks {
		<-done
	}
	return results
}

func validateVerificationChecks(checks []VerificationCheck) error {
	dependencies := make(map[string][]string, len(checks))
	for _, check := range checks {
		if strings.TrimSpace(check.Name) == "" {
			return errors.New("verification check requires a name")
		}
		if check.Run == nil {
			return fmt.Errorf("verification check %q requires a runner", check.Name)
		}
		if _, exists := dependencies[check.Name]; exists {
			return fmt.Errorf("duplicate verification check %q", check.Name)
		}
		dependencies[check.Name] = append([]string(nil), check.DependsOn...)
	}
	return validateDependencyGraph(dependencies)
}

func validateDependencyGraph(graph map[string][]string) error {
	for name, dependencies := range graph {
		for _, dependency := range dependencies {
			if dependency == name {
				return fmt.Errorf("verification check %q depends on itself", name)
			}
			if _, exists := graph[dependency]; !exists {
				return fmt.Errorf("verification check %q depends on unknown check %q", name, dependency)
			}
		}
	}
	state := make(map[string]byte, len(graph))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("verification checks contain a dependency cycle at %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, dependency := range graph[name] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func CommandVerificationChecks(root string, checks []CheckConfig, processRunner ProcessRunner) []VerificationCheck {
	if processRunner == nil {
		processRunner = ExecProcessRunner{}
	}
	out := make([]VerificationCheck, 0, len(checks))
	for _, configured := range checks {
		check := configured
		out = append(out, VerificationCheck{Name: check.Name, DependsOn: append([]string(nil), check.DependsOn...), Run: func(ctx context.Context) (string, error) {
			output, err := processRunner.Run(ctx, root, check.Executable, check.Args...)
			if err != nil {
				return output, fmt.Errorf("%s failed: %w", check.Executable, err)
			}
			return output, nil
		}})
	}
	return out
}
