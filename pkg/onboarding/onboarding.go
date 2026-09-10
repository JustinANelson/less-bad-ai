package onboarding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JustinANelson/less-bad-ai/pkg/linter"
	"github.com/JustinANelson/less-bad-ai/pkg/runner"
)

type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

type DoctorResult struct {
	Root   string  `json:"root"`
	Checks []Check `json:"checks"`
}

func (r DoctorResult) HasFailures() bool {
	for _, check := range r.Checks {
		if check.Status == Fail {
			return true
		}
	}
	return false
}

type SetupResult struct {
	Root             string   `json:"root"`
	InitializedGit   bool     `json:"initialized_git"`
	CreatedBaseline  bool     `json:"created_baseline"`
	CreatedConfig    bool     `json:"created_config"`
	ConfigCommitted  bool     `json:"config_committed"`
	ConfigPath       string   `json:"config_path"`
	Agent            string   `json:"agent"`
	Checks           []string `json:"checks"`
	IdentityFallback bool     `json:"identity_fallback"`
}

type Options struct {
	Dir            string
	AllowSensitive bool
	GitName        string
	GitEmail       string
	Discover       func(string) (runner.Config, error)
	LookPath       func(string) (string, error)
}

func Setup(ctx context.Context, opts Options) (SetupResult, error) {
	dir, err := absoluteDirectory(opts.Dir)
	if err != nil {
		return SetupResult{}, err
	}
	lookPath := lookup(opts)
	gitPath, err := lookPath("git")
	if err != nil {
		return SetupResult{}, errors.New("Git is required; install Git and rerun `lbai setup`")
	}

	root, repoExists := existingRoot(ctx, gitPath, dir)
	if !repoExists {
		root = dir
	}
	cfg, configExists, err := loadOrDiscover(root, opts)
	if err != nil {
		return SetupResult{}, fmt.Errorf("detect coding agent and project checks: %w", err)
	}
	if err := validateRuntime(cfg, root, lookPath); err != nil {
		return SetupResult{}, err
	}
	if _, err := linter.LoadConfig(root); err != nil {
		return SetupResult{}, fmt.Errorf("validate architectural rules: %w", err)
	}

	result := SetupResult{Root: root, Agent: agentDescription(cfg.Worker), Checks: checkNames(cfg)}
	if !repoExists {
		if _, err := git(ctx, gitPath, root, "init", "--quiet"); err != nil {
			return result, fmt.Errorf("initialize Git repository: %w", err)
		}
		result.InitializedGit = true
		if _, err := git(ctx, gitPath, root, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return result, fmt.Errorf("select initial Git branch: %w", err)
		}
	}

	hasHead := commandSucceeds(ctx, gitPath, root, "rev-parse", "--verify", "HEAD^{commit}")
	cleanBeforeSetup := gitText(ctx, gitPath, root, "status", "--porcelain=v1", "--untracked-files=all") == ""
	if !hasHead && !opts.AllowSensitive {
		paths, err := untrackedPaths(ctx, gitPath, root)
		if err != nil {
			return result, err
		}
		if sensitive := sensitivePaths(paths); len(sensitive) > 0 {
			return result, fmt.Errorf("refusing to commit possible secrets: %s; add them to .gitignore or rerun with --allow-sensitive", strings.Join(sensitive, ", "))
		}
	}

	if !configExists {
		path, err := runner.WriteConfig(root, cfg)
		if err != nil {
			return result, err
		}
		result.CreatedConfig, result.ConfigPath = true, path
	} else {
		result.ConfigPath = filepath.Join(root, ".lbai", "config.toml")
	}

	fallback, err := ensureIdentity(ctx, gitPath, root, opts.GitName, opts.GitEmail)
	if err != nil {
		return result, err
	}
	result.IdentityFallback = fallback

	if !hasHead {
		if _, err := git(ctx, gitPath, root, "add", "--all"); err != nil {
			return result, fmt.Errorf("stage initial project baseline: %w", err)
		}
		if _, err := git(ctx, gitPath, root, "commit", "--quiet", "-m", "chore: establish lbai baseline"); err != nil {
			return result, fmt.Errorf("commit initial project baseline: %w", err)
		}
		result.CreatedBaseline, result.ConfigCommitted = true, result.CreatedConfig
	} else if result.CreatedConfig && cleanBeforeSetup {
		if _, err := git(ctx, gitPath, root, "add", "--", ".lbai/config.toml"); err != nil {
			return result, fmt.Errorf("stage detected LBAI configuration: %w", err)
		}
		if _, err := git(ctx, gitPath, root, "commit", "--quiet", "-m", "chore: configure less-bad-ai"); err != nil {
			return result, fmt.Errorf("commit detected LBAI configuration: %w", err)
		}
		result.ConfigCommitted = true
	}
	return result, nil
}

