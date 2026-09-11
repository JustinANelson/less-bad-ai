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
// decisions recorded by earlier ones. It returns "" (no error) when no
// memory has been recorded yet, which is the expected state on a project's
// first run.
//
// This deliberately reads only ARCHITECTURE.md's "Recent Decisions" section,
// not AI_CONTEXT.md's "Latest Verified Change". The latter holds the raw,
// uncurated worker summary (command-agent stdout can include tool-call
// transcripts, shell output, and even embedded error text), which is prompt
// noise rather than project memory and was observed in practice to confuse
// a subsequent run into misreading it as part of the task. Recent Decisions
// is short, structured, and synthesized by lbai itself, which is what makes
// it safe to feed back into an agent prompt.
func LoadContext(root string) (string, error) {
	body, err := sectionFromFile(root, "ARCHITECTURE.md", "Recent Decisions")
	if err != nil {
		return "", err
	}
	if body == "" {
		return "", nil
	}
	return truncate("## Recent project decisions\n\n"+body, maxContextChars), nil
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
