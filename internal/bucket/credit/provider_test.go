package credit

import (
	"context"
	"testing"

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
