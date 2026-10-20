package sanitize

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1039_exact_stage_brace_absorption_test.go — 2026-10-20 精确阶段的花括号吸收。
//
// ## 缺陷
//
// 模型把占位符的花括号写重了（"{{SENSITIVE:secret:1}}"），客户端看到的是
// "{s3cr3t}"——**值复原了，但多出来的那一对花括号留在了输出里**。
//
// ## 为什么既有判据没抓到
//
// 容错阶段（restoreLooseMarkers → rewriteLooseSpans）**早就会**吸收这一对
// 花括号（absorbMarkerDelimiters），而且 r1039_degenerate_marker_residue_test.go
// 里 `TestR1039DoubledBraceMarkerAbsorbsOuterBraces` 正是钉它的——但那条判据
// **直接调容错阶段**，绕过了真实管线里**先跑**的精确阶段。
//
// 真实管线（restoreText）是：
//
//	精确阶段（PlaceholderPattern.ReplaceAllStringFunc）→ 容错阶段
//
// 规范拼写 "{SENSITIVE:secret:1}" 由**精确阶段先行消费**，容错阶段根本看不到
// 那个标记 ⇒ 吸收逻辑永远轮不到执行 ⇒ 残渣必然出现。
//
// ⇒ 这条判据必须打在**整条管线**上（RestoreOutputOrMask），而不是单独打容错阶段。
// 这正是「判据必须与被测实现同层」的另一个形态：**判据要打在使用者看到的那一层**。
//
// ## 判据纪律
//
//   - 断言输出**逐字相等**，不是「不含 X」：残渣类缺陷对 NotContains 完全失明。
//   - 每条都配一条「前提仍在场」：同一段文本里的**规范**拼写必须先被还原，
//     否则「输出干净」可能只是「压根没匹配到」那种假绿。
//   - 吸收规则是**两侧对称**的：只有两侧都多出一对花括号才吸。单侧多一个
//     闭花括号时**不吸**——那可能是客户端自己在拼的 JSON 结构字符。

// r1039ExactSecret 是本文件共用的真值。
const r1039ExactSecret = "p@ss w0rd-秘密"

// TestR1039ExactStageAbsorbsDoubledBraces 钉住**多写的花括号**被吸收。
func TestR1039ExactStageAbsorbsDoubledBraces(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039ExactSecret}

	// 前提仍在场：规范拼写必须先被还原，否则「吸收干净」与「没匹配到」分不开。
	canonical, err := s.RestoreOutputOrMask(context.Background(), "a {SENSITIVE:secret:1} b", sm)
	require.NoError(t, err)
	require.Equal(t, "a "+r1039ExactSecret+" b", canonical,
		"前提失效：规范拼写没有被还原，本判据无法区分「吸收干净」与「没匹配到」")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "doubled ascii braces",
			in:   "{{SENSITIVE:secret:1}}",
			want: r1039ExactSecret,
		},
		{
			name: "doubled full-width braces around an ascii marker",
			in:   "｛｛SENSITIVE:secret:1｝｝",
			want: r1039ExactSecret,
		},
		{
			name: "doubled braces with surrounding prose",
			in:   "before {{SENSITIVE:secret:1}} after",
			want: "before " + r1039ExactSecret + " after",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), tc.in, sm)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got,
				"精确阶段必须把属于标记自己的那一对花括号一起吸进来")
		})
	}
}

// TestR1039ExactStageKeepsSingleSidedBrace 钉住**两侧对称**这条规则。
//
// 模型只多写了一个闭花括号（"{SENSITIVE:secret:1}}"）时，**不吸收**：
// 单侧的闭花括号可能是客户端自己在拼的 JSON 结构字符，吃掉它会让客户端
// 拿到的 JSON 少一个括号。
//
// ⚠ 与容错阶段的 absorbMarkerDelimiters 同规则（也要求两侧同时多出），
// 两阶段的行为必须一致——否则同一段文本走不同阶段会得到不同的残渣。
//
// 预期值说明：精确阶段把**整个标记**（含标记自己的那一对花括号）替换成真值，
// 所以标记自己的花括号本来就会消失；这条判据关心的是**标记之外**多出来的
// 那一个闭花括号必须留下。
func TestR1039ExactStageKeepsSingleSidedBrace(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039ExactSecret}

	got, err := s.RestoreOutputOrMask(context.Background(), "{SENSITIVE:secret:1}}", sm)
	require.NoError(t, err)
	assert.Equal(t, r1039ExactSecret+"}", got,
		"单侧多出的花括号不得吸收（保持左右对称，别吃客户端的结构字符）")

	// 另一条可达形态：多写了**开花**括号，而闭花括号缺失（e 抵达文本末尾）。
	// 这时右侧根本没有闭花括号可吸 ⇒ 左侧那一对也不吸。
	//
	// 预期值说明：标记自己那一对花括号属于标记，会随标记一起被替换掉；文本里
	// 本来就只有**一个**闭花括号（标记自己的），所以末尾不留 '}'。
	//
	// ⚠ 这条是「右侧检查」存在的**唯一**可观测差异：若把「右侧必须是闭花括号」
	// 那条显式检查去掉，core 检查会因为「core 恰好相同」而放行，于是左侧那个
	// 多余的 '{' 被单独吃掉 —— 变成了**单侧吸收**，与容错阶段不一致。
	got, err = s.RestoreOutputOrMask(context.Background(), "{{SENSITIVE:secret:1}", sm)
	require.NoError(t, err)
	assert.Equal(t, "{"+r1039ExactSecret, got,
		"右侧没有闭花括号可吸时，左侧多出的开花括号也不得单独吸收")
}

