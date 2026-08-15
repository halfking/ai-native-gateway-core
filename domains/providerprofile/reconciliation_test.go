package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// TestComputeDiffRate 验证对账差异率计算：(gateway - provider) / provider。
// provider 侧为 0 时分母为零，按约定返回 0（无法定义差异）。
func TestComputeDiffRate(t *testing.T) {
	tests := []struct {
		name     string
		gateway  float64
		provider float64
		want     float64
	}{
		{"完全一致", 100.0, 100.0, 0},
		{"网关高10%", 110.0, 100.0, 0.1},
		{"网关低25%", 75.0, 100.0, -0.25},
		{"账单为零不 panic", 50.0, 0, 0},
		{"两侧都为零", 0, 0, 0},
		{"四舍五入到4位小数", 100.0, 300.0, -0.6667},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := providerprofile.ComputeDiffRate(tt.gateway, tt.provider)
			// 差异率存储在 numeric(5,4)，比较也按 4 位小数对齐。
			if diff := got - tt.want; diff > 0.00005 || diff < -0.00005 {
				t.Fatalf("ComputeDiffRate(%v, %v) = %v, want %v", tt.gateway, tt.provider, got, tt.want)
			}
		})
	}
}

// fakeReconciliationStore 内存版 ReconciliationStore，用于验证
// CostReconciler 的编排行为（不验证 SQL）。
type fakeReconciliationStore struct {
	records map[int64]*providerprofile.ReconciliationRecord // key: providerID
	upserts [][]providerprofile.GatewayMonthlyUsage
}

func newFakeReconciliationStore() *fakeReconciliationStore {
	return &fakeReconciliationStore{records: map[int64]*providerprofile.ReconciliationRecord{}}
}

func (s *fakeReconciliationStore) UpsertGatewayUsage(ctx context.Context, month time.Time, usage []providerprofile.GatewayMonthlyUsage) error {
	s.upserts = append(s.upserts, usage)
	for _, u := range usage {
		rec := s.ensure(u.ProviderID, month)
		rec.GatewayInputTokens = u.InputTokens
		rec.GatewayOutputTokens = u.OutputTokens
		rec.GatewayTotalTokens = u.TotalTokens
		rec.GatewayTotalCost = u.TotalCost
	}
	return nil
}

func (s *fakeReconciliationStore) SaveRecord(ctx context.Context, rec *providerprofile.ReconciliationRecord) error {
	s.records[rec.ProviderID] = rec
	return nil
}

func (s *fakeReconciliationStore) Get(ctx context.Context, providerID int64, month time.Time) (*providerprofile.ReconciliationRecord, error) {
	if rec, ok := s.records[providerID]; ok {
		return rec, nil
	}
	return nil, nil
}

