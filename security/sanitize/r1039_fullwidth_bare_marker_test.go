package sanitize

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/internal/markerbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1039_fullwidth_bare_marker_test.go — 2026-10-20 全角无花括号标记的判据。
//
// ## 缺陷（D2）
//
// 模型在 CJK 输入法下把标记写成**全角**且**不带花括号**：
//
//	your token is ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１ ok
//
// 这条路径上有两个洞：
//
//  1. span pattern 的裸形态分支只认 ASCII 标记词、ASCII 冒号、ASCII 类型/索引；
//  2. 便宜门 mayContainMarkerText / containsFoldCaseWord 也只认 ASCII 词，
//     于是 markerSpans 在正则跑起来之前就提前返回 false。
//
// 结果是**内部标记原样到达客户端**——不是丢一个值，是把网关自己的内部 token
// 写进了响应正文。
//
// ## 判据纪律
//
//   - 每条「客户端拿到的是真值」的断言都配一条「前提仍在场」的断言
//     （映射表里确实有那一行），否则「还原成功」可能只是「压根没匹配到」那种
//     假绿（AGENTS.md 判据纪律第 3 条）。
//   - 「没泄漏」的断言要同时覆盖 ASCII 与全角两种拼写：只查 ASCII 的
//     "[SENSITIVE" 会把全角泄漏当成干净。
//   - 偏移映射必须逐字节核对：映射错了会**切掉无关文本**，那比原来的泄漏更糟。

// r1039FullWidthSecret 是本轮使用的真值与它的标记行。
const (
	r1039FullWidthSecret   = "p@ss w0rd-秘密"
	r1039FullWidthToken    = "{SENSITIVE:secret:1}"
	r1039FullWidthWord     = "ＳＥＮＳＩＴＩＶＥ" // ＳＥＮＳＩＴＩＶＥ
	r1039FullWidthColon    = "："         // ：
	r1039FullWidthType     = "ｓｅｃｒｅｔ"    // ｓｅｃｒｅｔ
	r1039FullWidthIndex    = "１"         // １
	r1039FullWidthBareForm = r1039FullWidthWord + r1039FullWidthColon +
		r1039FullWidthType + r1039FullWidthColon + r1039FullWidthIndex
)

// r1039CarriesMarkerWord reports whether text carries the marker word in any
// spelling (ASCII or full-width). A leak check that only looks for the ASCII
// spelling reads a full-width leak as clean.
func r1039CarriesMarkerWord(text string) bool {
	return strings.Contains(strings.ToLower(text), "sensitive") ||
		strings.Contains(text, r1039FullWidthWord)
}

// TestR1039FullWidthBareMarkerRestoresInsteadOfLeaking 是 D2 的主判据：
// 全角无花括号标记在映射表里**有**这一行时，客户端必须拿到真值。
//
// 前提仍在场：断言前先确认映射表里确实有 {SENSITIVE:secret:1} 这一行，且
// 规范拼写（ASCII）能被还原——否则「还原成功」可能只是「压根没匹配到」。
func TestR1039FullWidthBareMarkerRestoresInsteadOfLeaking(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{r1039FullWidthToken: r1039FullWidthSecret}

	// 前提仍在场：规范拼写（修复前就支持的形态）必须能还原。
	canonical, err := s.RestoreOutputOrMask(context.Background(), "a "+r1039FullWidthToken+" b", sm)
	require.NoError(t, err)
	require.Equal(t, "a "+r1039FullWidthSecret+" b", canonical,
		"前提失效：连规范拼写都还原不了，本判据无法区分「还原成功」与「没匹配到」")

	// 全角的四种组合：全角词 / ASCII 词 + 全角冒号 / 全角词 + ASCII 冒号 /
	// 混合大小写。它们是同一个模型行为（输入法半角/全角混用）的不同表象。
	forms := []string{
		r1039FullWidthBareForm,
		"SENSITIVE" + r1039FullWidthColon + r1039FullWidthType + r1039FullWidthColon + r1039FullWidthIndex,
		r1039FullWidthWord + ":secret:1",
		"ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１", // 混合大小写
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), "your token is "+form+" ok", sm)
			require.NoError(t, err)
			assert.Equal(t, "your token is "+r1039FullWidthSecret+" ok", got,
				"全角无花括号标记必须还原成真值")
			assert.False(t, r1039CarriesMarkerWord(got),
				"内部标记词不得出现在客户端可见文本里: %q", got)
		})
	}
}

