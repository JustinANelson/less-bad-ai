package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Formatter best-effort auto-formats changed files in place and returns a
// human-readable summary. It never fails the transaction: a missing tool is
// silently skipped, and a tool's own failure is folded into the summary as a
// warning rather than returned as an error, since verification (build/test)
// remains the authoritative correctness gate.
type Formatter interface {
	Format(ctx context.Context, files []string) (string, error)
}

// AutoFormat runs a zero-configuration formatter per file extension when the
// corresponding tool is available, mirroring the "only if installed" pattern
// already used for check discovery in config.go. Java/Kotlin are
// intentionally skipped: there is no single ubiquitous zero-install
// formatter for them the way gofmt ships with Go.
type AutoFormat struct{ Root string }

type formatTool struct {
	extensions []string
	locate     func(root string) (executable string, leadingArgs []string, found bool)
}

func (f AutoFormat) Format(ctx context.Context, files []string) (string, error) {
	groups := map[string][]string{}
	for _, file := range files {
		if info, err := os.Stat(filepath.Join(f.Root, filepath.FromSlash(file))); err != nil || !info.Mode().IsRegular() {
			continue // skip deleted/non-regular paths from the changed-file list
		}
		ext := strings.ToLower(filepath.Ext(file))
		groups[ext] = append(groups[ext], file)
	}
	var summary []string
	for _, tool := range formatTools() {
		var matched []string
		for _, ext := range tool.extensions {
			matched = append(matched, groups[ext]...)
		}
		if len(matched) == 0 {
			continue
		}
		executable, leadingArgs, found := tool.locate(f.Root)
		if !found {
			continue
		}
		sort.Strings(matched)
		args := append(append([]string(nil), leadingArgs...), matched...)
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir = f.Root
		out, err := cmd.CombinedOutput()
		name := filepath.Base(executable)
		if err != nil {
			summary = append(summary, fmt.Sprintf("%s: failed on %d file(s): %v\n%s", name, len(matched), err, truncate(string(out), 2048)))
			continue
		}
		summary = append(summary, fmt.Sprintf("%s: processed %d file(s)", name, len(matched)))
	}
	return strings.Join(summary, "\n"), nil
}

func formatTools() []formatTool {
	return []formatTool{
		{extensions: []string{".go"}, locate: func(string) (string, []string, bool) {
			path, err := exec.LookPath("gofmt")
			return path, []string{"-l", "-w"}, err == nil
		}},
		{extensions: []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"}, locate: locatePrettier},
		{extensions: []string{".py"}, locate: locatePython},
		{extensions: []string{".rs"}, locate: func(string) (string, []string, bool) {
			path, err := exec.LookPath("rustfmt")
			return path, nil, err == nil
		}},
	}
}

func locatePrettier(root string) (string, []string, bool) {
	local := filepath.Join(root, "node_modules", ".bin", prettierBinaryName())
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, []string{"--write"}, true
	}
	if path, err := exec.LookPath("prettier"); err == nil {
		return path, []string{"--write"}, true
	}
	return "", nil, false
}

func prettierBinaryName() string {
	if runtime.GOOS == "windows" {
		return "prettier.cmd"
	}
	return "prettier"
}

func locatePython(string) (string, []string, bool) {
	if path, err := exec.LookPath("ruff"); err == nil {
		return path, []string{"format"}, true
	}
	if path, err := exec.LookPath("black"); err == nil {
		return path, nil, true
	}
	return "", nil, false
}
