package sanitize

import (
	"github.com/kaixuan/llm-gateway-go/internal/credentialtext"
	"regexp"
)

// Value capture keeps credential labels and tool JSON syntax intact. Literal
// reserved markers are excluded by PatternDetector before becoming fragments.
//
// r1028（2026-10-14）：未加引号的值首字符不得为 `=`。原字符类放行纯 `=` 串，
// `echo '=== pwd ==='`（bash 分节装饰）于是被解析成「pwd = 赋值 ==」——生产
// 154 实测把一条 1279 字节的纯 git 检查命令整流 output_policy_blocked
// （request 5d9e1267，raw_data 帧重建取证），同日 03:44 另一会话同型误伤；
// 红act 侧同一规则会把正文里的分节标题遮成 [REDACTED:secret]。真实口令不会
// 以未加引号的 `=` 开头；带引号的值（含 "="）走引号分支不受影响。
const credentialValuePattern = credentialtext.PasswordPattern
const accountValuePattern = credentialtext.AccountPattern

var structuredPassword = regexp.MustCompile(credentialtext.StructuredPasswordPattern)
var structuredAccount = regexp.MustCompile(credentialtext.StructuredAccountPattern)
var credentialPair = regexp.MustCompile(credentialtext.CredentialPairPattern)

func operationalPatternEntries() []patternEntry {
	return []patternEntry{
		{sType: TypeAccount, regex: regexp.MustCompile(credentialtext.CredentialPairPattern), group: 1, kind: "credential_pair"},
		{sType: TypeSecret, regex: regexp.MustCompile(credentialtext.CredentialPairPattern), group: 2, kind: "credential_pair"},
		{sType: TypeSecret, regex: regexp.MustCompile(credentialValuePattern), group: 1, kind: "credential_declaration"},
		{sType: TypeAccount, regex: regexp.MustCompile(accountValuePattern), group: 1, kind: "account_declaration"},
		{sType: TypeSecret, regex: regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]+)`), group: 1},
		{sType: TypeSecret, regex: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----.*?(?:-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|$)`)},
		{sType: TypeSecret, regex: regexp.MustCompile(`\b(?:postgres(?:ql)?|mysql|redis|mongodb(?:\+srv)?|https?)://([^\s/@]+:[^\s/@]+)@`), group: 1},
		{sType: TypeServerIP, regex: regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)},
		{sType: TypeServerIP, regex: regexp.MustCompile(`(?:[0-9A-Fa-f]{0,4}:){2,}[0-9A-Fa-f:.]*(?:%[A-Za-z0-9_.-]+)?`)},
	}
}
