package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

type Agent interface {
	Run(context.Context, string) (string, error)
}

func NewAgent(cfg AgentConfig, root, modelOverride string) (Agent, error) {
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	switch cfg.Type {
	case "command":
		return &CommandAgent{Root: root, Command: cfg.Command}, nil
	case "openai":
		return &OpenAIAgent{Endpoint: cfg.Endpoint, Model: cfg.Model, APIKeyEnv: cfg.APIKeyEnv, Client: http.DefaultClient}, nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", cfg.Type)
	}
}

type CommandAgent struct {
	Root    string
	Command []string
}

func (a *CommandAgent) Run(ctx context.Context, prompt string) (string, error) {
	if len(a.Command) == 0 {
		return "", fmt.Errorf("empty agent command")
	}
	args := append(append([]string{}, a.Command[1:]...), prompt)
	cmd := exec.CommandContext(ctx, a.Command[0], args...)
	cmd.Dir = a.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent command: %w: %s", err, truncate(string(out), 8192))
	}
	return truncate(strings.TrimSpace(string(out)), 8192), nil
}

type OpenAIAgent struct {
	Endpoint, Model, APIKeyEnv string
	Client                     *http.Client
}

func (a *OpenAIAgent) Run(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{"model": a.Model, "messages": []map[string]string{{"role": "user", "content": prompt}}}
	b, _ := json.Marshal(body)
	url := strings.TrimRight(a.Endpoint, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.APIKeyEnv != "" {
		if key := os.Getenv(a.APIKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("agent returned %s: %s", resp.Status, truncate(string(raw), 4096))
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode agent response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("agent response contained no choices")
	}
	return result.Choices[0].Message.Content, nil
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
