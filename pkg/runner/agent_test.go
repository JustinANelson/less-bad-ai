package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCommandAgentRunBoundsWaitDespiteOrphanedGrandchild locks in a fix
// found via live testing: a shell-wrapper agent command (cmd /c, sh -c)
// that spawns its own child to do the real work can leave that grandchild
// running and holding the inherited stdout/stderr pipes open even after
// the direct child is killed on cancellation. Without cmd.WaitDelay,
// CombinedOutput blocks until the orphan exits on its own — silently
// defeating any caller-configured timeout.
func TestCommandAgentRunBoundsWaitDespiteOrphanedGrandchild(t *testing.T) {
	var command []string
	switch runtime.GOOS {
	case "windows":
		command = []string{"cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul & rem"}
	default:
		if _, err := exec.LookPath("sh"); err != nil {
			t.Skip("sh not available")
		}
		command = []string{"sh", "-c", "sleep 30 &"}
	}
	// Not t.TempDir(): the orphaned grandchild this test deliberately
	// creates keeps the directory as its working directory for up to
	// ~30s after the test returns, which would make Windows fail t's
	// automatic cleanup. Best-effort remove it instead, ignoring errors.
	root, err := os.MkdirTemp("", "lbai-orphan-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	agent := &CommandAgent{Root: root, Command: command}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := agent.Run(ctx, "prompt"); err == nil {
		t.Fatal("expected the cancelled command to return an error")
	}
	if elapsed := time.Since(start); elapsed > commandWaitDelay+5*time.Second {
		t.Fatalf("Run took %v, want it bounded by commandWaitDelay (%v) despite the orphaned grandchild", elapsed, commandWaitDelay)
	}
}

func TestGitSafeDirectoryEnvPreservesExistingCommandConfig(t *testing.T) {
	environ := []string{
		"PATH=test",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.sslVerify",
		"GIT_CONFIG_VALUE_0=false",
	}
	got := gitSafeDirectoryEnv(environ, `C:\work\project`)
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "http.sslVerify",
		"GIT_CONFIG_VALUE_0": "false",
		"GIT_CONFIG_KEY_1":   "safe.directory",
		"GIT_CONFIG_VALUE_1": `C:\work\project`,
	}
	for _, entry := range got {
		key, value, found := strings.Cut(entry, "=")
		if found {
			if expected, exists := want[key]; exists {
				if value != expected {
					t.Fatalf("%s = %q, want %q", key, value, expected)
				}
				delete(want, key)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("environment omitted values: %#v", want)
	}
}

func TestOpenAIAgentAppliesStructuredResponse(t *testing.T) {
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) != 2 || request.Messages[0]["role"] != "system" || !strings.Contains(request.Messages[0]["content"], "operations") {
			t.Errorf("request omitted structured-output instructions: %#v", request)
		}
		content := `{"summary":"implemented feature","operations":[{"operation":"write","path":"feature.txt","content":"done"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()

	agent := OpenAIAgent{Endpoint: server.URL, Model: "test", Client: server.Client(), Applier: RootFileOperationApplier{Root: root}}
	summary, err := agent.Run(context.Background(), "make a feature")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "implemented feature" {
		t.Fatalf("summary = %q", summary)
	}
	b, err := os.ReadFile(filepath.Join(root, "feature.txt"))
	if err != nil || string(b) != "done" {
		t.Fatalf("applied content = %q, %v", b, err)
	}
}

func TestOpenAIAgentRejectsFreeFormResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "I changed the file."}}}})
	}))
	defer server.Close()
	agent := OpenAIAgent{Endpoint: server.URL, Model: "test", Client: server.Client(), Applier: RootFileOperationApplier{Root: t.TempDir()}}
	if _, err := agent.Run(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "structured agent response") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestDecodeAgentResponseRejectsUnknownAndTrailingContent(t *testing.T) {
	for _, content := range []string{
		`{"summary":"ok","operations":[],"unexpected":true}`,
		`{"summary":"ok","operations":[]} trailing`,
		`{"summary":"ok"}`,
	} {
		if _, err := decodeAgentResponse(content); err == nil {
			t.Fatalf("decodeAgentResponse accepted %q", content)
		}
	}
}

type failingFileOperationApplier struct{}

func (failingFileOperationApplier) Apply(context.Context, []FileOperation) error {
	return errors.New("scripted patch failure")
}

func TestOpenAIAgentSurfacesOperationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		content := `{"summary":"change","operations":[{"operation":"write","path":"file.txt","content":"data"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	agent := OpenAIAgent{Endpoint: server.URL, Model: "test", Client: server.Client(), Applier: failingFileOperationApplier{}}
	if _, err := agent.Run(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "scripted patch failure") {
		t.Fatalf("Run error = %v", err)
	}
}
