package compression

// diff 引擎专项落地后的行为钉测（R38，翻转自 R35 复现钉测）。
// R35/R36 时期本文件钉的是【缺陷行为】（结构性 fail-open / 打标 no-op）
// 作为专项翻转靶；R38 专项落地（diff.go 两段式锚点 + isSummaryMarkerMsg
// 认未打标摘要前缀 + Anthropic system 摘要检测 + markerize string 形态
// blocks 规范化）后断言已翻转，本文件现在守住【修复后行为】：
// 压缩车道下一轮 delta 命中（IsNewSess=false）+ string 形态打标成功。
// 函数名保留 R35 复现史（_R35 后缀）以维持审计锚定。
//
// 病灶①（结构性 fail-open，两车道同构，R36 对照臂订正）的修复分两层：
//   - 布局层（findTwoSegmentAnchor）：保留布局是【两段非相邻片段】（首条
//     user 头段 + 连续尾部段，中间被压缩），连续唯一匹配恒失配。修=锚点
//     回退两段式：尾段连续唯一命中定锚，头段在锚前连续出现作血统证据；
//     分解点歧义或尾段非唯一仍 fail-open（与连续分支同款唯一性纪律）。
//   - 可见性层（isSummaryMarkerMsg 加宽 + hasAnthropicSystemSummary 深检）：
//     未打标的 dynCtx 摘要（CompressionSummaryPrefix）与 Anthropic 顶层
//     system 摘要（含 string 形态中部驻留、已打标 [smm_v1: 前缀形态）现在
//     都被识别为压缩血统，进 relaxed 分支。
//
// 病灶②（打标 no-op）的修复：markerizeAnthropicSystem string 分支改用
// anthropicSummaryIndex 找中部摘要，并规范化为 blocks 形态产出
// （orig 块 + marker 行打头的摘要块）——marker 行必须是摘要文本的第一
// 行，summaryMarkerContent 下次才能剥它（幂等），且原提示词顺序不变。

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
// +新增一条消息时 delta 引擎命中（R38 翻转）。三个对照臂（R36 订正 R35 初版
// 结论后定型，R38 全部翻转）：
//   - A：OpenAI 车道产线重建体（未打标）——dynCtx 摘要以
//     CompressionSummaryPrefix 打头，isSummaryMarkerMsg 加宽后识别 ⇒ relaxed
//     分支 + 两段式锚点 ⇒ delta 命中（可见性层修复的承重臂）；
//   - B：A + smm_v1 打标（injectSummaryMarker）——marker 被识别（原有语义）
//     ⇒ relaxed 分支 + 两段式 ⇒ delta 命中（布局层修复的承重臂：B 单独
//     翻转证明两段式锚点在「marker 可见但布局非连续」下拿回增益）；
//   - C：人工连续布局（[smm_v1 summary, u3, a3]，非产线形态）——连续唯一
//     命中（原有语义），证明两段式分支没有吸收连续形态的行为。
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
	// 修复后行为（R38 翻转）：Anthropic system 摘要被识别（可见性层）+
	// 两段式锚点命中 ⇒ IsNewSess=false 且 DeltaCount=1。
	if res.IsNewSess || res.DeltaCount != 1 {
		t.Fatalf("R38 anchor: expected two-segment anchor hit (IsNewSess=false, DeltaCount=1) for Anthropic compressed lane, got IsNewSess=%v DeltaCount=%d — the Anthropic system-summary visibility or the two-segment anchor regressed", res.IsNewSess, res.DeltaCount)
	}

	// 对照臂 A（OpenAI 车道，产线重建体未打标）：RebuildOpenAIAfterSummary
	// 同样保留 B-track 首条 user ⇒ [dynCtx summary, u1, u3, a3]。dynCtx 前缀
	// （CompressionSummaryPrefix）被加宽后的 isSummaryMarkerMsg 识别 ⇒
	// hasGatewaySummary=true 走 relaxed 分支 ⇒ 两段式锚点命中。
	// 修复后行为（R38 翻转）：delta 命中。
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
		t.Fatalf("contrast A: unmarked dynCtx summary must be recognised and anchor (IsNewSess=false, DeltaCount=1), got IsNewSess=%v DeltaCount=%d — the isSummaryMarkerMsg widening for CompressionSummaryPrefix regressed", contrastA.IsNewSess, contrastA.DeltaCount)
	}

	// 对照臂 B（A + smm_v1 打标）：marker 被识别（原有语义），保留段
	// [u1,u3,a3] 非连续 ⇒ 两段式锚点。修复后行为（R38 翻转）：delta 命中。
	// B 与 A 同时命中隔离两层修复各自承重：删可见性加宽则 A 红而 B 绿，
	// 删两段式分支则 B 红而（连续形态的）C 绿。
	markerO, markedOpenAI := injectSummaryMarker(rebuiltOpenAI, "openai")
	if markerO == "" || markedOpenAI == nil {
		t.Fatal("contrast B precondition: injectSummaryMarker must mark the production openai rebuild (dynCtx msg is gateway-summary content)")
	}
	contrastB, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, markedOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if contrastB.IsNewSess || contrastB.DeltaCount != 1 {
		t.Fatalf("contrast B: marked but non-contiguous layout must anchor via the two-segment path (IsNewSess=false, DeltaCount=1), got IsNewSess=%v DeltaCount=%d — findTwoSegmentAnchor regressed", contrastB.IsNewSess, contrastB.DeltaCount)
	}

	// 对照臂 C（人工连续布局，非产线形态）：保留段恰为客户端历史的连续后缀
	// 时 relaxed 分支唯一命中 ⇒ 锚点命中（保绿锚，R38 未翻转——证明两段式
	// 分支没有改变连续形态的行为路径）。
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

// Repro-②：非空 string 形态 system 的压缩体，injectSummaryMarker 打标
// 成功（R38 翻转）。修复=markerizeAnthropicSystem string 分支用
// anthropicSummaryIndex 找中部摘要，规范化为 blocks 产出（orig 块 +
// marker 行打头的摘要块），marker 行在摘要文本第一行保幂等。对照臂
// blocks 形态（新增 gateway block 以前缀开头）打标成功——R35 时期证明
// no-op 是 string 形态追加位置的结构性问题。
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
	// 修复后行为（R38 翻转）：打标成功——marker 非空、body 为 blocks 规范
	// 形态。幂等钉：再次打标返回相同 marker 且不再变形。
	if marker == "" || marked == nil {
		t.Fatalf("R38 markerize: expected successful marking for non-empty string system, got marker=%q marked=%v — the string-shape normalisation regressed", marker, marked != nil)
	}
	marker2, marked2 := injectSummaryMarker(marked, "anthropic-messages")
	if marker2 != marker || marked2 == nil {
		t.Fatalf("R38 markerize idempotency: re-marking must return the same marker, got first=%q second=%q", marker, marker2)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(marked, &top); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(top["system"], &blocks); err != nil {
		t.Fatalf("R38 markerize: marked system must be block shape, got %s", top["system"])
	}
	if len(blocks) != 2 {
		t.Fatalf("R38 markerize: expected [orig, marked summary] blocks, got %d: %+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].Text, "You are Claude.") {
		t.Fatalf("R38 markerize: first block must carry the original prompt, got %q", blocks[0].Text)
	}
	if !strings.HasPrefix(blocks[1].Text, marker) || !strings.Contains(blocks[1].Text, "summary text") {
		t.Fatalf("R38 markerize: second block must start with the marker line and carry the summary, got %q", blocks[1].Text)
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
