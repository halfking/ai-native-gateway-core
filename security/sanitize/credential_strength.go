package sanitize

import (
	"strings"
)

// ★ D1（2026-11-19 审计轮）签名 thinking 的**分级阻断**判据。
//
// 背景：r1058 契约是「签名 thinking 只检测、不改写、原样放行」。签名覆盖
// thinking 字节 ⇒ 改写在密码学上不可能 ⇒ 能用的补偿控制只剩**拒**。
// 但「一律拒」会把「一次误报」升级成「整个会话砖化」——r1058 事故本体
// 就是这么发生的（3049 条生产会话当场砖化，检出值＝英文单词 "token"，
// 客户端不可自愈：重发的还是同一份会话）。
//
// 因此这里做**分级**：
//
//	· **强凭据形态**（厂商密钥前缀 / JWT / 长不透明令牌）⇒ 拒整请求（400）。
//	  这类内容一旦出网到「不验签的上游」（自建 vLLM / Ollama / 内部代理），
//	  就是明文泄露 —— 而 `signature` 字段完全由客户端控制，客户端可以
//	  伪造带签名的 thinking 块来绕过脱敏。
//	· **散文级弱命中**（`password=MySecretPass123` 这类）⇒ 仍原样放行，
//	  但日志从 WARN 升级为 ERROR 并带规则名 —— 让「被放行的敏感内容」在
//	  观测面上可见，而不是静默 WARN。
//
// ★ 判据刻意取**窄**：宁可漏过「无厂商前缀的裸长串」，也不误伤模型散文。
//   误伤的代价（会话砖化）远高于漏过一次的代价（一次未脱敏的出网）。
//   残余风险登记见 docs/12小时内修订审计-2026-11-19-0400.md §二 D1。

// strongCredentialPrefixes 是厂商密钥/令牌的高特异性前缀。
//
// 这些形态在自然语言散文里几乎不出现（"sk-" / "ghp_" / "xoxb-" 之类），
// 因此命中即可判定为「真凭据」，不需要再叠加长度/熵条件。
var strongCredentialPrefixes = []string{
	// Anthropic / OpenAI
	"sk-ant-", "sk-proj-", "sk-or-v1-",
	// 阿里云
	"ak-",
	// GitHub
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
	// GitLab
	"glpat-",
	// Slack
	"xoxb-", "xoxp-", "xoxa-", "xoxr-",
	// Google
	"AIza",
	// PyPI / HuggingFace
	"npm_", "hf_",
	// AWS
	"AKIA", "ASIA",
}

// strongCredentialCandidate 判断一个命中值是否属于「必须脱敏否则即泄露」的
// 强凭据形态。
//
// 判据族与 detector.go 的 credibleBearerCandidate / credibleSecretCandidate
// 同源（都是「整串属性」判据，RE2 无法用前瞻断言表达），但**更窄**——
// 因为这里的结果会决定「拒整个请求」，误判成本是会话砖化。
//
// 作用域（r1059）：只对 secret（口令/密钥/令牌/JWT）调用；account/email 等
// PII 类型由 classifySignedThinkingFragments 在入口处排除——形状判据
// （长度/含数字/点分）本为不透明令牌设计，套在结构化 PII 上必然误伤
// （长用户名、多点邮箱、18 位身份证、16-19 位银行卡）。
func strongCredentialCandidate(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	// ① 高特异性厂商前缀。
	for _, p := range strongCredentialPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	// ② 裸 "sk-"（OpenAI 旧格式）：叠加整体属性，避免误伤 "sk-test" 一类。
	if strings.HasPrefix(v, "sk-") && len(v) >= 16 && containsASCIIDigit(v) {
		return true
	}
	// ③ JWT 形态（三段点分、≥16 字符）。
	//
	//   r1060（2026-11-26）：此前的「≥16 字符 + ≥2 个点」过宽，导致
	//   ZCode 会话砖化（request 9a4de025，types=secret，rules=secret.password）。
	//   该判据会误伤「this.is.a.sentence」/ 「config.database.host」等散文。
	//   真实 JWT 特征：恰好 2 个点（三段）、每段是 base64url、总长 ≥100。
	//   收紧为：恰好 2 个点 + base64ish 字符 + ≥50 字符（允许短 JWT，但
	//   仍高于散文阈值）。
	if strings.Count(v, ".") == 2 && len(v) >= 50 && isBase64ishToken(v) {
		return true
	}
	// ④ 长不透明令牌（≥24 且含数字）——不透明 bearer 令牌的典型长度。
	//
	//   阈值取 24 而不是 16：16 档会误伤「password=<长英文词+数字>」一类
	//   散文。且必须排除「十六进制簇」形态 —— 模型的思考里出现 commit
	//   hash / UUID 是常事，那些不是凭据，误判即砖化。
	//
	//   r1060（2026-11-26）：24 字符仍不够保守，「password for user123 account」
	//   （28 字符）这类散文会被误判。提升到 32 字符 + 要求更高熵度（同时
	//   含大小写字母），降低散文误判率。
	if len(v) >= 32 && containsASCIIDigit(v) && !hexishToken(v) && hasMixedCase(v) {
		return true
	}
	return false
}

