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
	"strconv"
	"strings"
)

type Agent interface {
	Run(context.Context, string) (string, error)
}

const structuredResponsePrompt = `Return exactly one JSON object with this schema: {"summary":"brief description","operations":[{"operation":"write","path":"repository/relative/path","content":"complete file contents"},{"operation":"delete","path":"repository/relative/path"}]}. Use an empty operations array when no edits are needed. Do not use Markdown fences or include prose outside the JSON object.`

func NewAgent(cfg AgentConfig, root, modelOverride string) (Agent, error) {
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	switch cfg.Type {
	case "command":
		return &CommandAgent{Root: root, Command: cfg.Command}, nil
	case "openai":
		return &OpenAIAgent{Endpoint: cfg.Endpoint, Model: cfg.Model, APIKeyEnv: cfg.APIKeyEnv, Client: http.DefaultClient, Applier: RootFileOperationApplier{Root: root}}, nil
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
	cmd.Env = gitSafeDirectoryEnv(os.Environ(), a.Root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent command: %w: %s", err, truncate(string(out), 8192))
	}
	return truncate(strings.TrimSpace(string(out)), 8192), nil
}

// gitSafeDirectoryEnv trusts only the active transaction root for Git commands
// launched by the agent. This avoids host/container ownership mismatches without
// changing the user's global Git configuration.
func gitSafeDirectoryEnv(environ []string, root string) []string {
	count := 0
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, "GIT_CONFIG_COUNT") {
			if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
				count = parsed
			}
		}
	}
	environ = setEnv(environ, "GIT_CONFIG_KEY_"+strconv.Itoa(count), "safe.directory")
	environ = setEnv(environ, "GIT_CONFIG_VALUE_"+strconv.Itoa(count), root)
	return setEnv(environ, "GIT_CONFIG_COUNT", strconv.Itoa(count+1))
}

func setEnv(environ []string, key, value string) []string {
	prefix := key + "="
	for i := len(environ) - 1; i >= 0; i-- {
		name, _, found := strings.Cut(environ[i], "=")
		if found && strings.EqualFold(name, key) {
			environ[i] = prefix + value
			return environ
		}
	}
	return append(environ, prefix+value)
}

type OpenAIAgent struct {
	Endpoint, Model, APIKeyEnv string
	Client                     *http.Client
	Applier                    FileOperationApplier
}

func (a *OpenAIAgent) Run(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{"model": a.Model, "messages": []map[string]string{{"role": "system", "content": structuredResponsePrompt}, {"role": "user", "content": prompt}}}
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
	response, err := decodeAgentResponse(result.Choices[0].Message.Content)
	if err != nil {
		return "", err
	}
	if a.Applier == nil {
		return "", fmt.Errorf("OpenAI agent requires a file operation applier")
	}
	if err := a.Applier.Apply(ctx, response.Operations); err != nil {
		return "", fmt.Errorf("apply agent file operations: %w", err)
	}
	return truncate(response.Summary, 8192), nil
}

type agentResponse struct {
	Summary    string          `json:"summary"`
	Operations []FileOperation `json:"operations"`
}

func decodeAgentResponse(content string) (agentResponse, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var response agentResponse
	if err := decoder.Decode(&response); err != nil {
		return agentResponse{}, fmt.Errorf("decode structured agent response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return agentResponse{}, fmt.Errorf("decode structured agent response: multiple JSON values")
		}
		return agentResponse{}, fmt.Errorf("decode structured agent response: trailing content: %w", err)
	}
	if strings.TrimSpace(response.Summary) == "" {
		return agentResponse{}, fmt.Errorf("decode structured agent response: summary is required")
	}
	if response.Operations == nil {
		return agentResponse{}, fmt.Errorf("decode structured agent response: operations array is required")
	}
	return response, nil
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
