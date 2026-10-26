package sanitize

import (
	"context"
	"strings"
	"testing"
)

// r1039（D7，2026-10-20）—— 三条规则在**普通运维散文**上误报。
//
// 实测误报（输入面 ScopeInputDeclarations）：
//
//	规则              yaml 位置            被误伤的 benign 文本          此前判成
//	financial.bank_card  configs/…:171-174  时间戳 2026102012345678        credit_card
//	financial.iban       configs/…:195-198  US20240001 / DE1234ABCD        custom
//	pii.passport         configs/…:39-42    配置项 P20240101 生效          custom
//
// 根因（都是**规则内容**层面的过度匹配，不是判据问题）：
//
//	· bank_card 只要「16-19 位数字」，不要求发卡行 IIN ⇒ 纳秒时间戳也是「卡号」；
//	· iban 只要「两个大写字母 + 两位数字」，没有国家代码表、没有长度 ⇒
//
// //	   10 位的 US20240001 也是「IBAN」；
//
//	  · passport 只要「E/G/P + 8 位数字」，不要求它是中国护照的真实格式 ⇒
//		   任何以 P 开头的 9 位 token 都是「护照号」。
//
// ⇒ 收窄三处正则（取舍写在 yaml 就地注释里）：
//
//	· bank_card：IIN 前缀族（Visa 4 / MC 51-55 / AmEx 34,37 / Discover
//	  6011,65xx / UnionPay 62 / JCB 35）+ 16-19 位（AmEx 15 位）；
//	· iban：SWIFT IBAN 国家代码表 + 总长 15-34 位；
//	· passport：中国护照真实格式（E+8 位，或 E+[A-G]+7 位）。
//
// ★ 判据纪律：每条收窄都必须配**正向**用例（真值仍然被识别），否则
// 「把规则删掉」也会让误伤用例全绿（AGENTS 判据纪律第 3 条：允许侧要有
// 「前提仍在场」的对照）。
func TestR1039FinancialAndPassportRulesDoNotFireOnBenignText(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for _, in := range []string{
		"时间戳 2026102012345678",
		"US20240001",
		"DE1234ABCD",
		"配置项 P20240101 生效",
	} {
		fragments, err := detector.DetectScope(context.Background(), in, ScopeInputDeclarations)
		if err != nil {
			t.Fatal(err)
		}
		if len(fragments) != 0 {
			t.Errorf("普通文本被误判成敏感信息：%q -> %+v", in, fragments)
		}
	}
}

// TestR1039GenuineFinancialAndPassportValuesStillDetected 是**正向对照**：
// 收窄不得拿召回换精度。
func TestR1039GenuineFinancialAndPassportValuesStillDetected(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for _, tc := range []struct {
		name, in, wantType, wantRule string
	}{
		// 银行卡：Visa / Mastercard / AmEx / Discover / UnionPay / JCB，
		// 含 4 位分组之间的空格与连字符。
		{"visa16", "4111111111111111", "credit_card", "financial.bank_card"},
		{"visa_spaced", "4111 1111 1111 1111", "credit_card", "financial.bank_card"},
		{"visa_dashed", "4111-1111-1111-1111", "credit_card", "financial.bank_card"},
		{"mastercard", "5500005555555559", "credit_card", "financial.bank_card"},
		{"amex15", "340000000000009", "credit_card", "financial.bank_card"},
		{"discover", "6011000000000004", "credit_card", "financial.bank_card"},
		{"unionpay19", "6222021234567890123", "credit_card", "financial.bank_card"},
		{"jcb", "3530111333300000", "credit_card", "financial.bank_card"},
		// IBAN：德国 / 英国 / 法国 / 挪威（最短 15 位）。
		{"iban_de", "DE89370400440532013000", "custom", "financial.iban"},
		{"iban_gb", "GB82WEST12345698765432", "custom", "financial.iban"},
		{"iban_fr", "FR1420041010050500013M02606", "custom", "financial.iban"},
		{"iban_no", "NO9386011117947", "custom", "financial.iban"},
		// 中国护照：旧版 E+8 位 / 电子护照 E+[A-G]+7 位。
		{"passport_legacy", "E12345678", "custom", "pii.passport"},
		{"passport_epassport", "EA1234567", "custom", "pii.passport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fragments, err := detector.DetectScope(context.Background(), tc.in, ScopeInputDeclarations)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range fragments {
				if string(f.Type) == tc.wantType && f.RuleID == tc.wantRule && f.Value == tc.in {
					found = true
				}
			}
			if !found {
				t.Errorf("真值没有被识别（收窄过头）：%q -> %+v", tc.in, fragments)
			}
		})
	}
}

// TestR1039NarrowedRulesStillRedactEndToEnd 把「真值仍被识别」落到**脱敏
// 结果**上（占位符 + 还原），而不只是「检测器报了片段」。
//
// 没有这一层，「检测器报了片段但调用方没遮蔽」这种半吊子实现也能过上面
// 那条用例。
func TestR1039NarrowedRulesStillRedactEndToEnd(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	sanitizer, err := NewSanitizer(detector)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, in, secret string }{
		{"visa", "我的卡号是 4111111111111111", "4111111111111111"},
		{"iban", "收款账号 DE89370400440532013000", "DE89370400440532013000"},
		{"passport", "护照号 E12345678 麻烦核对", "E12345678"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := sanitizer.SanitizeInput(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.SanitizedText, tc.secret) {
				t.Errorf("真值没有脱敏：%q -> %q", tc.in, result.SanitizedText)
			}
			if !strings.Contains(result.SanitizedText, "{SENSITIVE:") {
				t.Errorf("真值没有被占位符替换：%q -> %q", tc.in, result.SanitizedText)
			}
			restored, err := sanitizer.RestoreOutput(context.Background(), result.SanitizedText, result.SanitizeMap)
			if err != nil || restored != tc.in {
				t.Errorf("还原失败：%q -> %q (err=%v)", tc.in, restored, err)
			}
		})
	}
}