func (s *fakeReconciliationStore) ListByMonth(ctx context.Context, month time.Time) ([]providerprofile.ReconciliationRecord, error) {
	var out []providerprofile.ReconciliationRecord
	for _, rec := range s.records {
		if rec.Month.Equal(month) {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (s *fakeReconciliationStore) ensure(providerID int64, month time.Time) *providerprofile.ReconciliationRecord {
	if rec, ok := s.records[providerID]; ok {
		return rec
	}
	rec := &providerprofile.ReconciliationRecord{ProviderID: providerID, Month: month}
	s.records[providerID] = rec
	return rec
}

// fakeAlertSink 记录告警调用的内存 sink。
type fakeAlertSink struct {
	alerts []*providerprofile.ReconciliationRecord
}

func (s *fakeAlertSink) EmitCostDiffAlert(ctx context.Context, rec *providerprofile.ReconciliationRecord) error {
	s.alerts = append(s.alerts, rec)
	return nil
}

// TestCostReconciler_ImportBill 验证账单导入行为：
//  1. 已有网关聚合值时，导入账单后 diff 率被计算并持久化；
//  2. 差异超阈值时产生告警，未超阈值不告警。
func TestCostReconciler_ImportBill(t *testing.T) {
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		gatewayCost    float64
		gatewayTokens  int64
		billCost       float64
		billTokens     int64
		wantCostDiff   float64
		wantTokenDiff  float64
		wantAlertCount int
	}{
		{
			name:           "成本差异超阈值产生告警",
			gatewayCost:    120.0,
			gatewayTokens:  100000,
			billCost:       100.0,
			billTokens:     100000,
			wantCostDiff:   0.2,
			wantTokenDiff:  0,
			wantAlertCount: 1,
		},
		{
			name:           "差异在阈值内不告警",
			gatewayCost:    102.0,
			gatewayTokens:  101000,
			billCost:       100.0,
			billTokens:     100000,
			wantCostDiff:   0.02,
			wantTokenDiff:  0.01,
			wantAlertCount: 0,
		},
		{
			name:           "token差异超阈值产生告警",
			gatewayCost:    100.0,
			gatewayTokens:  80000,
			billCost:       100.0,
			billTokens:     100000,
			wantCostDiff:   0,
			wantTokenDiff:  -0.2,
			wantAlertCount: 1,
		},
		{
			name:           "网关无记录但账单有金额为100%差异告警",
			gatewayCost:    0,
			gatewayTokens:  0,
			billCost:       100.0,
			billTokens:     100000,
			wantCostDiff:   -1,
			wantTokenDiff:  -1,
			wantAlertCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeReconciliationStore()
			if tt.gatewayCost > 0 || tt.gatewayTokens > 0 {
				if err := store.UpsertGatewayUsage(context.Background(), month, []providerprofile.GatewayMonthlyUsage{{
					ProviderID:   42,
					InputTokens:  tt.gatewayTokens / 2,
					OutputTokens: tt.gatewayTokens / 2,
					TotalTokens:  tt.gatewayTokens,
					TotalCost:    tt.gatewayCost,
				}}); err != nil {
					t.Fatalf("seed gateway usage: %v", err)
				}
			}
			sink := &fakeAlertSink{}
			r := providerprofile.NewCostReconciler(nil, store, sink, providerprofile.DiffThresholds{
				Cost:  0.05,
				Token: 0.05,
			})

			rec, err := r.ImportBill(context.Background(), providerprofile.ProviderBill{
				ProviderID:   42,
				Month:        month,
				InputTokens:  tt.billTokens / 2,
				OutputTokens: tt.billTokens / 2,
				TotalTokens:  tt.billTokens,
				TotalCost:    tt.billCost,
				DataSource:   "manual",
			})
			if err != nil {
				t.Fatalf("ImportBill: %v", err)
			}
			if rec.CostDiffRate != tt.wantCostDiff {
				t.Errorf("CostDiffRate = %v, want %v", rec.CostDiffRate, tt.wantCostDiff)
			}
			if rec.TokenDiffRate != tt.wantTokenDiff {
				t.Errorf("TokenDiffRate = %v, want %v", rec.TokenDiffRate, tt.wantTokenDiff)
			}
			if len(sink.alerts) != tt.wantAlertCount {
				t.Errorf("alerts emitted = %d, want %d", len(sink.alerts), tt.wantAlertCount)
			}
			// 持久化的记录与返回值一致
			saved, err := store.Get(context.Background(), 42, month)
			if err != nil || saved == nil {
				t.Fatalf("saved record missing: %v", err)
			}
			if saved.CostDiffRate != rec.CostDiffRate || saved.TokenDiffRate != rec.TokenDiffRate {
				t.Errorf("saved record diff rates = (%v, %v), want (%v, %v)",
					saved.CostDiffRate, saved.TokenDiffRate, rec.CostDiffRate, rec.TokenDiffRate)
			}
			if saved.DataSource != "manual" {
				t.Errorf("DataSource = %q, want manual", saved.DataSource)
			}
		})
	}
}

// fakeUsageSource 内存版 GatewayUsageSource。
type fakeUsageSource struct {
	usage []providerprofile.GatewayMonthlyUsage
	err   error
}

func (s *fakeUsageSource) MonthlyUsageByProvider(ctx context.Context, month time.Time) ([]providerprofile.GatewayMonthlyUsage, error) {
	return s.usage, s.err
}

// TestCostReconciler_AggregateMonth 验证月度聚合编排：
//  1. 网关侧聚合值被写入 store；
//  2. 已有账单的记录 diff 率被刷新，差异超阈值时告警；
//  3. 无账单的记录不产生告警。
func TestCostReconciler_AggregateMonth(t *testing.T) {
	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("网关聚合值写入store且无账单不告警", func(t *testing.T) {
		store := newFakeReconciliationStore()
		sink := &fakeAlertSink{}
		source := &fakeUsageSource{usage: []providerprofile.GatewayMonthlyUsage{
			{ProviderID: 1, InputTokens: 400, OutputTokens: 600, TotalTokens: 1000, TotalCost: 10.5},
			{ProviderID: 2, InputTokens: 0, OutputTokens: 0, TotalTokens: 0, TotalCost: 0},
		}}
		r := providerprofile.NewCostReconciler(source, store, sink, providerprofile.DefaultDiffThresholds())

		if err := r.AggregateMonth(context.Background(), month); err != nil {
			t.Fatalf("AggregateMonth: %v", err)
		}
		if len(store.upserts) != 1 || len(store.upserts[0]) != 2 {
			t.Fatalf("UpsertGatewayUsage calls = %v, want 1 call with 2 providers", store.upserts)
		}
		rec, err := store.Get(context.Background(), 1, month)
		if err != nil || rec == nil {
			t.Fatalf("record for provider 1 missing: %v", err)
		}
		if rec.GatewayTotalTokens != 1000 || rec.GatewayTotalCost != 10.5 {
			t.Errorf("gateway aggregate = (%d, %v), want (1000, 10.5)", rec.GatewayTotalTokens, rec.GatewayTotalCost)
		}
		if len(sink.alerts) != 0 {
			t.Errorf("alerts emitted = %d, want 0 (no bill present)", len(sink.alerts))
		}
	})

	t.Run("已有账单的记录diff刷新且超阈值告警", func(t *testing.T) {
		store := newFakeReconciliationStore()
		sink := &fakeAlertSink{}
		// 预置：网关旧聚合 1000 tokens/100 cost，账单 1000 tokens/100 cost（diff=0）。
		rec := &providerprofile.ReconciliationRecord{
			ProviderID:          1,
			Month:               month,
			GatewayTotalTokens:  1000,
			GatewayTotalCost:    100,
			ProviderTotalTokens: 1000,
			ProviderTotalCost:   100,
		}
		if err := store.SaveRecord(context.Background(), rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// 新聚合值漂移到 1300 tokens / 130 cost → diff = +0.3，超阈值。
		source := &fakeUsageSource{usage: []providerprofile.GatewayMonthlyUsage{
			{ProviderID: 1, InputTokens: 300, OutputTokens: 1000, TotalTokens: 1300, TotalCost: 130},
		}}
		r := providerprofile.NewCostReconciler(source, store, sink, providerprofile.DefaultDiffThresholds())

		if err := r.AggregateMonth(context.Background(), month); err != nil {
			t.Fatalf("AggregateMonth: %v", err)
		}
		got, err := store.Get(context.Background(), 1, month)
		if err != nil || got == nil {
			t.Fatalf("record missing: %v", err)
		}
		if got.GatewayTotalTokens != 1300 || got.GatewayTotalCost != 130 {
			t.Errorf("gateway aggregate not refreshed: (%d, %v)", got.GatewayTotalTokens, got.GatewayTotalCost)
		}
		if got.CostDiffRate != 0.3 || got.TokenDiffRate != 0.3 {
			t.Errorf("diff rates = (%v, %v), want (0.3, 0.3)", got.CostDiffRate, got.TokenDiffRate)
		}
		if len(sink.alerts) != 1 {
			t.Errorf("alerts emitted = %d, want 1", len(sink.alerts))
		}
	})

	t.Run("source为nil时报错不panic", func(t *testing.T) {
		r := providerprofile.NewCostReconciler(nil, newFakeReconciliationStore(), &fakeAlertSink{}, providerprofile.DefaultDiffThresholds())
		if err := r.AggregateMonth(context.Background(), month); err == nil {
			t.Fatal("AggregateMonth with nil source should error")
		}
	})
}
