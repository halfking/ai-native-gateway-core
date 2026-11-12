//go:build integration

package admin

// monitor-summary total_requests 预物化（迁移 963）的**真库等价腿**。
//
// 口径等价链的三方对拍（在真库上逐凭据验证）：
//
//	实时两臂计数（SessionFamilyCredentialCountSourceSQL 逐凭据）
//	== 预物化值（CredRollupUpsertSQL 刷新后的 credential_total_requests_rollup）
//	== 读面表达式取值（CredentialTotalRequestsReadSQL 双形态 CASE）
//
// ★ 预物化读数必须等于实时计数——这是「口径零变化」的机器证据；
// ★ 读面表达式在表非空时必须取预物化分支的值（否则 CASE 形态退化）。
//
// 运行：
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestMonitorSummaryRollupParity
//
// 前提：迁移 963 已应用（表存在）。刷新函数自带 SET LOCAL statement_timeout。
// 本测试对生产/共享库只读 + 只写 963 自己的预物化表（幂等 UPSERT，刷新
// 面与网关 boot 后台 job 是同一语句）——不触碰业务表。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

func TestMonitorSummaryRollupParity(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the rollup parity holds")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 前提：963 已应用。
	var tableExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.credential_total_requests_rollup') IS NOT NULL").Scan(&tableExists); err != nil {
		t.Fatalf("probe table: %v", err)
	}
	if !tableExists {
		t.Fatal("迁移 963 未应用（credential_total_requests_rollup 不存在）——先应用迁移再跑本腿")
	}

	// ① 刷新（与 bg job 同一语句）。
	refresher := bg.NewCredentialRollupRefresher(pool)
	if err := refresher.RefreshOnce(ctx); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}

	// ② 预物化值 == 实时两臂计数（逐凭据对拍）。
	rows, err := pool.Query(ctx, "SELECT credential_id, total_requests FROM public.credential_total_requests_rollup ORDER BY credential_id LIMIT 20")
	if err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	defer rows.Close()
	compared := 0
	for rows.Next() {
		var credID, rolledUp int64
		if err := rows.Scan(&credID, &rolledUp); err != nil {
			t.Fatalf("scan rollup: %v", err)
		}
		var live int64
		liveSQL := "SELECT count(*) FROM " + dbpkg.SessionFamilyCredentialCountSourceSQL("$1::bigint") + " q"
		if err := pool.QueryRow(ctx, liveSQL, credID).Scan(&live); err != nil {
			t.Fatalf("live count for %d: %v", credID, err)
		}
		if live != rolledUp {
			t.Fatalf("凭据 %d 口径漂移：预物化=%d 实时两臂=%d——聚合表达式与按凭据计数不再等价", credID, rolledUp, live)
		}
		compared++
	}
	if rows.Err() != nil {
		t.Fatalf("rows: %v", rows.Err())
	}
	if compared == 0 {
		t.Skip("预物化表 0 行（库内 session_turns 无凭据流量）——本库无数据可对拍，不构成等价性证据")
	}

	// ③ 读面表达式在表非空时取预物化分支：选一个同时在 credentials 与
	//    rollup 里的凭据，把预物化值临时抬高 1（同事务写读，最后回滚），
	//    读面 CASE 必须返回抬高后的值（≠ 实时计数）⇒ 证明走的是预物化
	//    分支而不是回落分支。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var probeCred int64
	err = tx.QueryRow(ctx,
		"SELECT r.credential_id FROM public.credential_total_requests_rollup r JOIN public.credentials c ON c.id = r.credential_id ORDER BY r.credential_id LIMIT 1").Scan(&probeCred)
	if err != nil {
		t.Logf("no credential present in both credentials and rollup — CASE branch probe skipped (parity leg ①② already verified)")
		t.Logf("rollup parity verified on %d credentials", compared)
		return
	}
	var liveProbe int64
	if err := tx.QueryRow(ctx, "SELECT total_requests FROM public.credential_total_requests_rollup WHERE credential_id = $1", probeCred).Scan(&liveProbe); err != nil {
		t.Fatalf("read probe rollup value: %v", err)
	}
	if _, err := tx.Exec(ctx, "UPDATE public.credential_total_requests_rollup SET total_requests = total_requests + 1 WHERE credential_id = $1", probeCred); err != nil {
		t.Fatalf("bump: %v", err)
	}
	readSQL := "SELECT " + dbpkg.CredentialTotalRequestsReadSQL("c.id") + " FROM public.credentials c WHERE c.id = $1"
	var viaCase int64
	if err := tx.QueryRow(ctx, readSQL, probeCred).Scan(&viaCase); err != nil {
		t.Fatalf("read-face CASE probe: %v", err)
	}
	if viaCase != liveProbe+1 {
		t.Fatalf("读面 CASE 未取预物化分支：抬高后预物化=%d CASE 实取=%d（回落分支会给出实时计数 %d）——CASE 形态退化或 credExpr 绑定断裂", liveProbe+1, viaCase, liveProbe)
	}
	t.Logf("rollup parity verified on %d credentials; CASE branch probe credential=%d (bumped %d → CASE returned %d)", compared, probeCred, liveProbe, viaCase)
}
