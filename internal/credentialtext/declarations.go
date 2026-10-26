package credentialtext

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Values preserve quoting and punctuation outside a declaration. Capture one
// contains the value; all other groups are non-capturing for YAML parity.
const Value = `(?:\$\{[^}\r\n]*\}|"(?:\\.|[^"\\])*"|'[^']*'|` + "`[^`]*`" + `|“[^”]*”|‘[^’]*’|(?:\{SENSITIVE:[a-z_]+:[0-9]+\}|[^\s"'` + "`" + `“”‘’,;，；。！？<>{}=\]|][^\s"'` + "`" + `“”‘’,;，；。！？<>{}\]|]*)+)`
const SecretLabel = `(?:password|passwd|pwd|secret|token|api_key|apikey|access_key|authorization|密码|口令)`
const AccountLabel = `(?:account|user(?:name|_name)?|[a-z0-9_]*_user|login|帐号|账号|账户|用户名)`

// declarationSeparator recognizes the shapes that separate a credential label
// from its value.
//
// r1039（2026-10-20）：末尾新增 `|\s+`（**纯空格**）这一支。
//
// 为什么需要它：「密码 hunter2secret」「password hunter2secret」「口令 …」
// 这类**空格分隔**的声明此前完全不被识别（分隔符只认 `[:=：]`、中文动词
// 改成/改为/设置为/设为/更新为/是/为，以及英文 `\s+is\s+` /
// `\s+(set|change|update)d?\s+to`）⇒ 明文凭据直接出网关，既不脱敏也不进台账。
//
// 为什么**不能**把空格直接当分隔符（r1034 钉住的边界）：
// `security/sanitize/r1034_human_credentials_test.go:83` 明确把
// `password policy requires 12 characters` 钉成**不变**。若分隔符直接吃
// 空格，这句散文会被解析成「password = policy」⇒ 误伤。
//
// ⇒ 空格这一支**只产生候选**，由 credibleCredentialValue 做二次判定：值必须
// **像凭据**（足够长、字母数字混排、不含空白）才成立。散文词
// （policy / requires / characters）过不了这道门。
//
// ★ 这条分隔符是**输入面专属**的：security/sanitize 的检测器在
// ScopeInputDeclarations 之外（输出面 / 外传守卫）改用
// StructuredPasswordPattern / StructuredAccountPattern（只认 `[:=：]`），
// 所以「自然语言启发式」不会外溢到输出侧的工具参数阻断。
const declarationSeparator = `["']?(?:\*{1,2}|` + "`" + `)?\s*(?:[:=：]\s*|(?:改成|改为|设置为|设为|更新为|是|为)\s*|\s+(?:is|(?:set|changed?|updated?)\s+to)\s+|\s+)`
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

// plainSpaceLabelSeparator matches a declaration prefix that is **exactly** a
// credential/account label followed by whitespace — i.e. the
// "标签 + 纯空格 + 值" form that r1039 added to declarationSeparator.
//
// 「恰好」这三个字是判据的关键：成对声明（CredentialPairPattern）的
// 前缀长这样 —— `admin account to `、`username/密码：` —— 它们**不是**
// 「一个标签 + 空白」，所以这条正则**不匹配**它们 ⇒ 成对声明的既有语义
// （`admin/R7#kQ92!` 这种裸形态不算声明）完全不受影响。
var plainSpaceLabelSeparator = regexp.MustCompile(`(?i)^(?:` + SecretLabel + `|` + AccountLabel + `)["']?(?:\*{1,2}|` + "`" + `)?\s+$`)

// minCredibleCredentialLen is the length floor for the space-separated
// credential form.
//
// 取 8 的理由：「password policy requires 12 characters」里紧跟标签的词是
// `policy`（6 字符，纯字母）—— 门槛必须 **> 6** 才能把它挡在外面，而
// 真实口令（`hunter2secret` 12 位、`admin2024` 9 位）都在 8 以上。
const minCredibleCredentialLen = 8

// credibleCredentialValue reports whether a value found after a **plain space**
// separator looks like an actual credential rather than an English word or a
// prose fragment.
//
// 这是 r1039 给「纯空格分隔」这一支加的**二次判定**（judgement），也是
// 不把 `password policy requires 12 characters` 误伤成声明的唯一原因。
//
// 三条判据（缺一不可）：
//  1. 长度 ≥ minCredibleCredentialLen（按 rune 计数，中文口令不被字节数误杀）；
//  2. 至少含一个 ASCII 字母 —— 纯数字串（`20240101`）不是凭据；
//  3. 至少含一个 ASCII 数字 —— 纯字母串（`policy` / `requires` /
//     `characters` / `docker-base-images`）不是凭据。
//
// 另外：值里含空白 ⇒ 不是凭据（`two words#` 这类短语），带引号的散文同理。
//
// ★ 代价（明确写下，便于日后回看）：长度 < 8 的短口令（如 `abc123`）在
// **纯空格**形态下不再被识别；`:` / `=` / 中文动词 / `is` 这些分隔符形态
// 不受影响，仍照旧识别。
func credibleCredentialValue(value string) bool {
	if value == "" || utf8.RuneCountInString(value) < minCredibleCredentialLen {
		return false
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return false
	}
	var hasDigit, hasLetter bool
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}

// questionLead limits prose exemptions to unquoted natural-language declarations.
var questionLead = regexp.MustCompile(`(?i)^(?:` + SecretLabel + `|` + AccountLabel + `)["']?(?:\*{1,2}|` + "`" + `)?\s*(?:是|为|\s+is\s+)\s*$`)

// NonLiteralDeclaration recognizes references and questions while retaining
// quote semantics: shell single quotes and quoted human input denote literals.
func NonLiteralDeclaration(prefix, raw, value, suffix string, shell bool) bool {
	// r1039（2026-10-20）：「标签 + 纯空格 + 值」这一形态**只是候选**。
	//
	// 分隔符新增纯空格这一支之后，`password policy requires 12 characters`
	// 这类散文也会被解析成「password = policy」。r1034 的判据把它钉成
	// **不变**，所以这里做二次判定：值不像凭据 ⇒ 判为散文 ⇒ 整条声明作废
	// （豁免），文本原样通过。
	//
	// ★ 判据放在最前面，因为它**只否决**、不豁免得更少：后面的分支
	// （变量引用 / 疑问句 / 必填项）只会给出更多豁免，两者的合集不变。
	//
	// ★ 只作用于「恰好是标签 + 空白」的前缀 ⇒ 成对声明（`admin account to `
	// / `username/密码：`）与 `:`/`=`/中文动词形态**完全不受影响**。
	if plainSpaceLabelSeparator.MatchString(prefix) && !credibleCredentialValue(value) {
		return true
	}
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
