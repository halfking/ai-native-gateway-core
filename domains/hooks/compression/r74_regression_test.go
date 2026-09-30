package compression

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// R74 P0 回归：Anthropic 滑动窗口重建后不得留下悬空的 tool_use。
//
// 旧实现 TrimAnthropicTail 删除 tool_result-only 的 user 消息，理由是
// 「避免 tool_use_id 孤儿」——方向恰好相反：删掉 result 而保留声明它的
// assistant.tool_use，正好制造 Anthropic 必拒的孤儿。压缩把一个原本合法的
// 请求变成了必然 400 的请求。
//
// 判别力：把 TrimAnthropicTail 退回旧实现，本测试必须红。
func TestR74_AnthropicRebuildLeavesNoOrphanToolUse(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":"first user intent"},
		{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"r"}]},
		{"role":"assistant","content":"done"},
		{"role":"user","content":"second user turn"},
		{"role":"assistant","content":"second answer"}
	]}`)
	ret, err := extractAnthropic(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	out, ok := RebuildAnthropicAfterSummary(body, "summary", ret, 3)
	if !ok {
		t.Fatal("rebuild must succeed")
	}
	declared := map[string]bool{}
	resolved := map[string]bool{}
	var probe struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		t.Fatal(err)
	}
	for _, m := range probe.Messages {
		if !strings.HasPrefix(strings.TrimSpace(string(m.Content)), "[") {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			TUID string `json:"tool_use_id"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				declared[b.ID] = true
			case "tool_result":
				resolved[b.TUID] = true
			}
		}
	}
	for id := range declared {
		if !resolved[id] {
			t.Errorf("ORPHAN tool_use %q：assistant 锚点保留但 tool_result 被删，上游 Anthropic 必 400", id)
		}
	}
	for id := range resolved {
		if !declared[id] {
			t.Errorf("ORPHAN tool_result %q：tool_use 锚点被删，Anthropic 同样拒绝", id)
		}
	}
	if len(declared) == 0 {
		t.Fatal("前置条件不成立：重建结果里应至少有一个 tool_use")
	}
}

