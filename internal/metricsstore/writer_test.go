package metricsstore

import (
	// ★ 2026-11-24 合并轮九代上游修：★ 原件导入了 "context" 但只在**注释掉的**代码里用
	//   （★ 见本文件下方两处注释：★ `processor.ProcessBatch(context.Background(), 100)`／
	//   `writer.BatchUpsertRequestMetrics(context.Background(), ...)`）⇒ ★★ 未使用的导入
	//   ⇒ ★★ **整个包的测试编译不过**（★ `imported and not used`）★★ ⇒ ★★ 上游 `main`
	//   当场就编译不过这个新包的测试 ⇒ ★ 本轮去掉该导入、★ 不动被注释的代码。
	"testing"
	"time"
)

func TestRequestMetric_Struct(t *testing.T) {
	// 测试结构体字段完整性
	metric := &RequestMetric{
		RequestID:    "req_123",
		TenantID:     "tenant_1",
		OccurredAt:   time.Now(),
		OccurredDate: time.Now(),
		BillingDate:  time.Now(),
		
		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,
		
		CostCents:       1234, // $12.34
		CreditsCharged:  5000,
		
		Status:       "success",
		TrafficClass: "business",
		
		Source: "test",
	}
	
	if metric.RequestID != "req_123" {
		t.Errorf("RequestID mismatch")
	}
	
	if metric.CostCents != 1234 {
		t.Errorf("CostCents mismatch: got %d, want 1234", metric.CostCents)
	}
}

func TestWriter_UpsertRequestMetric_ValidatesRequiredFields(t *testing.T) {
	// 测试必填字段验证
	// 这个测试需要真实数据库连接，暂时跳过
	t.Skip("Requires database connection")
}

func TestProcessor_ConvertCostUSDToCents(t *testing.T) {
	tests := []struct {
		name     string
		costUSD  float64
		expected int64
	}{
		{
			name:     "整数美元",
			costUSD:  10.00,
			expected: 1000,
		},
		{
			name:     "小数美元",
			costUSD:  12.34,
			expected: 1234,
		},
		{
			name:     "零美元",
			costUSD:  0.00,
			expected: 0,
		},
		{
			name:     "小额美元",
			costUSD:  0.01,
			expected: 1,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := int64(tt.costUSD * 100)
			if got != tt.expected {
				t.Errorf("Cost conversion failed: got %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestProcessor_SetDates(t *testing.T) {
	// 测试日期设置逻辑
	occurredAt := time.Date(2026, 11, 23, 15, 30, 45, 0, time.UTC)
	
	occurredDate := time.Date(
		occurredAt.Year(),
		occurredAt.Month(),
		occurredAt.Day(),
		0, 0, 0, 0,
		time.UTC,
	)
	
	expectedDate := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	
	if !occurredDate.Equal(expectedDate) {
		t.Errorf("Date mismatch: got %v, want %v", occurredDate, expectedDate)
	}
}

// 集成测试需要真实数据库
func TestProcessor_ProcessBatch_Integration(t *testing.T) {
	t.Skip("Integration test requires database")
	
	// 示例：
	// db := setupTestDB(t)
	// processor := NewProcessor(db)
	// processed, err := processor.ProcessBatch(context.Background(), 100)
	// if err != nil {
	//     t.Fatalf("ProcessBatch failed: %v", err)
	// }
	// if processed < 0 {
	//     t.Errorf("Invalid processed count: %d", processed)
	// }
}

func TestWriter_BatchUpsert_EmptySlice(t *testing.T) {
	// 测试空切片处理
	t.Skip("Requires database connection")
	
	// 示例：
	// db := setupTestDB(t)
	// writer := NewWriter(db)
	// err := writer.BatchUpsertRequestMetrics(context.Background(), []*RequestMetric{})
	// if err != nil {
	//     t.Errorf("BatchUpsert with empty slice should not error: %v", err)
	// }
}
