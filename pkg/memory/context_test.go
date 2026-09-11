package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContextEmptyWhenNoMemoryExists(t *testing.T) {
	root := t.TempDir()
	got, err := LoadContext(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty context for a project with no memory, got %q", got)
	}
}

func TestLoadContextArchitectureOnly(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ARCHITECTURE.md", "# Architecture\n\n## Recent Decisions\n\n- decided to use gofmt\n")
	got, err := LoadContext(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "decided to use gofmt") {
		t.Fatalf("expected decisions in context, got %q", got)
	}
	if strings.Contains(got, "Last verified change") {
		t.Fatalf("did not expect an AI_CONTEXT.md section when the file is absent, got %q", got)
	}
}

// TestLoadContextIgnoresAIContextTranscript locks in a deliberate choice: a
// live end-to-end run showed that AI_CONTEXT.md's "Latest Verified Change"
// section holds raw, uncurated command-agent stdout (tool calls, shell
// output, even embedded error text) rather than a clean summary, and
// feeding it into a later prompt as "project context" confused that run's
// agent into believing the described work was already done. LoadContext
// must not surface it, no matter how noisy it is.
func TestLoadContextIgnoresAIContextTranscript(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ARCHITECTURE.md", "# Architecture\n\n## Recent Decisions\n\n- decision one\n")
	write(t, root, "AI_CONTEXT.md", "# AI Context\n\n## Latest Verified Change\n\nLast verified change: 2026-09-11T00:00:00Z\n\nexec\n\"powershell.exe\" -Command '...'\nfatal: detected dubious ownership in repository\n")
	got, err := LoadContext(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "decision one") {
		t.Fatalf("expected decisions in context, got %q", got)
	}
	if strings.Contains(got, "dubious ownership") || strings.Contains(got, "Latest Verified Change") || strings.Contains(got, "powershell") {
		t.Fatalf("AI_CONTEXT.md transcript leaked into agent context: %q", got)
	}
}

func TestLoadContextTruncatesOversizedContent(t *testing.T) {
	root := t.TempDir()
	huge := strings.Repeat("x", maxContextChars*2)
	write(t, root, "ARCHITECTURE.md", "# Architecture\n\n## Recent Decisions\n\n"+huge+"\n")
	got, err := LoadContext(root)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got)) > maxContextChars+len("...") {
		t.Fatalf("expected context to be capped near %d chars, got %d", maxContextChars, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated context to end with an ellipsis, got %q", got[len(got)-10:])
	}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
