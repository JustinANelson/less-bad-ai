package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/JustinANelson/less-bad-ai/pkg/gitengine"
	"github.com/JustinANelson/less-bad-ai/pkg/linter"
	"github.com/JustinANelson/less-bad-ai/pkg/memory"
	"github.com/JustinANelson/less-bad-ai/pkg/onboarding"
	"github.com/JustinANelson/less-bad-ai/pkg/runner"
	"github.com/JustinANelson/less-bad-ai/pkg/topology"
	"github.com/spf13/cobra"
)

type app struct {
	out, err io.Writer
	dir      string
	discover func(string) (runner.Config, error)
}

func New() *cobra.Command {
	cwd, _ := os.Getwd()
	a := &app{out: os.Stdout, err: os.Stderr, dir: cwd}
	root := &cobra.Command{Use: "lbai", Aliases: []string{"less-bad-ai"}, Short: "Run coding agents inside recoverable Git transactions", Version: Version, SilenceUsage: true, SilenceErrors: true}
	root.SetOut(a.out)
	root.SetErr(a.err)
	root.AddCommand(a.versionCommand(), a.setupCommand(), a.doctorCommand(), a.initCommand(), a.runCommand(), a.undoCommand(), a.statusCommand(), a.lintCommand(), a.logCommand(), a.uiCommand())
	return root
}

func (a *app) versionCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{Use: "version", Short: "Print version and build information", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		info := struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
			Date    string `json:"date"`
		}{Version: Version, Commit: Commit, Date: BuildDate}
		if asJSON {
			return json.NewEncoder(a.out).Encode(info)
		}
		fmt.Fprintf(a.out, "lbai %s (commit %s, built %s)\n", info.Version, info.Commit, info.Date)
		return nil
	}}
	c.Flags().BoolVar(&asJSON, "json", false, "write version information as JSON")
	return c
}

func (a *app) setupCommand() *cobra.Command {
	var allowSensitive, asJSON bool
	var gitName, gitEmail string
	c := &cobra.Command{Use: "setup", Aliases: []string{"bootstrap"}, Short: "Make the current project ready for lbai", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := onboarding.Setup(cmd.Context(), onboarding.Options{
			Dir: a.dir, AllowSensitive: allowSensitive, GitName: gitName, GitEmail: gitEmail, Discover: a.discover,
		})
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(a.out).Encode(result)
		}
		if result.InitializedGit {
			fmt.Fprintln(a.out, "[lbai] Initialized a Git repository.")
		}
		if result.CreatedConfig {
			fmt.Fprintf(a.out, "[lbai] Saved detected configuration to %s.\n", result.ConfigPath)
			if result.ConfigCommitted && !result.CreatedBaseline {
				fmt.Fprintln(a.out, "[lbai] Committed the detected configuration.")
			} else if !result.ConfigCommitted {
				fmt.Fprintln(a.out, "[lbai] Existing work was left untouched; commit .lbai/config.toml when ready.")
			}
		}
		if result.CreatedBaseline {
			fmt.Fprintln(a.out, "[lbai] Created the initial project baseline commit.")
		}
		if result.IdentityFallback {
			fmt.Fprintln(a.out, "[lbai] Note: configured a repository-local fallback Git identity; change it with git config user.name and user.email.")
		}
		fmt.Fprintf(a.out, "[lbai] Agent: %s\n", result.Agent)
		if len(result.Checks) == 1 && result.Checks[0] == "architecture" {
			fmt.Fprintln(a.out, "[lbai] Verification: architecture; project checks will be detected after generation")
		} else {
			fmt.Fprintf(a.out, "[lbai] Verification: %s\n", strings.Join(result.Checks, ", "))
		}
		fmt.Fprintln(a.out, "[lbai] Ready. Run: lbai run \"describe the change\"")
		return nil
	}}
	c.Flags().BoolVar(&allowSensitive, "allow-sensitive", false, "allow possible secret files in a new baseline commit")
	c.Flags().StringVar(&gitName, "git-name", "", "repository-local Git author name when one is not configured")
	c.Flags().StringVar(&gitEmail, "git-email", "", "repository-local Git author email when one is not configured")
	c.Flags().BoolVar(&asJSON, "json", false, "write setup result as JSON")
	return c
}

