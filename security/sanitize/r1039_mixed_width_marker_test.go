package sanitize

import (
	"context"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/internal/markerbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r1039_mixed_width_marker_test.go —— 2026-10-21 **半角全角混拼**标记的判据。
//
// ## 缺陷（本轮实测确认，比先前登记的更重）
//
// 先前登记的是「便宜门 mayContainMarkerText / containsFoldCaseWord 只认全 ASCII
// 词或全全角词，混拼两者都不成立 ⇒ 折宽副本不会被构造 ⇒ 标记原样到客户端」。
//
// **实测（2026-10-21，生产扫描路径 looseMarkerSpans）**：
//
//	"ＳENSITIVE：phone"   spans=0   cheapGate=false   ← 混拼（首字全角）
//	"SENSITIVＥ:phone"    spans=0   cheapGate=false   ← 混拼（末字全角）
//	"ＳＥＮＳＩＴＩＶＥ：phone"  spans=1  cheapGate=true    ← 全全角（本来就支持）
//	"SENSITIVE:phone"     spans=1  cheapGate=true    ← 全 ASCII（本来就支持）
//
// ⇒ 真正的问题**不在便宜门本身**：根因是 `foldMarkerWidth` 的**前置门**
// 是「全 ASCII 词」与「全全角词」的**析取** ⇒ 混拼形态两者皆不成立 ⇒
// **折宽副本根本不构造** ⇒ **生产扫描器本体也看不见它**（0 spans）。
//
// ## 修法（落在逐字符这一层，扫描器本体未改）
//
// `matchMarkerWordAnyWidthAt`：标记词的 9 个字符**各自**决定宽度
// （ASCII 大小写不敏感，或全角字母 Ａ-Ｚ/ａ-ｚ）。
// ⇒ 混拼形态被认出来 ⇒ 折宽副本照旧由 foldMarkerWidth 构造 ⇒
// 扫描器（折宽后按 ASCII 正则扫）一个字都不用改。
//
// `internal/markerbound` 是本包文法的**刻意副本** ⇒ 同一个修法必须同步，
// 否则截断切点会把混拼标记切成两半 ⇒ 半截标记随上游跑掉。
//
// ## 判据纪律
//
//   - **差分判据**（语义）：新门必须与一条**朴素参照**逐位等价，
//     且穷举 2^9 = 512 种宽度组合 —— 语义用「差分 + 穷举」钉，
//     不靠「挑几个例子看着对」。
//   - **成本判据**（不退化）：便宜门在 16KB 中文/英文正文上**零分配**
//     （hop.go 里记着上一次教训：16KB 上 5.6ns/0-alloc → 2477ns/12KB-alloc，~444×）。
//   - **误报门**：普通中文/散文/全角数字**不得**被拖进昂贵路径，
//     也不得被当成标记。
//   - **前提仍在场**：每条「客户端拿到的是真值」的断言都配一条「规范拼写确实能还原」
//     的断言，否则「还原成功」可能只是「压根没匹配到」那种假绿。
//   - **契约门**：sanitize 与 markerbound 两个副本的 span 集合必须**逐条相同**。

// r1039MixedWidthWordForms 是本轮要钉住的**词级**混拼形态。
//
// 它们是同一个模型行为（CJK 输入法半角/全角混用）的不同表象：同一个词里，
// 哪些字符是全角是随机的。旧实现要求 9 个字符**宽度一致**，这 6 个形态全都不满足。
//
// 词级形态带的是 type=phone ⇒ 它们用于「检��得到吗 / 门放行吗 / 两个副本一致吗」
// 这一类判断；**能否还原成真值**用下面那组（类型与映射表对上）。
var r1039MixedWidthWordForms = []string{
	"ＳENSITIVE：phone",        // 首字全角，其余 ASCII
	"SENSITIVＥ:phone",         // 末字全角，其余 ASCII
	"sensitive：phone",         // 词全 ASCII，冒号全角
	"ＳＥＮＳＩＴＩＶＥ：phone", // 全全角（回归基线）
	"SENSITIVE:phone",          // 全 ASCII（回归基线）
	"Ｓensitive:phone",         // 首字全角 + 大小写混排
}

// r1039MixedWidthSecretForms 是**类型与索引都对得上映射表**的混拼标记：
// 归一化后都是 {SENSITIVE:secret:1} ⇒ 能断言「客户端拿到的是真值」。
//
// 为什么要分两组：词级那组写的是 type=phone，而映射表里是 type=secret
// ⇒ 那一组**正确**的行为是遮蔽成 [REDACTED:phone]，拿它断言「还原成真值」
// 是我第一版判据自己写错了（判据红得对）。两组的分工必须写清楚。
var r1039MixedWidthSecretForms = []string{
	"SENSITIVE:secret:1",              // 全 ASCII（回归基线）
	"SENSITIVE：secret：1",            // 冒号全角
	"ＳENSITIVE：secret：1",           // 首字全角
	"SENSITIVＥ:secret:1",             // 末字全角
	"ＳＥＮＳＩＴＩＶＥ：ｓｅｃｒｅｔ：１", // 全全角（回归基线）
}

// r1039MixedWidthToken/secret 是本轮使用的真值与它的标记行。
const (
	r1039MixedWidthToken  = "{SENSITIVE:secret:1}"
	r1039MixedWidthSecret = "p@ss w0rd-秘密"
)

// naiveAnyWidthMarkerWord 是差分判据的**参照侧**：text 里是否出现标记词，
// 且它的 9 个字符**各自**用 ASCII 或全角字母拼写。
//
// 它刻意写得笨（逐个字节位置试、逐字符解码），这样它只可能与快实现
// **在语义上**不一致，而不会因为优化而共享同一个 bug。
func naiveAnyWidthMarkerWord(text string) bool {
	const word = "sensitive"
	for start := 0; start < len(text); start++ {
		off := start
		match := true
		for k := 0; k < len(word); k++ {
			if off >= len(text) {
				match = false
				break
			}
			r, size := utf8.DecodeRuneInString(text[off:])
			var got byte
			switch {
			case r < 0x80:
				got = byte(r)
			case r >= 0xFF21 && r <= 0xFF3A: // Ａ-Ｚ
				got = byte(r - 0xFEE0)
			case r >= 0xFF41 && r <= 0xFF5A: // ａ-ｚ
				got = byte(r - 0xFEE0)
			default:
				match = false
			}
			if !match {
				break
			}
			if asciiLower(got) != word[k] {
				match = false
				break
			}
			off += size
		}
		if match {
			return true
		}
	}
	return false
}

// TestR1039MixedWidthGateMatchesNaiveSemantics 是**语义**判据：
// containsMarkerWordAnyWidth 必须与朴素参照**逐位等价**。
//
// 覆盖：① 穷举 2^9 = 512 种宽度组合（每个字符各自 ASCII/全角）；
// ② 大小写混排；③ 应当**不**命中的形态（缺字、错字、尾部多字、散文）。
func TestR1039MixedWidthGateMatchesNaiveSemantics(t *testing.T) {
	// ① 穷举 512 种宽度组合
	base := []rune("sensitive")
	for mask := 0; mask < 1<<len(base); mask++ {
		var b strings.Builder
		for i, r := range base {
			if mask&(1<<i) != 0 {
				// 该位置用全角字母
				b.WriteRune(rune(0xFF41 + (r - 'a')))
				continue
			}
			b.WriteRune(r)
		}
		word := b.String()
		assert.Equal(t, true, naiveAnyWidthMarkerWord(word),
			"参照侧自身必须认为这是标记词: %q", word)
		assert.Equal(t, naiveAnyWidthMarkerWord(word), containsMarkerWordAnyWidth(word),
			"快实现与朴素参照不一致: %q", word)
		// 带上下文（前后各加噪声）也必须一致
		padded := "前缀 " + word + " 后缀"
		assert.Equal(t, naiveAnyWidthMarkerWord(padded), containsMarkerWordAnyWidth(padded),
			"带上下文的形态上快实现与朴素参照不一致: %q", padded)
	}

	// ② 大小写混排
	for _, word := range []string{
		"SENSITIVE", "sEnSiTiVe", "SeNsItIvE", "ＳＥＮＳＩＴＩＶＥ", "ｓｅｎｓｉｔｉｖｅ",
	} {
		assert.Equal(t, naiveAnyWidthMarkerWord(word), containsMarkerWordAnyWidth(word),
			"大小写混排上不一致: %q", word)
	}

	// ③ 应当**不**命中的形态
	for _, text := range []string{
		"",
		"sensitiv",
		"sensitivf",
		"sensitiver",
		"the sensitive data was returned",
		"most sensitive: phone number",
		"这是敏感数据，请妥善保管",
		"敏感字段：手机号",
		"全角标点，：（）；！？",
		"ＡＢＣ：ＡＢＣ",
		"订单已发货，联系电话：１３８００１３８００",
		"server_ip is a type name",
		"s.s.s.s.s.s.s.s.s.",
		"｛SENSITIVE｝",
	} {
		assert.Equal(t, naiveAnyWidthMarkerWord(text), containsMarkerWordAnyWidth(text),
			"不应命中的形态上不一致: %q", text)
	}
}

// TestR1039MixedWidthMarkerIsDetectedByProductionScan 钉住**生产扫描路径**
// 本身：混拼形态必须被认出来（修复前实测 0 spans）。
//
// 这条与「便宜门」分开钉：便宜门只是过滤器，扫描器才是判定者。
func TestR1039MixedWidthMarkerIsDetectedByProductionScan(t *testing.T) {
	for _, form := range r1039MixedWidthWordForms {
		t.Run(form, func(t *testing.T) {
			assert.NotEmpty(t, looseMarkerSpans(form),
				"生产扫描路径必须看见混拼标记: %q", form)
			assert.True(t, mayContainMarkerText(form),
				"便宜门必须放行混拼标记: %q", form)
			assert.NotEmpty(t, markerSpans(form),
				"残留探针必须看见混拼标记: %q", form)
			assert.True(t, placeholderCandidate(form),
				"还原前的门必须放行混拼标记: %q", form)
		})
	}
}

// TestR1039MixedWidthMarkerRestoresInsteadOfLeaking 是端到端判据：
// 混拼标记在映射表里**有**这一行时，客户端必须拿到真值。
//
// 前提仍在场：断言前先确认规范拼写确实能还原，否则「还原成功」可能只是
// 「压根没匹配到」那种假绿。
func TestR1039MixedWidthMarkerRestoresInsteadOfLeaking(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	sm := SanitizeMap{r1039MixedWidthToken: r1039MixedWidthSecret}

	canonical, err := s.RestoreOutputOrMask(context.Background(), "a "+r1039MixedWidthToken+" b", sm)
	require.NoError(t, err)
	require.Equal(t, "a "+r1039MixedWidthSecret+" b", canonical,
		"前提失效：连规范拼写都还原不了，本判据无法区分「还原成功」与「没匹配到」")

	for _, form := range r1039MixedWidthSecretForms {
		t.Run(form, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), "your token is "+form+" ok", sm)
			require.NoError(t, err)
			assert.Equal(t, "your token is "+r1039MixedWidthSecret+" ok", got,
				"混拼标记必须还原成真值")
			assert.False(t, r1039CarriesMarkerWord(got),
				"内部标记词不得出现在客户端可见文本里: %q", got)
		})
	}
}

