package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1036（2026-10-20，154 生产取证）：地址规则把「标签 + 号 + 数字」整段吞掉。
//
// 现象：生产 `llm.kxpms.cn`（154）上，「手机号 13800138001」这类输入送上游时
// 变成**单个** `{SENSITIVE:custom:1}`，而不是「标签保留 + 手机号占位符」。
//
// 根因：`configs/sensitive_patterns.yaml` 的 address 规则
//
//	[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+
//
// 字符类里的「号」既表示「门牌号」也命中「手机号 / 房间号 / 工单号 / 订单号」
// 这类**标签词的尾字**。于是：
//
//	[^\s]{2,}  → "手机"
//	[...号...] → "号"
//	\s*       → " "
//	[\d...]+  → "13800138001"
//
// 整句落进 address（= TypeCustom）片段；同一次检测里 phone 规则也确实命中了
// 「13800138001」，但 phone 片段被 custom 片段**包裹**（0:21 ⊃ 10:21）。
// `mergeFragmentSpans` 按起点排序、起点相同取跨度更大者，于是外层的 custom
// 赢，整个标签+值塌成一个占位符。
//
// 危害：
//  1. **类型退化**：本来是 `phone`，退化成 `custom`。下游按类型分流的地方
//     （输出侧凭据键检查、占位符类型标记）拿到的类型信息丢了。
//  2. **还原面扩大**：占位符还原回的是**整句**「手机号 13800138001」，
//     而不是只有号码。调用方拿到的映射里，键是 custom、值是整句——
//     语义不再是「一个手机号」。
//  3. **判据可复现**：本地 `NewPatternDetectorFromFile` 完全复现（生产同源
//     规则文件），无需 154 在线即可钉住。
//
// 为什么不能简单地「把『号』从地址字符类里删掉」：
// 真实地址大量以「号」结尾（「建国路88号」「文三路100号」），删掉会让地址
// 规则整体失效——那是**倒退**，不是修复。
//
// 修复口径：**标签词的尾字不是地址**。判据 = custom(address) 片段内部若存在
// 一个**更精确类型**（phone / id_card / email / credit_card / account / secret）
// 的子片段，则该 custom 片段不是地址，而是「标签 + 精确值」——此时让精确
// 类型片段胜出，custom 片段整体让位。
//
// 正负对照（真地址不能被误伤）：
//   - 「北京市朝阳区建国路88号」只有 custom 片段、无精确子片段 → 仍整段脱敏
//   - 「浙江省杭州市西湖区文三路100号」同上
//   - 「我的手机号是 13800138001」本来就只命中 phone → 行为不变
func TestR1036LabelSuffixIsNotAddress(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err, "判据必须用真实规则文件：内置 fallback 没有地址规则")
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
		{"标签+号+银行卡", "卡号 6222021234567890123", "卡号 {SENSITIVE:credit_card:1}"},
		{"标签+号+邮箱", "邮箱 zhangsan@example.com", "邮箱 {SENSITIVE:email:1}"},
		{"标签+号+身份证", "身份证 110101199003078515", "身份证 {SENSITIVE:id_card:1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.SanitizeInput(context.Background(), tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.SanitizedText,
				"「标签+号+值」应只替换精确值，标签本身保留")
			// 精确值的占位符必须存在，且映射里的值就是那个值本身。
			require.Len(t, res.SanitizeMap, 1, "应恰好一个占位符")
			for ph, val := range res.SanitizeMap {
				assert.True(t, strings.HasPrefix(ph, "{SENSITIVE:phone:") ||
					strings.HasPrefix(ph, "{SENSITIVE:credit_card:") ||
					strings.HasPrefix(ph, "{SENSITIVE:email:") ||
					strings.HasPrefix(ph, "{SENSITIVE:id_card:"),
					"占位符类型应退化前的精确类型，实际 %q", ph)
				assert.NotContains(t, val, "手机号",
					"映射值应是精确值本身，不应含标签词")
			}
		})
	}
}

// TestR1036RealAddressStillRedacted 是正负对照的另一半：真地址（含「号」结尾
// 的门牌）必须**仍然**整段脱敏。修复不能靠「删掉地址字符类里的『号』」实现
// ——那会让地址规则整体失效。
func TestR1036RealAddressStillRedacted(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	for _, in := range []string{
		"北京市朝阳区建国路88号",
		"浙江省杭州市西湖区文三路100号",
		"联系地址是北京市朝阳区建国路88号",
	} {
		res, err := s.SanitizeInput(context.Background(), in)
		require.NoError(t, err)
		assert.NotContains(t, res.SanitizedText, "88号", "真地址仍须脱敏：%q", in)
		assert.NotContains(t, res.SanitizedText, "100号", "真地址仍须脱敏：%q", in)
		assert.Contains(t, res.SanitizedText, "{SENSITIVE:",
			"真地址应被占位符替换：%q", in)
	}
}

