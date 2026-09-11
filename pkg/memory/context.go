package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// maxContextChars bounds LoadContext's output so a long-lived project's
// accumulated memory cannot blow out an agent's prompt budget.
const maxContextChars = 4000

// LoadContext returns a bounded summary of durable project memory suitable
// for prepending to an agent prompt, so later runs stay consistent with
// decisions and context recorded by earlier ones. It returns "" (no error)
// when no memory has been recorded yet, which is the expected state on a
// project's first run.
func LoadContext(root string) (string, error) {
	var sections []string
	if body, err := sectionFromFile(root, "ARCHITECTURE.md", "Recent Decisions"); err != nil {
		return "", err
	} else if body != "" {
		sections = append(sections, "## Recent project decisions\n\n"+body)
	}
	if body, err := sectionFromFile(root, "AI_CONTEXT.md", "Latest Verified Change"); err != nil {
		return "", err
	} else if body != "" {
		sections = append(sections, "## Last verified change\n\n"+body)
	}
	if len(sections) == 0 {
		return "", nil
	}
	return truncate(strings.Join(sections, "\n\n"), maxContextChars), nil
}

func sectionFromFile(root, name, title string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return markdownSectionBody(string(b), title), nil
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:n])) + "..."
}
