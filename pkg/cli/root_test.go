package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/jnels/less-bad-ai/pkg/memory"
)

func TestServeReportsPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	a := app{out: io.Discard, err: io.Discard}
	err = a.serve(context.Background(), t.TempDir(), port, false)
	if err == nil || !strings.Contains(err.Error(), "listen on 127.0.0.1:"+strconv.Itoa(port)) {
		t.Fatalf("serve error = %v", err)
	}
}

func TestRunAcceptsAndJoinsUnquotedPromptWords(t *testing.T) {
	cmd := (&app{}).runCommand()
	args := []string{"make", "it", "work"}
	if err := cmd.Args(cmd, args); err != nil {
		t.Fatalf("run rejected prompt words: %v", err)
	}
	if got := strings.Join(args, " "); got != "make it work" {
		t.Fatalf("joined prompt = %q", got)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("run accepted an empty prompt")
	}
}

func TestRenderSummaryReportsModulesWithBoundedASCIIRows(t *testing.T) {
	result := memory.FinalizeResult{
		CodeCommit: "0123456789abcdef",
		TouchedFiles: []string{
			"pkg/zeta/z.go",
			"README.md",
			"pkg/alpha/a.go",
			"pkg/alpha/b.go",
		},
	}
	var output bytes.Buffer
	renderSummary(&output, result, 3, "Extracted helper\n\x1b[31mwith colored output", 72)
	text := output.String()
	for _, expected := range []string{"Modules touched", "pkg/alpha, pkg/zeta, root", "3 checked, 0 violations", "Committed (01234567)", "lbai undo"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary omitted %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("summary contains a terminal escape:\n%q", text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if len(line) != 72 {
			t.Fatalf("summary line has width %d, want 72: %q", len(line), line)
		}
	}
}

func TestRenderSummaryUsesSafeMinimumWidthAndFallbackReview(t *testing.T) {
	var output bytes.Buffer
	renderSummary(&output, memory.FinalizeResult{}, -1, "", 10)
	text := output.String()
	if !strings.Contains(text, "No changes requ") || !strings.Contains(text, "none") {
		t.Fatalf("fallback summary missing values:\n%s", text)
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if len(line) != 40 {
			t.Fatalf("minimum-width line has width %d: %q", len(line), line)
		}
	}
}