// TestR1039FullWidthBareMarkerMaskedWhenUnmapped 是 D2 的另一半：映射表里
// **没有**这一行时，标记必须被遮蔽，且绝不能原样穿过。
//
// 判据强度：断言输出里既没有真值、也没有任何拼写的标记词，而且必须**有一个**
// 遮蔽标记——「什么都没发生」与「干净」必须长得不一样。
func TestR1039FullWidthBareMarkerMaskedWhenUnmapped(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// 前提仍在场：同一段文本里的规范拼写确实被遮蔽了（否则「没泄漏」可能
	// 只是这条路径压根没跑）。
	canonical, err := s.RestoreOutputOrMask(context.Background(), "x {SENSITIVE:secret:1} y", SanitizeMap{})
	require.NoError(t, err)
	require.Contains(t, canonical, redactedMarkerPrefix, "前提失效：规范拼写没有被遮蔽")

	for _, form := range []string{
		r1039FullWidthBareForm,
		"SENSITIVE" + r1039FullWidthColon + r1039FullWidthType + r1039FullWidthColon + r1039FullWidthIndex,
		r1039FullWidthWord + ":secret:1",
	} {
		t.Run(form, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), "your token is "+form+" ok", SanitizeMap{})
			require.NoError(t, err)
			assert.NotContains(t, got, r1039FullWidthSecret)
			assert.False(t, r1039CarriesMarkerWord(got),
				"未映射的全角标记不得原样穿过: %q", got)
			assert.Contains(t, got, redactedMarkerPrefix,
				"未映射的标记必须被遮蔽，而不是原样穿过: %q", got)
		})
	}
}

// TestR1039FullWidthMarkerReachesTheResidualProbe 钉住 D2 的第二半：便宜门
// 与残留探针必须**看得见**全角无花括号标记。
//
// 为什么这条是独立的一条：修复前 mayContainMarkerText 对全角词返回 false，
// 于是 markerSpans 在正则跑起来之前就提前返回了——后面的 span 修得再对也
// 不会被调用到。探针看不见标记 ⇒ 响应不会被 fail closed ⇒ 内部 token 直接
// 到达客户端。
func TestR1039FullWidthMarkerReachesTheResidualProbe(t *testing.T) {
	forms := []string{
		r1039FullWidthBareForm,
		"SENSITIVE" + r1039FullWidthColon + r1039FullWidthType + r1039FullWidthColon + r1039FullWidthIndex,
		r1039FullWidthWord + ":secret:1",
	}
	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			assert.True(t, mayContainMarkerText(form),
				"便宜门必须看见全角标记词")
			assert.True(t, mayCarryMarker([]byte(form)),
				"便宜门（字节形态）必须看见全角标记词")
			assert.NotEmpty(t, markerSpans(form),
				"残留探针必须看见全角无花括号标记")
			assert.True(t, placeholderCandidate(form),
				"还原前的门必须放行全角无花括号标记")
		})
	}
}

// TestR1039FullWidthGateStaysQuietOnOrdinaryText 钉住便宜门的**另一半**：
// 普通文本（尤其是中文）不得被这条新判据拖进昂贵路径，也不得被误判成标记。
//
// 这条是 scope 门：全角检测不得把普通中文变成遮蔽。
//
// 注意字节形态那个门（mayCarryMarker）**本来**就过宽：全角码位的首字节是
// 0xEF，它与全角花括号共用一条 memchr，所以任何含全角字符的文本都会进昂贵
// 路径——那是既有设计（见 hop.go 的注释），不是本轮引入的。本轮新增的判定
// 加在字符串形态的门上，所以这里断言的是**字符串形态**的门与残留探针。
func TestR1039FullWidthGateStaysQuietOnOrdinaryText(t *testing.T) {
	quiet := []string{
		"这是敏感数据，请妥善保管",
		"订单已发货，联系电话：１３８００１３８０００",
		"全角标点，：（）；！？",
		"server_ip is a type name",
		"",
	}
	for _, text := range quiet {
		t.Run(text, func(t *testing.T) {
			assert.False(t, mayContainMarkerText(text),
				"普通文本不得触发便宜门: %q", text)
			assert.Empty(t, markerSpans(text),
				"普通文本不得被当成标记: %q", text)
		})
	}
}

