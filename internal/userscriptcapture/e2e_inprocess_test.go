//go:build integration

// e2e_inprocess_test.go — P0-3 进程内全链 E2E（真 PG + 真组件）：
// HTTP(HMAC+真 KeyVerifier 身份层) → Store(三表+SAVEPOINT) → 同事务
// outbox 行 → RenderWireEnvelope → body_ref 读取端 → canonical session
// 标注（M3）。这是双进程 live E2E 的进程内等价物——live 版堵点见
// scripts/e2e-capture-p03.sh（网关空库 bootstrap 依赖 installer 基线，
// 属部署链问题，非 capture 链问题）。
//
// 启用：TEST_PG_URL=<DSN> go test -tags=integration ./internal/userscriptcapture/
package userscriptcapture

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

func TestCaptureE2EInProcess(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set")
	}
	dsn = testdb.Create(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// —— 最小 schema：capture 三表（迁移 928 正本）+ outbox + sessions +
	// api_keys/applications（KeyVerifier 验证 SQL 的最小形）。
	mustExec := func(sql string) {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("bootstrap: %v\nSQL: %.120s", err, sql)
		}
	}
	// 迁移 ledger 表由基线建；一次性库先补最小形（realdb 测试同款）。
	mustExec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT '')`)
	migration, _ := os.ReadFile("../../sql/migrations/startup/931_userscript_capture_family.sql")
	mustExec(string(migration))
	mustExec(`CREATE TABLE IF NOT EXISTS public.outbox_events (
		id BIGSERIAL PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, event_type TEXT NOT NULL,
		schema_version INT NOT NULL, tenant_id TEXT NOT NULL, aggregate_id TEXT NOT NULL DEFAULT '',
		aggregate_version INT NOT NULL DEFAULT 1, occurred_at TIMESTAMPTZ NOT NULL,
		payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending')`)
	mustExec(`CREATE TABLE IF NOT EXISTS public.sessions (
		id BIGSERIAL PRIMARY KEY, session_id TEXT NOT NULL, tenant_id VARCHAR(255) NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	mustExec(`CREATE TABLE IF NOT EXISTS public.applications (
		id BIGINT PRIMARY KEY, code TEXT, name TEXT, default_client_profile TEXT, customer_id TEXT)`)
	mustExec(`CREATE TABLE IF NOT EXISTS public.api_keys (
		id BIGSERIAL PRIMARY KEY, application_id BIGINT REFERENCES applications(id),
		tenant_id TEXT NOT NULL, key_hash TEXT NOT NULL, key_prefix TEXT,
		owner_user TEXT, rate_limit_rpm INT, rate_limit_concurrent INT, rate_limit_tpm INT,
		key_tier TEXT, budget_usd NUMERIC DEFAULT 0, status TEXT DEFAULT 'active',
		enabled BOOLEAN DEFAULT TRUE, key_alias TEXT, expires_at TIMESTAMPTZ)`)

	// —— 真 KeyVerifier（数据面身份层）：插入同款 HMAC 哈希的 key。
	const secret = "e2e-secret-32-bytes-xxxxxxxxxxxxxx"
	const rawKey = "sk-e2e-capture-key-000000000000000"
	mustExec(`INSERT INTO applications (id, code, name) VALUES (910001,'e2e-app','e2e')`)
	if _, err := pool.Exec(ctx,
		`INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, enabled, status,
			rate_limit_rpm, rate_limit_concurrent, rate_limit_tpm, budget_usd)
		 VALUES (910001,'e2e-tenant',$1,'sk-e2e',TRUE,'active',60,5,100000,0)`,
		authentication.HashAPIKey(secret, rawKey)); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	// 会话行（M3 canonical session 标注的校验对象）。
	mustExec(`INSERT INTO public.sessions (session_id, tenant_id) VALUES ('gw_sess_e2e','e2e-tenant')`)

	// —— 真 Handler：真 store + main 同款 outbox writer + 真 KeyVerifier 适配。
	kv := authentication.NewKeyVerifier()
	kv.SetSecretKey(secret)
	kv.SetDB(pool, secret)

	handler := NewHandler(HandlerConfig{
		Secret: "capture-hmac-secret-32-bytes-xxxxxx",
		Keys:   keyVerifierE2E{kv},
		Store:  NewStore(PoolBeginner(pool.Begin)),
		OutboxWriter: func(ctx context.Context, tx pgx.Tx, envelope outbox.EventEnvelope) error {
			payloadJSON, err := json.Marshal(envelope.Payload)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO outbox_events (event_id, event_type, schema_version, tenant_id,
					aggregate_id, aggregate_version, occurred_at, payload, status)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending') ON CONFLICT (event_id) DO NOTHING`,
				envelope.EventID, envelope.EventType, envelope.SchemaVersion, envelope.TenantID,
				envelope.AggregateID, envelope.AggregateVersion, envelope.OccurredAt, payloadJSON)
			return err
		},
	})

	// —— 一条 HTTP capture 请求（HMAC 三头 + Bearer + X-Gw-Session-Id）。
	const capSecret = "capture-hmac-secret-32-bytes-xxxxxx"
	body := `{"schema_version":"1.0","source_system":"e2e-web","client_session_key":"csk",` +
		`"messages":[{"source_conversation_id":"c1","source_message_id":"m1","sequence":1,` +
		`"content_revision":1,"role":"user","occurred_at":"2026-11-02T12:00:00Z",` +
		`"idempotency_key":"e2e-1","content":"E2E：用户偏好深色主题"}]}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/v1/userscript/captures", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("X-Gw-Session-Id", "gw_sess_e2e")
	req.Header.Set("X-Userscript-Timestamp", ts)
	req.Header.Set("X-Userscript-Nonce", "e2e-nonce")
	req.Header.Set("X-Userscript-Signature", sign(capSecret, ts, "e2e-nonce", []byte(body)))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capture status = %d body=%s", rec.Code, rec.Body.String())
	}

	// —— 断言 1：ack 带 canonical session（M3）与 body_ref。
	var ack Ack
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Accepted != 1 || ack.CanonicalSession != "gw_sess_e2e" {
		t.Fatalf("ack = %+v (want canonical_session=gw_sess_e2e)", ack)
	}
	ref := ack.Messages[0].BodyRef
	if !IsCaptureBodyRef(ref) {
		t.Fatalf("body_ref = %q", ref)
	}

	// —— 断言 2：outbox 行已落（同事务），且 wire 渲染是合法 envelope。
	var evType string
	if err := pool.QueryRow(ctx,
		`SELECT event_type FROM outbox_events WHERE event_type='userscript.captured.v1'`).Scan(&evType); err != nil {
		t.Fatalf("outbox row: %v", err)
	}
	row, err := LookupCaptureBody(ctx, pool, ref, "e2e-tenant")
	if err != nil || row.Content != "E2E：用户偏好深色主题" {
		t.Fatalf("lookup = %+v err=%v", row, err)
	}

	// —— 断言 3：身份层拒绝坏 key（真 KeyVerifier 路径）。
	req2 := httptest.NewRequest(http.MethodPost, "/v1/userscript/captures", strings.NewReader(body))
	req2.Header.Set("Authorization", "Bearer sk-wrong")
	req2.Header.Set("X-Userscript-Timestamp", ts)
	req2.Header.Set("X-Userscript-Nonce", "e2e-n2")
	req2.Header.Set("X-Userscript-Signature", sign(capSecret, ts, "e2e-n2", []byte(body)))
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status = %d, want 401 (real KeyVerifier path)", rec2.Code)
	}
}


// keyVerifierE2E 把真 KeyVerifier 窄化为 handler 身份层接口。
type keyVerifierE2E struct{ kv *authentication.KeyVerifier }

func (k keyVerifierE2E) VerifyKeyTenant(raw string) (string, bool) {
	info, err := k.kv.Verify(context.Background(), raw)
	if err != nil || info == nil || !info.StatusValid() {
		return "", false
	}
	return info.TenantID, true
}
