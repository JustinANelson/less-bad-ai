package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateDocsPreservesUserAuthoredSections(t *testing.T) {
	root := t.TempDir()
	architecture := "# Architecture\n\nUser introduction.\n\n## Topology\n\nOld generated topology.\n\n## Constraints\n\nNever delete this section.\n"
	contextDoc := "# AI Context\n\nHuman-maintained guidance.\n"
	if err := os.WriteFile(filepath.Join(root, "ARCHITECTURE.md"), []byte(architecture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AI_CONTEXT.md"), []byte(contextDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	trace := testTrace("trace-1", time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	if err := writeTrace(root, trace); err != nil {
		t.Fatal(err)
	}
	if err := updateDocs(root, trace); err != nil {
		t.Fatal(err)
	}
	if err := updateDocs(root, trace); err != nil {
		t.Fatal(err)
	}

	gotArchitecture := readFile(t, filepath.Join(root, "ARCHITECTURE.md"))
	for _, text := range []string{"User introduction.", "## Constraints\n\nNever delete this section.", "## Recent Decisions", trace.ADRDecision} {
		if !strings.Contains(gotArchitecture, text) {
			t.Fatalf("ARCHITECTURE.md lost %q:\n%s", text, gotArchitecture)
		}
	}
	if count := strings.Count(gotArchitecture, trace.ADRDecision); count != 1 {
		t.Fatalf("decision appears %d times after repeated update:\n%s", count, gotArchitecture)
	}
	gotContext := readFile(t, filepath.Join(root, "AI_CONTEXT.md"))
	if !strings.Contains(gotContext, "Human-maintained guidance.") || !strings.Contains(gotContext, "## Latest Verified Change") {
		t.Fatalf("AI_CONTEXT.md was not updated safely:\n%s", gotContext)
	}
}

func TestTraceRoundTripAndDeterministicLogOrdering(t *testing.T) {
	root := t.TempDir()
	newer := testTrace("trace-b", time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	older := testTrace("trace-a", time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	newer.TracePath = ".lbai/traces/newer.json"
	older.TracePath = ".lbai/traces/older.json"
	for _, trace := range []Trace{newer, older} {
		if err := writeTrace(root, trace); err != nil {
			t.Fatal(err)
		}
	}
	if err := updateDocs(root, newer); err != nil {
		t.Fatal(err)
	}
	traces, err := List(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 || traces[0].TraceID != "trace-b" || traces[0].TracePath != ".lbai/traces/newer.json" {
		t.Fatalf("unexpected trace round trip: %#v", traces)
	}
	log := readFile(t, filepath.Join(root, "docs", "decisions", "LOG.md"))
	if strings.Index(log, "older.json") > strings.Index(log, "newer.json") {
		t.Fatalf("decision log is not chronological:\n%s", log)
	}
}

func TestSynthesizeMessageIsConventionalAndBounded(t *testing.T) {
	prompt := strings.Repeat("\u00e9", 100)
	message := synthesizeMessage([]string{"pkg/memory/file.go"}, prompt)
	if err := validateGeneratedSubject(message); err != nil {
		t.Fatal(err)
	}
	if len([]rune(message)) > 72 {
		t.Fatalf("subject has %d characters: %q", len([]rune(message)), message)
	}
}

func TestWriteTraceRejectsUnsafePath(t *testing.T) {
	trace := testTrace("trace-1", time.Now().UTC())
	trace.TracePath = "../outside.json"
	if err := writeTrace(t.TempDir(), trace); err == nil {
		t.Fatal("writeTrace accepted an unsafe path")
	}
}

func TestWriteTraceRejectsInvalidSHAAndTouchedFiles(t *testing.T) {
	for name, mutate := range map[string]func(*Trace){
		"invalid SHA":     func(trace *Trace) { trace.CommitSHA = "not-a-sha" },
		"unsorted files":  func(trace *Trace) { trace.TouchedFiles = []string{"z.go", "a.go"} },
		"duplicate files": func(trace *Trace) { trace.TouchedFiles = []string{"a.go", "a.go"} },
		"escaping file":   func(trace *Trace) { trace.TouchedFiles = []string{"../outside.go"} },
	} {
		t.Run(name, func(t *testing.T) {
			trace := testTrace("trace-1", time.Now().UTC())
			mutate(&trace)
			if err := writeTrace(t.TempDir(), trace); err == nil {
				t.Fatal("writeTrace accepted invalid trace")
			}
		})
	}
}

func testTrace(id string, timestamp time.Time) Trace {
	return Trace{
		Version: TraceVersion, TraceID: id, CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		Timestamp: timestamp, UserPrompt: "prompt", WorkerSummary: "summary",
		TouchedFiles: []string{"pkg/memory/file.go"}, ADRDecision: "Preserve project memory.",
		TracePath: ".lbai/traces/" + id + ".json",
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