// TestR1039MixedWidthMarkerMaskedWhenUnmapped 是另一半：映射表里**没有**这一行时，
// 混拼标记必须被遮蔽，绝不能原样穿过。
//
// 判据强度：输出里既没有真值、也没有任何拼写的标记词，而且必须**有一个**遮蔽标记
// ——「什么都没发生」与「干净」必须长得不一样。
func TestR1039MixedWidthMarkerMaskedWhenUnmapped(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	canonical, err := s.RestoreOutputOrMask(context.Background(), "x {SENSITIVE:secret:1} y", SanitizeMap{})
	require.NoError(t, err)
	require.Contains(t, canonical, redactedMarkerPrefix, "前提失效：规范拼写没有被遮蔽")

	for _, form := range r1039MixedWidthSecretForms {
		t.Run(form, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), "your token is "+form+" ok", SanitizeMap{})
			require.NoError(t, err)
			assert.NotContains(t, got, r1039MixedWidthSecret)
			assert.False(t, r1039CarriesMarkerWord(got),
				"未映射的混拼标记不得原样穿过: %q", got)
			assert.Contains(t, got, redactedMarkerPrefix,
				"未映射的混拼标记必须被遮蔽，而不是原样穿过: %q", got)
		})
	}
}

// TestR1039MixedWidthGateStaysQuietOnOrdinaryText 是**误报门**：
// 普通文本（尤其是中文、全角数字、全角标点）不得被拖进昂贵路径，
// 也不得被当成标记。
//
// 这条是 scope 门：混拼检测不得把普通中文变成遮蔽。
func TestR1039MixedWidthGateStaysQuietOnOrdinaryText(t *testing.T) {
	quiet := []string{
		"这是敏感数据，请妥善保管",
		"敏感字段：手机号",
		"订单已发货，联系电话：１３８００１３８００",
		"全角标点，：（）；！？",
		"ＡＢＣ：ＡＢＣ",
		"全角 ＡＢＣ 与 ａｂｃ 混排",
		"server_ip is a type name",
		"",
	}
	for _, text := range quiet {
		t.Run(text, func(t *testing.T) {
			assert.False(t, mayContainMarkerText(text),
				"普通文本不得触发便宜门: %q", text)
			assert.Empty(t, markerSpans(text),
				"普通文本不得被当成标记: %q", text)
			assert.False(t, containsMarkerWordAnyWidth(text),
				"普通文本不得被混拼判据命中: %q", text)
		})
	}

	// ⚠ 另一类：**含**标记词但不是标记的散文。
	// 便宜门是**否定过滤器**，它对这类文本返回 true 是**正确**的
	//（散文里也可能藏着被模型改写过的标记）——真正必须为 0 的是**span**
	// 与**遮蔽**。把两类混在一起断言，是我第一版判据自己写错了（判据红得对）。
	wordy := []string{
		"the sensitive data was returned",
		"most sensitive: phone number",
		"sensitive:12345",
	}
	for _, text := range wordy {
		t.Run("wordy/"+text, func(t *testing.T) {
			assert.Empty(t, markerSpans(text),
				"含词散文不得被当成标记: %q", text)
			assert.Empty(t, looseMarkerSpans(text),
				"含词散文不得被生产扫描路径当成标记: %q", text)
		})
	}
}

