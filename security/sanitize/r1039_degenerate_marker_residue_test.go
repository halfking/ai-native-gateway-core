package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1039_degenerate_marker_residue_test.go — 2026-10-20 退化标记残留的判据。
//
// ## 缺陷（D3）
//
// 标记退化成解析不了的东西时，客户端看到的是**补丁式**的结果：
//
//	{SENSITIVE:secret:1a}  →  {[REDACTED:secret]1a}   ← 多余的 '{'，尾巴 '1a' 留着
//	{SENSITIVE:sec        →  {[REDACTED:sec]          ← '{' 留着，类型名 'sec' 是垃圾
//	{{SENSITIVE:secret:1}} →  {s3cr3t}                ← 多余的花括号
//
// 根因有两条：
//
//  1. 退化跨段（花括号分支整段命中但内部不是标记）走的是**内部裸形态重扫**，
//     重扫只认得段内那截裸标记，段外的 '{' 与尾巴都留了下来；
//  2. span 的边界是**模式匹配到的范围**，不是标记自己的边界——模型多写的一层
//     花括号在 span 之外，于是留在了输出里。
//
// ## 为什么既有判据没抓到
//
// placeholder_loose_test.go:198-209 对退化用例只断言 NotContains(got, "s3cr3t")
// ——「真值没泄漏」这一条。**残留**（多余的 '{'、垃圾类型名、垃圾尾巴）它一条
// 都没管，于是缺陷活了下来。本文件的判据一律断言**输出逐字相等**。
//
// ## 判据纪律
//
//   - 断言「输出必须逐字等于 X」，而不是「输出里不含 Y」：后者对残留完全失明。
//   - 每条遮蔽断言都配一条「前提仍在场」：同一段文本里的**规范**拼写必须先被
//     还原或遮蔽，否则「输出干净」可能只是「压根没匹配到」那种假绿。

// r1039DegenerateSecret 是本轮退化用例共用的真值。
const r1039DegenerateSecret = "p@ss w0rd-秘密"

// TestR1039DegenerateMarkerLeavesNoResidue 是 D3 的主判据。
//
// 断言输出**逐字相等**：退化标记必须被**整段**替换成一个干净的遮蔽标记，
// 不得留下多余的花括号、不得留下垃圾尾巴、不得留下垃圾类型名。
func TestR1039DegenerateMarkerLeavesNoResidue(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039DegenerateSecret}

	// 前提仍在场：同一段文本里的**规范**拼写必须先被还原。
	canonical, err := s.RestoreOutputOrMask(context.Background(), "a {SENSITIVE:secret:1} b", sm)
	require.NoError(t, err)
	require.Equal(t, "a "+r1039DegenerateSecret+" b", canonical,
		"前提失效：规范拼写没有被还原，本判据无法区分「遮蔽干净」与「没匹配到」")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "garbage index",
			in:   "{SENSITIVE:secret:1a}",
			want: RedactionMarker(TypeSecret),
		},
		{
			name: "negative index",
			in:   "{SENSITIVE:secret:-1}",
			want: RedactionMarker(TypeSecret),
		},
		{
			name: "non digit index",
			in:   "{SENSITIVE:secret:X}",
			want: RedactionMarker(TypeSecret),
		},
		{
			name: "truncated type",
			in:   "{SENSITIVE:sec",
			want: unknownRedactedMarker,
		},
		{
			name: "truncated type keeps no brace",
			in:   "x {SENSITIVE:sec y",
			want: "x " + unknownRedactedMarker + " y",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), c.in, sm)
			require.NoError(t, err)
			assert.Equal(t, c.want, got,
				"退化标记必须被整段替换成干净的遮蔽标记，不得留下残留")
			assert.NotContains(t, got, "{",
				"退化标记不得留下多余的花括号: %q", got)
			assert.NotContains(t, got, "}",
				"退化标记不得留下多余的花括号: %q", got)
		})
	}
}

// TestR1039DegenerateMaskNamesOnlyRealTypes 钉住遮蔽物的**类型名**。
//
// 退化标记的类型名可能是被截断的碎片（"{SENSITIVE:sec" 里的 "sec" 可能是
// "secret" 或 "server_ip"）。把它原样点回客户端，等于宣称「这里遮掉的是一
// 类叫 sec 的东西」——而网关根本没有这一类规则。类型不可知时说 unknown。
func TestR1039DegenerateMaskNamesOnlyRealTypes(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// 真实类型：点名。
	known, err := s.RestoreOutputOrMask(context.Background(), "{SENSITIVE:secret:1a}", SanitizeMap{})
	require.NoError(t, err)
	assert.Equal(t, RedactionMarker(TypeSecret), known,
		"真实类型必须被点名（客户端要能自证遮掉了哪一类）")

	// 编出来的类型：不点名。
	for _, bogus := range []string{"{SENSITIVE:sec:1a}", "{SENSITIVE:zzz:1a}"} {
		t.Run(bogus, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), bogus, SanitizeMap{})
			require.NoError(t, err)
			assert.Equal(t, unknownRedactedMarker, got,
				"网关没有的类别不得被点名: %q", got)
		})
	}
}

