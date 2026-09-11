package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoFormatFixesMisformattedGoFile(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	misformatted := "package main\nfunc main(){\nprintln(\"hi\")\n}\n"
	if err := os.WriteFile(path, []byte(misformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := (AutoFormat{Root: root}).Format(context.Background(), []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "gofmt") {
		t.Fatalf("expected gofmt in summary, got %q", summary)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	if string(got) != want {
		t.Fatalf("file not formatted:\n%s", got)
	}
}

func TestAutoFormatSkipsUnavailableTools(t *testing.T) {
	root := t.TempDir()
	summary, err := (AutoFormat{Root: root}).Format(context.Background(), []string{"main.rs"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary, "rustfmt") {
		t.Fatalf("did not expect rustfmt to run when not installed: %q", summary)
	}
}

func TestAutoFormatSkipsDeletedFiles(t *testing.T) {
	root := t.TempDir()
	// "deleted.go" never exists in root, mimicking a file removed by the
	// worker and still present in the changed-file list.
	summary, err := (AutoFormat{Root: root}).Format(context.Background(), []string{"deleted.go"})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "" {
		t.Fatalf("expected no formatting for a nonexistent file, got %q", summary)
	}
}

func TestAutoFormatIgnoresUnknownExtensions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := (AutoFormat{Root: root}).Format(context.Background(), []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "" {
		t.Fatalf("expected no formatter for .md files, got %q", summary)
	}
}