// classifySignedThinkingFragments 把签名 thinking 的检出片段分成
// 「强凭据」（必须拒）与「散文级弱命中」（放行但记 ERROR）。
//
// r1059（2026-11-25，211 ZCode 会话砖化轮，request 180d27c2…，8.9MB
// /v1/messages → 400 "Invalid request body"；同一会话 16:07/16:20/16:31/16:37
// 四连挡）：强拒闸此前对**所有检出类型**跑「不透明令牌」形状判据
// （≥24 且含数字 / JWT 三段点），于是——
//
//	· account：长用户名 / 带点账号名被判成强凭据（生产实证 types=account）；
//	· email：多点分邮箱被判成 JWT 形态（钉测复现 types=email）；
//	· id_card / credit_card / passport：18 位身份证、16-19 位银行卡本就含
//	  数字，凡长度过线即「强凭据」。
//
// 每一种都是同一颗雷：拒整请求 ⇒ 客户端重发同一会话 ⇒ 砖化。逐一按类型
// 豁免是打地鼠（account 豁了 email 顶上），所以作用域**反向收紧**：
//
//	· **只有 secret（口令/密钥/令牌/JWT）参与强拒**——强拒的正当性是
//	  「凭据外泄」，签名可伪造 ⇒ 对不验签上游等于明文出网，这条风险只对
//	  凭据成立；
//	· 其余类型（account/email/phone/id_card/credit_card/name/custom…）
//	  一律降 weak：签名 thinking 携带 PII 出网是「脱敏旁路」（ERROR 观测
//	  可见），不是凭据泄露；形状判据本为不透明令牌设计，套在结构化 PII
//	  上必然误伤（r1024「身份不是机密」、r1043 D3 同一判据族的延伸）。
//
// secret 面的形状判据（strongCredentialCandidate）不动：sk-/ghp_/AKIA 等
// 厂商前缀照旧强拒，散文级弱值照旧放行（r1058/D1 契约不变）。
func classifySignedThinkingFragments(fragments []SensitiveFragment) (strong []SensitiveFragment, weak []SensitiveFragment) {
	for _, f := range fragments {
		if f.Type != TypeSecret {
			// 非凭据类永不触发整请求拒绝。仍进 weak 面记 ERROR 观测，
			// 保持「被放行的敏感内容在观测面上可见」。
			weak = append(weak, f)
			continue
		}
		if strongCredentialCandidate(f.Value) {
			strong = append(strong, f)
			continue
		}
		weak = append(weak, f)
	}
	return strong, weak
}

// containsASCIIDigit 报告字符串里是否含 ASCII 数字。
func containsASCIIDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

// hexishToken 判断字符串是否为「十六进制簇」形态：纯 hex，或 UUID 形态。
//
// 模型的思考里出现 commit hash（40 位 hex）或 UUID 是常事，它们长度够、
// 常带数字，却**不是凭据** —— 把它们判成强凭据等于批量砖化会话。
func hexishToken(s string) bool {
	isHex := func(c byte) bool {
		return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
	}
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) && s[i] != '-' {
			return false
		}
	}
	return true
}

// isBase64ishToken 判断字符串是否主要由 base64url 字符集构成。
//
// r1060（2026-11-26）：用于 JWT 判据收紧，避免把「this.is.a.sentence」
// 这类散文误判为 JWT。真实 JWT 的三个段都是 base64url 编码（字母+数字+
// 下划线+连字符），不含空格、逗号等散文常见字符。
//
// 判据：≥80% 字符是 base64url 字符集（A-Za-z0-9_-），允许少量分隔符（点）。
func isBase64ishToken(s string) bool {
	if len(s) == 0 {
		return false
	}
	base64Count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			base64Count++
		} else if c == '.' {
			// 点是 JWT 分隔符，不计入 base64 字符但允许
			continue
		} else {
			// 空格、逗号、其他标点 → 不是 token
			return false
		}
	}
	// ≥80% 是 base64url 字符
	return base64Count*10 >= len(s)*8
}

// hasMixedCase 报告字符串是否同时包含大小写字母。
//
// r1060（2026-11-26）：用于不透明令牌判据收紧。纯小写散文（"password for
// user123 account"）不应被判为强凭据。真实不透明令牌通常含大小写混合以
// 提升熵度（base64/base62 编码的自然结果）。
func hasMixedCase(s string) bool {
	hasUpper := false
	hasLower := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			hasUpper = true
		} else if c >= 'a' && c <= 'z' {
			hasLower = true
		}
		if hasUpper && hasLower {
			return true
		}
	}
	return false
}