// TestR1039MixedWidthProseIsNotRedacted 是 scope 门的**端到端**那一半：
// 普通散文不得被遮蔽（逐字不变）。
func TestR1039MixedWidthProseIsNotRedacted(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	for _, text := range []string{
		"这是敏感数据",
		"敏感字段：手机号",
		"请保护好你的 秘密 值",
		"全角 ＡＢＣ 与 ａｂｃ 混排",
		"ＡＢＣ：ＡＢＣ",
		"the sensitive data was returned",
		"most sensitive: phone number",
		`sensitive:12345`,
	} {
		t.Run(text, func(t *testing.T) {
			got, err := s.RestoreOutputOrMask(context.Background(), text, SanitizeMap{})
			require.NoError(t, err)
			assert.Equal(t, text, got, "普通散文不得被遮蔽")
		})
	}
}

// TestR1039MixedWidthCheapGateDoesNotAllocate 是**成本判据**：
// 便宜门在真实尺寸的正文上必须**零分配**。
//
// 背景（hop.go mayCarryMarker 的注释）：上一轮把过滤器从朴素循环换成
// memchr 时，在 16KB 真实响应体上由 5.6ns/0-alloc 退化为 2477ns/12KB-alloc
// （~444×）——「为修漏判而放大常态成本」是本仓反复吃过亏的那一类。
// ⇒ 本轮放宽了判定（混拼也要看见），所以**必须**钉住它没有把成本放大。
func TestR1039MixedWidthCheapGateDoesNotAllocate(t *testing.T) {
	if testing.Short() {
		t.Skip("alloc gate skipped in -short")
	}
	cases := map[string]string{
		"chinese 16KB": strings.Repeat("这是敏感数据，请妥善保管，订单已发货。", 320),
		"english 16KB": strings.Repeat("The quick brown fox jumps over the lazy dog. ", 320),
		"fullwidth 16KB": strings.Repeat("ＡＢＣ：ＡＢＣ，１３８００１３８００。", 320),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, func() {
				_ = mayContainMarkerText(body)
			})
			t.Logf("%s (%d bytes): %.1f allocs/op", name, len(body), allocs)
			assert.Equal(t, float64(0), allocs,
				"便宜门在 %s 上必须零分配（不得为了看见混拼而放大常态成本）", name)
		})
	}
}

