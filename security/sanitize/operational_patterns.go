package sanitize

import "regexp"

// Value capture keeps credential labels and tool JSON syntax intact. Literal
// reserved markers are excluded by PatternDetector before becoming fragments.
const credentialValuePattern = `(?i)(?:password|passwd|pwd|secret|token|api_key|apikey|access_key|authorization|密码|口令)["']?\s*[:=：]\s*("(?:\\.|[^"\\])*"|'[^']*'|(?:\{SENSITIVE:[a-z_]+:[0-9]+\}|[^\s"',;，；<>{}\]]+)+)`
const accountValuePattern = `(?i)(?:account|username|user_name|login|帐号|账号|账户|用户名)["']?\s*[:=：]\s*("(?:\\.|[^"\\])*"|'[^']*'|(?:\{SENSITIVE:[a-z_]+:[0-9]+\}|[^\s"',;，；<>{}\]]+)+)`

func operationalPatternEntries() []patternEntry {
	return []patternEntry{
		{sType: TypeSecret, regex: regexp.MustCompile(credentialValuePattern), group: 1},
		{sType: TypeAccount, regex: regexp.MustCompile(accountValuePattern), group: 1},
		{sType: TypeSecret, regex: regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]+)`), group: 1},
		{sType: TypeSecret, regex: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----.*?(?:-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|$)`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`\b(?:postgres(?:ql)?|mysql|redis|mongodb(?:\+srv)?|https?)://([^\s/@]+:[^\s/@]+)@`), group: 1},
		{sType: TypeServerIP, regex: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)},
		{sType: TypeServerIP, regex: regexp.MustCompile(`(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f:.]*(?:%[A-Za-z0-9_.-]+)?`)},
	}
}
