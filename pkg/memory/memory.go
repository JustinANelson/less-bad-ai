package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const TraceVersion = 1

var commitSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

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
	Git  GitRunner
}

type GitRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, root string, args ...string) ([]byte, error) {
	return git(ctx, root, args...)
}

type FinalizeOptions struct{ Base, Prompt, WorkerSummary, ReviewerSummary, Message string }
type FinalizeResult struct {
	CodeCommit, MetadataCommit, TracePath string
	TouchedFiles                          []string
}

func (f Finalizer) Finalize(ctx context.Context, o FinalizeOptions) (FinalizeResult, error) {
	var result FinalizeResult
	files, err := f.gitLines(ctx, "diff", "--name-only", o.Base, "--")
	if err != nil {
		return result, err
	}
	untracked, err := f.gitLines(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return result, err
	}
	files = uniqueSorted(append(files, untracked...))
	result.TouchedFiles = files
	if len(files) == 0 {
		return result, fmt.Errorf("agent produced no changes")
	}
	if _, err := f.runGit(ctx, "add", "--all"); err != nil {
		return result, err
	}
	message := o.Message
	if message == "" {
		message = synthesizeMessage(files, o.Prompt)
		if err := validateGeneratedSubject(message); err != nil {
			return result, err
		}
	}
	timestamp := f.now().UTC()
	traceID := fmt.Sprintf("lbai-%d", timestamp.UnixNano())
	message += "\n\nWhy: Generated and verified through the less-bad-ai transaction pipeline.\nLBAI-Trace-ID: " + traceID
	if _, err := f.runGit(ctx, "commit", "-m", message); err != nil {
		return result, fmt.Errorf("commit verified changes: %w", err)
	}
	sha, err := f.gitText(ctx, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.CodeCommit = sha
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
	if _, err := f.runGit(ctx, "add", rel, "ARCHITECTURE.md", "AI_CONTEXT.md", filepath.ToSlash(filepath.Join("docs", "decisions", "LOG.md"))); err != nil {
		return result, err
	}
	if _, err := f.runGit(ctx, "commit", "-m", fmt.Sprintf("docs(memory): record trace for %s", short(sha))); err != nil {
		return result, fmt.Errorf("commit transaction memory: %w", err)
	}
	result.MetadataCommit, err = f.gitText(ctx, "rev-parse", "HEAD")
	return result, err
}
func (f Finalizer) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f Finalizer) runGit(ctx context.Context, args ...string) ([]byte, error) {
	runner := f.Git
	if runner == nil {
		runner = ExecGitRunner{}
	}
	return runner.Run(ctx, f.Root, args...)
}

func (f Finalizer) gitText(ctx context.Context, args ...string) (string, error) {
	b, err := f.runGit(ctx, args...)
	return strings.TrimSpace(string(b)), err
}

