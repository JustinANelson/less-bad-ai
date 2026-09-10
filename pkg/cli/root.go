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

	"github.com/jnels/less-bad-ai/pkg/gitengine"
	"github.com/jnels/less-bad-ai/pkg/linter"
	"github.com/jnels/less-bad-ai/pkg/memory"
	"github.com/jnels/less-bad-ai/pkg/runner"
	"github.com/jnels/less-bad-ai/pkg/topology"
	"github.com/spf13/cobra"
)

type app struct {
	out, err io.Writer
	dir      string
}

func New() *cobra.Command {
	cwd, _ := os.Getwd()
	a := &app{out: os.Stdout, err: os.Stderr, dir: cwd}
	root := &cobra.Command{Use: "lbai", Aliases: []string{"less-bad-ai"}, Short: "Run coding agents inside recoverable Git transactions", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(a.out)
	root.SetErr(a.err)
	root.AddCommand(a.runCommand(), a.undoCommand(), a.statusCommand(), a.lintCommand(), a.logCommand(), a.uiCommand())
	return root
}

func (a *app) runCommand() *cobra.Command {
	var dry, skipReview, serve bool
	var message, model string
	var retries int
	c := &cobra.Command{Use: "run <prompt>", Aliases: []string{"r", "exec"}, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		engine := gitengine.New(a.dir)
		root, err := engine.Root(ctx)
		if err != nil {
			return err
		}
		var runCfg runner.Config
		var rules linter.Config
		if !dry {
			runCfg, err = runner.LoadConfig(root)
			if err != nil {
				return err
			}
			rules, err = linter.LoadConfig(root)
			if err != nil {
				return err
			}
		}
		plan, err := engine.Begin(ctx, gitengine.BeginOptions{Prompt: args[0], DryRun: dry})
		if err != nil {
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
		verify := runner.CommandVerifier{Root: root, Build: runCfg.Build, Lint: func(ctx context.Context) (string, error) {
			paths, err := changedPaths(ctx, root, plan.Head)
			if err != nil {
				return "", err
			}
			diagnostics, err := linter.Scan(root, paths, rules)
			if err != nil {
				return "", err
			}
			text := renderDiagnostics(diagnostics, true)
			if len(diagnostics) > 0 {
				return text, fmt.Errorf("%d architecture violation(s)", len(diagnostics))
			}
			return text, nil
		}}
		pipeline := runner.Pipeline{Worker: worker, Reviewer: reviewer, Verifier: verify, Diff: runner.GitDiff{Root: root, Base: plan.Head}, MaxRetries: retries, SkipReview: skipReview, Rollback: func(ctx context.Context) error { _, e := engine.Undo(ctx, false); return e }, Progress: func(step, msg string) { fmt.Fprintf(a.out, "[lbai] [%s] %s\n", step, msg) }}
		result, err := pipeline.Run(ctx, args[0])
		if err != nil {
			return err
		}
		if err := engine.CaptureCreatedFiles(ctx); err != nil {
			_, _ = engine.Undo(ctx, false)
			return err
		}
		final, err := (memory.Finalizer{Root: root}).Finalize(ctx, memory.FinalizeOptions{Base: plan.Head, Prompt: args[0], WorkerSummary: result.WorkerSummary, ReviewerSummary: result.ReviewerSummary, Message: message})
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
		printSummary(a.out, final, len(rules.Boundaries), result.ReviewerSummary)
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
		if path != "" {
			paths, err = pathsUnder(root, path)
		} else {
			base := "HEAD"
			if state, e := gitengine.LoadState(root); e == nil && state.LastCleanHead != "" {
				base = state.LastCleanHead
			}
			paths, err = changedPaths(cmd.Context(), root, base)
		}
		if err != nil {
			return err
		}
		d, err := linter.Scan(root, paths, cfg)
		if err != nil {
			return err
		}
		fmt.Fprint(a.out, renderDiagnostics(d, fixHint))
		if len(d) > 0 || strict && hasWarnings(d) {
			return fmt.Errorf("lint found %d violation(s)", len(d))
		}
		fmt.Fprintln(a.out, "[lbai][OK] No architectural violations.")
		return nil
	}}
	c.Flags().StringVarP(&path, "path", "p", "", "file or directory to lint")
	c.Flags().BoolVar(&strict, "strict", false, "return non-zero for warnings")
	c.Flags().BoolVar(&fixHint, "fix-hint", false, "show remediation hints")
	return c
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

func changedPaths(ctx context.Context, root, base string) ([]string, error) {
	tracked, err := gitOutput(ctx, root, "diff", "--name-only", base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, b := range [][]byte{tracked, untracked} {
		for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				set[filepath.ToSlash(line)] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
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
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
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