// TestR1039MixedWidthSpanContractMatchesMarkerbound 是**契约门**：
// internal/markerbound 是本包文法的**刻意副本**——截断切点必须避开标记，
// 副本不认识新拼写 ⇒ 切点会把新拼写切成两半 ⇒ 半截标记随上游跑掉。
// ⇒ 两个副本的 span 集合必须**逐条相同**。
func TestR1039MixedWidthSpanContractMatchesMarkerbound(t *testing.T) {
	contexts := []struct{ prefix, suffix string }{
		{"", ""},
		{"plain text ", " more"},
		{"中文前缀", "中文后缀"},
		{"{\"key\":\"", "\"}"},
		{"tool_call(fn): ", "\nnext"},
	}
	for _, form := range r1039MixedWidthWordForms {
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

// TestR1039MixedWidthMarkerReservedOnInputSide 钉住输入面的**保留**：
// 客户端回声里改写过的混拼标记必须占用索引水位，否则它可能别名到本轮稍后
// 分配的凭据上（reserveMutatedPlaceholders 的职责）。
func TestR1039MixedWidthMarkerReservedOnInputSide(t *testing.T) {
	// ⚠ 这里用的是**带索引**的那一组：裸形态若无索引，索引位是 -1，
	// 而 reserveMutatedPlaceholders 只在 `index > offsets[typ]` 时抬高水位
	// ⇒ 无索引的裸形**本就不占用**水位（既有语义，不是本轮的缺口）。
	// 拿无索引形态断言「占用水位」是我第一版判据自己写错了（判据红得对）。
	for _, form := range r1039MixedWidthSecretForms {
		t.Run(form, func(t *testing.T) {
			offsets := map[SensitiveType]int{}
			reserveMutatedPlaceholders("客户端回声："+form+" 结束", offsets)
			assert.Greater(t, offsets[TypeSecret], 0,
				"混拼标记必须占用索引水位（否则会别名到本轮凭据）")
		})
	}
}

// 下面是混拼判据的**成本**基准。
//
// 跑法：go test ./security/sanitize/ -run '^$' -bench MayContainMarkerText -benchmem
// 门槛：allocs/op 必须为 0，且 ns/op 与 ASCII 路径**同量级**
// （混拼检测不得比纯 ASCII 判定贵一个数量级）。
//
// ⚠ 基准的**输入必须彼此不同**才有意义：第一版我把 "Ascii" 与 "MixedWidth"
// 两条基准指向了**同一份中文语料** ⇒ 两条读数必然相同 ⇒ 什么也没证明。
// 现在每条基准一个**确定的语料种类**，并注明它走哪条候选路径。
var (
	// benchQuietASCII：纯 ASCII 英文正文 ⇒ 只走候选 1（memchr 's'/'S'）
	// ⇒ 这是便宜门的**下界**（旧门在这上面也只走一条 memchr）。
	benchQuietASCII = strings.Repeat("The quick brown fox jumps over the lazy dog. ", 320)

	// benchQuietChinese：中文正文，**含全角标点**（，。）⇒
	// 全角标点的 UTF-8 首字节是 0xEF（与全角字母同首字节）⇒ 候选 2
	// （memchr 0xEF）**会被进入** ⇒ 汉字本身在候选 2 里立刻被否掉。
	// ⚠ 这条是新门的**上界**，也是旧门的上界（旧门同样 memchr 0xEF）。
	benchQuietChinese = strings.Repeat("这是敏感数据，请妥善保管。", 320)

	// benchQuietFullwidth：全角字母/数字正文 ⇒ 候选 2 在**每一个**字符处被进入。
	benchQuietFullwidth = strings.Repeat("ＡＢＣ：ＡＢＣ，１３８００１３８００。", 320)

	// benchMixedWidthHit：含混拼标记 ⇒ 命中路径（候选 2 在标记词处一次成功）。
	benchMixedWidthHit = strings.Repeat("x", 400) + "ＳENSITIVE：phone" + strings.Repeat("y", 400)
)

func BenchmarkMayContainMarkerTextAscii(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayContainMarkerText(benchQuietASCII)
	}
}

func BenchmarkMayContainMarkerTextChinese(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayContainMarkerText(benchQuietChinese)
	}
}

func BenchmarkMayContainMarkerTextFullwidth(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayContainMarkerText(benchQuietFullwidth)
	}
}

func BenchmarkMayContainMarkerTextMixedWidthHit(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayContainMarkerText(benchMixedWidthHit)
	}
}