// TestR1039ExactStageKeepsContainerBraces 钉住**不误伤**：
// 标记左边紧邻的不是花括号时，不得把左边的结构字符吸进来。
//
// 预期值说明：标记自己那一对花括号属于标记，会随标记一起被替换掉；容器自己
// 的花括号必须原样保留。
func TestR1039ExactStageKeepsContainerBraces(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039ExactSecret}

	got, err := s.RestoreOutputOrMask(context.Background(), `{"a":{SENSITIVE:secret:1}}`, sm)
	require.NoError(t, err)
	assert.Equal(t, `{"a":`+r1039ExactSecret+`}`, got,
		"左侧紧邻的不是开花括号时不得吸收（那会把容器自己的结构字符吃进来）")
}

// TestR1039ExactStageMaskNamesOnlyRealTypes 钉住**类型词表**这条一致性规则。
//
// 模型自造的类型（"{SENSITIVE:ip:1}"——网关没有 ip 这个类别的规则）不得被
// 遮蔽标记**原样回显**：那等于声称一个不存在的类别。
//
// 容错阶段已经这么做了（maskType），精确阶段此前没有——两个阶段对同一段
// 文本会给出不同的遮蔽标记。判据钉的是**两阶段一致**。
func TestR1039ExactStageMaskNamesOnlyRealTypes(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// 映射表里没有这一条 ⇒ 走遮蔽路径。
	got, err := s.RestoreOutputOrMask(context.Background(), "{{SENSITIVE:ip:1}}", SanitizeMap{})
	require.NoError(t, err)
	assert.Equal(t, RedactionMarker(unknownRedactionType), got,
		"模型自造的类型必须收敛成 unknown，且多余的花括号一并吸收")

	// 对照：真实类型照旧点名。
	got, err = s.RestoreOutputOrMask(context.Background(), "{{SENSITIVE:secret:1}}", SanitizeMap{})
	require.NoError(t, err)
	assert.Equal(t, RedactionMarker(TypeSecret), got,
		"真实类型的遮蔽标记必须照旧点名该类型")
}

// TestR1039ExactStageRestoresEmptyValue 钉住「还原值本身可以是空串」。
//
// 「没解析出来」与「解析出来但值是空的」必须能区分：用 replacement=="" 当
// 未命中的哨兵，会把空串误判成未命中 ⇒ 空串被遮蔽成 [REDACTED:secret]。
//
// 这条用例是**反向**的：它守的是实现里的 `resolved` 标志，不是花括号逻辑。
func TestR1039ExactStageRestoresEmptyValue(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": ""}

	got, err := s.RestoreOutputOrMask(context.Background(), "a{SENSITIVE:secret:1}b", sm)
	require.NoError(t, err)
	assert.Equal(t, "ab", got,
		"还原值是空串时必须照原样还原（不得被当成『没解析出来』而遮蔽）")
}

// TestR1039ExactStageLeavesForeignMarkerIntact 钉住**外来标记不被本轮改动**。
//
// 外来标记是**下游 hop** 签发的，本轮只负责原样透传，让签发方去复原。
// 吸收花括号会改动下游负责的那段文本 ⇒ 外来标记**不吸收**。
func TestR1039ExactStageLeavesForeignMarkerIntact(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	ctx := WithForeignMarkers(context.Background(), foreignMarkerSet{
		"{SENSITIVE:secret:1}": {},
	})
	got, err := s.RestoreOutputOrMask(ctx, "{{SENSITIVE:secret:1}}", SanitizeMap{
		"{SENSITIVE:secret:1}": r1039ExactSecret,
	})
	require.NoError(t, err)
	assert.Equal(t, "{{SENSITIVE:secret:1}}", got,
		"外来标记必须原样透传：既不还原，也不吸收它周围的花括号")
}
