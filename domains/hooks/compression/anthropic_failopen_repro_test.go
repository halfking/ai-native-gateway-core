package compression

// R35 复现钉测（R34 §四#1 / D03 域文档 2026-10-03 回注）：压缩车道结构性
// fail-open 的可执行复现。本文件钉的是【当前缺陷行为】——期望值刻意
// 断言现状（fail-open / no-op），作为 diff 引擎专项修复的翻转靶：专项落地时
// 应把断言翻转为 delta 命中 / marker 注入，并改写本注记。修复落地前，这三个
// 钉测守住「症状可复现、不被无意改动」。
//
// 病灶①（结构性 fail-open，两车道同构，R36 对照臂订正）：压缩保留布局
// （首条 user 原样 + 尾部 turn 对，中间隔着被压缩的历史）不是客户端完整
// 历史的连续子序列 ⇒ findDeltaAnchor 的「唯一连续后缀匹配」恒失配 ⇒ 压缩
// 后每轮 fail-open 全量重发+重压缩，delta 增益在两条车道都结构性拿不到
// （成本问题，无数据丢失）。OpenAI 车道实证见 Repro-① 对照臂 A/B。
//
// 病灶①的两层叠加（订正 R35 初版「失配系 Anthropic 摘要驻位特异」的结论）：
//   - 布局层（两车道共同根因，充分条件）：保留布局非连续 ⇒ 即使 marker 完全
//     可见且被识别（对照臂 B），relaxed 分支照样失配。
//   - 可见性层（车道特异）：Anthropic 摘要驻顶层 system 字段而
//     hasGatewaySummary 只扫 messages[]；OpenAI 重建体的 dynCtx 摘要消息虽在
//     messages[]，但其前缀是 CompressionSummaryPrefix（"[Gateway compacted
//     conversation summary…"），而 isSummaryMarkerMsg 只认 CompactionMarkerPrefix
//     （"[smm_v1:"）——未经 injectSummaryMarker 打标前同样不被识别为 marker
//     （对照臂 A 走严格前缀分支）。打标（smm_v1）只解决识别，不解决布局。
//
// 病灶②（打标 no-op）：rebuildAnthropicSystemField 对非空 string 形态 system
// 产出 "<orig><prefix><summary>"（追加在原文之后），而
// markerizeAnthropicSystem→isAnthropicSummaryContent 只认「以前缀开头」⇒
// injectSummaryMarker 静默 no-op，病灶①的可见性缺口永远补不上。
//
// 附加观察（R35 新增，R36 推广到两车道，回注 D03）：病灶①单独修「marker
// 可见性」仍拿不到锚点——保留布局是【两段非相邻片段】（首条 user 与尾部
// turn 对中间隔着被压缩的历史，OpenAI 车道同款，见 rebuilder_openai.go
// splitSystemAndTail），不是客户端历史的连续子序列，唯一连续后缀匹配仍失配
// （对照臂 B 实证）。专项须处理两段式锚点（首条 user 例外 + 连续尾部后缀），
// 或改两车道保留布局为纯连续尾部——两车道须同修，非 Anthropic 专项。

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
// +新增一条消息时 delta 引擎 fail-open。三个对照臂（R36 订正 R35 初版结论：
// 初版对照臂手工构造了产线不存在的 [summary,u3,a3] 连续布局并断言「OpenAI
// 命中锚点」，把失配误归因于「Anthropic 摘要驻位的结构性问题」——真实 OpenAI
// 重建体（保留 B-track 首条 user，rebuilder_openai.go splitSystemAndTail）同构
// fail-open，实证见对照臂 A/B）：
//   - A：OpenAI 车道产线重建体（未打标）——dynCtx 摘要前缀不被
//     isSummaryMarkerMsg 识别 ⇒ 严格前缀分支 + 布局非连续 ⇒ fail-open；
//   - B：A + smm_v1 打标（injectSummaryMarker）——marker 被识别 ⇒ relaxed
//     分支，但保留段 [u1,u3,a3] 非连续 ⇒ 仍 fail-open。隔离「布局非连续」
//     为充分条件：只修可见性拿不到 delta 增益；
//   - C：人工连续布局（[smm_v1 summary, u3, a3]，非产线形态）——锚点命中，
//     证明 delta 引擎本身在连续布局下工作正常，专项修法方向=布局连续化或
//     两段式锚点。
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
	// 当前行为（病灶①复现）：IsNewSess=true —— 每轮 fail-open 全量重发+重压缩。
	// diff 引擎专项落地后此断言应翻转为：IsNewSess=false 且 DeltaCount=1。
	if !res.IsNewSess {
		t.Fatalf("R35 repro: expected structural fail-open (IsNewSess=true) for Anthropic compressed lane, got delta result DeltaCount=%d — if the diff-engine project landed, flip this assertion and update the note", res.DeltaCount)
	}

	// 对照臂 A（OpenAI 车道，产线重建体未打标）：RebuildOpenAIAfterSummary
	// 同样保留 B-track 首条 user ⇒ [dynCtx summary, u1, u3, a3]。dynCtx 前缀
	// （CompressionSummaryPrefix）不被 isSummaryMarkerMsg（只认 [smm_v1:）识别
	// ⇒ hasGatewaySummary=false 走严格前缀分支 ⇒ 与客户端历史前缀失配。
	// 当前行为：fail-open。专项落地后此断言应翻转为 IsNewSess=false。
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
	if !contrastA.IsNewSess {
		t.Fatalf("contrast A: production OpenAI compressed lane must fail-open too (IsNewSess=true), got DeltaCount=%d — layout discontinuity is lane-agnostic; if the diff-engine project landed, flip this assertion", contrastA.DeltaCount)
	}

	// 对照臂 B（A + smm_v1 打标）：marker 被识别（relaxed 分支），但保留段
	// [u1,u3,a3] 在客户端历史中非连续 ⇒ 唯一连续匹配数=0 ⇒ 仍 fail-open。
	// 隔离「布局非连续」单独充分——修 marker 可见性拿不到增益。
	markerO, markedOpenAI := injectSummaryMarker(rebuiltOpenAI, "openai")
	if markerO == "" || markedOpenAI == nil {
		t.Fatal("contrast B precondition: injectSummaryMarker must mark the production openai rebuild (dynCtx msg is gateway-summary content)")
	}
	contrastB, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, markedOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !contrastB.IsNewSess {
		t.Fatalf("contrast B: marker-recognized but non-contiguous layout must still fail-open (IsNewSess=true), got DeltaCount=%d — visibility fix alone gains nothing; flip when the diff-engine project lands", contrastB.DeltaCount)
	}

	// 对照臂 C（人工连续布局，非产线形态）：保留段恰为客户端历史的连续后缀
	// 时 relaxed 分支唯一命中 ⇒ 锚点命中。证明引擎本身工作正常——失配的
	// 决定变量是布局连续性，不是摘要驻位或压缩语义。
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

// Repro-②：非空 string 形态 system 的压缩体，injectSummaryMarker 静默 no-op
// （摘要追加在原文之后，isAnthropicSummaryContent 只认前缀开头）。对照臂：
// blocks 形态（新增 gateway block 以前缀开头）打标成功——证明 no-op 是
// string 形态追加位置的结构性问题。
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
	// 当前行为（病灶②复现）：no-op——marker 为空、body 原样丢弃。
	// 专项落地后应翻转为 marker!="" && marked!=nil。
	if marker != "" || marked != nil {
		t.Fatalf("R35 repro: expected silent no-op for non-empty string system, got marker=%q marked=%v — if the diff-engine project landed, flip this assertion and update the note", marker, marked != nil)
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
