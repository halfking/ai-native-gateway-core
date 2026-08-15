package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// 对账集成测试使用的哨兵 provider_id，避免与真实供应商数据冲突。
const (
	testReconProviderA = int64(990001)
	testReconProviderB = int64(990002)
)

// cleanupReconData 清理对账表中本测试写入的数据。
//
// 注意：provider_events 在部分环境（columnar 转换后的库）不支持 DELETE，
// 事件表不做清理；sink 按 (provider, month) 幂等去重保证重复运行时事件
// 行数恒为 1，断言仍然成立。
func cleanupReconData(t *testing.T) {
	t.Helper()
	pool := setupTestDB(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `DELETE FROM provider_cost_reconciliation WHERE provider_id IN ($1, $2)`,
		testReconProviderA, testReconProviderB)
	if err != nil {
		t.Fatalf("cleanup reconciliation rows: %v", err)
	}
}

// monthOf 返回月份首日（UTC）。
func monthOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// TestPGReconciliationStore_GatewayUpsertPreservesBill 验证：
//  1. UpsertGatewayUsage 写入网关聚合值；
//  2. SaveRecord（账单导入路径）写入账单值后，再次 UpsertGatewayUsage
//     只刷新 gateway_* 列，不覆盖账单列。
func TestPGReconciliationStore_GatewayUpsertPreservesBill(t *testing.T) {
	pool := setupTestDB(t)
	cleanupReconData(t)
	ctx := context.Background()
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	store := providerprofile.NewPGReconciliationStore(pool)

	// 第一次写入网关聚合。
	err := store.UpsertGatewayUsage(ctx, month, []providerprofile.GatewayMonthlyUsage{
		{ProviderID: testReconProviderA, InputTokens: 400, OutputTokens: 600, TotalTokens: 1000, TotalCost: 10.5},
		{ProviderID: testReconProviderB, InputTokens: 100, OutputTokens: 100, TotalTokens: 200, TotalCost: 2.0},
	})
	if err != nil {
		t.Fatalf("UpsertGatewayUsage: %v", err)
	}
	rec, err := store.Get(ctx, testReconProviderA, month)
	if err != nil || rec == nil {
		t.Fatalf("Get after upsert: %v %v", rec, err)
	}
	if rec.GatewayTotalTokens != 1000 || rec.GatewayInputTokens != 400 || rec.GatewayOutputTokens != 600 || rec.GatewayTotalCost != 10.5 {
		t.Fatalf("gateway columns = %+v", rec)
	}

	// 写入账单（SaveRecord 全量 upsert）。
	rec.ProviderTotalTokens = 990
	rec.ProviderTotalCost = 9.0
	rec.DataSource = "manual"
	if err := store.SaveRecord(ctx, rec); err != nil {
		t.Fatalf("SaveRecord: %v", err)
	}

	// 再次刷新网关聚合，账单列必须保留。
	err = store.UpsertGatewayUsage(ctx, month, []providerprofile.GatewayMonthlyUsage{
		{ProviderID: testReconProviderA, InputTokens: 500, OutputTokens: 700, TotalTokens: 1200, TotalCost: 12.0},
	})
	if err != nil {
		t.Fatalf("UpsertGatewayUsage (2nd): %v", err)
	}
	rec, err = store.Get(ctx, testReconProviderA, month)
	if err != nil || rec == nil {
		t.Fatalf("Get after 2nd upsert: %v %v", rec, err)
	}
	if rec.GatewayTotalTokens != 1200 || rec.GatewayTotalCost != 12.0 {
		t.Errorf("gateway columns not refreshed: %+v", rec)
	}
	if rec.ProviderTotalTokens != 990 || rec.ProviderTotalCost != 9.0 || rec.DataSource != "manual" {
		t.Errorf("bill columns clobbered by gateway upsert: %+v", rec)
	}

	// ListByMonth 能查到该月记录。
	recs, err := store.ListByMonth(ctx, month)
	if err != nil {
		t.Fatalf("ListByMonth: %v", err)
	}
	var foundA, foundB bool
	for _, r := range recs {
		switch r.ProviderID {
		case testReconProviderA:
			foundA = true
		case testReconProviderB:
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("ListByMonth missing providers: A=%v B=%v", foundA, foundB)
	}
}

// TestPGGatewayMonthlyUsageSource 验证从 request_logs_hot + request_logs
// 按 provider + 月度聚合，且跨表重复 request_id 只计一次。
func TestPGGatewayMonthlyUsageSource(t *testing.T) {
	pool := setupTestDB(t)
	cleanupReconData(t)
	ctx := context.Background()
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// 热表两行（同一供应商），冷表一行与热表重复 request_id（迁移中的重叠），
	// 一行为冷表独有。聚合应只计 3 个不同 request_id。
	seed := []string{
		`INSERT INTO request_logs_hot (request_id, ts, tenant_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, success)
		 VALUES ('recon-test-1', '2026-07-10T00:00:00Z', 'recon-test', 990001, 100, 200, 300, 1.5, true)
			 ON CONFLICT DO NOTHING`,
		`INSERT INTO request_logs_hot (request_id, ts, tenant_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, success)
		 VALUES ('recon-test-2', '2026-07-11T00:00:00Z', 'recon-test', 990001, 10, 20, 30, 0.25, true)
			 ON CONFLICT DO NOTHING`,
		`INSERT INTO request_logs (request_id, ts, tenant_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, success)
		 VALUES ('recon-test-1', '2026-07-10T00:00:00Z', 'recon-test', 990001, 100, 200, 300, 1.5, true)
			 ON CONFLICT DO NOTHING`,
		`INSERT INTO request_logs (request_id, ts, tenant_id, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd, success)
		 VALUES ('recon-test-3', '2026-07-12T00:00:00Z', 'recon-test', 990002, 7, 8, 15, 0.75, true)
			 ON CONFLICT DO NOTHING`,
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM request_logs_hot WHERE tenant_id='recon-test'`)
		pool.Exec(ctx, `DELETE FROM request_logs WHERE tenant_id='recon-test'`)
	})
	for _, q := range seed {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("seed request log: %v\nsql: %s", err, q)
		}
	}

	source := providerprofile.NewPGGatewayMonthlyUsageSource(pool)
	usage, err := source.MonthlyUsageByProvider(ctx, month)
	if err != nil {
		t.Fatalf("MonthlyUsageByProvider: %v", err)
	}
	var gotA, gotB *providerprofile.GatewayMonthlyUsage
	for i := range usage {
		switch usage[i].ProviderID {
		case testReconProviderA:
			gotA = &usage[i]
		case testReconProviderB:
			gotB = &usage[i]
		}
	}
	if gotA == nil || gotB == nil {
		t.Fatalf("missing providers in aggregation: %+v", usage)
	}
	// A: 3 行去重后 2 行 → tokens 330, cost 1.75。
	if gotA.TotalTokens != 330 || gotA.InputTokens != 110 || gotA.OutputTokens != 220 || gotA.TotalCost != 1.75 {
		t.Errorf("provider A aggregate = %+v, want tokens=330 cost=1.75", gotA)
	}
	if gotB.TotalTokens != 15 || gotB.TotalCost != 0.75 {
		t.Errorf("provider B aggregate = %+v, want tokens=15 cost=0.75", gotB)
	}
}

// TestPGReconciliationEventSink 验证 diff 告警写入 provider_events
// （event_kind='cost_reconciliation_diff'），且同一 (provider, month)
// 幂等去重。
func TestPGReconciliationEventSink(t *testing.T) {
	pool := setupTestDB(t)
	cleanupReconData(t)
	ctx := context.Background()
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	sink := providerprofile.NewPGReconciliationEventSink(pool)
	rec := &providerprofile.ReconciliationRecord{
		ProviderID:          testReconProviderA,
		Month:               month,
		GatewayTotalTokens:  1200,
		GatewayTotalCost:    12.0,
		ProviderTotalTokens: 1000,
		ProviderTotalCost:   10.0,
		TokenDiffRate:       0.2,
		CostDiffRate:        0.2,
	}
	if err := sink.EmitCostDiffAlert(ctx, rec); err != nil {
		t.Fatalf("EmitCostDiffAlert: %v", err)
	}
	// 同一 (provider, month) 重复告警应被去重。
	if err := sink.EmitCostDiffAlert(ctx, rec); err != nil {
		t.Fatalf("EmitCostDiffAlert (dedup): %v", err)
	}

	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_events
		WHERE event_kind='cost_reconciliation_diff'
		  AND payload_json->>'provider_id' = '990001'
		  AND payload_json->>'month' = $1`, month.Format("2006-01")).Scan(&n)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 1 {
		t.Errorf("event count = %d, want 1 (deduped)", n)
	}
}
