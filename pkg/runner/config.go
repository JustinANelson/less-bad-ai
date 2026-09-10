package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Worker   AgentConfig `toml:"worker"`
	Reviewer AgentConfig `toml:"reviewer"`
	Build    Command     `toml:"build"`
}

type AgentConfig struct {
	Type      string   `toml:"type"`
	Command   []string `toml:"command"`
	Endpoint  string   `toml:"endpoint"`
	Model     string   `toml:"model"`
	APIKeyEnv string   `toml:"api_key_env"`
}

type Command struct {
	Executable string   `toml:"executable"`
	Args       []string `toml:"args"`
}

func LoadConfig(root string) (Config, error) {
	path := filepath.Join(root, ".lbai", "config.toml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("missing %s; copy .lbai/config.example.toml and configure a worker", path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read runner config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode runner config: %w", err)
	}
	if err := cfg.Worker.Validate(); err != nil {
		return Config{}, fmt.Errorf("worker: %w", err)
	}
	if cfg.Reviewer.Type != "" {
		if err := cfg.Reviewer.Validate(); err != nil {
			return Config{}, fmt.Errorf("reviewer: %w", err)
		}
	}
	return cfg, nil
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
