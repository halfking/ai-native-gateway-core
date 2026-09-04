package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// TestRequestLogWriteGetList 覆盖请求日志的写入、查询与过滤列表。
func TestRequestLogWriteGetList(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteRequestLogStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	write := func(requestID, tenantID, sessionID string, ts time.Time, status int, body json.RawMessage) error {
		return store.WriteRequest(ctx, &storage.RequestLog{
			RequestID:  requestID,
			TenantID:   tenantID,
			SessionID:  sessionID,
			Timestamp:  ts,
			Method:     "POST",
			Path:       "/v1/chat/completions",
			StatusCode: status,
			Duration:   1500 * time.Millisecond,
			Body:       body,
		})
	}

	// tenant-a：sess-1 三条、sess-2 两条（时间递增）；tenant-b：一条。
	for i := 0; i < 3; i++ {
		if err := write(fmt.Sprintf("req-%d", i), "tenant-a", "sess-1",
			base.Add(time.Duration(i)*time.Minute), 200, json.RawMessage(`{"model":"gpt-4o"}`)); err != nil {
			t.Fatalf("WriteRequest req-%d: %v", i, err)
		}
	}
	for i := 3; i < 5; i++ {
		if err := write(fmt.Sprintf("req-%d", i), "tenant-a", "sess-2",
			base.Add(time.Duration(i)*time.Minute), 500, nil); err != nil {
			t.Fatalf("WriteRequest req-%d: %v", i, err)
		}
	}
	if err := write("req-other", "tenant-b", "sess-9", base, 404, nil); err != nil {
		t.Fatalf("WriteRequest req-other: %v", err)
	}

	// Get 往返：字段一致；Body 大字段不入库，读回为 nil。
	got, err := store.GetRequest(ctx, "req-0")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.RequestID != "req-0" || got.TenantID != "tenant-a" || got.SessionID != "sess-1" {
		t.Fatalf("标识字段往返不一致: %+v", got)
	}
	if got.Method != "POST" || got.Path != "/v1/chat/completions" || got.StatusCode != 200 {
		t.Fatalf("请求字段往返不一致: %+v", got)
	}
	if got.Duration != 1500*time.Millisecond {
		t.Fatalf("Duration = %v, want 1.5s", got.Duration)
	}
	if !got.Timestamp.Equal(base) {
		t.Fatalf("Timestamp = %v, want %v", got.Timestamp, base)
	}
	if got.Body != nil {
		t.Fatalf("Body 应不入库读回为 nil, got %s", got.Body)
	}

	// Get 不存在 → ErrNotFound。
	if _, err := store.GetRequest(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetRequest err = %v, want storage.ErrNotFound", err)
	}

	// has_body 标记：有 Body 记 1，无 Body 记 0。
	var hasBody int
	if err := db.QueryRow(`SELECT has_body FROM request_logs WHERE request_id = ?`, "req-0").Scan(&hasBody); err != nil {
		t.Fatalf("查询 has_body(req-0) 失败: %v", err)
	}
	if hasBody != 1 {
		t.Fatalf("req-0 has_body = %d, want 1", hasBody)
	}
	if err := db.QueryRow(`SELECT has_body FROM request_logs WHERE request_id = ?`, "req-other").Scan(&hasBody); err != nil {
		t.Fatalf("查询 has_body(req-other) 失败: %v", err)
	}
	if hasBody != 0 {
		t.Fatalf("req-other has_body = %d, want 0", hasBody)
	}

	// 按 tenant 过滤：5 条，ts DESC。
	logs, err := store.ListRequests(ctx, &storage.RequestFilter{TenantID: "tenant-a"})
	if err != nil {
		t.Fatalf("ListRequests(tenant): %v", err)
	}
	if len(logs) != 5 {
		t.Fatalf("ListRequests(tenant) 长度 = %d, want 5", len(logs))
	}
	if logs[0].RequestID != "req-4" || logs[4].RequestID != "req-0" {
		t.Fatalf("ListRequests(tenant) 未按 ts DESC: %q..%q", logs[0].RequestID, logs[4].RequestID)
	}

	// 时间范围过滤 [base+1m, base+3m]：命中 req-1/2/3，倒序。
	logs, err = store.ListRequests(ctx, &storage.RequestFilter{
		TenantID:  "tenant-a",
		StartTime: base.Add(time.Minute),
		EndTime:   base.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ListRequests(时间范围): %v", err)
	}
	if len(logs) != 3 || logs[0].RequestID != "req-3" || logs[1].RequestID != "req-2" || logs[2].RequestID != "req-1" {
		t.Fatalf("时间范围过滤结果不符: %v", reqIDs(logs))
	}

	// session 过滤。
	logs, err = store.ListRequests(ctx, &storage.RequestFilter{TenantID: "tenant-a", SessionID: "sess-2"})
	if err != nil {
		t.Fatalf("ListRequests(session): %v", err)
	}
	if len(logs) != 2 || logs[0].RequestID != "req-4" || logs[1].RequestID != "req-3" {
		t.Fatalf("session 过滤结果不符: %v", reqIDs(logs))
	}

	// limit 截断：Limit=2 只返回最新的 2 条。
	logs, err = store.ListRequests(ctx, &storage.RequestFilter{TenantID: "tenant-a", Limit: 2})
	if err != nil {
		t.Fatalf("ListRequests(limit): %v", err)
	}
	if len(logs) != 2 || logs[0].RequestID != "req-4" || logs[1].RequestID != "req-3" {
		t.Fatalf("limit 结果不符: %v", reqIDs(logs))
	}

	// filter 为 nil：默认 limit=100，返回全部 6 条。
	logs, err = store.ListRequests(ctx, nil)
	if err != nil {
		t.Fatalf("ListRequests(nil filter): %v", err)
	}
	if len(logs) != 6 {
		t.Fatalf("nil filter 长度 = %d, want 6", len(logs))
	}
}

// TestNormalizeRequestLimit 覆盖 limit 归一化边界。
func TestNormalizeRequestLimit(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{in: 0, want: 100},
		{in: -1, want: 100},
		{in: 5, want: 5},
		{in: 1000, want: 1000},
		{in: 2000, want: 1000},
	}
	for _, c := range cases {
		if got := normalizeRequestLimit(c.in); got != c.want {
			t.Fatalf("normalizeRequestLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// reqIDs 提取请求 ID 列表，便于错误信息输出。
func reqIDs(logs []*storage.RequestLog) []string {
	out := make([]string, 0, len(logs))
	for _, l := range logs {
		out = append(out, l.RequestID)
	}
	return out
}
