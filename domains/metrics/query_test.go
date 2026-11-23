package metrics

import (
	"strings"
	"testing"
	"time"
)

// 这组用例钉住 request_metrics 查询层的**口径**：
// 早先每个查询函数各写一份过滤条件，出现过三类静默错误 ——
// ① 列名写错（providers.name → 实际是 display_name，查询直接报错）；
// ② 时间字段取错（DailySummary 读 filter.StartDate/EndDate 的零值 → 永远查不到数据）；
// ③ filter 里的维度字段被接收但没进 SQL（ProviderSummary 忽略 ProviderID）。
// 三类都表现为「查出来的数不对/为空」，但断点完全不同，所以这里逐条钉死。

func TestBuildSummaryQuery_BasicFilter(t *testing.T) {
	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: false,
	}

	query, args := BuildSummaryQuery(filter)

	// 验证查询包含必要的子句
	if query == "" {
		t.Error("Query should not be empty")
	}

	// 验证参数数量
	expectedArgs := 3 // start_time, end_time, tenant_id
	if len(args) != expectedArgs {
		t.Errorf("Expected %d args, got %d", expectedArgs, len(args))
	}

	// 验证包含探测过滤
	if filter.IncludeProbes == false {
		if !strings.Contains(query, "traffic_class = 'business'") {
			t.Error("查询应包含探测流量过滤 traffic_class = 'business'")
		}
	}

	// 验证走的是发生时间列
	if !strings.Contains(query, "occurred_at >=") {
		t.Error("汇总查询应按 occurred_at 过滤")
	}
}

func TestBuildSummaryQuery_WithOptionalFilters(t *testing.T) {
	providerID := int64(1)
	sessionID := "session_123"

	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
		ProviderID:    &providerID,
		SessionID:     &sessionID,
	}

	query, args := BuildSummaryQuery(filter)

	// 验证参数数量增加
	expectedArgs := 5 // start_time, end_time, tenant_id, provider_id, session_id
	if len(args) != expectedArgs {
		t.Errorf("Expected %d args, got %d", expectedArgs, len(args))
	}

	// 验证参数值
	if args[0] != filter.StartTime {
		t.Error("First arg should be StartTime")
	}
	if args[1] != filter.EndTime {
		t.Error("Second arg should be EndTime")
	}
	if args[2] != filter.TenantID {
		t.Error("Third arg should be TenantID")
	}

	// ★ 可选维度必须真的进 SQL（否则「过滤了却没生效」）
	if !strings.Contains(query, "provider_id = $4") {
		t.Errorf("provider_id 过滤条件缺失，SQL=%s", query)
	}
	if !strings.Contains(query, "session_id = $5") {
		t.Errorf("session_id 过滤条件缺失，SQL=%s", query)
	}
}

func TestBuildSummaryQuery_UseBillingDate(t *testing.T) {
	filter := MetricsFilter{
		TenantID:       "tenant_1",
		UseBillingDate: true,
		StartDate:      time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		EndDate:        time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes:  false,
	}

	query, args := BuildSummaryQuery(filter)

	// 验证使用 billing_date 而非 occurred_at
	if query == "" {
		t.Error("Query should not be empty")
	}
	if !strings.Contains(query, "billing_date") {
		t.Errorf("记账口径应使用 billing_date，SQL=%s", query)
	}
	if strings.Contains(query, "occurred_at") {
		t.Errorf("记账口径不应出现 occurred_at，SQL=%s", query)
	}

	// 验证参数
	if len(args) < 3 {
		t.Error("Should have at least 3 args")
	}
	if args[0] != filter.StartDate {
		t.Error("记账口径第一个参数应是 StartDate")
	}
}

// ★ 缺陷回归：记账口径没设 StartDate/EndDate（零值）时，必须回落到 StartTime/EndTime。
// 零值日期会让条件恒为「无结果」，而空结果与过滤太严在页面上长得一模一样。
func TestBuildSummaryQuery_BillingDateFallsBackToTimeWindow(t *testing.T) {
	filter := MetricsFilter{
		TenantID:       "tenant_1",
		UseBillingDate: true,
		StartTime:      time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes:  true,
	}

	_, args := BuildSummaryQuery(filter)
	if len(args) < 2 {
		t.Fatalf("期望至少 2 个参数，实际 %d", len(args))
	}
	if args[0] != filter.StartTime {
		t.Error("未设置 StartDate 时应回落到 StartTime")
	}
	if args[1] != filter.EndTime {
		t.Error("未设置 EndDate 时应回落到 EndTime")
	}
}

// ★ 缺陷回归：DailySummary 原先在非记账口径下读 filter.StartDate/EndDate（零值）
// ⇒ 条件恒为「occurred_date >= 0001-01-01 AND occurred_date < 0001-01-01」⇒ 永远 0 行。
// 这里钉死它必须用 StartTime/EndTime。
func TestBuildDailySummaryQuery_UsesStartTimeEndTime(t *testing.T) {
	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
	}

	query, args := buildDailySummaryQuery(filter)
	if len(args) < 2 {
		t.Fatalf("期望至少 2 个参数，实际 %d", len(args))
	}
	if args[0] != filter.StartTime {
		t.Error("DailySummary 第一个参数应是 StartTime（而不是零值的 StartDate）")
	}
	if args[1] != filter.EndTime {
		t.Error("DailySummary 第二个参数应是 EndTime（而不是零值的 EndDate）")
	}
	if !strings.Contains(query, "occurred_date") {
		t.Errorf("DailySummary 应按 occurred_date 分组/过滤，SQL=%s", query)
	}
}