func Diagnose(ctx context.Context, opts Options) DoctorResult {
	dir, err := absoluteDirectory(opts.Dir)
	if err != nil {
		return DoctorResult{Root: opts.Dir, Checks: []Check{{Name: "directory", Status: Fail, Detail: err.Error()}}}
	}
	result := DoctorResult{Root: dir}
	lookPath := lookup(opts)
	gitPath, err := lookPath("git")
	if err != nil {
		result.Checks = append(result.Checks, Check{Name: "git", Status: Fail, Detail: "Git was not found", Hint: "Install Git, then run lbai setup."})
		return result
	}
	result.Checks = append(result.Checks, Check{Name: "git", Status: Pass, Detail: gitPath})

	root, repoExists := existingRoot(ctx, gitPath, dir)
	if repoExists {
		result.Root = root
		result.Checks = append(result.Checks, Check{Name: "repository", Status: Pass, Detail: root})
	} else {
		result.Checks = append(result.Checks, Check{Name: "repository", Status: Fail, Detail: "not a Git repository", Hint: "Run lbai setup."})
	}
	if repoExists && commandSucceeds(ctx, gitPath, root, "rev-parse", "--verify", "HEAD^{commit}") {
		result.Checks = append(result.Checks, Check{Name: "baseline", Status: Pass, Detail: "initial commit exists"})
	} else {
		result.Checks = append(result.Checks, Check{Name: "baseline", Status: Fail, Detail: "no initial commit", Hint: "Run lbai setup."})
	}

	cfg, explicit, cfgErr := loadOrDiscover(result.Root, opts)
	if cfgErr != nil {
		result.Checks = append(result.Checks, Check{Name: "agent", Status: Fail, Detail: cfgErr.Error(), Hint: "Install Codex, Claude, or Aider, or create .lbai/config.toml."})
	} else {
		detail := agentDescription(cfg.Worker)
		if cfg.Worker.Type == "command" {
			if !executableAvailable(cfg.Worker.Command[0], result.Root, lookPath) {
				result.Checks = append(result.Checks, Check{Name: "agent", Status: Fail, Detail: detail + " is not available"})
			} else {
				result.Checks = append(result.Checks, Check{Name: "agent", Status: Pass, Detail: detail})
			}
		} else {
			result.Checks = append(result.Checks, Check{Name: "agent", Status: Pass, Detail: detail})
			if cfg.Worker.APIKeyEnv != "" && os.Getenv(cfg.Worker.APIKeyEnv) == "" {
				result.Checks = append(result.Checks, Check{Name: "credentials", Status: Fail, Detail: cfg.Worker.APIKeyEnv + " is not set", Hint: "Set the configured API key environment variable."})
			}
		}
		if cfg.Reviewer.Type != "" {
			reviewerStatus := Pass
			reviewerHint := ""
			if cfg.Reviewer.Type == "command" && !executableAvailable(cfg.Reviewer.Command[0], result.Root, lookPath) {
				reviewerStatus, reviewerHint = Fail, "Install the configured reviewer or update .lbai/config.toml."
			}
			result.Checks = append(result.Checks, Check{Name: "reviewer", Status: reviewerStatus, Detail: agentDescription(cfg.Reviewer), Hint: reviewerHint})
			if cfg.Reviewer.Type == "openai" && cfg.Reviewer.APIKeyEnv != "" && os.Getenv(cfg.Reviewer.APIKeyEnv) == "" && cfg.Reviewer.APIKeyEnv != cfg.Worker.APIKeyEnv {
				result.Checks = append(result.Checks, Check{Name: "reviewer credentials", Status: Fail, Detail: cfg.Reviewer.APIKeyEnv + " is not set"})
			}
		}
		configMode := "automatic discovery"
		if explicit {
			configMode = filepath.Join(result.Root, ".lbai", "config.toml")
		}
		result.Checks = append(result.Checks, Check{Name: "configuration", Status: Pass, Detail: configMode})
		if len(cfg.Checks) == 0 {
			result.Checks = append(result.Checks, Check{Name: "verification", Status: Warn, Detail: "no project checks detected yet; they will be detected again after generation", Hint: "Optional: add [[checks]] to .lbai/config.toml to pin custom commands."})
		} else {
			missing := missingExecutables(cfg.Checks, result.Root, lookPath)
			if len(missing) > 0 {
				result.Checks = append(result.Checks, Check{Name: "verification", Status: Fail, Detail: "missing executables: " + strings.Join(missing, ", ")})
			} else {
				result.Checks = append(result.Checks, Check{Name: "verification", Status: Pass, Detail: strings.Join(checkNames(cfg), ", ")})
			}
		}
	}

	if repoExists {
		name := gitText(ctx, gitPath, root, "config", "--get", "user.name")
		email := gitText(ctx, gitPath, root, "config", "--get", "user.email")
		if name == "" || email == "" {
			result.Checks = append(result.Checks, Check{Name: "identity", Status: Warn, Detail: "Git author identity is incomplete", Hint: "Run lbai setup to add a repository-local fallback."})
		} else {
			result.Checks = append(result.Checks, Check{Name: "identity", Status: Pass, Detail: name + " <" + email + ">"})
		}
	}
	rules := filepath.Join(result.Root, ".lbai", "rules.toml")
	if _, err := linter.LoadConfig(result.Root); err != nil {
		result.Checks = append(result.Checks, Check{Name: "architecture", Status: Fail, Detail: err.Error(), Hint: "Correct .lbai/rules.toml or remove it to use automatic rules."})
	} else if info, err := os.Stat(rules); err == nil && info.Mode().IsRegular() {
		result.Checks = append(result.Checks, Check{Name: "architecture", Status: Pass, Detail: rules})
	} else {
		result.Checks = append(result.Checks, Check{Name: "architecture", Status: Pass, Detail: "automatic conservative rules"})
	}
	return result
}