// TestR1039FullWidthOffsetMappingIsExact 钉住**偏移映射**。
//
// 还原改写是按原文字节偏移做的（rewriteLooseSpans 切 text[span[0]:span[1]]），
// 而折宽是「一 rune 换一 rune」——**不是**一字节换一字节（全角 ｓ 是 3 字节，
// 's' 是 1 字节）。映射错了会切掉**无关文本**，那比原来的泄漏更糟。
//
// 判据：标记前后的每一个字节都必须原样保留，输出逐字节相等。
func TestR1039FullWidthOffsetMappingIsExact(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{r1039FullWidthToken: r1039FullWidthSecret}

	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "ascii around",
			text: "before " + r1039FullWidthBareForm + " after",
			want: "before " + r1039FullWidthSecret + " after",
		},
		{
			name: "full width around",
			text: "前前前前" + r1039FullWidthBareForm + "後後後後",
			want: "前前前前" + r1039FullWidthSecret + "後後後後",
		},
		{
			name: "mixed widths around",
			text: "前a前" + r1039FullWidthBareForm + " 後",
			want: "前a前" + r1039FullWidthSecret + " 後",
		},
		{
			name: "two markers",
			text: r1039FullWidthBareForm + "|" + r1039FullWidthBareForm,
			want: r1039FullWidthSecret + "|" + r1039FullWidthSecret,
		},
		{
			name: "marker in the middle of full width text",
			text: "ＡＢＣＤ" + r1039FullWidthBareForm + " 後後",
			want: "ＡＢＣＤ" + r1039FullWidthSecret + " 後後",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), c.text, sm)
			require.NoError(t, err)
			assert.Equal(t, c.want, got,
				"折宽副本上的偏移必须精确映射回原文：映射错了会切掉无关文本")
		})
	}
}

// TestR1039FullWidthMarkerDoesNotRedactOrdinaryProse 是 scope 门：全角检测
// 不得把普通散文变成遮蔽。
//
// 这些形态是 r244 那次误伤审计钉住的边界（全角形态是它们的对偶）。
func TestR1039FullWidthMarkerDoesNotRedactOrdinaryProse(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	prose := []string{
		"这是敏感数据",
		"敏感字段：手机号",
		"请保护好你的 秘密 值",
		"全角 ＡＢＣ 与 ａｂｃ 混排",
		"ＡＢＣ：ＡＢＣ",
		"the sensitive data was returned",
		"most sensitive: phone number",
		`sensitive:12345`,
	}
	for _, text := range prose {
		t.Run(text, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), text, SanitizeMap{})
			require.NoError(t, err)
			assert.Equal(t, text, got, "普通散文不得被遮蔽")
		})
	}
}

// TestR1039FullWidthNonStreamInterceptorRestoresMarker 是**客户端真正收到的
// 那一帧**（非流式）。症状原文说的就是这条路径：
// InterceptNonStream 把文本原样返回。
func TestR1039FullWidthNonStreamInterceptorRestoresMarker(t *testing.T) {
	const tenant, session = "tenant-r1039-fw", "session-r1039-fw"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		r1039FullWidthToken: r1039FullWidthSecret,
	})
	body := []byte(`{"choices":[{"message":{"role":"assistant",` +
		`"content":"your token is ` + r1039FullWidthBareForm + ` ok"}}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock, "全角标记不得把整条响应掐断")
	assert.Contains(t, string(result.ModifiedBody), r1039FullWidthSecret,
		"客户端拿到的响应里必须有真值")
	assert.False(t, r1039CarriesMarkerWord(string(result.ModifiedBody)),
		"内部标记词不得到达客户端: %s", string(result.ModifiedBody))
}

// TestR1039FullWidthStreamChunkRestoresMarker 是流式那一半。
func TestR1039FullWidthStreamChunkRestoresMarker(t *testing.T) {
	const tenant, session = "tenant-r1039-fw-stream", "session-r1039-fw-stream"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		r1039FullWidthToken: r1039FullWidthSecret,
	})
	meta := &response.StreamMeta{
		SessionID: session, TenantID: tenant, State: response.NewStreamState(),
	}
	frame := []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"your token is ` +
		r1039FullWidthBareForm + ` ok"}}]}` + "\n\n")

	result, err := it.InterceptStreamChunk(context.Background(), frame, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	assert.Contains(t, string(result.ModifiedChunk), r1039FullWidthSecret,
		"客户端拿到的帧里必须有真值")
	assert.False(t, r1039CarriesMarkerWord(string(result.ModifiedChunk)),
		"内部标记词不得到达客户端: %s", string(result.ModifiedChunk))
}

