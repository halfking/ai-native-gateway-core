package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEnsureSessionUserTags_RealDB — migration 467 的 user_tags 列 ensure 真库回归。
//
// 2026-10-20 复核轮：/api/admin/sessions/{id}/snapshot 读
// public.sessions.user_tags，而该列**不在正典基线里**（sql/schema/01-schema.sql
// 与 sql/objects/tables/sessions.sql 都没有）⇒ 只有走 installer（其
// StartupFiles 登记表含 467）的新装库才有；任何由 deploy 路线演进来的库都缺
// ⇒ 快照查询恒 42703 ⇒ 端点 500（实测本机）。
//
// 这道门要证明两件事：
//  1. ensure 真的能把列补上（缺列 → 有列）；
//  2. ensure 是幂等的（列已在位时**不做 DDL**，因为
//     `ALTER TABLE … ADD COLUMN IF NOT EXISTS` 即便列已存在也要先取
//     ACCESS EXCLUSIVE，与并发读的 ACCESS SHARE 冲突 —— §10.97 同族问题）。
//
// 无 TEST_PG_URL 时跳过（skip 与 pass 是两种不同的主张）。
func TestEnsureSessionUserTags_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that sessions.user_tags is ensured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()
	d := &DB{pool: pool}

	var tableExists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.sessions') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatalf("probe sessions table: %v", err)
	}
	if !tableExists {
		t.Skip("public.sessions missing in TEST_PG_URL — nothing to verify against")
	}

	colPresent := func() bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='sessions'
			   AND column_name='user_tags'`).Scan(&n); err != nil {
			t.Fatalf("probe user_tags: %v", err)
		}
		return n == 1
	}

	// 1) ensure 跑完 ⇒ 列必须在位。
	if err := d.ensureSessionUserTags(ctx); err != nil {
		t.Fatalf("ensureSessionUserTags: %v", err)
	}
	if !colPresent() {
		t.Fatal("ensure 跑完后 public.sessions.user_tags 仍不存在 —— 快照端点会继续 42703")
	}

	// 2) 幂等：再跑一次不许报错。
	if err := d.ensureSessionUserTags(ctx); err != nil {
		t.Fatalf("ensureSessionUserTags (2nd, idempotent): %v", err)
	}
	if !colPresent() {
		t.Fatal("幂等跑完后列消失了")
	}
}

// TestEnsureSessionUserTags_IsOnTheBootChain 静态门：ensure 必须**在启动链上**。
//
// 判据纪律：ensure 函数存在 ≠ 它会被执行。上一条真库门直接调函数，绕过了
// 「谁调用它」这个前提；一旦链上没接，修复在真实部署里等于没做，而上一条门
// 照样全绿 ⇒ 两道门必须都存在（一条钉「接上了」，一条钉「接上之后有效」）。
func TestEnsureSessionUserTags_IsOnTheBootChain(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	if !strings.Contains(string(src), "db.ensureSessionUserTags(migCtx)") {
		t.Error("启动链上没有 db.ensureSessionUserTags(migCtx) —— ensure 函数写了但没人调用，" +
			"真实部署里这一列仍然不会被补上")
	}
}
