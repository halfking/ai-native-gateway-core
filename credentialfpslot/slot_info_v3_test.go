package credentialfpslot

import (
	"context"
	"testing"
)

// TestSlotInfoV3_Basic 测试 V3.1 SlotInfo 的基本功能
// V3.1 已知问题：miniredis pipeline 行为与真实 Redis 不一致，跳过 Redis 模式测试
func TestSlotInfoV3_Basic(t *testing.T) {
	t.Skip("miniredis pipeline behavior differs from real Redis; tested in integration tests")
}

// TestSlotInfoV3_SharedSlot 测试 V3.1 共享语义：多个请求共享同一 slot
func TestSlotInfoV3_SharedSlot(t *testing.T) {
	t.Skip("miniredis pipeline behavior differs from real Redis; tested in integration tests")
}

// TestSlotInfoV3_MemoryMode 测试内存模式下的 SlotInfo
func TestSlotInfoV3_MemoryMode(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil) // nil client = memory mode
	ctx := context.Background()
	credID := 500
	limit := 3

	// 初始状态
	info, err := m.SlotInfoV3(ctx, credID, &limit)
	if err != nil {
		t.Fatalf("SlotInfoV3 failed: %v", err)
	}
	if len(info) != limit {
		t.Errorf("expected %d slots, got %d", limit, len(info))
	}
	for _, s := range info {
		if !s.MemoryMode {
			t.Error("expected MemoryMode=true")
		}
	}

	// 获取一个 slot
	l1, ok := m.Acquire(ctx, credID, &limit, "sess-mem", "tenant-x")
	if !ok {
		t.Fatal("Acquire failed")
	}

	// 检查状态
	info, err = m.SlotInfoV3(ctx, credID, &limit)
	if err != nil {
		t.Fatalf("SlotInfoV3 after acquire failed: %v", err)
	}

	found := false
	for _, s := range info {
		if s.Holder == "sess-mem" {
			found = true
			if !s.MemoryMode {
				t.Error("expected MemoryMode=true")
			}
			// Phase 6.3 TODO: 内存模式下的 inflight 计数需要额外维护
			// 当前内存模式暂不支持 inflight 计数，后续迭代添加
		}
	}
	if !found {
		t.Error("slot with holder 'sess-mem' not found")
	}

	m.Release(ctx, l1)
}

// TestSlotInfoV3_Disabled 测试禁用状态
func TestSlotInfoV3_Disabled(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: false}, nil)
	ctx := context.Background()
	credID := 600
	limit := 3

	info, err := m.SlotInfoV3(ctx, credID, &limit)
	if err != nil {
		t.Fatalf("SlotInfoV3 failed: %v", err)
	}
	if len(info) != 0 {
		t.Errorf("expected 0 slots when disabled, got %d", len(info))
	}
}

// TestSlotInfoV3_NilLimit 测试 nil limit
func TestSlotInfoV3_NilLimit(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()
	credID := 700

	info, err := m.SlotInfoV3(ctx, credID, nil)
	if err != nil {
		t.Fatalf("SlotInfoV3 with nil limit failed: %v", err)
	}
	if len(info) != 3 {
		t.Errorf("expected 3 slots (default limit), got %d", len(info))
	}
}

// TestSlotInfoV3_ZeroLimit 测试零 limit
func TestSlotInfoV3_ZeroLimit(t *testing.T) {
	m := New(Config{DefaultLimit: 3, Enabled: true}, nil)
	ctx := context.Background()
	credID := 800
	zeroLimit := 0

	info, err := m.SlotInfoV3(ctx, credID, &zeroLimit)
	if err != nil {
		t.Fatalf("SlotInfoV3 with zero limit failed: %v", err)
	}
	if len(info) != 0 {
		t.Errorf("expected 0 slots with zero limit, got %d", len(info))
	}
}

// TestSlotInfoV3_ExpiredSlot 测试过期 slot 的状态
func TestSlotInfoV3_ExpiredSlot(t *testing.T) {
	t.Skip("miniredis pipeline behavior differs from real Redis; tested in integration tests")
}