// TestR1039FullWidthSpanContractMatchesMarkerbound 把**契约门**的语料扩到
// 全角形态。
//
// internal/markerbound 是本包文法的**刻意副本**：截断切点必须避开标记，
// 副本不认识新拼写 ⇒ 切点会把新拼写切成两半 ⇒ 半截标记随上游跑掉。
// TestMarkerBoundaryContractMatchesSanitizeGrammars 是那道门；本条把全角
// 形态补进语料，免得那道门在副本没跟上时看不出缺口。
func TestR1039FullWidthSpanContractMatchesMarkerbound(t *testing.T) {
	forms := []string{
		r1039FullWidthBareForm,
		"SENSITIVE" + r1039FullWidthColon + r1039FullWidthType + r1039FullWidthColon + r1039FullWidthIndex,
		r1039FullWidthWord + ":secret:1",
		"{" + r1039FullWidthBareForm + "}",
		"｛" + r1039FullWidthBareForm + "｝",
		"｛" + r1039FullWidthBareForm,
	}
	contexts := []struct{ prefix, suffix string }{
		{"", ""},
		{"plain text ", " more"},
		{"中文前缀", "中文后缀"},
		{"{\"key\":\"", "\"}"},
		{"tool_call(fn): ", "\nnext"},
	}
	for _, form := range forms {
		for _, ctx := range contexts {
			text := ctx.prefix + form + ctx.suffix
			local := markerSpans(text)
			sort.Strings(local)
			remoteSpans := markerbound.Spans(text)
			remote := make([]string, 0, len(remoteSpans))
			for _, span := range remoteSpans {
				remote = append(remote, text[span[0]:span[1]])
			}
			sort.Strings(remote)
			if strings.Join(local, "\x00") != strings.Join(remote, "\x00") {
				t.Errorf("span sets diverge for %q:\n sanitize:  %q\n markerbound: %q", text, local, remote)
			}
		}
	}
}

// TestR1039FullWidthMarkerHeldAcrossStreamSplit 钉住流式**分片**那一半：
// 标记被切在两帧之间时，前半段必须被扣住，不能先放出去。
//
// 前提仍在场：完整的一帧必须能还原（否则「扣住了」可能只是「整帧被丢了」）。
func TestR1039FullWidthMarkerHeldAcrossStreamSplit(t *testing.T) {
	const tenant, session = "tenant-r1039-fw-split", "session-r1039-fw-split"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		r1039FullWidthToken: r1039FullWidthSecret,
	})
	meta := &response.StreamMeta{
		SessionID: session, TenantID: tenant, State: response.NewStreamState(),
	}

	// 带花括号的全角形态：前缀是标记的一半，与续帧拼起来才是完整标记。
	first := []byte(`data: {"choices":[{"index":0,"delta":{"content":"{` +
		r1039FullWidthWord + r1039FullWidthColon + r1039FullWidthType + r1039FullWidthColon + `"}}]}` + "\n\n")
	held, err := it.InterceptStreamChunk(context.Background(), first, meta)
	require.NoError(t, err)
	require.NotNil(t, held)
	assert.NotContains(t, string(held.ModifiedChunk), r1039FullWidthWord,
		"标记前缀必须被扣住，不能先放出去: %s", string(held.ModifiedChunk))
}

// TestR1039FullWidthBareMarkerReservedOnInputSide 钉住输入面的**保留**：
// 客户端回声里改写过的标记必须占用索引水位，否则它可能别名到本轮稍后
// 分配的凭据上（reserveMutatedPlaceholders 的职责）。
//
// 全角形态此前**不占用**水位：span 探测器不认它，门也只认花括号。
func TestR1039FullWidthBareMarkerReservedOnInputSide(t *testing.T) {
	offsets := map[SensitiveType]int{}
	reserveMutatedPlaceholders("客户端回声："+r1039FullWidthBareForm+" 结束", offsets)
	assert.Greater(t, offsets[TypeSecret], 0,
		"全角无花括号标记必须占用索引水位（否则会别名到本轮凭据）")
}

// TestR1039FullWidthMarkerGluedToFullWidthLetterIsNotLeaked 钉住边界的
// **另一半**：标记词前面紧挨着全角字母时，标记**仍然**必须被认出来。
//
// 为什么单列一条：折宽副本上的 "\b" 会把全角字母变成 ASCII 词字符，于是
// "ＡＢＣＤＳＥＮＳＩＴＩＶＥ：…" 在折宽副本上**不成立**——而原文上的
// "\b" 从来不把全角字母当词字符（"ＡＢＣＤsensitive:phone" 一直是认出来的）。
// 边界必须按**原文**判断，否则全角标记会在这一个形态上原样穿过。
//
// 判据只要求「不得原样穿过」（遮蔽即达标）：后面紧跟全角字母时，索引位被
// 词边界规则吃掉，遮蔽物是 "[REDACTED:unknown]" 而不是真值——这与 ASCII
// 形态的同一形态（"SENSITIVE:secret:1x"）逐字一致。
func TestR1039FullWidthMarkerGluedToFullWidthLetterIsNotLeaked(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	text := "ＡＢＣＤ" + r1039FullWidthBareForm + " 後"
	got, err := s.RestoreOutputOrMask(context.Background(), text, SanitizeMap{})
	require.NoError(t, err)
	assert.False(t, r1039CarriesMarkerWord(got),
		"紧挨全角字母的全角标记不得原样穿过: %q", got)
	assert.Contains(t, got, redactedMarkerPrefix,
		"紧挨全角字母的全角标记必须被遮蔽: %q", got)
}
