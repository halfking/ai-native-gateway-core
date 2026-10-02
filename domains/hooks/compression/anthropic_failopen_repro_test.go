package compression

// R35 复现钉测（R34 §四#1 / D03 域文档 2026-10-03 回注）：Anthropic 车道
// 结构性 fail-open 的可执行复现。本文件钉的是【当前缺陷行为】——期望值刻意
// 断言现状（fail-open / no-op），作为 diff 引擎专项修复的翻转靶：专项落地时
// 应把断言翻转为 delta 命中 / marker 注入，并改写本注记。修复落地前，这两个
// 钉测守住「症状可复现、不被无意改动」。
//
// 病灶①（结构性 fail-open）：Anthropic 摘要驻顶层 system 字段，而
// findDeltaAnchor 的 hasGatewaySummary 只扫 messages[] ⇒ 恒走「无摘要→严格
// 前缀匹配」分支；Anthropic 保留布局（首条 user 原样 + 尾部 turn 对）不是
// 客户端完整历史的前缀 ⇒ 压缩后每轮 fail-open 全量重发+重压缩，delta 增益
// 在 Anthropic 车道结构性拿不到（成本问题，无数据丢失）。
//
// 病灶②（打标 no-op）：rebuildAnthropicSystemField 对非空 string 形态 system
// 产出 "<orig><prefix><summary>"（追加在原文之后），而
// markerizeAnthropicSystem→isAnthropicSummaryContent 只认「以前缀开头」⇒
// injectSummaryMarker 静默 no-op，病灶①的可见性缺口永远补不上。
//
// 附加观察（R35 新增，回注 D03）：病灶①单独修「marker 可见性」仍拿不到
// 锚点——Anthropic 保留布局是【两段非相邻片段】（首条 user 与尾部 turn 对
// 中间隔着被压缩的历史），不是客户端历史的连续子序列，唯一连续后缀匹配
// 仍失配。专项须处理两段式锚点（首条 user 例外 + 连续尾部后缀），或改
// Anthropic 保留布局为纯连续尾部。

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
// +新增一条消息时 delta 引擎 fail-open。同构 OpenAI 车道（摘要 marker 驻
// messages[]）在完全对称的场景命中锚点——证明失配是 Anthropic 摘要驻位的
// 结构性问题，而非压缩语义本身。
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

	// 对照臂（OpenAI 车道，同构场景）：摘要 marker 驻 messages[] 时唯一后缀
	// 匹配命中锚点，DeltaCount=1。
	lastOpenAI := makeBody([]map[string]string{
		summaryMsg("prior turns"),
		userMsg("u3 and now"),
		assistantMsg("a3 ok"),
	})
	clientOpenAI := makeBody([]map[string]string{
		userMsg("u1 hello"), assistantMsg("a1 hi"),
		userMsg("u2 how are you"), assistantMsg("a2 fine"),
		userMsg("u3 and now"), assistantMsg("a3 ok"),
		userMsg("u4 new question"),
	})
	contrast, err := BuildOutboundMessages(clientOpenAI, &SessionState{SchemaVersion: 1}, lastOpenAI, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if contrast.IsNewSess || contrast.DeltaCount != 1 {
		t.Fatalf("contrast arm: OpenAI compressed lane must anchor (IsNewSess=false, DeltaCount=1), got %+v", contrast)
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
