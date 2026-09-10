package topology

import (
	"regexp"
	"strings"
)

const (
	maxPromptDisplay  = 280
	maxSummaryDisplay = 1000
)

var displayRedactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`), `[REDACTED PRIVATE KEY]`},
	{regexp.MustCompile(`(?i)(authorization\s*:\s*)[^\r\n]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`), `Bearer [REDACTED]`},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|secret)(\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`), `${1}${2}[REDACTED]`},
	{regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9]{8,}|github_pat_[a-z0-9_]{8,}|AKIA[0-9A-Z]{16})\b`), `[REDACTED]`},
}

func sanitizeDisplayText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	for _, redaction := range displayRedactions {
		value = redaction.pattern.ReplaceAllString(value, redaction.replacement)
	}
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-3])) + "..."
}
