package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// find_recent_gateway_session_pg_test.go — 2026-11-11（ZCode 事故轮 ·
// 会话粘滞查询修复）：真库门 —— 用**真实的 PostgreSQL** 跑真实的
// FindRecentGatewaySession，证明参数类型与列类型对齐。
//
// 离线门（find_recent_gateway_session_param_test.go）判的是「参数类型 ↔
// 占位符 cast」这一维；本门判的是**语义**那一维：这条查询在真库上能不能
// 真的跑通（列类型、索引、SQL 语法都参与）。
//
// ★ 为什么两个门都要：离线门永远在线、但只看类型；真库门看全貌、但没有
//
//	DSN 时会 skip（仓内判据：门禁对沉睡文件不作证 ⇒ 两个门互补）。
//
// ★ 失败形态（修复前）：查询在**编码期**就失败，返回
//
//	"failed to encode args[1]: unable to encode 2 into text format for
//	 text (OID 25): cannot find encode plan"
//	⇒ 生产表现是 503「session store unavailable」，且日志里看不到原因。
//
// 跑法（仓内 DSN 约定，TEST_PG_DSN 优先）：
//
//	TEST_PG_DSN='postgres://user:pass@host:5432/db?sslmode=disable' \
//	  go test ./domains/hooks/observability/telemetry/ -run TestFindRecentGatewaySession_LivePG -v
func TestFindRecentGatewaySession_LivePG(t *testing.T) {
	dsn := resolveTestDSN()
	if dsn == "" {
		t.Skip("TEST_PG_DSN (or LLM_GATEWAY_TEST_PG_DSN) not set — live PG gate asleep")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to PG: %v", err)
	}
	defer pool.Close()

	c := &Client{}
	c.SetDB(pool)

	// 传一个不可能命中的 identity_hash：期望是「查得到、但没有行」⇒
	// err 为 nil。若参数类型与列类型不匹配，err 会是编码期的
	// "cannot find encode plan"。
	const (
		tenant = "default"
		apiKey = 2
		hash   = "0000000000000000000000000000000000000000000000000000000000000000"
	)
	sessionID, err := c.FindRecentGatewaySession(ctx, tenant, hash, apiKey, 5*time.Minute)
	if err != nil {
		t.Fatalf("sticky-session query must not fail against a real PG: %v", err)
	}
	if sessionID != "" {
		t.Logf("sticky-session lookup returned %q (unexpected for a synthetic hash)", sessionID)
	}
}
