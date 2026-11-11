package core

import (
	"context"
	"testing"
	"time"
)

// Mock BucketProvider for testing
type mockBucketProvider struct {
	name       string
	bucketType BucketType
	state      *BucketState
	err        error
}

func (m *mockBucketProvider) Name() string {
	return m.name
}

func (m *mockBucketProvider) Type() BucketType {
	return m.bucketType
}

func (m *mockBucketProvider) GetState(ctx context.Context, key BucketKey) (*BucketState, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.state, nil
}

func (m *mockBucketProvider) UpdateState(ctx context.Context, key BucketKey, delta BucketDelta) (*BucketState, error) {
	if m.err != nil {
		return nil, m.err
	}
	// 简单模拟更新
	newState := &BucketState{
		Capacity:  m.state.Capacity,
		Used:      m.state.Used,
		Available: m.state.Available,
		UpdatedAt: time.Now(),
	}
	
	switch delta.Operation {
	case DeltaAdd:
		newState.Used += delta.Amount
		newState.Available -= delta.Amount
	case DeltaSubtract:
		newState.Used -= delta.Amount
		newState.Available += delta.Amount
	case DeltaSet:
		newState.Used = delta.Amount
		newState.Available = newState.Capacity - delta.Amount
	}
	
	m.state = newState
	return newState, nil
}

func (m *mockBucketProvider) Query(ctx context.Context, query BucketQuery) (*BucketResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &BucketResult{
		RowsAffected: 1,
		Data:         []BucketState{*m.state},
	}, nil
}

func (m *mockBucketProvider) Reset(ctx context.Context, key BucketKey) error {
	if m.err != nil {
		return m.err
	}
	m.state = &BucketState{
		Capacity:  m.state.Capacity,
		Used:      0,
		Available: m.state.Capacity,
		UpdatedAt: time.Now(),
	}
	return nil
}

func (m *mockBucketProvider) GetMetrics(ctx context.Context) (*BucketMetrics, error) {
	return &BucketMetrics{
		TotalRequests:   100,
		SuccessRequests: 95,
		FailedRequests:  5,
	}, nil
}

func TestBucketManager_RegisterProvider(t *testing.T) {
	manager := NewBucketManager(DefaultManagerConfig())
	
	provider := &mockBucketProvider{
		name:       "test-provider",
		bucketType: BucketTypeCredit,
		state: &BucketState{
			Capacity:  1000,
			Used:      100,
			Available: 900,
		},
	}
	
	// 测试注册
	err := manager.RegisterProvider(provider)
	if err != nil {
		t.Errorf("RegisterProvider failed: %v", err)
	}
	
	// 测试重复注册
	err = manager.RegisterProvider(provider)
	if err == nil {
		t.Error("Expected error when registering duplicate provider")
	}
	
	// 测试获取
	p, err := manager.GetProvider(BucketTypeCredit)
	if err != nil {
		t.Errorf("GetProvider failed: %v", err)
	}
	if p.Name() != "test-provider" {
		t.Errorf("Expected provider name 'test-provider', got '%s'", p.Name())
	}
}

func TestBucketManager_GetState(t *testing.T) {
	manager := NewBucketManager(DefaultManagerConfig())
	cache := NewMemoryCache(1000)
	manager.SetCache(cache)
	
	provider := &mockBucketProvider{
		name:       "test-provider",
		bucketType: BucketTypeCredit,
		state: &BucketState{
			Capacity:  1000,
			Used:      100,
			Available: 900,
			UpdatedAt: time.Now(),
		},
	}
	
	err := manager.RegisterProvider(provider)
	if err != nil {
		t.Fatalf("RegisterProvider failed: %v", err)
	}
	
	key := BucketKey{
		Type:     BucketTypeCredit,
		TenantID: "tenant1",
		Window:   CurrentHourWindow(),
	}
	
	ctx := context.Background()
	
	// 第一次获取（缓存未命中）
	state1, err := manager.GetState(ctx, key)
	if err != nil {
		t.Errorf("GetState failed: %v", err)
	}
	if state1.Used != 100 {
		t.Errorf("Expected used=100, got %d", state1.Used)
	}
	
	// 第二次获取（应该命中缓存）
	state2, err := manager.GetState(ctx, key)
	if err != nil {
		t.Errorf("GetState failed: %v", err)
	}
	if state2.Used != 100 {
		t.Errorf("Expected used=100, got %d", state2.Used)
	}
	
	// 检查缓存统计
	stats := cache.GetStats()
	if stats.Hits == 0 {
		t.Error("Expected cache hit but got none")
	}
}

func TestBucketManager_UpdateState(t *testing.T) {
	manager := NewBucketManager(DefaultManagerConfig())
	
	provider := &mockBucketProvider{
		name:       "test-provider",
		bucketType: BucketTypeCredit,
		state: &BucketState{
			Capacity:  1000,
			Used:      100,
			Available: 900,
			UpdatedAt: time.Now(),
		},
	}
	
	err := manager.RegisterProvider(provider)
	if err != nil {
		t.Fatalf("RegisterProvider failed: %v", err)
	}
	
	key := BucketKey{
		Type:     BucketTypeCredit,
		TenantID: "tenant1",
		Window:   CurrentHourWindow(),
	}
	
	ctx := context.Background()
	
	// 增加使用量
	delta := BucketDelta{
		Amount:    50,
		Operation: DeltaAdd,
	}
	
	state, err := manager.UpdateState(ctx, key, delta)
	if err != nil {
		t.Errorf("UpdateState failed: %v", err)
	}
	
	if state.Used != 150 {
		t.Errorf("Expected used=150, got %d", state.Used)
	}
	if state.Available != 850 {
		t.Errorf("Expected available=850, got %d", state.Available)
	}
}

