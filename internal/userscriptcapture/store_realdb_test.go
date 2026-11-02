//go:build integration

// store_realdb_test.go — capture 三表族的真库验证（P0-3 M1）：
// 迁移 928 建表 + Persist 正向（batch/messages/bodies + body_ref 签发）
// + 幂等重放（同 idempotency_key 重发命中唯一约束计 duplicate）。
//
// 启用：TEST_PG_URL=<一次性 PG DSN> go test -tags=integration ./internal/userscriptcapture/
package userscriptcapture

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

func TestCapturePersistRealDB(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; capture realdb test requires a disposable PostgreSQL database")
	}
	dsn = testdb.Create(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = pool.Close(ctx) }()

	// 迁移 ledger 表由更早的基线迁移创建；本测试只跑 928，先补最小
	// ledger 形态（与生产同款两列）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version TEXT PRIMARY KEY,
			description TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatalf("bootstrap ledger: %v", err)
	}

	// outbox_events 由网关基线迁移建；一次性库补最小同形表（列名/约束
	// 与生产一致，够 INSERT ON CONFLICT 用）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.outbox_events (
			id BIGSERIAL PRIMARY KEY,
			event_id TEXT NOT NULL UNIQUE,
			event_type TEXT NOT NULL,
			schema_version INT NOT NULL,
			tenant_id TEXT NOT NULL,
			aggregate_id TEXT NOT NULL DEFAULT '',
			aggregate_version INT NOT NULL DEFAULT 1,
			occurred_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending'
		)`); err != nil {
		t.Fatalf("bootstrap outbox: %v", err)
	}

	// 建表：直接执行迁移 928 正本（真库验证迁移本身可跑）。
	migration, err := os.ReadFile("../../sql/migrations/startup/928_userscript_capture_family.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration 928: %v", err)
	}

	store := NewStore(PoolBeginner(func(ctx context.Context) (pgx.Tx, error) {
		return pool.Begin(ctx)
	}))

	batch := Batch{
		SchemaVersion: SchemaVersion, SourceSystem: "chatgpt-web",
		ClientSessionKey: "csk-real",
		Messages: []Message{
			{
				SourceConversationID: "conv-1", SourceMessageID: "m-1",
				Sequence: 1, ContentRevision: 1, Role: "user",
				OccurredAt: time.Now().UTC(), IdempotencyKey: "idem-r1",
				Content: "真实库验证：用户偏好深色主题",
			},
			{
				SourceConversationID: "conv-1", SourceMessageID: "m-2",
				Sequence: 2, ContentRevision: 1, Role: "assistant",
				OccurredAt: time.Now().UTC(), IdempotencyKey: "idem-r2",
				Content: "已记录偏好",
			},
		},
	}

	var capturedEvent *outbox.EventEnvelope
	writeOutbox := func(ctx context.Context, tx pgx.Tx, env outbox.EventEnvelope) error {
		capturedEvent = &env
		return nil
	}
	ack, err := store.Persist(ctx, "tenant-real", "user-1", batch, writeOutbox)
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if ack.Accepted != 2 || ack.Duplicates != 0 {
		t.Fatalf("ack = %+v", ack)
	}
	for _, m := range ack.Messages {
		if len(m.BodyRef) <= len(BodyRefPrefix) || m.BodyRef[:len(BodyRefPrefix)] != BodyRefPrefix {
			t.Fatalf("body_ref = %q, want internal://capture/<uuid>", m.BodyRef)
		}
	}

	// 幂等重放：同批次同 idempotency_key 重发 → 2 duplicates，不报错。
	ack2, err := store.Persist(ctx, "tenant-real", "user-1", batch, writeOutbox)
	if err != nil {
		t.Fatalf("replay Persist: %v", err)
	}
	if ack2.Accepted != 0 || ack2.Duplicates != 2 {
		t.Fatalf("replay ack = %+v, want 0 accepted / 2 duplicates", ack2)
	}

	// 落库核对：bodies 内容与 body_ref 对得上。
	var content string
	if err := pool.QueryRow(ctx,
		`SELECT content FROM public.capture_bodies WHERE body_ref = $1`, ack.Messages[0].BodyRef,
	).Scan(&content); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if content != batch.Messages[0].Content {
		t.Fatalf("stored content = %q", content)
	}

	// 批次头核对。
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT message_count FROM public.capture_batches WHERE batch_id::text = $1`, ack.BatchID,
	).Scan(&count); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if count != 2 {
		t.Fatalf("batch message_count = %d", count)
	}

	// M2 断言：userscript.captured.v1 事件形状（无 content、带 body_ref）。
	if capturedEvent == nil {
		t.Fatal("outbox event not written")
	}
	if capturedEvent.EventType != "userscript.captured.v1" || capturedEvent.TenantID != "tenant-real" {
		t.Fatalf("event = %+v", capturedEvent)
	}
	msgs, _ := capturedEvent.Payload["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("event messages = %d, want 2", len(msgs))
	}
	for _, m := range msgs {
		if _, hasContent := m["content"]; hasContent {
			t.Fatal("event payload must not carry content (bodies stay behind body_ref)")
		}
		if m["body_ref"] == "" || m["idempotency_key"] == "" {
			t.Fatalf("event message missing locator fields: %+v", m)
		}
	}
	// 重放后事件不重写（writer 未被再次调用 ⇒ capturedEvent 仍是首次值）。
	if ack2.Accepted != 0 && capturedEvent == nil {
		t.Fatal("replay must not rewrite event")
	}

	// M3：canonical session 标注——sessions 表有该会话 ⇒ true；无 ⇒ false。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.sessions (
            id BIGSERIAL PRIMARY KEY,
            session_id TEXT NOT NULL,
            tenant_id VARCHAR(255) NOT NULL,
            created_at TIMESTAMPTZ NOT NULL DEFAULT now()
        )`); err != nil {
		t.Fatalf("bootstrap sessions: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.sessions (session_id, tenant_id) VALUES ($1,$2)`,
		"gw_sess_e2e", "tenant-real"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if !store.SessionExists(ctx, "tenant-real", "gw_sess_e2e") {
		t.Fatal("existing session must be found")
	}
	if store.SessionExists(ctx, "tenant-real", "gw_missing") || store.SessionExists(ctx, "tenant-other", "gw_sess_e2e") {
		t.Fatal("missing/cross-tenant session must not be found")
	}

	// M2 读取端：LookupCaptureBody 正租户命中、跨租户 ErrCaptureBodyNotFound。
	row, err := LookupCaptureBody(ctx, pool, ack.Messages[0].BodyRef, "tenant-real")
	if err != nil {
		t.Fatalf("LookupCaptureBody: %v", err)
	}
	if row.Content != batch.Messages[0].Content || row.Role != "user" || row.SHA256 == "" {
		t.Fatalf("body row = %+v", row)
	}
	if _, err := LookupCaptureBody(ctx, pool, ack.Messages[0].BodyRef, "tenant-other"); err != ErrCaptureBodyNotFound {
		t.Fatalf("cross-tenant lookup must be not-found, got %v", err)
	}
	if !IsCaptureBodyRef(ack.Messages[0].BodyRef) {
		t.Fatal("IsCaptureBodyRef must accept issued refs")
	}
	if IsCaptureBodyRef("internal://body/req-1/prompt") {
		t.Fatal("IsCaptureBodyRef must reject turn-stream refs")
	}
}
