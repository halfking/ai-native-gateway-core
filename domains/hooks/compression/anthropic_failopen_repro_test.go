package compression

// R35 复现钉测（R34 §四#1 / D03 域文档 2026-10-03 回注；R36 对照臂订正；
// diff 引擎专项 2026-10-03 落地后翻转）。本文件最初钉的是【缺陷行为】
// （fail-open / 打标 no-op），作为专项的翻转靶；专项落地后断言已翻转为
// 期望终态（delta 命中 / marker 注入），并保留三臂结构作为回归基线：
// 任何一侧识别层或锚点层回退，此文件转红。
//
// 病灶①（结构性 fail-open，两车道同构，R36 对照臂订正）：压缩保留布局
// （首条 user 原样 + 尾部 turn 对，中间隔着被压缩的历史）不是客户端完整
// 历史的连续子序列 ⇒ 旧 findDeltaAnchor 的「唯一连续后缀匹配」恒失配 ⇒
// 压缩后每轮 fail-open 全量重发+重压缩，delta 增益在两条车道都结构性拿不到。
//
// 修复（两段式锚点，两车道同修）：findDeltaAnchor 的 compressed 分支枚举
// head/tail 切分点——head（B-track 首段）须等于客户端历史前缀，tail（连续
// 保留段）须在其后唯一连续出现；多个切分点给出不同锚点=歧义，fail-open
// 不降级（与旧「重复出现 fail-open」同一保守契约）。
//
// 可见性层（车道特异，随专项一并修复）：
//   - Anthropic 摘要驻顶层 system 字段（string 形态为 append 形态）——引擎的
//     gateway-summary 判定现包含 hasAnthropicSystemSummary（前缀形态 +
//     追加分隔符 contains 双代际），不再只扫 messages[]；
//   - OpenAI 重建体未打标的 dynCtx 摘要（CompressionSummaryPrefix 前缀，
//     双代际的另一代）同样计入 gateway-summary 判定并从可比对段剔除。
//
// 病灶②（打标 no-op，已修）：rebuildAnthropicSystemField 对非空 string 形态
// system 产出 "<orig><prefix><summary>"（追加在原文之后——保序原文在前以
// 维持上游 prompt cache 前缀稳定，刻意不改为前置摘要），而
// markerizeAnthropicSystem→isAnthropicSummaryContent 只认「以前缀开头」⇒
// injectSummaryMarker 静默 no-op。修复后识别层接受追加分隔符形态
// （containsAnthropicSummarySeparator），smm_v1 标记前置于完整 system 文本。

import (
	"encoding/json"
	"strings"
	"testing"
)

