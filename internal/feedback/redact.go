package feedback

import "regexp"

// What a secret becomes in a record a session wrote
const redacted = "[redacted]"

// Where a secret may start
// The start of the text or a character outside a word or a JSON escape such as the n of a newline
const start = `(^|[^A-Za-z0-9_]|\\[nrtbf])`

// Secrets a model may copy from a transcript into a session record
// 1. the first group is kept before the secret and the second after it
// 2. no secret matches a quote or a backslash so redacting JSON text keeps it valid
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`()-----BEGIN [A-Z ]*PRIVATE KEY-----[^"\\]*?-----END [A-Z ]*PRIVATE KEY-----()`),
	regexp.MustCompile(start + `(?:AKIA|ASIA)[0-9A-Z]{16}()`),
	regexp.MustCompile(start + `gh[pousr]_[A-Za-z0-9]{36,}()`),
	regexp.MustCompile(start + `github_pat_[A-Za-z0-9_]{22,}()`),
	regexp.MustCompile(start + `xox[abprs]-[A-Za-z0-9-]{10,}()`),
	regexp.MustCompile(start + `AIza[0-9A-Za-z_-]{35}()`),
	regexp.MustCompile(start + `sk-[A-Za-z0-9_-]{20,}()`),
	regexp.MustCompile(start + `eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}()`),
	regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/-]{8,}=*()`),
	regexp.MustCompile(`(://[^/\s:@"'\\]+:)[^/\s@"'\\]+(@)`),
	regexp.MustCompile(`(?i)(\b(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)\s*[:=]\s*)[^\s"'\\,;]+()`),
}

// The text with every secret replaced the same way each time
func redact(s string) string {
	for _, re := range secrets {
		s = re.ReplaceAllString(s, "${1}"+redacted+"${2}")
	}
	return s
}