func TestMemoryCache_Basic(t *testing.T) {
	cache := NewMemoryCache(10)
	
	key := BucketKey{
		Type:     BucketTypeCredit,
		TenantID: "tenant1",
		Window:   CurrentHourWindow(),
	}
	
	state := &BucketState{
		Capacity:  1000,
		Used:      100,
		Available: 900,
		UpdatedAt: time.Now(),
	}
	
	// 测试 Set 和 Get
	err := cache.Set(key, state, 1*time.Minute)
	if err != nil {
		t.Errorf("Set failed: %v", err)
	}
	
	retrieved, ok := cache.Get(key)
	if !ok {
		t.Error("Expected cache hit")
	}
	if retrieved.Used != 100 {
		t.Errorf("Expected used=100, got %d", retrieved.Used)
	}
	
	// 测试 Delete
	err = cache.Delete(key)
	if err != nil {
		t.Errorf("Delete failed: %v", err)
	}
	
	_, ok = cache.Get(key)
	if ok {
		t.Error("Expected cache miss after delete")
	}
}

func TestMemoryCache_Expiration(t *testing.T) {
	cache := NewMemoryCache(10)
	
	key := BucketKey{
		Type:     BucketTypeCredit,
		TenantID: "tenant1",
		Window:   CurrentHourWindow(),
	}
	
	state := &BucketState{
		Capacity:  1000,
		Used:      100,
		Available: 900,
	}
	
	// 设置很短的 TTL
	err := cache.Set(key, state, 100*time.Millisecond)
	if err != nil {
		t.Errorf("Set failed: %v", err)
	}
	
	// 立即获取应该成功
	_, ok := cache.Get(key)
	if !ok {
		t.Error("Expected cache hit")
	}
	
	// 等待过期
	time.Sleep(200 * time.Millisecond)
	
	// 再次获取应该失败
	_, ok = cache.Get(key)
	if ok {
		t.Error("Expected cache miss after expiration")
	}
}

func TestMemoryCache_Eviction(t *testing.T) {
	cache := NewMemoryCache(3) // 最多 3 个条目
	
	// 添加 4 个条目，应该触发淘汰
	for i := 0; i < 4; i++ {
		key := BucketKey{
			Type:       BucketTypeCredit,
			TenantID:   "tenant1",
			ResourceID: string(rune('A' + i)),
			Window:     CurrentHourWindow(),
		}
		
		state := &BucketState{
			Capacity:  1000,
			Used:      int64(i * 100),
			Available: 1000 - int64(i*100),
		}
		
		err := cache.Set(key, state, 1*time.Minute)
		if err != nil {
			t.Errorf("Set failed: %v", err)
		}
	}
	
	stats := cache.GetStats()
	if stats.Size > 3 {
		t.Errorf("Cache size %d exceeds max size 3", stats.Size)
	}
	if stats.Evictions == 0 {
		t.Error("Expected at least one eviction")
	}
}

func TestSimpleMetricsCollector(t *testing.T) {
	collector := NewSimpleMetricsCollector()
	
	// 记录一些请求
	for i := 0; i < 10; i++ {
		collector.RecordRequest(BucketTypeCredit, true, 100*time.Millisecond)
	}
	
	// 记录缓存命中
	collector.RecordCacheHit(BucketTypeCredit, true)
	collector.RecordCacheHit(BucketTypeCredit, true)
	collector.RecordCacheHit(BucketTypeCredit, false)
	
	// 记录配额使用
	collector.RecordQuotaUsage(BucketTypeCredit, "tenant1", 500, 1000)
	
	// 获取指标
	metrics, err := collector.GetMetrics(BucketTypeCredit)
	if err != nil {
		t.Errorf("GetMetrics failed: %v", err)
	}
	
	if metrics.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", metrics.TotalRequests)
	}
	if metrics.SuccessRequests != 10 {
		t.Errorf("Expected 10 success requests, got %d", metrics.SuccessRequests)
	}
	if metrics.CacheHits != 2 {
		t.Errorf("Expected 2 cache hits, got %d", metrics.CacheHits)
	}
	if metrics.CacheMisses != 1 {
		t.Errorf("Expected 1 cache miss, got %d", metrics.CacheMisses)
	}
	if metrics.UsageRate != 0.5 {
		t.Errorf("Expected usage rate 0.5, got %f", metrics.UsageRate)
	}
}

func TestTimeWindow(t *testing.T) {
	// 测试 CurrentMinuteWindow
	window := CurrentMinuteWindow()
	if window.Size != time.Minute {
		t.Errorf("Expected size=1m, got %v", window.Size)
	}
	
	// 测试 IsInWindow
	now := time.Now().UTC()
	if !window.IsInWindow(now) {
		t.Error("Expected now to be in current minute window")
	}
	
	past := now.Add(-2 * time.Minute)
	if window.IsInWindow(past) {
		t.Error("Expected past time to be outside current minute window")
	}
	
	// 测试 Contains
	subWindow := NewTimeWindow(window.Start.Add(10*time.Second), 10*time.Second)
	if !window.Contains(subWindow) {
		t.Error("Expected window to contain sub-window")
	}
}

func TestBucketKey_String(t *testing.T) {
	key := BucketKey{
		Type:       BucketTypeCredit,
		TenantID:   "tenant1",
		ResourceID: "resource1",
		Window:     CurrentHourWindow(),
	}
	
	str := key.String()
	if str == "" {
		t.Error("Expected non-empty string representation")
	}
	
	// 验证格式
	if len(str) < 10 {
		t.Errorf("String representation too short: %s", str)
	}
}