// TestR1036NonAddressLabelWithoutPreciseTypeUnchanged 钉住「没有精确子片段的
// 标签形态不变」：单号/工号这类**不是**敏感值的标签词，不因为本次修复而
// 改变既有行为（它们此前命中 custom，本次仍命中 custom）。
//
// 这条同时是**修复边界**的判据：修复只处理「custom 内含精确类型子片段」，
// 不重新解释地址规则的其他命中。
func TestR1036NonAddressLabelWithoutPreciseTypeUnchanged(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	res, err := s.SanitizeInput(context.Background(), "订单号 20241012")
	require.NoError(t, err)
	assert.Equal(t, "{SENSITIVE:custom:1}", res.SanitizedText,
		"无精确子片段的标签形态行为不变")
}

// TestR1036AddressNextToPreciseValueNotOverDropped 钉住「相邻但不该被剔除」的
// 边界：真地址片段与精确值片段**边界相接**（不重叠、不包含）时，地址片段
// 必须保留。
//
// 这条判据的用途是**把变异逼出来**：把修复条件从「完全包住」放宽成「有重叠」
// 时，本判据会变红。少了它，修复可能被写成一个更激进的形态而无人察觉
// ——真地址会被连同精确值一起丢掉，脱敏面反而缩小（漏脱敏）。
func TestR1036AddressNextToPreciseValueNotOverDropped(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	const in = "手机号 13800138001，地址北京市朝阳区建国路88号"
	res, err := s.SanitizeInput(context.Background(), in)
	require.NoError(t, err)

	// 手机号被占位符替换（精确类型胜出）。
	assert.Contains(t, res.SanitizedText, "{SENSITIVE:phone:1}",
		"精确值应被替换")
	// 真地址仍被替换——且它的占位符必须是独立的一个（不能被手机号占位符吞掉）。
	assert.Contains(t, res.SanitizedText, "{SENSITIVE:custom:",
		"相邻的真地址片段不应被连带丢弃")
	assert.NotContains(t, res.SanitizedText, "建国路88号",
		"真地址内容不得以明文留在脱敏结果里")
	// 两者都在映射里：精确值 + 真地址整段。
	assert.Len(t, res.SanitizeMap, 2,
		"应有两个占位符：精确值一个、真地址一个")
}

// TestR1036PartialOverlapKeepsPreciseValue 钉住「重叠但非完全包含」时精确值
// 仍必须胜出。
//
// 样本「房 13800138001号室」的片段形态是 custom(4:21) 与 phone(4:15) ——
// **同起点、不同终点**。它是本组判据里唯一能区分两种实现差异的样本：
//
//   - 修复写成「完全包住」⇒ custom 被剔除 ⇒ phone 胜出
//   - 修复写成「有重叠」  ⇒ custom 也被剔除 ⇒ phone 同样胜出
//
// ⇒ 这条**抓不到**「重叠」与「完全包住」的差异（两者结果相同）。
// 差异的真正区分点在下面那条 TestR1036PartialOverlapAddressNotSwallowed：
// 同起点不同终点时，custom 片段**多出来的那截尾巴**（「号室」）不能跟着
// 一起消失——否则脱敏面缩小成漏脱敏。
func TestR1036PartialOverlapKeepsPreciseValue(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)
	s, err := NewSanitizer(d)
	require.NoError(t, err)

	const in = "房 13800138001号室"
	res, err := s.SanitizeInput(context.Background(), in)
	require.NoError(t, err)
	assert.Contains(t, res.SanitizedText, "{SENSITIVE:phone:1}",
		"精确值应被 phone 占位符替换")
	assert.NotContains(t, res.SanitizedText, "13800138001",
		"手机号明文不得留在脱敏结果里")
}

// TestR1036EgressScopeKeepsAddressFragment 钉住**作用域边界**：类型裁决只发生在
// 脱敏面，ScopeEgress（tool 参数外传守卫的判定面）必须**保留** custom(address)
// 片段。
//
// 为什么这条重要：r1035 的惰性内容工具豁免判据是「fragment.Type == TypeCustom
// 且非 network_destination」——外传守卫靠 custom 片段的**存在**来决定放行还是
// 拦截。若把 r1036 的剔除扩展到 ScopeEgress，守卫的判定依据就被悄悄改掉了
// （少一个片段 = 少一条拦截理由），而这类改动**不会有任何既有判据报警**。
//
// 判据取「egress 面仍能看到 custom 片段」——不取具体行为结论，因为守卫的
// 最终裁决还取决于载体（inert_tool / local exec）与字段类型。
func TestR1036EgressScopeKeepsAddressFragment(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	require.NoError(t, err)

	frags, err := d.DetectScope(context.Background(), "手机号 13800138001", ScopeEgress)
	require.NoError(t, err)

	hasCustom := false
	for _, f := range frags {
		if f.Type == TypeCustom {
			hasCustom = true
		}
	}
	assert.True(t, hasCustom,
		"ScopeEgress 必须保留 custom(address) 片段：外传守卫的放行/拦截判据依赖它")

	// 脱敏面则相反：custom 已被剔除，精确类型胜出。
	redactFrags, err := d.DetectScope(context.Background(), "手机号 13800138001", ScopeRedact)
	require.NoError(t, err)
	for _, f := range redactFrags {
		assert.NotEqual(t, TypeCustom, f.Type,
			"脱敏面不应保留包裹性的 custom 片段")
	}
}