func (a *app) doctorCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{Use: "doctor", Aliases: []string{"diagnose"}, Short: "Check whether this project is ready for lbai", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result := onboarding.Diagnose(cmd.Context(), onboarding.Options{Dir: a.dir, Discover: a.discover})
		if asJSON {
			if err := json.NewEncoder(a.out).Encode(result); err != nil {
				return err
			}
		} else {
			for _, check := range result.Checks {
				fmt.Fprintf(a.out, "[%s] %s: %s\n", strings.ToUpper(string(check.Status)), check.Name, check.Detail)
				if check.Hint != "" {
					fmt.Fprintf(a.out, "       %s\n", check.Hint)
				}
			}
		}
		if result.HasFailures() {
			return errors.New("project is not ready; run `lbai setup` after addressing the checks above")
		}
		return nil
	}}
	c.Flags().BoolVar(&asJSON, "json", false, "write diagnostics as JSON")
	return c
}

func (a *app) initCommand() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Detect the project and write a starter configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := gitengine.New(a.dir).Root(cmd.Context())
		if err != nil {
			return fmt.Errorf("init requires an existing Git repository; run `lbai setup` instead: %w", err)
		}
		discover := a.discover
		if discover == nil {
			discover = runner.DiscoverConfig
		}
		cfg, err := discover(root)
		if err != nil {
			return err
		}
		path, err := runner.WriteConfig(root, cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "[lbai] Wrote %s\n", path)
		fmt.Fprintf(a.out, "[lbai] Worker: %s\n", strings.Join(cfg.Worker.Command, " "))
		checks := cfg.Checks
		if len(checks) == 0 {
			fmt.Fprintln(a.out, "[lbai] Checks: automatic after generation; add [[checks]] to pin custom commands")
		} else {
			for _, check := range checks {
				fmt.Fprintf(a.out, "[lbai] Check %s: %s\n", check.Name, strings.Join(append([]string{check.Executable}, check.Args...), " "))
			}
		}
		return nil
	}}
}

func (a *app) runCommand() *cobra.Command {
	var dry, skipReview, serve bool
	var message, model string
	var retries int
	c := &cobra.Command{Use: "run <prompt...>", Aliases: []string{"r", "exec"}, Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		prompt := strings.Join(args, " ")
		engine := gitengine.New(a.dir)
		root, err := engine.Root(ctx)
		if err != nil {
			return fmt.Errorf("project is not ready; run `lbai setup`: %w", err)
		}
		var runCfg runner.Config
		var rules linter.Config
		if !dry {
			runCfg, err = runner.LoadConfig(root)
			if err != nil {
				return fmt.Errorf("load agent configuration: %w; run `lbai doctor` for setup guidance", err)
			}
			rules, err = linter.LoadConfig(root)
			if err != nil {
				return fmt.Errorf("load architectural rules: %w; run `lbai doctor` for setup guidance", err)
			}
		}
		plan, err := engine.Begin(ctx, gitengine.BeginOptions{Prompt: prompt, DryRun: dry})
		if err != nil {
			if strings.Contains(err.Error(), "initial commit") {
				return fmt.Errorf("%w; run `lbai setup` to create the project baseline", err)
			}
			return err
		}
		fmt.Fprintf(a.out, "[lbai] Snapshotting HEAD (ref: %s)\n", plan.SnapshotRef)
		if dry {
			fmt.Fprintf(a.out, "[lbai] Dry run: repository=%s head=%s dirty=%t\n", plan.Root, short(plan.Head), plan.Dirty)
			return nil
		}
		worker, err := runner.NewAgent(runCfg.Worker, root, model)
		if err != nil {
			_, _ = engine.Undo(ctx, false)
			return err
		}
		var reviewer runner.Agent
		if !skipReview && runCfg.Reviewer.Type != "" {
			reviewer, err = runner.NewAgent(runCfg.Reviewer, root, model)
			if err != nil {
				_, _ = engine.Undo(ctx, false)
				return err
			}
		}
		configuredChecks := runCfg.Checks
		verificationChecks := runner.CommandVerificationChecks(root, configuredChecks, nil)
		if len(configuredChecks) == 0 {
			verificationChecks = append(verificationChecks, runner.AutoVerificationCheck(root, nil))
		}
		projectContext, err := memory.LoadContext(root)
		if err != nil {
			_, _ = engine.Undo(ctx, false)
			return fmt.Errorf("load project memory: %w", err)
		}
		agentPrompt := withProjectContext(prompt, projectContext)
		verificationChecks = append(verificationChecks, runner.VerificationCheck{Name: "architecture", Run: func(ctx context.Context) (string, error) {
			paths, err := changedPaths(ctx, root, plan.Head)
			if err != nil {
				return "", err
			}
			diagnostics, _, err := scanRegressions(ctx, root, plan.Head, paths, rules)
			if err != nil {
				return "", err
			}
			text := renderDiagnostics(diagnostics, true)
			if len(diagnostics) > 0 {
				return text, fmt.Errorf("%d architecture violation(s)", len(diagnostics))
			}
			return text, nil
		}})
		verify := runner.GraphVerifier{Checks: verificationChecks}
		pipeline := runner.Pipeline{Worker: worker, Reviewer: reviewer, Verifier: verify, Diff: runner.GitDiff{Root: root, Base: plan.Head}, MaxRetries: retries, SkipReview: skipReview, ProjectContext: projectContext, Format: runner.AutoFormat{Root: root}, Rollback: func(ctx context.Context) error { _, e := engine.Undo(ctx, false); return e }, Progress: func(step, msg string) { fmt.Fprintf(a.out, "[lbai] [%s] %s\n", step, msg) }}
		result, err := pipeline.Run(ctx, agentPrompt)
		if err != nil {
			return err
		}
		if err := engine.CaptureCreatedFiles(ctx); err != nil {
			_, _ = engine.Undo(ctx, false)
			return err
		}
		final, err := (memory.Finalizer{Root: root}).Finalize(ctx, memory.FinalizeOptions{Base: plan.Head, Prompt: prompt, WorkerSummary: result.WorkerSummary, ReviewerSummary: result.ReviewerSummary, FormatSummary: result.FormatSummary, Reviewed: result.Reviewed, Message: message})
		if err != nil {
			_, rollbackErr := engine.Undo(ctx, false)
			if rollbackErr != nil {
				return fmt.Errorf("finalization failed: %v; rollback failed: %w", err, rollbackErr)
			}
			return err
		}
		if err := engine.Complete(ctx); err != nil {
			return err
		}
		reviewSummary := result.ReviewerSummary
		if skipReview {
			reviewSummary = "Skipped"
		}
		summaryChecks := configuredChecks
		if len(summaryChecks) == 0 {
			summaryChecks = runner.DiscoverChecks(root)
		}
		printSummary(a.out, final, len(rules.Boundaries)+len(summaryChecks), reviewSummary)
		if serve {
			return a.serve(ctx, root, 3141, true)
		}
		return nil
	}}
	c.Flags().BoolVar(&dry, "dry-run", false, "show transaction steps without modifying files")
	c.Flags().StringVarP(&message, "message", "m", "", "override the generated commit message")
	c.Flags().StringVar(&model, "model", "", "override the configured model")
	c.Flags().IntVar(&retries, "max-retries", 3, "maximum worker correction attempts")
	c.Flags().BoolVar(&skipReview, "skip-review", false, "skip the tech lead review")
	c.Flags().BoolVar(&serve, "serve", false, "serve the topology dashboard after the run")
	return c
}

