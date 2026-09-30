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

func TestMergeCatalogSearchHitsKeepsModelListMatch(t *testing.T) {
	hits := []catalogSearchHit{{
		SessionID: "gw_13543929-a852-440e-a96f-138e7bff99ea",
		Model:     "deepseek-v4-flash-260425",
	}}
	got := filterCatalogItems(mergeCatalogSearchHits(nil, hits, "tenant-a"), "deepseek-v4-flash")
	if len(got) != 1 || got[0].CurrentModel != "deepseek-v4-flash-260425" || got[0].Status != "" {
		t.Fatalf("model list hit dropped: %+v", got)
	}
}

func TestCatalogSearchSQLDoesNotScanRequestLogs(t *testing.T) {
	if strings.Contains(catalogSearchSQL, "request_logs") {
		t.Fatal("search SQL must stay on session_summaries; request_logs ILIKE measured about 16s")
	}
	if !strings.Contains(catalogSearchSQL, "session_summaries") || !strings.Contains(catalogSearchSQL, "btrim(session_key)") {
		t.Fatal("search SQL missing summary source or blank-id guard")
	}
	if !strings.Contains(catalogSearchSQL, "array_to_string(models_used, chr(10))") {
		t.Fatal("search must match each models_used element; space join can hit across names")
	}
	element := strings.Index(catalogSearchSQL, "unnest(models_used)")
	primary := strings.Index(catalogSearchSQL, "NULLIF(btrim(primary_model)")
	if element < 0 || primary < 0 || element > primary {
		t.Fatal("matching models_used element must be selected before primary_model")
	}
	if strings.Contains(catalogUsageSQL, "request_logs_with_current_month") {
		t.Fatal("usage SQL must not use the turn-joined view")
	}
	if !strings.Contains(catalogUsageSQL, "request_logs_hot") || !strings.Contains(catalogUsageSQL, "FROM request_logs") {
		t.Fatal("usage SQL must read hot and partitioned request_logs")
	}
}
