package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const TraceVersion = 1

type Trace struct {
	Version               int       `json:"version"`
	TraceID               string    `json:"trace_id"`
	CommitSHA             string    `json:"commit_sha"`
	Timestamp             time.Time `json:"timestamp"`
	UserPrompt            string    `json:"user_prompt"`
	WorkerSummary         string    `json:"worker_summary"`
	TechLeadModifications string    `json:"tech_lead_modifications"`
	TouchedFiles          []string  `json:"touched_files"`
	ADRDecision           string    `json:"adr_decision"`
	TracePath             string    `json:"-"`
}

type Finalizer struct {
	Root string
	Now  func() time.Time
}
type FinalizeOptions struct{ Base, Prompt, WorkerSummary, ReviewerSummary, Message string }
type FinalizeResult struct {
	CodeCommit, MetadataCommit, TracePath string
	TouchedFiles                          []string
}

func (f Finalizer) Finalize(ctx context.Context, o FinalizeOptions) (FinalizeResult, error) {
	var result FinalizeResult
	files, err := gitLines(ctx, f.Root, "diff", "--name-only", o.Base, "--")
	if err != nil {
		return result, err
	}
	untracked, err := gitLines(ctx, f.Root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return result, err
	}
	files = uniqueSorted(append(files, untracked...))
	result.TouchedFiles = files
	if len(files) == 0 {
		return result, fmt.Errorf("agent produced no changes")
	}
	if _, err := git(ctx, f.Root, "add", "--all"); err != nil {
		return result, err
	}
	message := o.Message
	if message == "" {
		message = synthesizeMessage(files, o.Prompt)
	}
	traceID := fmt.Sprintf("lbai-%d", f.now().UTC().UnixNano())
	message += "\n\nWhy: Generated and verified through the less-bad-ai transaction pipeline.\nLBAI-Trace-ID: " + traceID
	if _, err := git(ctx, f.Root, "commit", "-m", message); err != nil {
		return result, fmt.Errorf("commit verified changes: %w", err)
	}
	sha, err := gitText(ctx, f.Root, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.CodeCommit = sha
	timestamp := f.now().UTC()
	name := timestamp.Format("20060102T150405Z") + "_" + short(sha) + ".json"
	rel := filepath.ToSlash(filepath.Join(".lbai", "traces", name))
	result.TracePath = rel
	trace := Trace{Version: TraceVersion, TraceID: traceID, CommitSHA: sha, Timestamp: timestamp, UserPrompt: o.Prompt, WorkerSummary: o.WorkerSummary, TechLeadModifications: o.ReviewerSummary, TouchedFiles: files, ADRDecision: "Changes were generated, validated, reviewed, and accepted by the configured transaction pipeline.", TracePath: rel}
	if err := writeTrace(f.Root, trace); err != nil {
		return result, err
	}
	if err := updateDocs(f.Root, trace); err != nil {
		return result, err
	}
	if _, err := git(ctx, f.Root, "add", rel, "ARCHITECTURE.md", "AI_CONTEXT.md", filepath.ToSlash(filepath.Join("docs", "decisions", "LOG.md"))); err != nil {
		return result, err
	}
	if _, err := git(ctx, f.Root, "commit", "-m", fmt.Sprintf("docs(memory): record trace for %s", short(sha))); err != nil {
		return result, fmt.Errorf("commit transaction memory: %w", err)
	}
	result.MetadataCommit, err = gitText(ctx, f.Root, "rev-parse", "HEAD")
	return result, err
}
func (f Finalizer) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func writeTrace(root string, t Trace) error {
	path := filepath.Join(root, filepath.FromSlash(t.TracePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
func updateDocs(root string, t Trace) error {
	arch := fmt.Sprintf("# Architecture\n\n## Topology\n\nThe repository topology is derived from source imports by `lbai ui`.\n\n## Recent Decisions\n\n- %s: %s (`%s`)\n", t.Timestamp.Format(time.RFC3339), t.ADRDecision, short(t.CommitSHA))
	if err := os.WriteFile(filepath.Join(root, "ARCHITECTURE.md"), []byte(arch), 0o644); err != nil {
		return err
	}
	context := fmt.Sprintf("# AI Context\n\nLast verified change: %s\n\n%s\n", t.Timestamp.Format(time.RFC3339), fallback(t.WorkerSummary, "No worker summary was provided."))
	if err := os.WriteFile(filepath.Join(root, "AI_CONTEXT.md"), []byte(context), 0o644); err != nil {
		return err
	}
	logPath := filepath.Join(root, "docs", "decisions", "LOG.md")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	existing, _ := os.ReadFile(logPath)
	if len(existing) == 0 {
		existing = []byte("# Decision Log\n\n")
	}
	entry := fmt.Sprintf("## %s — %s\n\n- Trace: `%s`\n- Files: %s\n- Decision: %s\n\n", t.Timestamp.Format(time.RFC3339), short(t.CommitSHA), t.TracePath, strings.Join(t.TouchedFiles, ", "), t.ADRDecision)
	return os.WriteFile(logPath, append(existing, entry...), 0o644)
}
func List(root string, limit int) ([]Trace, error) {
	entries, err := filepath.Glob(filepath.Join(root, ".lbai", "traces", "*.json"))
	if err != nil {
		return nil, err
	}
	var traces []Trace
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var t Trace
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		t.TracePath = filepath.ToSlash(path)
		traces = append(traces, t)
	}
	sort.Slice(traces, func(i, j int) bool { return traces[i].Timestamp.After(traces[j].Timestamp) })
	if limit > 0 && len(traces) > limit {
		traces = traces[:limit]
	}
	return traces, nil
}
func synthesizeMessage(files []string, prompt string) string {
	scope := "project"
	if len(files) > 0 {
		parts := strings.Split(filepath.ToSlash(files[0]), "/")
		if len(parts) > 1 {
			scope = parts[len(parts)-2]
		}
	}
	summary := strings.TrimSpace(strings.Split(prompt, "\n")[0])
	if summary == "" {
		summary = "apply verified agent changes"
	}
	summary = strings.ToLower(strings.TrimSuffix(summary, "."))
	if len(summary) > 60 {
		summary = summary[:60]
	}
	return fmt.Sprintf("feat(%s): %s", sanitize(scope), summary)
}
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "project"
	}
	return b.String()
}
func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			seen[filepath.ToSlash(v)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func fallback(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	b, err := cmd.CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return b, nil
}
func gitText(ctx context.Context, root string, args ...string) (string, error) {
	b, e := git(ctx, root, args...)
	return strings.TrimSpace(string(b)), e
}
func gitLines(ctx context.Context, root string, args ...string) ([]string, error) {
	s, e := gitText(ctx, root, args...)
	if e != nil {
		return nil, e
	}
	if s == "" {
		return nil, nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), nil
}
