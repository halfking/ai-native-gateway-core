package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// TestTurnsUpsertAndOrder 验证 WriteTurnMeta 的 UPSERT 语义与 GetTurnsMeta 的升序返回。
func TestTurnsUpsertAndOrder(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteTurnsStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	// 写入 3 轮元数据。
	for i := 1; i <= 3; i++ {
		meta := &storage.TurnMeta{
			TenantID:            "tenant-a",
			SessionID:           "sess-1",
			TurnNo:              i,
			Timestamp:           base.Add(time.Duration(i) * time.Second),
			CompressionStrategy: "full",
			PromptTokens:        100 * i,
			CompletionTokens:    10 * i,
		}
		if err := store.WriteTurnMeta(ctx, meta); err != nil {
			t.Fatalf("WriteTurnMeta turn %d: %v", i, err)
		}
	}

	// 同 key 重复写 turn 2：UPSERT 不报错且值为最新。
	updated := &storage.TurnMeta{
		TenantID:            "tenant-a",
		SessionID:           "sess-1",
		TurnNo:              2,
		Timestamp:           base.Add(99 * time.Second),
		CompressionStrategy: "delta",
		PromptTokens:        222,
		CompletionTokens:    22,
	}
	if err := store.WriteTurnMeta(ctx, updated); err != nil {
		t.Fatalf("重复写入 turn 2: %v", err)
	}

	metas, err := store.GetTurnsMeta(ctx, "tenant-a", "sess-1")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("GetTurnsMeta 长度 = %d, want 3（UPSERT 不应新增行）", len(metas))
	}
	// 按 turn_no 升序。
	if metas[0].TurnNo != 1 || metas[1].TurnNo != 2 || metas[2].TurnNo != 3 {
		t.Fatalf("顺序错误: %d, %d, %d, want 1, 2, 3", metas[0].TurnNo, metas[1].TurnNo, metas[2].TurnNo)
	}
	// turn 2 为最新值。
	m := metas[1]
	if m.CompressionStrategy != "delta" || m.PromptTokens != 222 || m.CompletionTokens != 22 {
		t.Fatalf("turn 2 未覆盖为最新值: %+v", m)
	}
	if !m.Timestamp.Equal(base.Add(99 * time.Second)) {
		t.Fatalf("turn 2 时间戳 = %v, want %v", m.Timestamp, base.Add(99*time.Second))
	}
	// 其余轮次不受影响。
	if metas[0].PromptTokens != 100 || metas[2].CompletionTokens != 30 {
		t.Fatalf("其他轮次被误改: %+v %+v", metas[0], metas[2])
	}
	// 租户隔离。
	other, err := store.GetTurnsMeta(ctx, "tenant-b", "sess-1")
	if err != nil {
		t.Fatalf("GetTurnsMeta(tenant-b): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("租户隔离失效: len = %d, want 0", len(other))
	}
}

// TestGetTurnsMetaEmpty 验证无数据时返回非 nil 空切片。
func TestGetTurnsMetaEmpty(t *testing.T) {
	store := NewSQLiteTurnsStore(openTestDB(t))
	metas, err := store.GetTurnsMeta(context.Background(), "tenant-x", "sess-empty")
	if err != nil {
		t.Fatalf("GetTurnsMeta: %v", err)
	}
	if metas == nil || len(metas) != 0 {
		t.Fatalf("GetTurnsMeta = %v, want 非 nil 空切片", metas)
	}
}
