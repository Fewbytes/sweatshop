// Package linelog redacts secrets from line component logs.
//
// The redacted lines go to the station API, where the viceroy reads them with
// `sweatshop line logs`.
package linelog

import (
	"io"
	"regexp"
	"strings"
)

// Placeholder replaces every secret Redact finds.
const Placeholder = "[REDACTED]"

// secretPatterns are shapes of credentials that are recognisable without
// knowing their value. The value of a known token is better covered by
// passing it to Redact.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)((?:token|secret|password|passwd|api[_-]?key|authorization)["']?\s*[=:]\s*["']?(?:(?:bearer|basic)\s+)?)[^\s"',;]+`),
	regexp.MustCompile(`()\b(?:sk-ant-|sk-|ghp_|gho_|ghs_|github_pat_|xox[abprs]-|AKIA)[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`()-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
}

// Redact returns s with every occurrence of a secret value, and every
// credential-shaped string, replaced by Placeholder. Empty secrets are
// ignored. It is the backstop: components must not log secrets in the first
// place.
func Redact(s string, secrets ...string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, Placeholder)
		}
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "${1}"+Placeholder)
	}
	return s
}

// Tee is an io.Writer for a log.Logger. Each Write is one log entry (the
// log package's contract): Tee writes it, redacted, to Out and hands the same
// redacted text to Record.
type Tee struct {
	Out     io.Writer
	Record  func(entry string)
	Secrets []string
}

// Write redacts p, forwards it to Out and records it. It reports len(p) on
// success so log.Logger does not treat redaction as a short write.
func (t Tee) Write(p []byte) (int, error) {
	entry := Redact(string(p), t.Secrets...)
	if _, err := io.WriteString(t.Out, entry); err != nil {
		return 0, err
	}
	t.Record(strings.TrimRight(entry, "\n"))
	return len(p), nil
}