// ★ 日期列必须按 UTC 日历日裁剪：DATE 列与 timestamptz 参数比较时，
// PG 会按会话时区转换 ⇒ 同一条件在 UTC 与 +08 部署上差一天。
func TestBuildDailySummaryQuery_DateColumnUsesUTCCalendarDay(t *testing.T) {
	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
	}

	query, _ := buildDailySummaryQuery(filter)
	if !strings.Contains(query, "AT TIME ZONE 'UTC'") {
		t.Errorf("日期列过滤必须显式按 UTC 日历日裁剪，SQL=%s", query)
	}
}

// ★ 缺陷回归：providers 的展示名列是 display_name，没有 name 列。
func TestBuildProviderSummaryQuery_UsesDisplayName(t *testing.T) {
	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
	}

	query, _ := buildProviderSummaryQuery(filter)
	if !strings.Contains(query, "p.display_name") {
		t.Errorf("供应商汇总应使用 p.display_name，SQL=%s", query)
	}
	if strings.Contains(query, "p.name") {
		t.Errorf("供应商汇总不应出现 p.name（providers 无此列），SQL=%s", query)
	}
}

// ★ 缺陷回归：ProviderSummary 原先完全忽略 filter.ProviderID ——
// 参数被接收了但根本没进 SQL，调用方以为按供应商过滤了其实没有。
func TestBuildProviderSummaryQuery_HonorsProviderFilter(t *testing.T) {
	providerID := int64(9001)
	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
		ProviderID:    &providerID,
	}

	query, args := buildProviderSummaryQuery(filter)
	if !strings.Contains(query, "rm.provider_id = $") {
		t.Errorf("供应商汇总必须应用 filter.ProviderID，SQL=%s", query)
	}
	found := false
	for _, a := range args {
		if v, ok := a.(int64); ok && v == providerID {
			found = true
		}
	}
	if !found {
		t.Errorf("供应商 ID %d 未出现在参数里", providerID)
	}
}

// ★ 所有可选维度都必须落到 SQL（防止再出现「接收了但没用」）。
func TestBuildFilterClauses_AllDimensionFiltersApplied(t *testing.T) {
	providerID := int64(1)
	sessionID := "s1"
	payerID := "p1"
	teamID := int64(7)
	gatewayNodeID := "node-1"
	status := "success"
	trafficClass := "business"

	filter := MetricsFilter{
		TenantID:      "tenant_1",
		StartTime:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:       time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		IncludeProbes: true,
		ProviderID:    &providerID,
		SessionID:     &sessionID,
		PayerID:       &payerID,
		TeamID:        &teamID,
		GatewayNodeID: &gatewayNodeID,
		Status:        &status,
		TrafficClass:  &trafficClass,
	}

	clauses, args := buildFilterClauses(filter, "", "occurred_at", kindTimestamp)
	joined := strings.Join(clauses, " AND ")

	for _, want := range []string{
		"occurred_at >=", "occurred_at <",
		"tenant_id =", "provider_id =", "session_id =", "payer_id =",
		"team_id =", "gateway_node_id =", "status =", "traffic_class =",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("过滤条件缺少 %q，实际=%s", want, joined)
		}
	}
	// 2(时间) + 1(租户) + 7(维度) = 10
	if len(args) != 10 {
		t.Errorf("期望 10 个参数，实际 %d（args=%v）", len(args), args)
	}
}

// ★ 别名必须贯通：供应商汇总的表有别名 rm，过滤条件也要带别名，否则列名歧义。
func TestBuildFilterClauses_AppliesTableAlias(t *testing.T) {
	filter := MetricsFilter{
		TenantID:  "tenant_1",
		StartTime: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
	}

	clauses, _ := buildFilterClauses(filter, "rm.", "occurred_at", kindTimestamp)
	joined := strings.Join(clauses, " AND ")
	if !strings.Contains(joined, "rm.occurred_at") {
		t.Errorf("过滤条件应带表别名 rm.，实际=%s", joined)
	}
	if !strings.Contains(joined, "rm.tenant_id") {
		t.Errorf("租户过滤应带表别名 rm.，实际=%s", joined)
	}
}

func TestSummary_CostConversion(t *testing.T) {
	// 测试费用转换逻辑
	tests := []struct {
		name        string
		costCents   int64
		expectedUSD float64
	}{
		{
			name:        "1000分 = $10.00",
			costCents:   1000,
			expectedUSD: 10.00,
		},
		{
			name:        "1234分 = $12.34",
			costCents:   1234,
			expectedUSD: 12.34,
		},
		{
			name:        "1分 = $0.01",
			costCents:   1,
			expectedUSD: 0.01,
		},
		{
			name:        "0分 = $0.00",
			costCents:   0,
			expectedUSD: 0.00,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := &Summary{
				TotalCostCents: tt.costCents,
			}

			// 转换
			summary.TotalCostUSD = float64(summary.TotalCostCents) / 100.0

			// 验证
			if summary.TotalCostUSD != tt.expectedUSD {
				t.Errorf("Cost conversion failed: got %.2f, want %.2f",
					summary.TotalCostUSD, tt.expectedUSD)
			}
		})
	}
}

// 集成测试需要真实数据库
func TestQuerySummary_Integration(t *testing.T) {
	t.Skip("Integration test requires database")
}

func TestQueryDailySummary_Integration(t *testing.T) {
	t.Skip("Integration test requires database")
}

func TestQueryProviderSummary_Integration(t *testing.T) {
	t.Skip("Integration test requires database")
}

func TestQuerySessionCount_Integration(t *testing.T) {
	t.Skip("Integration test requires database")
}
