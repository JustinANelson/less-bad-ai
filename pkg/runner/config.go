package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Worker   AgentConfig   `toml:"worker"`
	Reviewer AgentConfig   `toml:"reviewer"`
	Checks   []CheckConfig `toml:"checks,omitempty"`
}

type AgentConfig struct {
	Type      string   `toml:"type"`
	Command   []string `toml:"command"`
	Endpoint  string   `toml:"endpoint"`
	Model     string   `toml:"model"`
	APIKeyEnv string   `toml:"api_key_env"`
}

type CheckConfig struct {
	Name       string   `toml:"name"`
	Executable string   `toml:"executable"`
	Args       []string `toml:"args"`
	DependsOn  []string `toml:"depends_on,omitempty"`
}

func LoadConfig(root string) (Config, error) {
	path := filepath.Join(root, ".lbai", "config.toml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DiscoverConfig(root)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read runner config: %w", err)
	}
	var cfg Config
	decoder := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode runner config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// DiscoverConfig provides a zero-configuration starting point without writing
// anything to the repository. An explicit .lbai/config.toml always takes
// precedence through LoadConfig.
func DiscoverConfig(root string) (Config, error) {
	return discoverConfig(root, exec.LookPath)
}

type executableLookup func(string) (string, error)

func discoverConfig(root string, lookPath executableLookup) (Config, error) {
	worker, err := discoverAgent(lookPath)
	if err != nil {
		return Config{}, err
	}
	return Config{Worker: worker, Reviewer: worker, Checks: discoverChecks(root, lookPath)}, nil
}

func discoverAgent(lookPath executableLookup) (AgentConfig, error) {
	candidates := []AgentConfig{
		{Type: "command", Command: []string{"codex", "exec", "--sandbox", "workspace-write", "--ephemeral"}},
		{Type: "command", Command: []string{"claude", "-p", "--permission-mode", "acceptEdits"}},
		{Type: "command", Command: []string{"aider", "--yes", "--message"}},
	}
	for _, candidate := range candidates {
		if _, err := lookPath(candidate.Command[0]); err == nil {
			return candidate, nil
		}
	}
	return AgentConfig{}, errors.New("no supported coding agent found; install codex, claude, or aider, or create .lbai/config.toml")
}

func discoverChecks(root string, lookPath executableLookup) []CheckConfig {
	type candidate struct {
		marker     string
		executable string
		args       []string
	}
	for _, c := range []candidate{
		{marker: "Cargo.toml", executable: "cargo", args: []string{"test"}},
		{marker: "pom.xml", executable: "mvn", args: []string{"test"}},
	} {
		if regularFile(filepath.Join(root, c.marker)) && executableAvailable(c.executable, lookPath) {
			return []CheckConfig{{Name: "test", Executable: c.executable, Args: c.args}}
		}
	}
	if regularFile(filepath.Join(root, "go.mod")) && executableAvailable("go", lookPath) {
		return []CheckConfig{
			{Name: "test", Executable: "go", Args: []string{"test", "./..."}},
			{Name: "vet", Executable: "go", Args: []string{"vet", "./..."}},
		}
	}
	if wrapper := discoverWrapper(root, "gradlew"); wrapper != "" {
		return []CheckConfig{{Name: "test", Executable: wrapper, Args: []string{"test"}}}
	}
	if checks := discoverNodeChecks(root, lookPath); len(checks) > 0 {
		return checks
	}
	if hasPytestConfig(root) {
		for _, executable := range []string{"python3", "python"} {
			if executableAvailable(executable, lookPath) {
				return []CheckConfig{{Name: "test", Executable: executable, Args: []string{"-m", "pytest"}}}
			}
		}
	}
	return nil
}

func discoverWrapper(root, name string) string {
	candidates := []string{name, name + ".bat"}
	if runtime.GOOS == "windows" {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}
	for _, candidate := range candidates {
		if regularFile(filepath.Join(root, candidate)) {
			return filepath.Join(".", candidate)
		}
	}
	return ""
}

func hasPytestConfig(root string) bool {
	if regularFile(filepath.Join(root, "pytest.ini")) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	return err == nil && strings.Contains(string(b), "[tool.pytest")
}

func discoverNodeChecks(root string, lookPath executableLookup) []CheckConfig {
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &manifest) != nil {
		return nil
	}
	var scripts []string
	for _, name := range []string{"build", "test", "lint"} {
		if manifest.Scripts[name] != "" {
			scripts = append(scripts, name)
		}
	}
	if len(scripts) == 0 {
		return nil
	}
	manager := ""
	for _, manager := range []struct {
		lockfile   string
		executable string
	}{
		{lockfile: "pnpm-lock.yaml", executable: "pnpm"},
		{lockfile: "yarn.lock", executable: "yarn"},
		{lockfile: "bun.lock", executable: "bun"},
		{lockfile: "bun.lockb", executable: "bun"},
	} {
		if regularFile(filepath.Join(root, manager.lockfile)) && executableAvailable(manager.executable, lookPath) {
			return nodeCheckConfigs(manager.executable, scripts)
		}
	}
	if executableAvailable("npm", lookPath) {
		manager = "npm"
	}
	if manager == "" {
		return nil
	}
	return nodeCheckConfigs(manager, scripts)
}

func nodeCheckConfigs(manager string, scripts []string) []CheckConfig {
	checks := make([]CheckConfig, 0, len(scripts))
	for _, script := range scripts {
		checks = append(checks, CheckConfig{Name: script, Executable: manager, Args: []string{script}})
	}
	return checks
}

func executableAvailable(name string, lookPath executableLookup) bool {
	_, err := lookPath(name)
	return err == nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// WriteConfig persists a discovered configuration without overwriting user
// configuration. The file contains command names and environment-variable
// names, never credential values.
func WriteConfig(root string, cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	path := filepath.Join(root, ".lbai", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create configuration directory: %w", err)
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encode runner config: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("configuration already exists at %s", path)
		}
		return "", fmt.Errorf("create runner config: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write runner config: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close runner config: %w", err)
	}
	return path, nil
}

func (c Config) Validate() error {
	if err := c.Worker.Validate(); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	if c.Reviewer.Type != "" {
		if err := c.Reviewer.Validate(); err != nil {
			return fmt.Errorf("reviewer: %w", err)
		}
	}
	graph := make(map[string][]string, len(c.Checks))
	for i, check := range c.Checks {
		if strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Executable) == "" {
			return fmt.Errorf("check %d requires name and executable", i)
		}
		if _, exists := graph[check.Name]; exists {
			return fmt.Errorf("duplicate verification check %q", check.Name)
		}
		if check.Name == "architecture" {
			return errors.New("verification check name \"architecture\" is reserved by lbai")
		}
		graph[check.Name] = append([]string(nil), check.DependsOn...)
	}
	return validateDependencyGraph(graph)
}

func (c AgentConfig) Validate() error {
	switch c.Type {
	case "command":
		if len(c.Command) == 0 {
			return errors.New("command provider requires command")
		}
	case "openai":
		if c.Endpoint == "" {
			return errors.New("openai provider requires endpoint")
		}
		if c.Model == "" {
			return errors.New("openai provider requires model")
		}
	default:
		return fmt.Errorf("unsupported provider type %q", c.Type)
	}
	return nil
}
