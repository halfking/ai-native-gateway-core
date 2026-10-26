package sanitize

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026-10-20 12h 审计轮修正的门禁。
//
// ★ 缺陷本体：dropLabelWrappedAddressFragments 原实现把「包住精确片段」的
// custom 片段**整段丢弃**。但「包住」只说明 custom 跨度更大，**不说明**它
// 多出来的那部分只是标签 ——
//
//	「手机号 13800138001」                 残余 =「手机号        」⇒ 标签
//	「北京市朝阳区建国路88号 13800138001」 残余 =「北京市朝阳区建国路88号」⇒ **真地址**
//
// 两者结构完全同形（都是「前缀 + 空格 + 精确值」），原实现无法区分 ⇒
// 真地址那一例被整段丢弃，**地址明文留在脱敏结果里**。
//
// ★ 为什么既有门没抓到：TestR1036AddressNextToPreciseValueNotOverDropped
// 用的是「手机号 …，地址北京市朝阳区建国路88号」—— **精确值在前**。
// 那个顺序下 address 规则匹配的是「地址北京市…88号」这一段，它**不包住**
// phone 片段 ⇒ 不触发丢弃 ⇒ 门自然绿。
// ⇒ 漏的是**地址在前**的方向。下面的用例专门钉住这个方向。
//
// 变异：把 detector.go 里「残余含地址专有字 ⇒ 继续按 custom 脱敏」那段
// 去掉（恢复成整段丢弃）⇒ TestR1036AddressBeforePreciseValueKeepsAddressRedacted
// 会以「地址明文留在脱敏结果里」失败。
func TestR1036AddressBeforePreciseValueKeepsAddressRedacted(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err, "判据必须用真实规则文件：内置 fallback 没有地址规则")
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	cases := []struct {
		name string
		in   string
	}{
		{"地址在前+手机号", "地址是北京市朝阳区建国路88号 13800138001"},
		{"地址在前+银行卡", "地址是北京市朝阳区建国路88号 6222021234567890123"},
		{"省市区+路+门牌号+手机号", "浙江省杭州市西湖区文三路100号 13800138001"},
		{"带前缀标签", "地址是北京市朝阳区建国路88号 手机号 13800138001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.SanitizeInput(context.Background(), tc.in)
			require.NoError(t, err)
			// 地址**必须**仍以明文不存在于脱敏结果里。
			assert.NotContains(t, res.SanitizedText, "建国路",
				"真地址明文留在脱敏结果里（custom 片段被连带丢弃）：%s", res.SanitizedText)
			assert.NotContains(t, res.SanitizedText, "文三路",
				"真地址明文留在脱敏结果里（custom 片段被连带丢弃）：%s", res.SanitizedText)
			// 精确值必须仍被精确类型替换（r1036 的原意不能丢）。
			assert.Contains(t, res.SanitizedText, "{SENSITIVE:",
				"应当存在占位符：%s", res.SanitizedText)
		})
	}
}

// 对照臂：**纯标签**形态必须仍然让位给精确类型（r1036 的原意不能被本次修正破坏）。
// 没有这条对照，门可能被「一律按 custom 遮住」这种过度脱敏的改法骗过。
//
// 变异：把 detector.go 里「残余不含地址专有字 ⇒ 让位」那段去掉 ⇒ 本用例会失败。
func TestR1036PureLabelStillYieldsToPreciseType(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"标签+号+手机号", "手机号 13800138001", "手机号 {SENSITIVE:phone:1}"},
		{"前缀+标签+号+手机号", "请记住手机号 13800138001", "请记住手机号 {SENSITIVE:phone:1}"},
		{"轮次前缀+标签+号+手机号", "第1轮：手机号 13800138001", "第1轮：手机号 {SENSITIVE:phone:1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.SanitizeInput(context.Background(), tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.SanitizedText,
				"纯标签形态必须让位给精确类型（标签文字保持可见）")
		})
	}
}

// addressSpecificChars 必须与 yaml 的 address 规则字符类保持一致。
//
// 为什么要这道门：本次修正用「残余里是否出现地址专有字」来区分
// 「标签」与「真地址」。这组字是**手写**的，而真正的判据是 yaml 里的正则
// 字符类 —— yaml 改了地址规则而本常量没跟着改时，判据会**静默失配**
// （标签与地址又分不出来了），而没有任何一条报错。
//
// 变异：把 addressSpecificChars 里的「路」删掉 ⇒ 本用例会失败。
func TestAddressSpecificCharsMatchYAMLRule(t *testing.T) {
	raw, err := os.ReadFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	yamlText := string(raw)

	// 取出 address 规则的 regex 行。
	idx := strings.Index(yamlText, "address:")
	require.GreaterOrEqual(t, idx, 0, "yaml 里没有 address 规则")
	rest := yamlText[idx:]
	regexIdx := strings.Index(rest, "regex:")
	require.GreaterOrEqual(t, regexIdx, 0, "address 规则里没有 regex")
	line := rest[regexIdx:]
	if end := strings.IndexAny(line, "\n"); end >= 0 {
		line = line[:end]
	}

	for _, ch := range addressSpecificChars {
		assert.Contains(t, line, string(ch),
			"addressSpecificChars 里的 %q 不在 yaml 的 address 正则里 ⇒ "+
				"「标签 vs 真地址」的判据会静默失配。yaml: %s", string(ch), line)
	}
}