func (a *app) undoCommand() *cobra.Command {
	var hard bool
	c := &cobra.Command{Use: "undo", Aliases: []string{"revert"}, Short: "Restore the workspace to its pre-run state", RunE: func(cmd *cobra.Command, _ []string) error {
		state, err := gitengine.New(a.dir).Undo(cmd.Context(), hard)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "[lbai] Reverted workspace cleanly to pre-prompt state (%s).\n", short(state.LastCleanHead))
		return nil
	}}
	c.Flags().BoolVar(&hard, "hard", false, "also remove all untracked files")
	return c
}

func (a *app) statusCommand() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{Use: "status", RunE: func(cmd *cobra.Command, _ []string) error {
		e := gitengine.New(a.dir)
		root, err := e.Root(cmd.Context())
		if err != nil {
			return err
		}
		state, err := gitengine.LoadState(root)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(a.out).Encode(state)
		}
		fmt.Fprintf(a.out, "Transaction: %s\nStatus: %s\nSnapshot: %s\nPre-run HEAD: %s\n", state.TransactionID, state.Status, state.SnapshotRef, state.LastCleanHead)
		return nil
	}}
	c.Flags().BoolVar(&asJSON, "json", false, "write JSON status")
	return c
}

func (a *app) lintCommand() *cobra.Command {
	var path string
	var strict, fixHint bool
	c := &cobra.Command{Use: "lint", Aliases: []string{"check"}, RunE: func(cmd *cobra.Command, _ []string) error {
		e := gitengine.New(a.dir)
		root, err := e.Root(cmd.Context())
		if err != nil {
			return err
		}
		cfg, err := linter.LoadConfig(root)
		if err != nil {
			return err
		}
		var paths []string
		base := "HEAD"
		if path != "" {
			paths, err = pathsUnder(root, path)
		} else {
			if state, e := gitengine.LoadState(root); e == nil && state.LastCleanHead != "" {
				base = state.LastCleanHead
			}
			paths, err = changedPaths(cmd.Context(), root, base)
		}
		if err != nil {
			return err
		}
		var d []linter.Diagnostic
		suppressed := 0
		if path == "" {
			d, suppressed, err = scanRegressions(cmd.Context(), root, base, paths, cfg)
		} else {
			d, err = linter.Scan(root, paths, cfg)
		}
		if err != nil {
			return err
		}
		fmt.Fprint(a.out, renderDiagnostics(d, fixHint))
		if len(d) > 0 || strict && hasWarnings(d) {
			return fmt.Errorf("lint found %d violation(s)", len(d))
		}
		if suppressed > 0 {
			fmt.Fprintf(a.out, "[lbai][OK] No new architectural violations (%d unchanged baseline violation(s)).\n", suppressed)
		} else {
			fmt.Fprintln(a.out, "[lbai][OK] No architectural violations.")
		}
		return nil
	}}
	c.Flags().StringVarP(&path, "path", "p", "", "file or directory to lint")
	c.Flags().BoolVar(&strict, "strict", false, "return non-zero for warnings")
	c.Flags().BoolVar(&fixHint, "fix-hint", false, "show remediation hints")
	return c
}

