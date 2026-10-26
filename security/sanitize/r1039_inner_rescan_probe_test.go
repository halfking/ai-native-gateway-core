package sanitize

// r1039_inner_rescan_probe_test.go —— 2026-10-27 **自审轮**的判据。
//
// ## 这一轮审出了什么（两处，均已实测确认）
//
// 上游（r1039 混拼修复轮）把「标记词的半角全角混拼」修了，但**自审**发现
// 这一族里还有**三处**门，上游只放宽了两处：
//
// **缺口一（混拼族·第三处门）**：`hasReservedPlaceholder` 的**预筛**用的是
// ASCII-only 的 containsFoldCaseWordBytes ⇒ 混拼形态不被识别。
// 实测：`{"phone":"ＳENSITIVE：phone"}` ⇒ 守卫 **false**（canonical ASCII 同形态 true）。
//
// **缺口二（与混拼无关，canonical 也漏）**：**探针路径缺「内层重扫」**。
// brace 分支会把一整段 JSON 吞成一个候选（`{"pwd":"SENSITIVE:phone"}` 整个
// 就是一个候选），而那个候选既不匹配占位符语法也不像标记 ⇒ 被判掉 ⇒
// **裸形标记就在它内部，却没人看见** ⇒
//   - `placeholderCandidate` 假 ⇒ **工具调用参数车道整个被跳过**
//     （function.arguments 是一段 JSON 字符串，标记就落在里面）
//   - `hasReservedPlaceholder` 假 ⇒ 残留守卫不触发
// ⇒ 实测（修复前）：客户端**原样收到** `{"pwd":"SENSITIVE:phone"}`
// ——canonical ASCII 与混拼形态**都漏**。
//
// ⇒ 重写路径早就在做这个内层重扫（rewriteLooseSpans 的 case (b)），
// **只有探针路径没有** ⇒ 探针低报 ⇒ 该触发的门不触发。
//
// ## 判据纪律
//
//   - **前提仍在场**：每条「被认出来」的断言都配一条「canonical 形态同样被
//     认出来」与「散文不被认出来」的断言。
//   - **端到端**：判的是**客户端真正收到的那一帧**（拦截器的 ModifiedBody），
//     不是内部 span 细节。
//   - **契约门**：两个刻意副本（sanitize / internal/markerbound）的 span 集合
//     必须**逐条相同**。
//   - **成本门**：探针在 16KB 正文上**零分配**，且不放宽到「永远为真」。

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

// r1039InnerRescanForms 是**裸形标记**的形态集合：canonical ASCII 与混拼。
//
// 索引一律缺省（裸形没有索引）⇒ 下游解析出的索引是 −1 ⇒ 它**永远不可能**
// 对应到映射表的一行 ⇒ 正确行为是**遮蔽**（既有语义），不是还原。
var r1039InnerRescanForms = []string{
	"SENSITIVE:phone", // canonical ASCII（前提仍在场）
	"ＳENSITIVE：phone", // 混拼：首字全角
	"SENSITIVＥ:phone", // 混拼：末字全角
	"sensitive：phone", // 词全 ASCII + 冒号全角
}

// r1039InnerRescanProse 是**不得**被当成标记的散文（放在 JSON 字符串值里，
// 正是内层重扫最容易误伤的位置）。
var r1039InnerRescanProse = []string{
	"the sensitive data was returned",
	"most sensitive: phone number",
	"sensitive:12345",
	"敏感字段：手机号",
	"订单已发货，联系电话：１３８００１３８００",
}

// TestR1039InnerRescanProbesSeeBareMarkerInsideJSONRun 是**核心判据**：
// 探针路径必须看得见「JSON 字符串值里的裸形标记」。
//
// 修复前实测：markerSpans / placeholderCandidate / hasReservedPlaceholder
// 对 `{"pwd":"SENSITIVE:phone"}` 一律报「没有标记」⇒ 门不触发。
func TestR1039InnerRescanProbesSeeBareMarkerInsideJSONRun(t *testing.T) {
	ctx := context.Background()
	for _, form := range r1039InnerRescanForms {
		t.Run(form, func(t *testing.T) {
			// 裸形直接可见（基线）：探针必须看得见
			assert.NotEmpty(t, markerSpans(form),
				"裸形标记必须被探针看见: %q", form)
			assert.True(t, placeholderCandidate(form),
				"裸形标记必须被候选探针放行: %q", form)
			assert.True(t, hasReservedPlaceholder(ctx, []byte(form)),
				"裸形标记必须被残留守卫看见: %q", form)

			// ★ 缺口本体：同一个标记**落在 JSON 字符串值里**
			// （brace 分支吞掉整段 JSON ⇒ 裸形在它内部）
			inner := `{"pwd":"` + form + `"}`
			assert.NotEmpty(t, markerSpans(inner),
				"JSON 字符串值里的裸形标记必须被探针看见（内层重扫）: %q", inner)
			assert.True(t, placeholderCandidate(inner),
				"JSON 字符串值里的裸形标记必须被候选探针放行: %q", inner)
			assert.True(t, hasReservedPlaceholder(ctx, []byte(inner)),
				"JSON 字符串值里的裸形标记必须被残留守卫看见: %q", inner)
			assert.True(t, containsNonForeignMarker(ctx, []byte(inner)),
				"JSON 字符串值里的裸形标记必须被 hop 探针看见: %q", inner)
		})
	}
}

