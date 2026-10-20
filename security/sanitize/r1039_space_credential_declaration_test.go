package sanitize

import (
	"context"
	"strings"
	"testing"
)

// r1039（D5，2026-10-20）—— 「标签 + **纯空格** + 值」这种声明形态此前
// **完全不被识别**。
//
// 缺陷本体：`internal/credentialtext/declarations.go` 的 declarationSeparator
// 只认 `[:=：]`、中文动词（改成/改为/设置为/设为/更新为/是/为）和英文
// `\s+is\s+` / `\s+(set|change|update)d?\s+to` ⇒ **纯空格不是分隔符**。
// 于是下面这四行此前**一行都不脱敏**（明文凭据直接出网关）：
//
//	密码 hunter2secret
//	password hunter2secret
//	口令 hunter2secret
//	账号 admin2024 密码 hunter2secret
//
// ★ 硬约束：不能简单地把 `\s+` 加进分隔符集合——
// `security/sanitize/r1034_human_credentials_test.go:83` 明确把
// `password policy requires 12 characters` 钉成**不变**（散文不是声明）。
// ⇒ 修法是**两级判定**：空格分隔符只产生**候选**，值还要过
// 「像不像凭据」这一关（长度 / 字母数字混排 / 不含空白），
// 见 internal/credentialtext 的 credibleCredentialValue。
//
// ★ 判据纪律：
//
//	· 判据同时跑**生产 yaml 检测器**与**内置 fallback**——两条规则集必须
//	  同口径（yaml 与 Go 常量字节一致，见 r1034_rule_parity_test.go）。
//	· **允许侧必须带「前提在场」对照**：光断言「散文没被脱敏」是不够的——
//	  规则压根没加载时它也绿。这里用「结构化形态仍然被识别」做对照臂。
func TestR1039SpaceSeparatedCredentialDeclarationsAreDetected(t *testing.T) {
	production, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for name, detector := range map[string]*PatternDetector{"production": production, "fallback": NewPatternDetector()} {
		t.Run(name, func(t *testing.T) {
			sanitizer, err := NewSanitizer(detector)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name, in, secret, account string
			}{
				{"密码", "密码 hunter2secret", "hunter2secret", ""},
				{"password", "password hunter2secret", "hunter2secret", ""},
				{"口令", "口令 hunter2secret", "hunter2secret", ""},
				{"账号+密码", "账号 admin2024 密码 hunter2secret", "hunter2secret", "admin2024"},
			} {
				result, err := sanitizer.SanitizeInput(context.Background(), tc.in)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(result.SanitizedText, tc.secret) {
					t.Errorf("%s：空格分隔的凭据声明没有被脱敏：%q -> %q", tc.name, tc.in, result.SanitizedText)
				}
				if tc.account != "" && strings.Contains(result.SanitizedText, tc.account) {
					t.Errorf("%s：空格分隔的帐号声明没有被脱敏：%q -> %q", tc.name, tc.in, result.SanitizedText)
				}
				// 值必须**整段**进映射（不能只遮一半）。
				values := map[string]bool{}
				for _, v := range result.SanitizeMap {
					values[v] = true
				}
				if !values[tc.secret] {
					t.Errorf("%s：整段凭据没有进映射：%q (map=%v)", tc.name, tc.in, result.SanitizeMap)
				}
				if tc.account != "" && !values[tc.account] {
					t.Errorf("%s：整段帐号没有进映射：%q (map=%v)", tc.name, tc.in, result.SanitizeMap)
				}
				restored, err := sanitizer.RestoreOutput(context.Background(), result.SanitizedText, result.SanitizeMap)
				if err != nil || restored != tc.in {
					t.Errorf("%s：还原失败（原文被改写）：%q -> %q (err=%v)", tc.name, tc.in, restored, err)
				}
			}
		})
	}
}

// TestR1039StructuredCredentialControlGroupStaysDetected 是**对照臂**：
// 结构化分隔符（`:` / `=` / `：` / 中文动词 / `is`）此前就被识别，收窄不得
// 把它们弄丢。
//
// 没有这条对照臂，「空格分隔」那条用例可能因为「整条规则彻底没加载」而
// 假绿。
func TestR1039StructuredCredentialControlGroupStaysDetected(t *testing.T) {
	production, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for name, detector := range map[string]*PatternDetector{"production": production, "fallback": NewPatternDetector()} {
		t.Run(name, func(t *testing.T) {
			for _, in := range []string{
				"密码：hunter2secret",
				"密码是 hunter2secret",
				"pwd = hunter2secret",
				"password: hunter2secret",
				"token: hunter2secret",
				"api_key=hunter2secret",
			} {
				fragments, err := detector.DetectScope(context.Background(), in, ScopeInputDeclarations)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, f := range fragments {
					if f.Type == TypeSecret && f.Value == "hunter2secret" {
						found = true
					}
				}
				if !found {
					t.Errorf("结构化凭据声明不再被识别（收窄过头）：%q (fragments=%v)", in, fragments)
				}
			}
		})
	}
}

// TestR1039ProseAfterCredentialLabelIsNotADeclaration 钉住**修复的边界**：
// 标签后面跟的是散文词时，整句必须原样通过。
//
// r1034 已经钉了 `password policy requires 12 characters`；这里补上中文与
// 其它形态的同类散文，并明确它们**不是**声明。
//
// ★ 前提在场对照：同一批里若把散文换成真口令，必须被识别（见
// TestR1039SpaceSeparatedCredentialDeclarationsAreDetected）——所以这条不是
// 「规则没加载」的假绿。
func TestR1039ProseAfterCredentialLabelIsNotADeclaration(t *testing.T) {
	production, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for name, detector := range map[string]*PatternDetector{"production": production, "fallback": NewPatternDetector()} {
		t.Run(name, func(t *testing.T) {
			sanitizer, err := NewSanitizer(detector)
			if err != nil {
				t.Fatal(err)
			}
			for _, in := range []string{
				"password policy requires 12 characters",
				"password expiry is handled by the scheduler",
				"token rotation happens every quarter",
				"secret sauce is on the third shelf",
				"密码策略要求至少十二个字符",
				"账号归属哪个部门请查一下花名册",
				"账号 ~/work/docker-base-images",
				// ★ 长度下限的「前提在场」用例：这些值**含数字**（只靠
				// 「必须含数字」这一条挡不住），只靠**长度**挡得住
				// （sha256 = 6 位、utf8mb4 = 7 位，都 < 8）。
				// 没有这两行，长度下限就没有任何判据钉着——把它删掉，
				// 上面那些散文用例仍然全绿（假绿）。
				"token sha256",
				"password utf8mb4",
				"口令 base64",
				// ★ 字母要求的「前提在场」用例：纯数字串（≥8 位）不是
				// 凭据——「密码 20240101」是一个日期，不是口令。
				"password 20240101",
				// ★ 「值不含空白」这条要求的「前提在场」用例。
				// 这一支识别的是**凭据 token**，不是短语：值里带空白的
				// 形态（多半是引号里的短语）不在本支的识别范围内。
				// ★ 范围边界（不是漏检）：短语形态的凭据由结构化分隔符
				// 那一支覆盖——r1034 的 `password is 'two words#'` 用例
				// 就是这种形态，它仍然被识别并可还原。
				"password \"hunter2 secret\"",
			} {
				result, err := sanitizer.SanitizeInput(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				if result.SanitizedText != in {
					t.Errorf("散文被误判成凭据声明：%q -> %q", in, result.SanitizedText)
				}
			}
		})
	}
}