func anthroBody(t *testing.T, system string, msgs []string) []byte {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(msgs))
	for i, m := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		raw = append(raw, json.RawMessage(`{"role":"`+role+`","content":`+quoteJSON(t, m)+`}`))
	}
	b, err := json.Marshal(map[string]any{
		"model":    "claude-test",
		"system":   system,
		"messages": raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Repro-①：真实 RebuildAnthropicAfterSummary 产出的压缩体，下一轮全量重发
// +新增一条消息时 delta 引擎必须命中锚点（修复前为结构性 fail-open）。三个
// 臂保留 R36 对照结构（R36 订正 R35 初版结论：初版对照臂手工构造了产线不存在的
// [summary,u3,a3] 连续布局并断言「OpenAI 命中锚点」，把失配误归因于「Anthropic
// 摘要驻位的结构性问题」——真实 OpenAI 重建体（保留 B-track 首条 user，
// rebuilder_openai.go splitSystemAndTail）同构 fail-open，实证见对照臂 A/B）：
//   - A：OpenAI 车道产线重建体（未打标）——未打标 dynCtx 前缀族计入
//     gateway-summary 判定后走 relaxed 两段式锚点 ⇒ 命中；
//   - B：A + smm_v1 打标（injectSummaryMarker）——marker 被识别 ⇒ relaxed
//     两段式锚点（布局非连续由 head+tail 切分处理）⇒ 命中。修复前此臂曾
//     隔离「布局非连续」为充分条件：只修可见性拿不到 delta 增益；
//   - C：人工连续布局（[smm_v1 summary, u3, a3]，非产线形态）——k=0 退化
//     路径（head 空 + tail 唯一连续出现），保绿锚证明引擎本身正常。
func TestRepro_AnthropicCompressedDeltaFailsOpen_R35(t *testing.T) {
	const summary = "prior turns: user greeted, assistant answered twice"
	body := anthroBody(t, "You are Claude.", []string{
		"u1 hello", "a1 hi", "u2 how are you", "a2 fine", "u3 and now", "a3 ok",
	})
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, ok := RebuildAnthropicAfterSummary(body, summary, ret, 1)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	// 前提钉：保留布局 = 首条 user + 尾部对（keepRecentPairs=1 → 尾部 2 条）。
	// [u1, u3, a3] 不是客户端历史 [u1,a1,u2,a2,u3,a3] 的连续子序列。
	rebuiltMsgs, err := extractMessages(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rebuiltMsgs) != 3 {
		t.Fatalf("precondition: expected retained layout [u1,u3,a3], got %d msgs: %s", len(rebuiltMsgs), rebuiltMsgs)
	}
	if !strings.Contains(string(rebuiltMsgs[0]), "u1 hello") {
		t.Fatalf("precondition: first retained msg must be B-track first user, got %s", rebuiltMsgs[0])
	}

	// 下一轮：客户端全量重发 + 新增 u4（客户端无压缩感知）。
	next := anthroBody(t, "You are Claude.", []string{
		"u1 hello", "a1 hi", "u2 how are you", "a2 fine", "u3 and now", "a3 ok", "u4 new question",
	})
	res, err := BuildOutboundMessages(next, &SessionState{SchemaVersion: 1}, rebuilt, "anthropic-messages")
	if err != nil {
		t.Fatal(err)
	}
	// 修复后（两段式锚点 + system 字段 gateway-summary 判定）：B-track 首条
	// user 锚客户端前缀、尾部 [u3,a3] 唯一连续出现 ⇒ 锚点命中，仅 u4 是增量。
	if res.IsNewSess || res.DeltaCount != 1 {
		t.Fatalf("anthropic compressed lane must anchor via two-segment match (IsNewSess=false, DeltaCount=1), got IsNewSess=%v DeltaCount=%d — anchor or system-summary detection regressed", res.IsNewSess, res.DeltaCount)
	}

	// 对照臂 A（OpenAI 车道，产线重建体未打标）：RebuildOpenAIAfterSummary
	// 同样保留 B-track 首条 user ⇒ [dynCtx summary, u1, u3, a3]。修复后：未打标
	// dynCtx（CompressionSummaryPrefix 前缀族）计入 gateway-summary 判定并从
	// 可比对段剔除 ⇒ relaxed 两段式锚点命中 ⇒ IsNewSess=false，仅 u4 是增量。
	openAIBody := makeBody([]map[string]string{
		userMsg("u1 hello"), assistantMsg("a1 hi"),
		userMsg("u2 how are you"), assistantMsg("a2 fine"),
		userMsg("u3 and now"), assistantMsg("a3 ok"),
	})
	retO, err := extractOpenAI(openAIBody)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltOpenAI, ok := RebuildOpenAIAfterSummary(openAIBody, summary, retO, 1)
	if !ok {
		t.Fatal("openai rebuild must succeed")
	}
	openAIMsgs, err := extractMessages(rebuiltOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	// 前提钉：产线保留布局 = [dynCtx summary, u1, u3, a3]（首条 user 在摘要后）。
	if len(openAIMsgs) != 4 {
		t.Fatalf("contrast A precondition: expected production layout [summary,u1,u3,a3], got %d msgs", len(openAIMsgs))
	}
	if !strings.Contains(string(openAIMsgs[1]), "u1 hello") {
		t.Fatalf("contrast A precondition: second msg must be B-track first user, got %s", openAIMsgs[1])
	}
	clientOpenAI := makeBody([]map[string]string{
		userMsg("u1 hello"), assistantMsg("a1 hi"),
		userMsg("u2 how are you"), assistantMsg("a2 fine"),
		userMsg("u3 and now"), assistantMsg("a3 ok"),
		userMsg("u4 new question"),
	})
	contrastA, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, rebuiltOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if contrastA.IsNewSess || contrastA.DeltaCount != 1 {
		t.Fatalf("contrast A: production OpenAI compressed lane must anchor via two-segment match (IsNewSess=false, DeltaCount=1), got IsNewSess=%v DeltaCount=%d — unmarked-dynCtx detection or two-segment anchor regressed", contrastA.IsNewSess, contrastA.DeltaCount)
	}

	// 对照臂 B（A + smm_v1 打标）：marker 被识别（relaxed 分支），保留段
	// [u1,u3,a3] 在客户端历史中非连续。修复前此臂曾实证「仅修 marker 可见性
	// 拿不到增益」（连续匹配恒失配）；两段式锚点落地后 head=首条 user +
	// tail=[u3,a3] 命中 ⇒ IsNewSess=false，仅 u4 是增量。
	markerO, markedOpenAI := injectSummaryMarker(rebuiltOpenAI, "openai")
	if markerO == "" || markedOpenAI == nil {
		t.Fatal("contrast B precondition: injectSummaryMarker must mark the production openai rebuild (dynCtx msg is gateway-summary content)")
	}
	contrastB, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, markedOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if contrastB.IsNewSess || contrastB.DeltaCount != 1 {
		t.Fatalf("contrast B: marker-recognized non-contiguous layout must anchor via two-segment match (IsNewSess=false, DeltaCount=1), got IsNewSess=%v DeltaCount=%d — two-segment anchor regressed", contrastB.IsNewSess, contrastB.DeltaCount)
	}

	// 对照臂 C（人工连续布局，非产线形态）：保留段恰为客户端历史的连续后缀。
	// 修复前此臂证明引擎在连续布局下工作正常；两段式锚点落地后它走 k=0
	// （head 空）退化路径——即旧 relaxed 单段匹配的一般化特例，保绿锚。
	lastOpenAI := makeBody([]map[string]string{
		summaryMsg("prior turns"),
		userMsg("u3 and now"),
		assistantMsg("a3 ok"),
	})
	contrastC, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, lastOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if contrastC.IsNewSess || contrastC.DeltaCount != 1 {
		t.Fatalf("contrast C: continuous retained suffix must anchor (IsNewSess=false, DeltaCount=1), got %+v", contrastC)
	}
}

// Repro-③（R34 §四#4 配套）：Recover persist 路径 Stabilize 失败残留的根因
// 语义钉——buildSessionState 的空值守卫在 res 无新 hash 时继承 prev 的
// CompressedPrefixHash。该继承对 CommitFinal（append 保持前缀稳定，旧 hash
// 仍有效）是正确语义；但对 smart_window 重写 body 的 Recover 路径是残留
// （旧 hash 对新 body 失效），故 recovery_coordinator 在 persist 后显式清零
// 补偿（见 recover persist 块守卫）。此钉守住该守卫语义不被无意改动——
// 若有人收紧/放宽 buildSessionState 空值守卫，此处红即语义变更信号。
// （Stabilize 失败态在真实 Recover 路径无注入缝——prefix 为包级函数，钉测
// 退守根因语义面，深度限制记 R35 轮文档。）
func TestRepro_BuildSessionStateInheritsPrevPrefixHashOnEmpty_R35(t *testing.T) {
	prev := &SessionState{SchemaVersion: 1, CompressedPrefixHash: "STALE_HASH"}
	res := &PrepareResult{MsgCount: 3, TokenEst: 100}
	state := buildSessionState(prev, []byte(`{"messages":[{"role":"user","content":"x"}]}`), res, true, 1)
	if state.CompressedPrefixHash != "STALE_HASH" {
		t.Fatalf("root-cause pin: empty res.CompressedPrefixHash must inherit prev hash, got %q — buildSessionState guard semantics changed; re-check recover-path compensation and cache-hit fast path", state.CompressedPrefixHash)
	}
}

// Repro-②：非空 string 形态 system 的压缩体（摘要追加在原文之后的产线
// 最常见形态）。修复前 injectSummaryMarker 静默 no-op（isAnthropicSummaryContent
// 只认前缀开头）；修复后识别层接受追加分隔符形态（containsAnthropicSummarySeparator），
// smm_v1 标记前置于完整 system 文本。对照臂：blocks 形态（新增 gateway block
// 以前缀开头）一直可打标。
func TestRepro_AnthropicInjectSummaryMarkerStringShapeNoOp_R35(t *testing.T) {
	// string 形态（原文非空，最常见生产形态）。
	bodyStr := anthroBody(t, "You are Claude.", []string{"u1 hello", "a1 hi", "u2 q"})
	ret, err := extractAnthropic(bodyStr)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltStr, ok := RebuildAnthropicAfterSummary(bodyStr, "summary text", ret, 2)
	if !ok {
		t.Fatal("rebuild (string system) must succeed")
	}
	marker, marked := injectSummaryMarker(rebuiltStr, "anthropic-messages")
	// 修复后（病灶②收口）：追加分隔符形态被识别 ⇒ smm_v1 标记前置于完整
	// system 文本，body 带标记返回。
	if marker == "" || marked == nil {
		t.Fatalf("string-shaped anthropic system with appended summary must markerize (marker prefixed), got marker=%q marked=%v — marker recognition regressed", marker, marked != nil)
	}

	// 对照臂（blocks 形态）：新增 gateway block 的文本以前缀开头 → 打标成功。
	blocksSys, err := json.Marshal([]map[string]string{
		{"type": "text", "text": "You are Claude."},
		{"type": "text", "text": "Be concise."},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawMsgs := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"u1 hello"}`),
		json.RawMessage(`{"role":"assistant","content":"a1 hi"}`),
		json.RawMessage(`{"role":"user","content":"u2 q"}`),
	}
	bodyBlocks, err := json.Marshal(map[string]any{
		"model":    "claude-test",
		"system":   json.RawMessage(blocksSys),
		"messages": rawMsgs,
	})
	if err != nil {
		t.Fatal(err)
	}
	retB, err := extractAnthropic(bodyBlocks)
	if err != nil {
		t.Fatal(err)
	}
	rebuiltBlocks, ok := RebuildAnthropicAfterSummary(bodyBlocks, "summary text", retB, 2)
	if !ok {
		t.Fatal("rebuild (blocks system) must succeed")
	}
	markerB, markedB := injectSummaryMarker(rebuiltBlocks, "anthropic-messages")
	if markerB == "" || markedB == nil {
		t.Fatalf("contrast arm: blocks-shaped system must markerize (marker injected), got marker=%q", markerB)
	}
}
