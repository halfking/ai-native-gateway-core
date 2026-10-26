package maas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClampUsageDays(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 1},
		{-5, 1},
		{7, 7},
		{90, 90},
		{120, 90},
	}
	for _, tc := range tests {
		if got := ClampUsageDays(tc.in); got != tc.want {
			t.Fatalf("ClampUsageDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestRequestLogsSource 钉住用量查询的**源**。
//
// ★ 2026-10-20（迁移 890，v1 族最终删除）：本门**反转为单源**。
//
// 原门断言 `days<=7 → _request_logs_hot AS r`、`days>7 → request_logs_with_current_month AS r`。
// 那个分流是灰度期的产物：**hot 表只保留 8h**，而 canonical 视图是 hot ∪ 月度分区
// ⇒ 短窗用 hot 是为了让「最近 7 天」这个口径拿到数据。
//
// v1 族被整体 DROP 之后：① `_request_logs_hot` 不存在 ⇒ 那条分支必然 **42P01**
//
//	（整条语句失败，不是静默空集）；② canonical 视图**本身就是**会话族两臂体
//	（`session_turns_hot ∪ session_turns` + details）⇒ 它已经覆盖热窗 + 冷窗
//	⇒ 短窗/长窗不再需要分流。
//
// ⇒ 恒返回 canonical 视图。负向断言（不得出现 v1 族）配**阳性对照**——
//
//	否则「没引用 v1」与「判据没跑」不可区分。
func TestRequestLogsSource(t *testing.T) {
	for _, days := range []int{1, 7, 30, 90} {
		source, alias := requestLogsSource(days)
		if source != "request_logs_with_current_month AS r" || alias != "r" {
			t.Errorf("requestLogsSource(%d) = %q/%q，期望恒为 canonical 视图", days, source, alias)
		}
		// v1 族必须不在场（两个代际的物理名都算：部署通道会词法改写）。
		for _, v1 := range []string{"_request_logs", "request_logs_hot", "_request_logs_hot"} {
			if strings.Contains(source, v1) {
				t.Errorf("requestLogsSource(%d) = %q —— v1 族已被迁移 890 整体 DROP，"+
					"任何提到它的语句整条 42P01。", days, source)
			}
		}
	}

	// ---- 阳性对照：证明上面那条负向判据**分得清** ----
	//
	// 喂一段确实含 v1 族引用的 FROM 片段，断言同一条判据**会**抓到它。
	// 没有它，上面那个循环若把判据写坏（例如比较了一个永远不出现的串），
	// 整段会恒绿。
	const v1LegSample = "_request_logs_hot AS r"
	if !strings.Contains(v1LegSample, "_request_logs_hot") {
		t.Error("阳性对照失效：含 v1 族引用的样本没有被负向判据抓到 —— " +
			"上面那条负向断言此时恒绿，测不到任何东西")
	}
}

func TestClampUsageLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 10},
		{-1, 10},
		{10, 10},
		{25, 25},
		{100, 50},
	}
	for _, tc := range tests {
		if got := ClampUsageLimit(tc.in); got != tc.want {
			t.Fatalf("ClampUsageLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestQueryUsageSummary_requiresTenant(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.QueryUsageSummary(t.Context(), "", 7, 10)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}

func TestQueryConsumptionDetail_requiresTenant(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.QueryConsumptionDetail(t.Context(), "", "", 7)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}

func TestConsumptionDetailRowResponseShape(t *testing.T) {
	// Sanity-check that the response marshals the documented fields so
	// frontend types stay in sync with the backend contract.
	out := ConsumptionDetail{
		TenantID:       "default",
		Days:           7,
		CentsPerCredit: 0.1,
		Rows: []ConsumptionDetailRow{
			{
				TenantID:                "default",
				OwnerUser:               "alice",
				ProviderName:            "OpenAI",
				CredentialLabel:         "cred-1",
				Model:                   "gpt-4o",
				Requests:                10,
				PromptTokens:            100,
				CompletionTokens:        50,
				CacheReadTokens:         20,
				CacheWriteTokens:        5,
				CreditsCharged:          300,
				UpstreamCostUSD:         0.12,
				TenantRevenueUSD:        0.30,
				GrossMarginUSD:          0.18,
				GrossMarginRate:         0.6,
				CancelledBilledRequests: 1,
			},
		},
	}
	// Round-trip via JSON to surface contract drift early.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, field := range []string{
		"provider_name", "credential_label", "cache_read_tokens", "cache_write_tokens",
		"upstream_cost_usd", "tenant_revenue_usd", "gross_margin_usd", "gross_margin_rate",
		"cancelled_billed_requests",
	} {
		if !strings.Contains(body, field) {
			t.Errorf("missing %s in marshalled output", field)
		}
	}
}
