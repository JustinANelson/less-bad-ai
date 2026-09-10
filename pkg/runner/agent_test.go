package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
