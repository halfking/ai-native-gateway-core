package credit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

func TestCreditBucketProvider_Basic(t *testing.T) {
	// 注意：这需要真实的数据库连接，这里只做基础测试
	provider := &CreditBucketProvider{
		pool:    nil, // 使用 nil 测试禁用状态
		cache:   core.NewMemoryCache(100),
		enabled: false,
	}

	if provider.Name() != "credit-bucket-provider" {
		t.Errorf("unexpected provider name: %s", provider.Name())
	}

	if provider.Type() != core.BucketTypeCredit {
		t.Errorf("unexpected bucket type: %s", provider.Type())
	}
}

func TestCreditBucketProvider_GetState_Disabled(t *testing.T) {
	provider := &CreditBucketProvider{
		pool:    nil,
		cache:   core.NewMemoryCache(100),
		enabled: false,
	}

	key := core.BucketKey{
		Type:     core.BucketTypeCredit,
		TenantID: "tenant1",
		Window:   core.CurrentHourWindow(),
	}

	ctx := context.Background()
	_, err := provider.GetState(ctx, key)

	if err == nil {
		t.Error("expected error when provider is disabled")
	}

	bucketErr, ok := err.(*core.BucketError)
	if !ok {
		t.Errorf("expected BucketError, got %T", err)
	}
	if bucketErr.Type != core.ErrTypeNotFound {
		t.Errorf("expected ErrTypeNotFound, got %s", bucketErr.Type)
	}
}

func TestCreditBucketProvider_Query_InvalidAction(t *testing.T) {
	provider := &CreditBucketProvider{
		pool:    nil,
		cache:   core.NewMemoryCache(100),
		enabled: true, // 启用但没有 pool，测试会在其他地方失败
	}

	query := core.BucketQuery{
		Type:   core.BucketTypeCredit,
		Action: "invalid_action",
	}

	ctx := context.Background()
	_, err := provider.Query(ctx, query)

	if err == nil {
		t.Error("expected error for invalid action")
	}

	bucketErr, ok := err.(*core.BucketError)
	if !ok {
		t.Errorf("expected BucketError, got %T", err)
	}
	if bucketErr.Type != core.ErrTypeInvalidInput {
		t.Errorf("expected ErrTypeInvalidInput, got %s", bucketErr.Type)
	}
}

func TestBucketsCoverLookback(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		oldest       *time.Time
		newest       *time.Time
		lookbackDays int
		want         bool
	}{
		{
			name:         "nil pointers",
			oldest:       nil,
			newest:       nil,
			lookbackDays: 7,
			want:         false,
		},
		{
			name:         "covers window",
			oldest:       timePtr(now.Add(-10 * 24 * time.Hour)),
			newest:       timePtr(now.Add(-1 * time.Hour)),
			lookbackDays: 7,
			want:         true,
		},
		{
			name:         "oldest too recent",
			oldest:       timePtr(now.Add(-5 * 24 * time.Hour)),
			newest:       timePtr(now.Add(-1 * time.Hour)),
			lookbackDays: 7,
			want:         false,
		},
		{
			name:         "newest too old",
			oldest:       timePtr(now.Add(-10 * 24 * time.Hour)),
			newest:       timePtr(now.Add(-5 * time.Hour)),
			lookbackDays: 7,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bucketsCoverLookback(now, tt.oldest, tt.newest, tt.lookbackDays)
			if got != tt.want {
				t.Errorf("bucketsCoverLookback() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRequestLogsSource 钉住 2026-11-11（48h 审计）收敛后的契约：
// 旧断言钉的是 v1 双臂（≤7 天 `_request_logs`、>7 天 `_request_logs_partitions`）——
// 那两张基表随迁移 890 在所有环境退役（211 实测 to_regclass 双空），
// 旧契约钉的是 42P01 地雷。现恒返回 canonical 视图（与 maas 包同名切换层一致）。
func TestRequestLogsSource(t *testing.T) {
	tests := []struct {
		lookbackDays int
		wantTable    string
		wantAlias    string
	}{
		{1, "request_logs_with_current_month", "rl"},
		{7, "request_logs_with_current_month", "rl"},
		{8, "request_logs_with_current_month", "rl"},
		{30, "request_logs_with_current_month", "rl"},
		{90, "request_logs_with_current_month", "rl"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("days_%d", tt.lookbackDays), func(t *testing.T) {
			gotTable, gotAlias := requestLogsSource(tt.lookbackDays)
			if gotTable != tt.wantTable {
				t.Errorf("table = %s, want %s", gotTable, tt.wantTable)
			}
			if gotAlias != tt.wantAlias {
				t.Errorf("alias = %s, want %s", gotAlias, tt.wantAlias)
			}
		})
	}
}

func TestServiceAdapter_Enabled(t *testing.T) {
	provider := &CreditBucketProvider{
		pool:    nil,
		cache:   core.NewMemoryCache(100),
		enabled: false,
	}

	adapter := &ServiceAdapter{
		provider: provider,
		manager:  nil,
	}

	if adapter.Enabled() {
		t.Error("expected adapter to be disabled")
	}
}

func TestServiceAdapter_QueryConsumedCreditsWindow_Validation(t *testing.T) {
	provider := &CreditBucketProvider{
		pool:    nil,
		cache:   core.NewMemoryCache(100),
		enabled: false,
	}

	adapter := &ServiceAdapter{
		provider: provider,
		manager:  nil,
	}

	ctx := context.Background()

	// 测试空租户ID
	_, err := adapter.QueryConsumedCreditsWindow(ctx, "", 7)
	if err == nil {
		t.Error("expected error for empty tenant_id")
	}
}

// Helper functions

func timePtr(t time.Time) *time.Time {
	return &t
}
