package admin

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func strp(v string) *string { return &v }

func TestSanitizeSummaryText_RemovesToolsAndTags(t *testing.T) {
	raw := `
<think>private reasoning</think>
{"tool_calls":[{"id":"a1","name":"search"}]}
trace_id=abc-123-xyz-0001
正文：用户要求生成总结。
`
	got := sanitizeSummaryText(raw)
	if strings.Contains(got, "tool_calls") || strings.Contains(got, "<think>") || strings.Contains(got, "trace_id") {
		t.Fatalf("sanitizeSummaryText did not remove noise, got=%q", got)
	}
	if !strings.Contains(got, "正文") {
		t.Fatalf("sanitizeSummaryText removed useful semantic content, got=%q", got)
	}
}

func TestBuildSummaryCorpus_RequiresUsefulContent(t *testing.T) {
	now := time.Now().UTC()
	logs := []sessionLogForSummary{
		{Ts: now, RequestPreview: strp(`<tool>{"tool_results":[1]}</tool>`), RequestStatus: "success"},
		{Ts: now.Add(time.Second), ResponsePreview: strp("用户希望比较两个方案并给出建议"), RequestStatus: "success"},
	}
	corpus := buildSummaryCorpus(logs)
	if strings.Contains(corpus, "tool_results") || strings.Contains(corpus, "<tool>") {
		t.Fatalf("corpus still contains tool markers: %q", corpus)
	}
	if !strings.Contains(corpus, "用户希望比较两个方案并给出建议") {
		t.Fatalf("expected preserved semantic text, got=%q", corpus)
	}
}

// TestBuildSummaryCorpusRuneGuardCJKBand 钉死 2026-09-29 二十二轮修复：
// 守卫与截断必须同为 rune 口径。原实现守卫按字节（len(corpus)>12000）而
// 截断按 rune（runes[:12000]）——CJK 语料落在 12001~36000 字节且 rune 数
// <12000 的带宽（本用例 100 轮×30 CJK ≈ 12300B / ≈6300 runes）时直接
// slice bounds out of range panic，logs-summary 端点被打挂。
func TestBuildSummaryCorpusRuneGuardCJKBand(t *testing.T) {
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	logs := make([]sessionLogForSummary, 100)
	for i := range logs {
		logs[i].Ts = now
		logs[i].RequestPreview = strp(strings.Repeat("会话总结测试内容详尽记录", 3)) // 30 CJK
		logs[i].RequestStatus = "success"
	}
	corpus := buildSummaryCorpus(logs) // 修复前此行 panic
	if runes := []rune(corpus); len(runes) >= sessionSummaryMaxCorpusLen {
		t.Fatalf("band corpus should stay below the rune cap, runes=%d", len(runes))
	}
	if !utf8.ValidString(corpus) {
		t.Fatalf("corpus is not valid UTF-8")
	}
}

// TestBuildSummaryCorpusRuneGuardTruncatesAtRuneCap 验证真截断带：rune 数
// 超过上限时截到恰好 sessionSummaryMaxCorpusLen 个 rune 且不产生非法
// UTF-8 前缀（原字节切行为）。
func TestBuildSummaryCorpusRuneGuardTruncatesAtRuneCap(t *testing.T) {
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	logs := make([]sessionLogForSummary, 200)
	for i := range logs {
		logs[i].Ts = now
		logs[i].RequestPreview = strp(strings.Repeat("会话总结测试内容详尽记录", 10)) // 100 CJK
		logs[i].RequestStatus = "success"
	}
	corpus := buildSummaryCorpus(logs)
	if runes := []rune(corpus); len(runes) != sessionSummaryMaxCorpusLen {
		t.Fatalf("corpus runes=%d, want exactly %d", len(runes), sessionSummaryMaxCorpusLen)
	}
	if !utf8.ValidString(corpus) {
		t.Fatalf("truncated corpus is not valid UTF-8")
	}
}

func TestIsValidSummary(t *testing.T) {
	if isValidSummary("太短", nil) {
		t.Fatal("short summary should be invalid")
	}
	if isValidSummary("无法总结，信息不足。", []string{"x"}) {
		t.Fatal("template rejection summary should be invalid")
	}
	if isValidSummary("<think>这是一段足够长的无效总结内容，包含思考标记不应通过校验。", []string{"要点"}) {
		t.Fatal("thinking tag summary should be invalid")
	}
	if isValidSummary("该会话围绕部署故障定位展开，先确认日志与网络，再逐步缩小到鉴权问题并完成修复验证。最终服务恢复，建议补充回归检查。", []string{"<think>"}) {
		t.Fatal("thinking tag key point should invalidate summary")
	}
	ok := "该会话围绕部署故障定位展开，先确认日志与网络，再逐步缩小到鉴权问题并完成修复验证。最终服务恢复，建议补充回归检查。"
	if !isValidSummary(ok, []string{"先定位", "后修复"}) {
		t.Fatal("expected valid summary to pass")
	}
}

func TestPickSummaryModel_UsesAutoRouting(t *testing.T) {
	if got := adminLLMModelAuto; got != "auto" {
		t.Fatalf("admin summary model = %q, want auto", got)
	}
}

func TestSessionSummaryCache_Expiry(t *testing.T) {
	cs := &cachedSessionSummary{
		Summary:   "test summary",
		KeyPoints: []string{"a", "b"},
		Model:     "auto",
		KeyID:     1,
		CachedAt:  time.Now().Add(-15 * time.Minute),
	}
	if !cs.expired() {
		t.Fatal("15-minute-old cache should be expired")
	}

	cs2 := &cachedSessionSummary{
		Summary:   "fresh",
		KeyPoints: []string{"c"},
		Model:     "auto",
		KeyID:     1,
		CachedAt:  time.Now(),
	}
	if cs2.expired() {
		t.Fatal("fresh cache should not be expired")
	}
}

func TestSessionSummaryCache_StoreLoad(t *testing.T) {
	key := "test-session:42"
	sessionSummaryCache.Store(key, &cachedSessionSummary{
		Summary:   "cached summary",
		KeyPoints: []string{"point1"},
		Model:     "auto",
		KeyID:     1,
		CachedAt:  time.Now(),
	})
	defer sessionSummaryCache.Delete(key)

	val, ok := sessionSummaryCache.Load(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	cs, ok := val.(*cachedSessionSummary)
	if !ok {
		t.Fatal("expected *cachedSessionSummary")
	}
	if cs.Summary != "cached summary" {
		t.Fatalf("unexpected summary: %q", cs.Summary)
	}
}