func (f Finalizer) gitLines(ctx context.Context, args ...string) ([]string, error) {
	s, err := f.gitText(ctx, args...)
	if err != nil || s == "" {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n"), nil
}

func writeTrace(root string, t Trace) error {
	if err := validateTrace(t); err != nil {
		return err
	}
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
	archPath := filepath.Join(root, "ARCHITECTURE.md")
	archBytes, err := os.ReadFile(archPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	arch := string(archBytes)
	if strings.TrimSpace(arch) == "" {
		arch = "# Architecture\n"
	}
	arch = replaceMarkdownSection(arch, "Topology", "The repository topology is derived from source imports by `lbai ui`.")
	decision := fmt.Sprintf("- %s: %s (`%s`)", t.Timestamp.Format(time.RFC3339), t.ADRDecision, short(t.CommitSHA))
	if existing := markdownSectionBody(arch, "Recent Decisions"); existing != "" {
		alreadyRecorded := false
		for _, line := range strings.Split(existing, "\n") {
			if strings.TrimSpace(line) == decision {
				alreadyRecorded = true
				break
			}
		}
		if alreadyRecorded {
			decision = existing
		} else {
			decision += "\n" + existing
		}
	}
	arch = replaceMarkdownSection(arch, "Recent Decisions", decision)
	if err := os.WriteFile(archPath, []byte(arch), 0o644); err != nil {
		return err
	}
	contextPath := filepath.Join(root, "AI_CONTEXT.md")
	contextBytes, err := os.ReadFile(contextPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	contextDoc := string(contextBytes)
	if strings.TrimSpace(contextDoc) == "" {
		contextDoc = "# AI Context\n"
	}
	latest := fmt.Sprintf("Last verified change: %s\n\n%s", t.Timestamp.Format(time.RFC3339), fallback(t.WorkerSummary, "No worker summary was provided."))
	contextDoc = replaceMarkdownSection(contextDoc, "Latest Verified Change", latest)
	if err := os.WriteFile(contextPath, []byte(contextDoc), 0o644); err != nil {
		return err
	}
	logPath := filepath.Join(root, "docs", "decisions", "LOG.md")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	traces, err := List(root, 0)
	if err != nil {
		return err
	}
	sort.Slice(traces, func(i, j int) bool {
		if traces[i].Timestamp.Equal(traces[j].Timestamp) {
			return traces[i].TraceID < traces[j].TraceID
		}
		return traces[i].Timestamp.Before(traces[j].Timestamp)
	})
	var log strings.Builder
	log.WriteString("# Decision Log\n\n")
	for _, trace := range traces {
		fmt.Fprintf(&log, "## %s — %s\n\n- Trace: `%s`\n- Files: %s\n- Decision: %s\n\n", trace.Timestamp.Format(time.RFC3339), short(trace.CommitSHA), trace.TracePath, strings.Join(trace.TouchedFiles, ", "), trace.ADRDecision)
	}
	return os.WriteFile(logPath, []byte(log.String()), 0o644)
}

func markdownSectionBody(document, title string) string {
	heading := "## " + title
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return ""
}

func replaceMarkdownSection(document, title, body string) string {
	heading := "## " + title
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(document, "\r\n", "\n"), "\n"), "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "## ") {
			end = i
			break
		}
	}
	replacement := []string{heading, "", strings.TrimSpace(body), ""}
	if start < 0 {
		return strings.Join(append(append(lines, ""), replacement...), "\n") + "\n"
	}
	updated := append([]string{}, lines[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, "\n") + "\n"
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, fmt.Errorf("resolve trace path %s: %w", path, err)
		}
		t.TracePath = filepath.ToSlash(rel)
		if err := validateTrace(t); err != nil {
			return nil, fmt.Errorf("validate %s: %w", path, err)
		}
		traces = append(traces, t)
	}
	sort.Slice(traces, func(i, j int) bool {
		if traces[i].Timestamp.Equal(traces[j].Timestamp) {
			return traces[i].TraceID < traces[j].TraceID
		}
		return traces[i].Timestamp.After(traces[j].Timestamp)
	})
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
	prefix := fmt.Sprintf("feat(%s): ", sanitize(scope))
	available := 72 - len([]rune(prefix))
	runes := []rune(summary)
	if len(runes) > available {
		summary = strings.TrimSpace(string(runes[:available]))
	}
	return prefix + summary
}

func validateGeneratedSubject(subject string) error {
	if len([]rune(subject)) > 72 {
		return fmt.Errorf("generated commit subject exceeds 72 characters")
	}
	if !strings.HasPrefix(subject, "feat(") || !strings.Contains(subject, "): ") {
		return fmt.Errorf("generated commit subject is not conventional: %q", subject)
	}
	return nil
}

func validateTrace(t Trace) error {
	if t.Version != TraceVersion || t.TraceID == "" || !commitSHA.MatchString(t.CommitSHA) || t.Timestamp.IsZero() || strings.TrimSpace(t.ADRDecision) == "" {
		return fmt.Errorf("invalid trace: version, trace_id, commit_sha, timestamp, and adr_decision are required")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(t.TracePath)))
	if clean != filepath.ToSlash(t.TracePath) || !strings.HasPrefix(clean, ".lbai/traces/") || !strings.HasSuffix(clean, ".json") || strings.HasPrefix(clean, "../") || filepath.IsAbs(t.TracePath) {
		return fmt.Errorf("invalid trace path %q", t.TracePath)
	}
	if len(t.TouchedFiles) == 0 || !sort.StringsAreSorted(t.TouchedFiles) {
		return fmt.Errorf("invalid trace: touched_files must be non-empty and sorted")
	}
	for i, path := range t.TouchedFiles {
		cleanPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if cleanPath != path || cleanPath == "." || strings.HasPrefix(cleanPath, "../") || filepath.IsAbs(path) {
			return fmt.Errorf("invalid trace touched path %q", path)
		}
		if i > 0 && path == t.TouchedFiles[i-1] {
			return fmt.Errorf("invalid trace: duplicate touched path %q", path)
		}
	}
	return nil
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