// TestR1039DegenerateContainerSpanKeepsClientBytes 钉住**反向**边界：
// 退化 span 若其实是个**容器**（JSON 片段、句子），整段替换会吞掉客户端
// 自己的字节。
//
// 这条是 r1022 的契约（毒分支 A：掩蔽 marker 的同时必须保留帧内其余字节）：
// 累积的工具参数必须是合法 JSON，"x tail" 不得被吞掉。
func TestR1039DegenerateContainerSpanKeepsClientBytes(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// 前提仍在场：同一形态的**紧**退化标记确实被整段替换了（否则下面
	// 「保留客户端字节」可能只是「什么都没做」）。
	tight, err := s.RestoreOutputOrMask(context.Background(), "{SENSITIVE:secret:1a}", SanitizeMap{})
	require.NoError(t, err)
	require.Equal(t, RedactionMarker(TypeSecret), tight,
		"前提失效：紧的退化标记没有被整段替换")

	// 容器形态：marker 之后还有客户端自己的字节。
	const container = `{"command":"echo {SENSITIVE:phone:13800138000x tail"}`
	got, err := s.RestoreOutputOrMask(context.Background(), container, SanitizeMap{})
	require.NoError(t, err)
	assert.Contains(t, got, "x tail",
		"容器形态的退化 span 不得吞掉客户端自己的字节: %q", got)
	assert.Contains(t, got, `"command"`,
		"容器形态的退化 span 不得破坏外层结构: %q", got)
	assert.False(t, r1039CarriesMarkerWord(got),
		"容器形态里的标记词也必须被遮蔽: %q", got)
}

// TestR1039DoubledBraceMarkerAbsorbsOuterBraces 钉住**多余花括号**这一条。
//
// 模型把花括号写重了（"{{SENSITIVE:secret:1}}"）时，外层那一对花括号在
// span 之外，留在了输出里：客户端看到的是 "{s3cr3t}"。
//
// 判据落在**容错阶段**（rewriteLooseSpans）：规范拼写由**精确阶段**先行
// 处理，那一段不在本轮改动范围内（见交付说明）。这里钉的是容错阶段自己的
// 行为：span 必须把属于标记自己的定界花括号吸进来。
func TestR1039DoubledBraceMarkerAbsorbsOuterBraces(t *testing.T) {
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039DegenerateSecret}

	stats := restoreLooseMarkers(context.Background(), "{{SENSITIVE:secret:1}}",
		sm.resolverFor(), nil, looseMaskUnknown)
	assert.Equal(t, r1039DegenerateSecret, stats.text,
		"容错阶段必须把多写的花括号一起吸进 span")
	assert.Equal(t, 1, stats.recovered)

	// 全角形态同理（那一对花括号是三个字节，偏移必须按 rune 算）。
	stats = restoreLooseMarkers(context.Background(), "｛｛ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１｝｝",
		sm.resolverFor(), nil, looseMaskUnknown)
	assert.Equal(t, r1039DegenerateSecret, stats.text,
		"容错阶段必须把多写的全角花括号一起吸进 span")
}

// TestR1039DegenerateMarkerDoesNotBreakJSONNesting 钉住**不误伤**：
// 普通的 JSON 嵌套不得被整段吞掉。
func TestR1039DegenerateMarkerDoesNotBreakJSONNesting(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	for _, text := range []string{
		`{"a":{"b":1},"c":{"d":2}}`,
		`{"note": "use {a} and {b} and {a.b.c} in the config"}`,
		"the sensitive data was returned",
		"sensitive:12345",
	} {
		t.Run(text, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), text, SanitizeMap{})
			require.NoError(t, err)
			assert.Equal(t, text, got, "普通文本/JSON 不得被改动")
		})
	}
}

// TestR1039DegenerateMarkerStillFailsClosed 钉住 fail-closed：退化标记即使
// 没被还原，也不得让响应带着内部标记词通过残留探针。
func TestR1039DegenerateMarkerStillFailsClosed(t *testing.T) {
	for _, text := range []string{
		"{SENSITIVE:secret:1a}",
		"{SENSITIVE:sec",
		"{SENSITIVE:secret:X}",
		"{SENSITIVE:secret:-1}",
	} {
		t.Run(text, func(t *testing.T) {
			assert.NotEmpty(t, markerSpans(text),
				"退化标记必须被残留探针看见（否则响应不会被 fail closed）")
			assert.True(t, containsNonForeignMarker(context.Background(), []byte(text)),
				"退化标记必须让响应 fail closed")
		})
	}
}

// TestR1039DegenerateMarkerNeverLeaksTheValue 钉住「真值不泄漏」这一条在
// 退化路径上依然成立（既有判据只断言了这一条，本条把它显式钉住）。
func TestR1039DegenerateMarkerNeverLeaksTheValue(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{"{SENSITIVE:secret:1}": r1039DegenerateSecret}

	for _, text := range []string{
		"{SENSITIVE:secret:1a}",
		"{SENSITIVE:sec",
		"{SENSITIVE:secret:X}",
		"{SENSITIVE:secret:-1}",
		"{SENSITIVE:secret:1a} and {SENSITIVE:secret:2b}",
	} {
		t.Run(text, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), text, sm)
			require.NoError(t, err)
			assert.NotContains(t, got, r1039DegenerateSecret,
				"退化标记不得还原出真值: %q", got)
			assert.False(t, strings.Contains(strings.ToLower(got), "sensitive"),
				"退化标记不得把内部标记词留在客户端文本里: %q", got)
		})
	}
}