func absoluteDirectory(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect project directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", abs)
	}
	return filepath.Clean(abs), nil
}

func lookup(opts Options) func(string) (string, error) {
	if opts.LookPath != nil {
		return opts.LookPath
	}
	return exec.LookPath
}

func loadOrDiscover(root string, opts Options) (runner.Config, bool, error) {
	path := filepath.Join(root, ".lbai", "config.toml")
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		cfg, err := runner.LoadConfig(root)
		return cfg, true, err
	}
	discover := opts.Discover
	if discover == nil {
		discover = runner.DiscoverConfig
	}
	cfg, err := discover(root)
	return cfg, false, err
}

func existingRoot(ctx context.Context, gitPath, dir string) (string, bool) {
	out, err := git(ctx, gitPath, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root, err := filepath.Abs(strings.TrimSpace(string(out)))
	return filepath.Clean(root), err == nil
}

func ensureIdentity(ctx context.Context, gitPath, root, name, email string) (bool, error) {
	fallback := false
	if name == "" {
		name = gitText(ctx, gitPath, root, "config", "--get", "user.name")
	}
	if email == "" {
		email = gitText(ctx, gitPath, root, "config", "--get", "user.email")
	}
	if name == "" {
		name, fallback = "Less Bad AI", true
	}
	if email == "" {
		email, fallback = "lbai@localhost", true
	}
	for _, setting := range []struct{ key, value string }{{key: "user.name", value: name}, {key: "user.email", value: email}} {
		if _, err := git(ctx, gitPath, root, "config", "--local", setting.key, setting.value); err != nil {
			return fallback, fmt.Errorf("configure repository Git identity: %w", err)
		}
	}
	return fallback, nil
}

func untrackedPaths(ctx context.Context, gitPath, root string) ([]string, error) {
	out, err := git(ctx, gitPath, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect initial project files: %w", err)
	}
	var paths []string
	for _, path := range strings.Split(string(out), "\x00") {
		if path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	return paths, nil
}

func sensitivePaths(paths []string) []string {
	var found []string
	for _, path := range paths {
		base := strings.ToLower(filepath.Base(path))
		ext := strings.ToLower(filepath.Ext(base))
		environment := base == ".env" || strings.HasPrefix(base, ".env.")
		if strings.HasSuffix(base, ".example") || strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".template") {
			environment = false
		}
		if environment || ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" || base == "id_rsa" || base == "id_ed25519" || base == "credentials.json" || base == "secrets.json" || base == ".npmrc" || base == ".pypirc" {
			found = append(found, path)
		}
	}
	sort.Strings(found)
	return found
}

