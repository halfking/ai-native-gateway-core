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

// TestTurnDetailsRetryIdempotentByRequestID（R51, 2026-09-21）：lite sink 的
// journal 链非原子（details 写点在 markJournaled 之前），失败重试会以新轮号
// 重写同一 request_id。details 的幂等键是 UNIQUE(tenant_id, request_id)，
// UPSERT 冲突目标必须覆盖该唯一索引：重试不得撞唯一索引报错（否则
// telemetry 侧永久失败至 failPermanent），也不得产生重复行。
func TestTurnDetailsRetryIdempotentByRequestID(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteTurnsStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	first := &storage.TurnDetails{
		TenantID:   "tenant-a",
		SessionID:  "sess-1",
		TurnNo:     1,
		RequestID:  "req-1",
		Timestamp:  base,
		Model:      "model-a",
		Success:    &[]bool{true}[0],
		StatusCode: 200,
		LatencyMs:  100,
	}
	if err := store.WriteTurnDetails(ctx, first); err != nil {
		t.Fatalf("首次写入 details: %v", err)
	}

	// 模拟失败重试：链路非原子导致轮号推进为 2，同一 request_id 再写。
	retry := &storage.TurnDetails{
		TenantID:   "tenant-a",
		SessionID:  "sess-1",
		TurnNo:     2,
		RequestID:  "req-1",
		Timestamp:  base.Add(time.Second),
		Model:      "model-b",
		Success:    &[]bool{true}[0],
		StatusCode: 200,
		LatencyMs:  200,
	}
	if err := store.WriteTurnDetails(ctx, retry); err != nil {
		t.Fatalf("重试写入 details（不应撞 request_id 唯一索引）: %v", err)
	}

	var (
		turnNo    int
		model     string
		latencyMs int
	)
	if err := db.QueryRow(
		`SELECT turn_no, model, latency_ms FROM session_turn_details WHERE tenant_id = ? AND request_id = ?`,
		"tenant-a", "req-1",
	).Scan(&turnNo, &model, &latencyMs); err != nil {
		t.Fatalf("读回 details: %v", err)
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM session_turn_details WHERE tenant_id = ? AND session_id = ?`,
		"tenant-a", "sess-1",
	).Scan(&n); err != nil {
		t.Fatalf("统计 details 行数: %v", err)
	}
	if n != 1 {
		t.Fatalf("details 行数 = %d, want 1（重试不得产生重复行）", n)
	}
	// 轮号保持首写锚点，特征列覆盖为重试的最新值。
	if turnNo != 1 {
		t.Fatalf("turn_no = %d, want 1（幂等键为 (tenant, request)，轮号不随重试迁移）", turnNo)
	}
	if model != "model-b" || latencyMs != 200 {
		t.Fatalf("特征未覆盖为最新值: model=%s latency_ms=%d, want model-b/200", model, latencyMs)
	}
}
