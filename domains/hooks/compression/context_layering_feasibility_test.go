package compression

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件钉住「上下文分层加载」方案在 L0（索引层）所依赖的**真实**装配机制：
//
//	L0-a 置顶：RebuildOpenAIAfterSummary 把摘要放在【system 之后、保留头之前】，
//	  即「最上层」—— 这正是用户设想的「最上层放一份执行摘要」。
//	L0-b 保尾：同时保留最近 keepRecentPairs 组消息，模型仍看得到最近的任务。
//	L0-c 形状可判：摘要落在一条 role=user 的 dynamic_context 消息里，
//	  带固定前缀 ⇒ 下游/运维可稳定识别「这段上下文是被网关压缩过的」。
//
// ★ 这三条是**现状能力**，不是新造机制：压缩链第 2 档
//   （tryLLMContextCompaction → RebuildOpenAIAfterSummary）今天就在用。
//   缺的不是「怎么把摘要放到最上层」，而是「摘要里放什么、怎么分层」
//   —— 见审计档结论。

// buildLayeringBody 造一条 12 条消息的 OpenAI 形状请求体。
func buildLayeringBody() []byte {
	msgs := []map[string]any{
		{"role": "system", "content": "you are a coding assistant"},
		{"role": "user", "content": "first user request (pinned by B-track)"},
		{"role": "assistant", "content": "answer 1"},
		{"role": "user", "content": "question 2"},
		{"role": "assistant", "content": "answer 2"},
		{"role": "user", "content": "question 3"},
		{"role": "assistant", "content": "answer 3"},
		{"role": "user", "content": "question 4"},
		{"role": "assistant", "content": "answer 4"},
		{"role": "user", "content": "question 5"},
		{"role": "assistant", "content": "answer 5"},
		{"role": "user", "content": "the most recent question"},
	}
	raw, _ := json.Marshal(map[string]any{
		"model":    "gpt-x",
		"stream":   true,
		"messages": msgs,
	})
	return raw
}

// TestLayering_SummaryIsPlacedAtTheTop —— L0-a：摘要置顶。
func TestLayering_SummaryIsPlacedAtTheTop(t *testing.T) {
	body := buildLayeringBody()
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatalf("extractOpenAI: %v", err)
	}

	summary := "步1 | 目标: 改鉴权重试 | 结果: 已在 internal/auth/client.go 落地"
	out, ok := RebuildOpenAIAfterSummary(body, summary, ret, 2)
	if !ok {
		t.Fatal("RebuildOpenAIAfterSummary returned false")
	}

	var probe struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("unmarshal rebuilt body: %v", err)
	}

	// 位置 0 = system（A-track），位置 1 = 摘要（C-track）⇒ 摘要就在最上层。
	require2 := func(cond bool, msg string, args ...any) {
		t.Helper()
		if !cond {
			t.Fatalf(msg, args...)
		}
	}
	require2(len(probe.Messages) >= 2, "rebuilt body too short: %d", len(probe.Messages))

	var m0, m1 struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(probe.Messages[0], &m0)
	_ = json.Unmarshal(probe.Messages[1], &m1)

	require2(m0.Role == "system", "message[0] must be the system message, got role=%q", m0.Role)
	require2(m1.Role == "user", "message[1] must be the dynamic_context user message, got role=%q", m1.Role)
	require2(strings.Contains(m1.Content, summary), "message[1] must carry the summary verbatim")
	require2(strings.HasPrefix(m1.Content, CompressionSummaryPrefix),
		"message[1] must carry the stable compression marker prefix, got %q", truncateForLog([]byte(m1.Content), 120))

	// 其余顶层键原样保留（model / stream）——装配不能动传输契约。
	require2(probe.Model == "gpt-x", "model must be preserved, got %q", probe.Model)
	require2(probe.Stream, "stream must be preserved")
}

// TestLayering_RecentTailIsPreserved —— L0-b：最近的任务仍然可见。
//
// 「注意力放在最近的任务」⇒ 压缩不能把最近几轮一起压掉。
func TestLayering_RecentTailIsPreserved(t *testing.T) {
	body := buildLayeringBody()
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatalf("extractOpenAI: %v", err)
	}
	out, ok := RebuildOpenAIAfterSummary(body, "SUMMARY", ret, 2)
	if !ok {
		t.Fatal("RebuildOpenAIAfterSummary returned false")
	}

	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	joined := string(probe.Messages[len(probe.Messages)-1])
	if !strings.Contains(joined, "the most recent question") {
		t.Fatalf("the most recent user message must survive compaction, got tail=%s", joined)
	}
}

// TestLayering_SummaryTextIsFreeFormButMarkerIsStable —— L0-c：形状稳定可判。
//
// 摘要正文可以是任意 LLM 产物（这点很重要：它让 L0 从「一段总结」升级成
// 「一行一步的目标与结果索引」不需要改装配器），但承载它的那条消息
// 必须带固定前缀 ⇒ 运维/下游可稳定判定「这是网关装配的上下文」。
func TestLayering_SummaryTextIsFreeFormButMarkerIsStable(t *testing.T) {
	body := buildLayeringBody()
	ret, err := extractOpenAI(body)
	if err != nil {
		t.Fatalf("extractOpenAI: %v", err)
	}
	// 索引形态的摘要（多行、每行一步）—— 这是用户设想里 L0 的实际长相。
	index := "【执行摘要索引（每一步的目标与结果）】\n" +
		"- 步1 | 目标: 改鉴权重试 | 结果: 已在 internal/auth/client.go 落地\n" +
		"- 步2 | 目标: 发布定价页 | 结果: 定价页已渲染生效期价版本\n"
	out, ok := RebuildOpenAIAfterSummary(body, index, ret, 2)
	if !ok {
		t.Fatal("RebuildOpenAIAfterSummary returned false")
	}
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(probe.Messages) < 2 {
		t.Fatalf("rebuilt body too short: %d", len(probe.Messages))
	}
	var m1 struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	_ = json.Unmarshal(probe.Messages[1], &m1)

	// ★ 在【解码后】的消息内容上断言（压缩前缀以 \n 结尾，raw JSON 里是转义的）。
	if !strings.Contains(m1.Content, "执行摘要索引") {
		t.Fatalf("multi-line index must survive the rebuild verbatim, got %q", truncateForLog([]byte(m1.Content), 200))
	}
	if !strings.Contains(m1.Content, "internal/auth/client.go") {
		t.Fatal("the index must keep the exact identifiers (files/paths) — that is what makes a reload decision possible")
	}
	if !strings.HasPrefix(m1.Content, CompressionSummaryPrefix) {
		t.Fatalf("the stable marker prefix must be present, got %q", truncateForLog([]byte(m1.Content), 120))
	}
}