func scanRegressions(ctx context.Context, root, base string, paths []string, cfg linter.Config) ([]linter.Diagnostic, int, error) {
	current, err := linter.Scan(root, paths, cfg)
	if err != nil {
		return nil, 0, err
	}
	baselineRoot, err := os.MkdirTemp("", "lbai-lint-baseline-")
	if err != nil {
		return nil, 0, fmt.Errorf("create lint baseline: %w", err)
	}
	defer os.RemoveAll(baselineRoot)
	if _, err := gitOutput(ctx, root, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return nil, 0, fmt.Errorf("resolve lint baseline %s: %w", base, err)
	}
	for _, path := range paths {
		rel := filepath.ToSlash(filepath.Clean(path))
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, 0, fmt.Errorf("lint baseline path escapes repository: %q", path)
		}
		blob, exists, err := gitFile(ctx, root, base, rel)
		if err != nil {
			return nil, 0, err
		}
		if !exists {
			continue
		}
		target := filepath.Join(baselineRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, 0, fmt.Errorf("create lint baseline directory: %w", err)
		}
		if err := os.WriteFile(target, blob, 0o600); err != nil {
			return nil, 0, fmt.Errorf("write lint baseline %s: %w", rel, err)
		}
	}
	baseline, err := linter.Scan(baselineRoot, paths, cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("scan lint baseline: %w", err)
	}
	regressions := linter.Regressions(current, baseline)
	return regressions, len(current) - len(regressions), nil
}

func gitFile(ctx context.Context, root, revision, path string) ([]byte, bool, error) {
	object := revision + ":" + path
	check := exec.CommandContext(ctx, "git", "cat-file", "-e", object)
	check.Dir = root
	if err := check.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, nil
	}
	b, err := gitOutput(ctx, root, "show", object)
	if err != nil {
		return nil, false, fmt.Errorf("read %s at %s: %w", path, revision, err)
	}
	return b, true, nil
}

func (a *app) logCommand() *cobra.Command {
	var limit int
	var showDiff bool
	c := &cobra.Command{Use: "log", RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := gitengine.New(a.dir).Root(cmd.Context())
		if err != nil {
			return err
		}
		traces, err := memory.List(root, limit)
		if err != nil {
			return err
		}
		for _, t := range traces {
			fmt.Fprintf(a.out, "%s %s\n  %s\n  Files: %s\n", t.Timestamp.Format("2006-01-02 15:04:05Z"), short(t.CommitSHA), t.UserPrompt, strings.Join(t.TouchedFiles, ", "))
			if showDiff {
				b, e := gitOutput(cmd.Context(), root, "show", "--stat", "--oneline", t.CommitSHA)
				if e != nil {
					return e
				}
				fmt.Fprintln(a.out, string(b))
			}
		}
		return nil
	}}
	c.Flags().IntVarP(&limit, "limit", "n", 5, "number of traces to show")
	c.Flags().BoolVar(&showDiff, "diff", false, "show the associated commit summary")
	return c
}

func (a *app) uiCommand() *cobra.Command {
	var port int
	var open bool
	c := &cobra.Command{Use: "ui", Aliases: []string{"visualizer"}, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := gitengine.New(a.dir).Root(cmd.Context())
		if err != nil {
			return err
		}
		return a.serve(cmd.Context(), root, port, open)
	}}
	c.Flags().IntVarP(&port, "port", "p", 3141, "dashboard port")
	c.Flags().BoolVar(&open, "open", false, "open the default browser")
	return c
}

