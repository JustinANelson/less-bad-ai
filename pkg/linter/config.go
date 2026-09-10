package linter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type Config struct {
	Archetype             Archetype             `toml:"archetype"`
	Boundaries            []Boundary            `toml:"boundaries"`
	ForbiddenDependencies []ForbiddenDependency `toml:"forbidden_dependencies"`
}

type Archetype struct {
	Name              string   `toml:"name"`
	Version           string   `toml:"version"`
	AllowedExtensions []string `toml:"allowed_extensions"`
}

type Boundary struct {
	Name             string   `toml:"name"`
	PathPattern      string   `toml:"path_pattern"`
	ForbiddenImports []string `toml:"forbidden_imports"`
	AllowedImports   []string `toml:"allowed_imports"`
	Hint             string   `toml:"hint"`
}

type ForbiddenDependency struct {
	Name    string `toml:"name"`
	Pattern string `toml:"pattern"`
	Hint    string `toml:"hint"`
}

func DefaultConfig() Config {
	return Config{Archetype: Archetype{
		Name: "permissive", Version: "1",
		AllowedExtensions: []string{".go", ".java", ".kt", ".kts", ".js", ".jsx", ".ts", ".tsx"},
	}}
}

func LoadConfig(root string) (Config, error) {
	path := filepath.Join(root, ".lbai", "rules.toml")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read rules: %w", err)
	}
	var cfg Config
	if err := decodeTOML(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode rules: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	for i, b := range c.Boundaries {
		if b.Name == "" || b.PathPattern == "" {
			return fmt.Errorf("boundary %d requires name and path_pattern", i)
		}
		if _, err := globRegexp(b.PathPattern); err != nil {
			return fmt.Errorf("boundary %q path_pattern: %w", b.Name, err)
		}
		for _, pattern := range append(append([]string{}, b.ForbiddenImports...), b.AllowedImports...) {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("boundary %q import pattern %q: %w", b.Name, pattern, err)
			}
		}
	}
	for i, d := range c.ForbiddenDependencies {
		if d.Name == "" && d.Pattern == "" {
			return fmt.Errorf("forbidden dependency %d requires name or pattern", i)
		}
		if d.Pattern != "" {
			if _, err := regexp.Compile(d.Pattern); err != nil {
				return fmt.Errorf("dependency pattern %q: %w", d.Pattern, err)
			}
		}
	}
	return nil
}