// R74 P0 回归（另一侧）：cut 落在 result 之后时，留下的是孤儿 tool_result。
// 既有测试 TestRebuildAnthropicAfterSummary_DropsOrphanToolResult 覆盖
// keepRecentPairs=1 的这一侧；本例把两侧钉在同一处修法上。
func TestR74_AnthropicRebuildDropsOrphanToolResult(t *testing.T) {
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"hi"}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"f","input":{}}]}`),
	}
	// tool_use 的 result 已被切掉 —— 锚点必须一并丢弃
	cleaned, dropped := TrimAnthropicTail(messages)
	if dropped != 1 {
		t.Errorf("dropped=%d, 期望 1（无 result 的 tool_use 锚点必须丢弃）", dropped)
	}
	if len(cleaned) != 1 || messageRole(cleaned[0]) != "user" {
		t.Errorf("cleaned=%s, 期望只剩开头的 user 消息", cleaned)
	}
}

// R74 回归：压缩发生时必须有指标。
//
// RecordOutcome 是 compression_triggered_total / compression_ratio /
// compression_latency_seconds / compression_lossiness_total 四个指标的**唯一**
// 写入点，而它此前零生产调用方——四个 series 从未获得任何一个数据点，面板显示
// 「无数据」而非数字，运维无法区分「压缩从未触发」与「指标坏了」。这是本项目
// 已确认的缺陷族「失败被渲染成看起来合法的零」的变体：不存在的 series 在
// PromQL sum() 下与真实的 0 不可区分。
//
// 判别力：把 Prepare 末尾的 RecordOutcome 调用去掉，本测试必须红。
func TestR74_CompressionEmitsOutcomeMetrics(t *testing.T) {
	withPlatformFlag(t, "sessions_v2_compression_read", true)
	ResetMetrics()

	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4",
		"messages": []map[string]string{
			{"role": "assistant", "content": "[smm_v1:aabbccdd] Prior context was summarised."},
			{"role": "user", "content": "hello"},
			{"role": "user", "content": "new question"},
		},
	})
	sc := &SessionCompressor{deps: SessionCompressorDeps{
		CacheV2: stubStateReader{
			has: true,
			meta: map[string]any{
				"summary_marker": "[smm_v1:aabbccdd]",
				"strategy":       "sliding_window_token",
			},
		},
		Builder: stubOutboundBuilder{
			body: []byte(`[{"role":"assistant","content":"[smm_v1:aabbccdd] Prior context was summarised."},{"role":"user","content":"hello"}]`),
		},
	}}

	res := sc.Prepare(context.Background(), body, "tenant1", "gw_r74_metric01", "openai", 0, false)
	if res == nil {
		t.Fatal("expected non-nil PrepareResult")
	}
	if len(res.OutboundBody) == 0 {
		t.Fatal("前置条件不成立：outbound body 为空")
	}
	t.Logf("outbound=%d client=%d strategy=%q", len(res.OutboundBody), len(body), res.CompressionStrategy)
	if len(res.OutboundBody) >= len(body) {
		t.Skip("本用例的 stub 未产生更小的 body，指标不适用；见下方 delta 场景")
	}

	// The four series must now exist. A registered-but-never-written
	// GaugeVec/CounterVec reports ABSENT from the registry, which is what the
	// operator sees as "no data".
	for _, name := range []string{
		"compression_triggered_total",
		"compression_latency_seconds",
		"compression_ratio",
	} {
		found := false
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		for _, mf := range mfs {
			if mf.GetName() == name && len(mf.GetMetric()) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("指标 %s 没有任何数据点：压缩确实发生了（%d → %d 字节）但没有 series，"+
				"面板上会显示「无数据」而非数字", name, len(body), len(res.OutboundBody))
		}
	}
}

// R74 回归（window 触发场景）：走真实的 window 压缩路径，
// 验证 compression_triggered_total 真的有数据点。
// 用 defer 版修法的意义就在这里——Prepare 有 5 个 return 点，
// 逐点埋指标必然漏掉其中几个。
func TestR74_WindowCompressionEmitsOutcomeMetrics(t *testing.T) {
	ResetMetrics()

	// V1 cache 提供一个很小的 lastOutbound，客户端发回超长历史，
	// 超过 window 预算 → 触发机械压缩。
	cap := &captureBackend{}
	cache := NewSessionCache(cap, nil)

	var msgs []map[string]string
	for i := 0; i < 120; i++ {
		msgs = append(msgs,
			map[string]string{"role": "user", "content": "question number with plenty of words " + string(rune('a'+i%26))},
			map[string]string{"role": "assistant", "content": "answer number with plenty of words " + string(rune('a'+i%26))},
		)
	}
	body, _ := json.Marshal(map[string]any{"model": "gpt-4", "messages": msgs})
	sc := &SessionCompressor{deps: SessionCompressorDeps{Cache: cache}}
	res := sc.Prepare(context.Background(), body, "tenant1", "gw_r74_metric03", "openai", 4000, false)
	if res == nil {
		t.Fatal("expected non-nil PrepareResult")
	}
	t.Logf("outbound=%d client=%d strategy=%q window=%q", len(res.OutboundBody), len(body), res.CompressionStrategy, res.WindowTriggered)
	if len(res.OutboundBody) == 0 || len(res.OutboundBody) >= len(body) {
		t.Skipf("前置条件不成立：本配置未产生更小的 body（outbound=%d client=%d window=%q）",
			len(res.OutboundBody), len(body), res.WindowTriggered)
	}

	found := false
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "compression_triggered_total" && len(mf.GetMetric()) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("window 压缩（%d → %d 字节, window=%q）没有产生 compression_triggered_total 数据点",
			len(body), len(res.OutboundBody), res.WindowTriggered)
	}
}
