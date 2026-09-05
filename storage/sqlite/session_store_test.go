package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// TestSessionCRUDRoundTrip 验证会话 CRUD 往返字段一致（含 metadata map）。
func TestSessionCRUDRoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()

	createdAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	want := &storage.Session{
		ID:        "sess-1",
		TenantID:  "tenant-a",
		UserID:    "user-1",
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Minute),
		Metadata:  map[string]interface{}{"model": "gpt-4o", "tokens": float64(128)},
	}
	if err := store.CreateSession(ctx, want); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := store.GetSession(ctx, "tenant-a", "sess-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != want.ID || got.TenantID != want.TenantID || got.UserID != want.UserID {
		t.Fatalf("标识字段往返不一致: got %+v", got)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("时间往返不一致: created=%v updated=%v, want %v/%v",
			got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	if got.Metadata["model"] != "gpt-4o" || got.Metadata["tokens"] != float64(128) {
		t.Fatalf("metadata 往返不一致: %v", got.Metadata)
	}

	// Update：修改 user_id 与 metadata 后再次读取应生效。
	want.UserID = "user-2"
	want.UpdatedAt = createdAt.Add(2 * time.Minute)
	want.Metadata = map[string]interface{}{"model": "gpt-4o-mini"}
	if err := store.UpdateSession(ctx, want); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	got, err = store.GetSession(ctx, "tenant-a", "sess-1")
	if err != nil {
		t.Fatalf("Update 后 GetSession: %v", err)
	}
	if got.UserID != "user-2" {
		t.Fatalf("Update 后 UserID = %q, want \"user-2\"", got.UserID)
	}
	if !got.UpdatedAt.Equal(createdAt.Add(2 * time.Minute)) {
		t.Fatalf("Update 后 UpdatedAt = %v, want %v", got.UpdatedAt, createdAt.Add(2*time.Minute))
	}
	if got.Metadata["model"] != "gpt-4o-mini" {
		t.Fatalf("Update 后 metadata 未生效: %v", got.Metadata)
	}

	// Delete 后 Get 应未找到。
	if err := store.DeleteSession(ctx, "tenant-a", "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSession(ctx, "tenant-a", "sess-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Delete 后 GetSession err = %v, want storage.ErrNotFound", err)
	}
}

// TestSessionMetadataNilRoundTrip 验证 nil 与空 map 的 metadata 往返一致。
func TestSessionMetadataNilRoundTrip(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// nil metadata：存 NULL，读回 nil。
	if err := store.CreateSession(ctx, &storage.Session{ID: "sess-nil", TenantID: "tenant-a", CreatedAt: ts, UpdatedAt: ts}); err != nil {
		t.Fatalf("CreateSession(nil metadata): %v", err)
	}
	got, err := store.GetSession(ctx, "tenant-a", "sess-nil")
	if err != nil {
		t.Fatalf("GetSession(nil metadata): %v", err)
	}
	if got.Metadata != nil {
		t.Fatalf("nil metadata 读回 = %v, want nil", got.Metadata)
	}

	// 空 map：存 "{}"，读回非 nil 空 map。
	if err := store.CreateSession(ctx, &storage.Session{
		ID: "sess-empty", TenantID: "tenant-a", CreatedAt: ts, UpdatedAt: ts,
		Metadata: map[string]interface{}{},
	}); err != nil {
		t.Fatalf("CreateSession(空 metadata): %v", err)
	}
	got, err = store.GetSession(ctx, "tenant-a", "sess-empty")
	if err != nil {
		t.Fatalf("GetSession(空 metadata): %v", err)
	}
	if got.Metadata == nil || len(got.Metadata) != 0 {
		t.Fatalf("空 map metadata 读回 = %v, want 非 nil 空 map", got.Metadata)
	}
}

// TestSessionNotFound 验证 Get/Update/Delete 对不存在数据统一返回 ErrNotFound。
func TestSessionNotFound(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()

	if _, err := store.GetSession(ctx, "tenant-x", "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetSession err = %v, want storage.ErrNotFound", err)
	}
	if err := store.UpdateSession(ctx, &storage.Session{ID: "missing", TenantID: "tenant-x"}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("UpdateSession err = %v, want storage.ErrNotFound", err)
	}
	if err := store.DeleteSession(ctx, "tenant-x", "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("DeleteSession err = %v, want storage.ErrNotFound", err)
	}
}

// TestSessionListPagination 验证列表的租户过滤、created_at 倒序与分页。
func TestSessionListPagination(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// 按时间正序插入 5 条，期望列表按 created_at 倒序返回。
	for i := 0; i < 5; i++ {
		sess := &storage.Session{
			ID:        fmt.Sprintf("sess-%d", i),
			TenantID:  "tenant-a",
			UserID:    "user-1",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			UpdatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := store.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession sess-%d: %v", i, err)
		}
	}
	// 其他租户的数据不应串扰。
	if err := store.CreateSession(ctx, &storage.Session{
		ID: "sess-other", TenantID: "tenant-b", CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("CreateSession tenant-b: %v", err)
	}

	// opts 为 nil：默认 limit=100，返回本租户全部 5 条且倒序。
	sessions, err := store.ListSessions(ctx, "tenant-a", nil)
	if err != nil {
		t.Fatalf("ListSessions(nil opts): %v", err)
	}
	if len(sessions) != 5 {
		t.Fatalf("ListSessions 长度 = %d, want 5", len(sessions))
	}
	for i, sess := range sessions {
		wantID := fmt.Sprintf("sess-%d", 4-i)
		if sess.ID != wantID {
			t.Fatalf("sessions[%d].ID = %q, want %q (created_at 倒序)", i, sess.ID, wantID)
		}
	}

	// limit=2, offset=1：跳过最新的 sess-4，应取 sess-3、sess-2。
	sessions, err = store.ListSessions(ctx, "tenant-a", &storage.ListOptions{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListSessions(分页): %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "sess-3" || sessions[1].ID != "sess-2" {
		t.Fatalf("分页结果 = %v, want [sess-3 sess-2]", ids(sessions))
	}

	// Limit<=0 / Offset<0：回退默认 limit=100、offset=0。
	sessions, err = store.ListSessions(ctx, "tenant-a", &storage.ListOptions{Limit: 0, Offset: -5})
	if err != nil {
		t.Fatalf("ListSessions(Limit=0): %v", err)
	}
	if len(sessions) != 5 {
		t.Fatalf("Limit<=0 时长度 = %d, want 5", len(sessions))
	}
}

// ids 提取会话 ID 列表，便于错误信息输出。
func ids(sessions []*storage.Session) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.ID)
	}
	return out
}