// TestR1039InnerRescanStaysQuietOnProseInsideJSON 是**误报门**：
// 内层重扫不得把散文当成标记。
//
// 前提仍在场：同一批散文在**可见**位置（不在 JSON 里）也必须判否 ——
// 否则「JSON 里判否」可能只是因为量具没跑到那一段。
func TestR1039InnerRescanStaysQuietOnProseInsideJSON(t *testing.T) {
	ctx := context.Background()
	for _, prose := range r1039InnerRescanProse {
		t.Run(prose, func(t *testing.T) {
			assert.Empty(t, markerSpans(prose),
				"散文不得产生 span: %q", prose)
			assert.False(t, placeholderCandidate(prose),
				"散文不得被候选探针放行: %q", prose)
			assert.False(t, hasReservedPlaceholder(ctx, []byte(prose)),
				"散文不得被残留守卫看见: %q", prose)

			inner := `{"pwd":"` + prose + `"}`
			assert.Empty(t, markerSpans(inner),
				"JSON 字符串值里的散文不得产生 span: %q", inner)
			assert.False(t, hasReservedPlaceholder(ctx, []byte(inner)),
				"JSON 字符串值里的散文不得被残留守卫看见: %q", inner)
		})
	}
}

// TestR1039InnerRescanPrefilterStaysAsciiOnlyButStillSafe 钉住一处**刻意的
// 窄门**：`hasReservedPlaceholder` 的预筛**只认 ASCII**（containsFoldCaseWordBytes）。
//
// 它一度被改成宽度感知（理由：混拼形态不被预筛识别），但**同轮的内层重扫**
// 已经把这件事完全覆盖 ⇒ 那次放宽是**冗余修复** ⇒ 已撤回
// （变异臂 M6 仍绿，正是它告诉我们的）。
//
// 这条判据钉住「窄门仍然安全」：混拼形态必须**照样**被守卫看见
// ⇒ 将来若有人「顺手」把预筛放宽，本条会提醒他：先问「它还冗余吗」。
func TestR1039InnerRescanPrefilterStaysAsciiOnlyButStillSafe(t *testing.T) {
	ctx := context.Background()
	for _, form := range r1039InnerRescanForms {
		t.Run(form, func(t *testing.T) {
			assert.True(t, hasReservedPlaceholder(ctx, []byte(form)),
				"混拼形态必须被守卫看见（哪怕预筛只认 ASCII）: %q", form)
			assert.True(t, hasReservedPlaceholder(ctx, []byte(`{"pwd":"`+form+`"}`)),
				"JSON 里的混拼形态必须被守卫看见: %q", form)
		})
	}
}

// TestR1039InnerRescanMasksBareMarkerInToolArguments 是**端到端**判据：
// 客户端真正收到的那一帧。
//
// 工具调用参数（function.arguments）是一段 JSON 字符串 ⇒ 模型把标记回显在
// 里面时，标记落在**嵌套 JSON 字符串**里 ⇒ 修复前参数车道被整个跳过 ⇒
// **原始 token 直达客户端**。
func TestR1039InnerRescanMasksBareMarkerInToolArguments(t *testing.T) {
	const tenant, session = "tenant-r1039-inner", "session-r1039-inner"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
	})
	ctx := context.Background()

	for _, form := range r1039InnerRescanForms {
		t.Run(form, func(t *testing.T) {
			body := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"function":` +
				`{"name":"login","arguments":"{\"pwd\":\"` + form + `\"}"}}]}}]}`)
			result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
				TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.ModifiedBody, "客户端必须拿到一帧（不得原样放行）")
			out := string(result.ModifiedBody)
			assert.False(t, r1039CarriesMarkerWord(out),
				"内部标记词不得到达客户端: %s", out)
			assert.Contains(t, out, "[REDACTED",
				"未映射的裸形标记必须被遮蔽（而不是原样穿过）: %s", out)
		})
	}

	// ★ 前提仍在场：同一条车道上，**花括号形态**必须被**还原成真值**
	// （映射表里有一行）⇒ 证明这条车道本身是通的、不是「一律遮蔽」。
	braced := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"function":` +
		`{"name":"login","arguments":"{\"pwd\":\"{SENSITIVE:phone:1}\"}"}}]}}]}`)
	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: braced,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.ModifiedBody)
	assert.Contains(t, string(result.ModifiedBody), "13800138000",
		"前提失效：花括号形态没有被还原 ⇒ 本判据无法区分「遮蔽干净」与「车道没跑」")
}