func agentDescription(cfg runner.AgentConfig) string {
	if cfg.Type == "command" && len(cfg.Command) > 0 {
		return strings.Join(cfg.Command, " ")
	}
	if cfg.Type == "openai" {
		return "OpenAI-compatible " + cfg.Model + " at " + cfg.Endpoint
	}
	return cfg.Type
}

func checkNames(cfg runner.Config) []string {
	names := make([]string, 0, len(cfg.Checks)+1)
	for _, check := range cfg.Checks {
		names = append(names, check.Name)
	}
	names = append(names, "architecture")
	return names
}

func missingExecutables(checks []runner.CheckConfig, root string, lookPath func(string) (string, error)) []string {
	set := map[string]bool{}
	for _, check := range checks {
		if !executableAvailable(check.Executable, root, lookPath) {
			set[check.Executable] = true
		}
	}
	var names []string
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func executableAvailable(name, root string, lookPath func(string) (string, error)) bool {
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(path))
		}
		info, err := os.Stat(filepath.Clean(path))
		return err == nil && !info.IsDir()
	}
	_, err := lookPath(name)
	return err == nil
}

func validateRuntime(cfg runner.Config, root string, lookPath func(string) (string, error)) error {
	for _, configured := range []struct {
		role  string
		agent runner.AgentConfig
	}{{role: "worker", agent: cfg.Worker}, {role: "reviewer", agent: cfg.Reviewer}} {
		role, agent := configured.role, configured.agent
		if agent.Type == "" {
			continue
		}
		if agent.Type == "command" && !executableAvailable(agent.Command[0], root, lookPath) {
			return fmt.Errorf("configured %s command %q is not available", role, agent.Command[0])
		}
		if agent.Type == "openai" && agent.APIKeyEnv != "" && os.Getenv(agent.APIKeyEnv) == "" {
			return fmt.Errorf("configured %s credential environment variable %s is not set", role, agent.APIKeyEnv)
		}
	}
	if missing := missingExecutables(cfg.Checks, root, lookPath); len(missing) > 0 {
		return fmt.Errorf("configured verification executables are not available: %s", strings.Join(missing, ", "))
	}
	return nil
}

func git(ctx context.Context, gitPath, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, gitPath, append([]string{"-C", root}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func gitText(ctx context.Context, gitPath, root string, args ...string) string {
	out, err := git(ctx, gitPath, root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func commandSucceeds(ctx context.Context, gitPath, root string, args ...string) bool {
	_, err := git(ctx, gitPath, root, args...)
	return err == nil
}
