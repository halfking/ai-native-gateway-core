package admin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOverlayCatalogUsageFillsEmptyRedisStats(t *testing.T) {
	item := sessionListItem{SessionID: "gw_1", CurrentModel: ""}
	overlayCatalogUsage(&item, catalogUsage{Turns: 1, Prompt: 99, Completion: 24, CostUSD: 0, Model: "deepseek-v4-flash"})
	if item.TotalTurns != 1 || item.TotalPromptTokens != 99 || item.TotalCompletionTokens != 24 {
		t.Fatalf("usage not overlaid: %+v", item)
	}
	if item.CurrentModel != "deepseek-v4-flash" {
		t.Fatalf("model = %q", item.CurrentModel)
	}
}

func TestOverlayCatalogUsageKeepsNonZeroRedisStats(t *testing.T) {
	item := sessionListItem{
		TotalTurns:            4,
		TotalPromptTokens:     10,
		TotalCompletionTokens: 3,
		TotalCostUSD:          0.2,
		CurrentModel:          "glm-4.7",
	}
	overlayCatalogUsage(&item, catalogUsage{Turns: 1, Prompt: 99, Completion: 24, CostUSD: 1, Model: "other"})
	if item.TotalTurns != 4 || item.TotalPromptTokens != 10 || item.TotalCompletionTokens != 3 || item.TotalCostUSD != 0.2 || item.CurrentModel != "glm-4.7" {
		t.Fatalf("existing stats overwritten: %+v", item)
	}
}

func TestOverlayCatalogUsageFillsOnlyZeroFields(t *testing.T) {
	item := sessionListItem{TotalTurns: 4, TotalPromptTokens: 10}
	overlayCatalogUsage(&item, catalogUsage{Turns: 1, Prompt: 99, Completion: 24, CostUSD: 0.5, Model: "deepseek-v4-flash"})
	if item.TotalTurns != 4 || item.TotalPromptTokens != 10 {
		t.Fatalf("non-zero redis fields changed: %+v", item)
	}
	if item.TotalCompletionTokens != 24 || item.TotalCostUSD != 0.5 || item.CurrentModel != "deepseek-v4-flash" {
		t.Fatalf("zero fields not filled: %+v", item)
	}
}

func TestFilterCatalogItemsMatchesModel(t *testing.T) {
	items := []sessionListItem{
		{SessionID: "gw_a", CurrentModel: "deepseek-v4-flash"},
		{SessionID: "gw_b", CurrentModel: "glm-5.2"},
	}
	got := filterCatalogItems(items, "DeepSeek")
	if len(got) != 1 || got[0].SessionID != "gw_a" {
		t.Fatalf("filtered = %+v", got)
	}
}

func TestLikeContainsEscapesWildcards(t *testing.T) {
	got := likeContains(`100%_off`)
	if got != `%100\%\_off%` {
		t.Fatalf("pattern = %q", got)
	}
}

func TestLikeContainsTruncatesByRune(t *testing.T) {
	q := strings.Repeat("界", 81)
	got := likeContains(q)
	inner := strings.Trim(got, "%")
	if utf8.RuneCountInString(inner) != 80 {
		t.Fatalf("runes = %d", utf8.RuneCountInString(inner))
	}
	if !utf8.ValidString(got) {
		t.Fatal("pattern is not valid UTF-8")
	}
}

func TestMergeCatalogSearchHitsKeepsTitleMatch(t *testing.T) {
	items := []sessionListItem{{SessionID: "gw_in_window", Title: ""}}
	hits := []catalogSearchHit{
		{SessionID: "gw_in_window", Title: "账单核对"},
		{SessionID: "  ", Title: "空白主键"},
		{SessionID: "gw_outside", Title: "目录外标题", Model: "glm-4.7"},
	}
	got := mergeCatalogSearchHits(items, hits, "tenant-a")
	if len(got) != 2 {
		t.Fatalf("len = %d, items = %+v", len(got), got)
	}
	if got[0].Title != "账单核对" {
		t.Fatalf("existing title not filled: %+v", got[0])
	}
	filtered := filterCatalogItems(got, "账单")
	if len(filtered) != 1 || filtered[0].SessionID != "gw_in_window" {
		t.Fatalf("title match dropped: %+v", filtered)
	}
	if got[1].SessionID != "gw_outside" || got[1].Status != "" || got[1].TenantID != "tenant-a" || got[1].CurrentModel != "glm-4.7" {
		t.Fatalf("outside hit = %+v", got[1])
	}
}

func TestCatalogSearchSQLDoesNotScanRequestLogs(t *testing.T) {
	if strings.Contains(catalogSearchSQL, "request_logs") {
		t.Fatal("search SQL must stay on session_summaries; request_logs ILIKE measured about 16s")
	}
	if !strings.Contains(catalogSearchSQL, "session_summaries") || !strings.Contains(catalogSearchSQL, "btrim(session_key)") {
		t.Fatal("search SQL missing summary source or blank-id guard")
	}
}

// TestCatalogUsageSQLReadsSessionFamily 钉住「用量只从 session 族取」。
//
// 这条断言曾经是反的：它要求 catalogUsageSQL 必须含 request_logs_hot 和
// FROM request_logs，等于把 S4 退役表钉成硬依赖，会主动阻止读端迁移。
// S4 停写（storage.request_logs_write_enabled=false）后 request_logs 只剩历史行，
// 聚合恒 0；而 overlayCatalogUsage 只补 0 值，于是同一会话显示对错取决于 Redis
// 缓存是否命中。守卫方向必须跟着事实源一起翻过来。
func TestCatalogUsageSQLReadsSessionFamily(t *testing.T) {
	if strings.Contains(catalogUsageSQL, "request_logs") {
		t.Fatalf("usage SQL must read the session family, not the retired request_logs: \n%s", catalogUsageSQL)
	}
	for _, want := range []string{
		"FROM session_turns_hot", // 未 promote 的近期行
		"FROM session_turns",     // 已 promote 的月分区行
		"session_id = ANY($1)",   // 命中 idx_session_turns_session
	} {
		if !strings.Contains(catalogUsageSQL, want) {
			t.Fatalf("usage SQL missing %q:\n%s", want, catalogUsageSQL)
		}
	}
}