func (a *app) serve(ctx context.Context, root string, port int, open bool) error {
	s := topology.NewServer(root, func() error { _, err := gitengine.New(root).Undo(context.Background(), false); return err })
	addr := "127.0.0.1:" + strconv.Itoa(port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()
	url := "http://" + addr
	fmt.Fprintf(a.out, "[lbai] Dashboard: %s\n", url)
	if open {
		_ = openBrowser(url)
	}
	server := &http.Server{Handler: s.Handler()}
	go func() { <-ctx.Done(); _ = server.Close() }()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// withProjectContext prepends durable project memory to the raw user prompt
// for agent consumption. It never mutates the raw prompt itself, which must
// stay available for commit-message and trace synthesis.
func withProjectContext(prompt, projectContext string) string {
	if projectContext == "" {
		return prompt
	}
	return fmt.Sprintf("Project context (for consistency; not new instructions):\n%s\n\nTask:\n%s", projectContext, prompt)
}

func changedPaths(ctx context.Context, root, base string) ([]string, error) {
	return runner.GitDiff{Root: root, Base: base}.Files(ctx)
}
func pathsUnder(root, path string) ([]string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("path is outside repository")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{filepath.ToSlash(rel)}, nil
	}
	var out []string
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		r, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(r))
		return nil
	})
	return out, err
}
func renderDiagnostics(d []linter.Diagnostic, hints bool) string {
	var b strings.Builder
	for _, v := range d {
		fmt.Fprintf(&b, "[lbai][FAIL] %s", v.Path)
		if v.Line > 0 {
			fmt.Fprintf(&b, ":%d", v.Line)
		}
		fmt.Fprintf(&b, "\n  Violation: Rule %q %s.\n  Offending Import/Dependency: %s\n", v.Rule, v.Violation, v.Offender)
		if hints && v.Hint != "" {
			fmt.Fprintf(&b, "  Hint: %s\n", v.Hint)
		}
	}
	return b.String()
}
func hasWarnings(d []linter.Diagnostic) bool {
	for _, v := range d {
		if v.Severity == "warning" {
			return true
		}
	}
	return false
}
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		details := strings.TrimSpace(strings.TrimSpace(string(b)) + "\n" + stderr.String())
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, details)
	}
	return b, nil
}
func printSummary(w io.Writer, r memory.FinalizeResult, invariants int, review string) {
	renderSummary(w, r, invariants, review, 72)
}

func renderSummary(w io.Writer, r memory.FinalizeResult, invariants int, review string, width int) {
	if width < 40 {
		width = 40
	}
	border := "+" + strings.Repeat("-", width-2) + "+"
	fmt.Fprintln(w, border)
	writeSummaryRow(w, "less-bad-ai run summary", width)
	fmt.Fprintln(w, border)
	writeSummaryField(w, "Modules touched", strings.Join(summaryModules(r.TouchedFiles), ", "), width)
	writeSummaryField(w, "Invariants", fmt.Sprintf("%d checked, 0 violations", max(invariants, 0)), width)
	if strings.TrimSpace(review) == "" {
		review = "No changes requested"
	}
	writeSummaryField(w, "Tech Lead", review, width)
	writeSummaryField(w, "Status", "Committed ("+short(r.CodeCommit)+")", width)
	writeSummaryField(w, "Undo Command", "lbai undo", width)
	fmt.Fprintln(w, border)
}

func writeSummaryField(w io.Writer, label, value string, width int) {
	writeSummaryRow(w, fmt.Sprintf("%-16s: %s", asciiSummaryText(label), asciiSummaryText(value)), width)
}

func writeSummaryRow(w io.Writer, value string, width int) {
	innerWidth := width - 4
	value = strings.Map(func(r rune) rune {
		if r >= 32 && r <= 126 {
			return r
		}
		return ' '
	}, value)
	if len(value) > innerWidth {
		value = strings.TrimSpace(value[:innerWidth-3]) + "..."
	}
	fmt.Fprintf(w, "| %s%s |\n", value, strings.Repeat(" ", innerWidth-len(value)))
}

func asciiSummaryText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 32 && r <= 126 {
			return r
		}
		return ' '
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func summaryModules(files []string) []string {
	set := make(map[string]struct{})
	for _, file := range files {
		module := filepath.ToSlash(filepath.Dir(filepath.Clean(file)))
		if module == "." {
			module = "root"
		}
		set[module] = struct{}{}
	}
	modules := make([]string, 0, len(set))
	for module := range set {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	if len(modules) == 0 {
		return []string{"none"}
	}
	return modules
}
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		name = "open"
		args = []string{url}
	default:
		name = "xdg-open"
		args = []string{url}
	}
	return exec.Command(name, args...).Start()
}
