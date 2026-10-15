package credentialtext

import (
	"regexp"
	"strings"
)

// Values preserve quoting and punctuation outside a declaration. Capture one
// contains the value; all other groups are non-capturing for YAML parity.
const Value = `(?:\$\{[^}\r\n]*\}|"(?:\\.|[^"\\])*"|'[^']*'|` + "`[^`]*`" + `|“[^”]*”|‘[^’]*’|(?:\{SENSITIVE:[a-z_]+:[0-9]+\}|[^\s"'` + "`" + `“”‘’,;，；。！？<>{}=\]|][^\s"'` + "`" + `“”‘’,;，；。！？<>{}\]|]*)+)`
const SecretLabel = `(?:password|passwd|pwd|secret|token|api_key|apikey|access_key|authorization|密码|口令)`
const AccountLabel = `(?:account|user(?:name|_name)?|[a-z0-9_]*_user|login|帐号|账号|账户|用户名)`
const declarationSeparator = `["']?(?:\*{1,2}|` + "`" + `)?\s*(?:[:=：]\s*|(?:改成|改为|设置为|设为|更新为|是|为)\s*|\s+(?:is|(?:set|changed?|updated?)\s+to)\s+)`
const PasswordPattern = `(?i)` + SecretLabel + declarationSeparator + `(` + Value + `)`
const StructuredPasswordPattern = `(?i)` + SecretLabel + `["']?(?:\*{1,2}|` + "`" + `)?\s*[:=：]\s*(` + Value + `)`
const StructuredAccountPattern = `(?i)` + AccountLabel + `["']?(?:\*{1,2}|` + "`" + `)?\s*[:=：]\s*(` + Value + `)`
const AccountPattern = `(?i)` + AccountLabel + declarationSeparator + `(` + Value + `)`

const pairContext = `(?:(?:管理员)?(?:帐号|账号|账户)(?:\s*[/／和及、]\s*(?:密码|口令))?|(?:用户名|username|user)\s*[/／]\s*(?:密码|password)|admin(?:istrator)?\s+(?:account|credentials))`
const pairSeparator = `\s*(?:改成|改为|设置为|设为|更新为|是|为|[:=：]|(?:set|changed?|updated?)\s+to|to|is)\s*`
const pairAccount = `(?:"[^"\r\n]+"|'[^'\r\n]+'|` + "`[^`]+`" + `|“[^”\r\n]+”|‘[^’\r\n]+’|[^\s/／:"'` + "`" + `“”‘’,;，；。！？<>{}=\]|]+)`

// CredentialPairPattern requires an explicit account declaration. Bare paths
// and arbitrary user/password-looking strings do not constitute declarations.
// Captures one and two are the account and secret respectively.
const CredentialPairPattern = `(?i)` + pairContext + pairSeparator + `(` + pairAccount + `)\s*[/／]\s*(` + Value + `)`

var variableReference = regexp.MustCompile(`^\$(?:[a-zA-Z_][a-zA-Z0-9_]*|\{[a-zA-Z_][a-zA-Z0-9_]*\})$`)

// questionLead limits prose exemptions to unquoted natural-language declarations.
var questionLead = regexp.MustCompile(`(?i)^(?:` + SecretLabel + `|` + AccountLabel + `)["']?(?:\*{1,2}|` + "`" + `)?\s*(?:是|为|\s+is\s+)\s*$`)

// NonLiteralDeclaration recognizes references and questions while retaining
// quote semantics: shell single quotes and quoted human input denote literals.
func NonLiteralDeclaration(prefix, raw, value, suffix string, shell bool) bool {
	quoted := raw != value
	if variableReference.MatchString(value) && (!quoted || shell && strings.HasPrefix(raw, `"`)) {
		return true
	}
	if quoted || !questionLead.MatchString(prefix) {
		return false
	}
	suffix = strings.TrimSpace(suffix)
	question := strings.HasPrefix(suffix, "?") || strings.HasPrefix(suffix, "？") || strings.HasSuffix(value, "?") || strings.HasSuffix(value, "？")
	if question {
		switch strings.ToLower(strings.TrimRight(value, "?？")) {
		case "什么", "什么呢", "多少", "多少呢", "what", "which", "how":
			return true
		}
	}
	// A field requirement followed by an instruction to fill it is schema prose.
	return value == "必填项" && strings.HasPrefix(suffix, "，请填写")
}