// TestSessionConcurrentReadWrite 验证 WAL 模式下 10 个协程并发写 + 并发读
// 互不阻塞、无错误（配合 -race 检测数据竞争）。
func TestSessionConcurrentReadWrite(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()

	const (
		writers   = 10
		perWriter = 20
		readers   = 10
		perReader = 20
	)

	var wg sync.WaitGroup
	errCh := make(chan error, writers+readers)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("sess-%02d-%02d", w, i)
				sess := &storage.Session{
					ID:        id,
					TenantID:  "tenant-concurrent",
					UserID:    fmt.Sprintf("user-%d", w),
					Metadata:  map[string]interface{}{"writer": w, "seq": i},
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
				}
				if err := store.CreateSession(ctx, sess); err != nil {
					errCh <- fmt.Errorf("并发写入 %s: %w", id, err)
					return
				}
			}
		}(w)
	}
	// 读协程与写并发：WAL 下读不阻塞写。
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perReader; i++ {
				if _, err := store.ListSessions(ctx, "tenant-concurrent", &storage.ListOptions{Limit: 5}); err != nil {
					errCh <- fmt.Errorf("并发读取: %w", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// 最终总数校验：写入互不丢失。
	// 注意不能用 nil opts（默认 limit=100 会截断），这里显式放大 limit，
	// 并辅以 COUNT(*) 直查数据库核对。
	sessions, err := store.ListSessions(ctx, "tenant-concurrent", &storage.ListOptions{Limit: writers * perWriter})
	if err != nil {
		t.Fatalf("收尾 ListSessions: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE tenant_id = ?`, "tenant-concurrent").Scan(&count); err != nil {
		t.Fatalf("统计写入总数失败: %v", err)
	}
	if count != writers*perWriter {
		t.Fatalf("并发写入总数 = %d, want %d", count, writers*perWriter)
	}
	if len(sessions) != writers*perWriter {
		t.Fatalf("ListSessions 返回总数 = %d, want %d", len(sessions), writers*perWriter)
	}
}

// TestSessionListIdleSessions 覆盖 ListIdleSessions（一致性对账 worker 的
// 空闲会话枚举，storage.IdleSessionLister）：只返回 updated_at 早于阈值的
// 会话、最近活跃优先、limit 有界。
func TestSessionListIdleSessions(t *testing.T) {
	db := openTestDB(t)
	store := NewSQLiteSessionStore(db)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// updated_at：idle-old 两个（base、base+1min）、active 一个（base+30min，
	// 视为仍在空闲阈值内）。租户混合插入，验证枚举跨租户。
	fixtures := []struct {
		id        string
		tenantID  string
		updatedAt time.Time
	}{
		{"idle-1", "tenant-a", base},
		{"idle-2", "tenant-b", base.Add(time.Minute)},
		{"active", "tenant-a", base.Add(30 * time.Minute)},
	}
	for _, f := range fixtures {
		sess := &storage.Session{ID: f.id, TenantID: f.tenantID, CreatedAt: base, UpdatedAt: f.updatedAt}
		if err := store.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession %s: %v", f.id, err)
		}
	}

	// 阈值取 base+10min：idle-1/idle-2 命中（跨租户），active 不命中。
	idleBefore := base.Add(10 * time.Minute)
	got, err := store.ListIdleSessions(ctx, idleBefore, 100)
	if err != nil {
		t.Fatalf("ListIdleSessions: %v", err)
	}
	if len(got) != 2 || got[0].ID != "idle-2" || got[1].ID != "idle-1" {
		t.Fatalf("ListIdleSessions = %v, want [idle-2 idle-1]（updated_at 倒序）", ids(got))
	}
	for _, sess := range got {
		if !sess.UpdatedAt.Before(idleBefore) {
			t.Fatalf("session %s updated_at %v 不早于阈值 %v", sess.ID, sess.UpdatedAt, idleBefore)
		}
	}

	// limit=1：只返回最近活跃的 idle-2。
	got, err = store.ListIdleSessions(ctx, idleBefore, 1)
	if err != nil {
		t.Fatalf("ListIdleSessions(limit=1): %v", err)
	}
	if len(got) != 1 || got[0].ID != "idle-2" {
		t.Fatalf("limit=1 结果 = %v, want [idle-2]", ids(got))
	}

	// limit<=0：回退默认页大小（等价于不限本次数据量）。
	got, err = store.ListIdleSessions(ctx, idleBefore, 0)
	if err != nil {
		t.Fatalf("ListIdleSessions(limit=0): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit<=0 时长度 = %d, want 2", len(got))
	}

	// 阈值推到所有会话之后：空切片（非 nil）。
	got, err = store.ListIdleSessions(ctx, base.Add(time.Hour), 100)
	if err != nil {
		t.Fatalf("ListIdleSessions(全空闲): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("全空闲时长度 = %d, want 3", len(got))
	}
}