// TestR1039InnerRescanLeavesProseToolArgumentsAlone 是**误报门**的端到端那一半：
// 参数里的散文必须**逐字不变**。
func TestR1039InnerRescanLeavesProseToolArgumentsAlone(t *testing.T) {
	const tenant, session = "tenant-r1039-inner-prose", "session-r1039-inner-prose"
	it := mutatedMarkerRestorer(t, tenant, session, map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
	})
	ctx := context.Background()
	for _, prose := range r1039InnerRescanProse {
		t.Run(prose, func(t *testing.T) {
			body := []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"function":` +
				`{"name":"login","arguments":"{\"note\":\"` + prose + `\"}"}}]}}]}`)
			result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
				TenantID: tenant, SessionID: session, ClientProtocol: "openai-chat", ResponseBody: body,
			})
			require.NoError(t, err)
			if result == nil || result.ModifiedBody == nil {
				return // 原样放行也是正确的（没有可改的可见车道）
			}
			assert.Contains(t, string(result.ModifiedBody), prose,
				"参数里的散文必须逐字保留: %s", string(result.ModifiedBody))
			assert.NotContains(t, string(result.ModifiedBody), "[REDACTED",
				"参数里的散文不得被遮蔽: %s", string(result.ModifiedBody))
		})
	}
}

// TestR1039InnerRescanSpanContractMatchesMarkerbound 是**契约门**：
// internal/markerbound 是本包文法的**刻意副本**——截断切点必须避开标记。
// 两边不同步 ⇒ 切点会把标记切开 ⇒ 半截标记随上游跑掉。
// ⇒ 两个副本的 span 集合必须**逐条相同**（含内层重扫出来的那些）。
func TestR1039InnerRescanSpanContractMatchesMarkerbound(t *testing.T) {
	texts := []string{
		`{"pwd":"SENSITIVE:phone"}`,
		`{"pwd":"ＳENSITIVE：phone"}`,
		`{"pwd":"SENSITIVＥ:phone"}`,
		`{"a":"SENSITIVE:phone","b":"ok"}`,
		`prefix {"pwd":"SENSITIVE:phone"} suffix`,
		`{"pwd":"the sensitive data was returned"}`,
		`{"pwd":"{SENSITIVE:phone:1}"}`,
	}
	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			local := markerSpans(text)
			sort.Strings(local)
			remoteSpans := markerbound.Spans(text)
			remote := make([]string, 0, len(remoteSpans))
			for _, span := range remoteSpans {
				remote = append(remote, text[span[0]:span[1]])
			}
			sort.Strings(remote)
			// ⚠ 比较**拼接后**的集合，不直接比切片：一侧可能是 nil、另一侧是
			// 空切片（两者语义相同：没有 span）⇒ 直接比切片会把「没差异」
			// 读成「有差异」（判据自己写错的一次）。
			assert.Equal(t, strings.Join(local, "\x00"), strings.Join(remote, "\x00"),
				"两个副本的 span 集合分叉了（内层重扫没同步？）: sanitize=%q markerbound=%q", local, remote)
		})
	}
}

// TestR1039InnerRescanGateIsNotAlwaysTrue 是**反向自证**：
// 内层重扫不得把探针变成「永远为真」。
//
// 判据：把探针面对的文本换成不含标记词的普通 JSON ⇒ 探针必须判否。
// 若某天有人把内层重扫写成「有花括号就算标记」，这里立刻红。
func TestR1039InnerRescanGateIsNotAlwaysTrue(t *testing.T) {
	ctx := context.Background()
	ordinary := []string{
		`{"name":"login","arguments":"{\"user\":\"alice\"}"}`,
		`{"choices":[{"message":{"content":"hello"}}]}`,
		`{"retries":5,"tags":["a","b"]}`,
	}
	for _, body := range ordinary {
		t.Run(body, func(t *testing.T) {
			assert.Empty(t, markerSpans(body),
				"普通 JSON 不得产生 span: %q", body)
			assert.False(t, placeholderCandidate(body),
				"普通 JSON 不得被候选探针放行: %q", body)
			assert.False(t, hasReservedPlaceholder(ctx, []byte(body)),
				"普通 JSON 不得被残留守卫看见: %q", body)
		})
	}
}

// TestR1039InnerRescanCheapGatesDoNotAllocate 是**成本门**：
// **便宜门**在真实尺寸的正文上必须**零分配**。
//
// 背景（hop.go mayCarryMarker 的注释）：上一轮把过滤器从朴素循环换成 memchr 时，
// 16KB 真实响应体上由 5.6ns/0-alloc 退化为 2477ns/12KB-alloc（~444×）。
// ⇒ 本轮给探针加了内层重扫 ⇒ **必须**钉住它没有把成本放大。
//
// ⚠ **判据对象要分清**（我第一版写错了）：
//   - **便宜门**（mayContainMarkerText / containsMarkerWordAnyWidth[Bytes]）
//     返回 bool ⇒ 可以要求**零分配**；
//   - **扫描器**（markerSpans）返回切片 ⇒ **必然分配**（正则匹配切片），
//     16KB JSON 实测 970 allocs/op（320 个 JSON 结构）—— 那是**既有行为**，
//     不是本轮引入的 ⇒ 对它只能要求**有界**，放大与否靠 **A/B 基准**量。
func TestR1039InnerRescanCheapGatesDoNotAllocate(t *testing.T) {
	if testing.Short() {
		t.Skip("alloc gate skipped in -short")
	}
	cases := map[string]string{
		"chinese 16KB":   strings.Repeat("这是敏感数据，请妥善保管，订单已发货。", 320),
		"english 16KB":   strings.Repeat("The quick brown fox jumps over the lazy dog. ", 320),
		"fullwidth 16KB": strings.Repeat("ＡＢＣ：ＡＢＣ，１３８００１３８００。", 320),
		"json 16KB":      strings.Repeat(`{"name":"login","note":"订单已发货，请妥善保管"}`, 320),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			// ⚠ 三条门分开量（它们的入参类型不同：string / []byte）⇒
			// 不用 map 装（混合签名的 map 装不下，编译期就报错）。
			allocsText := testing.AllocsPerRun(100, func() {
				_ = mayContainMarkerText(body)
			})
			allocsWord := testing.AllocsPerRun(100, func() {
				_ = containsMarkerWordAnyWidth(body)
			})
			t.Logf("%s (%d bytes): text=%.1f word=%.1f allocs/op",
				name, len(body), allocsText, allocsWord)
			assert.Equal(t, float64(0), allocsText, "便宜门 mayContainMarkerText 在 %s 上必须零分配", name)
			assert.Equal(t, float64(0), allocsWord, "便宜门 containsMarkerWordAnyWidth 在 %s 上必须零分配", name)
		})
	}
}

// TestR1039InnerRescanScannerAllocationsAreBounded 是扫描器的**有界**门。
//
// 扫描器返回切片 ⇒ 必然分配（正则匹配切片）。本轮加的内层重扫**不得**让
// 分配次数随「候选个数」再乘一层 ⇒ 判据钉住一个**上界**。
// 放大与否由 A/B 基准量（见 BenchmarkR1039InnerRescanProbe*）。
func TestR1039InnerRescanScannerAllocationsAreBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("alloc gate skipped in -short")
	}
	body := strings.Repeat(`{"name":"login","note":"订单已发货，请妥善保管"}`, 320)
	allocs := testing.AllocsPerRun(100, func() {
		_ = markerSpans(body)
	})
	t.Logf("json 16KB (%d bytes): %.1f allocs/op", len(body), allocs)
	assert.LessOrEqual(t, allocs, float64(1024),
		"扫描器在 16KB JSON 上的分配次数必须有界（内层重扫不得再乘一层）")
}

// BenchmarkR1039InnerRescanProbe 是内层重扫的**成本**基准。
//
// 跑法：go test ./security/sanitize/ -run '^$' -bench R1039InnerRescanProbe -benchmem
// 门槛：allocs/op 必须为 0；ns/op 与「无标记词的普通 JSON」同量级
// （内层重扫不得把常态成本放大一个数量级）。
var (
	benchR1039InnerJSONQuiet  = strings.Repeat(`{"name":"login","note":"订单已发货"}`, 320)
	benchR1039InnerJSONNested = strings.Repeat(`{"pwd":"SENSITIVE:phone"}`, 320)
)

func BenchmarkR1039InnerRescanProbeQuiet(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = markerSpans(benchR1039InnerJSONQuiet)
	}
}

func BenchmarkR1039InnerRescanProbeNested(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = markerSpans(benchR1039InnerJSONNested)
	}
}
