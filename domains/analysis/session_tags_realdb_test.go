// session_tags_realdb_test.go — 2026-10-04 §9.115 真库门。
//
// 背景：`session_tags` 由迁移 351 创建，而 351 直到本轮才被登记进
// installer 的启动链。后果是**生产写入路径**在全新安装上静默失效：
//
//	cmd/gateway/approval_integration.go
//	    proj := analysis.NewSessionStateProjector(...)
//	    cacheUpdateHook.SetStateProjector(proj)
//	domains/hooks/sessionaudit/cache_update_hook.go
//	    if h.stateProjector != nil { … h.stateProjector.Project(ctx, proj) }
//
// Project() 内部 `INSERT INTO session_tags`，在缺表的库上是 42P01；
// hook 把失败当 best-effort（"不阻断热路径"），所以**没有任何告警**——
// 只是 v6 审计状态再也投影不进统一打标层。
//
// 为什么这道门打 Project() 而不打 hook：hook 吞掉错误，观测不到。
// Project() 在 failed>0 时**返回** error（state_projector.go），所以直接调用
// 能观测到失败。这正是「观测量必须在被检验对象之外」的一个具体形态。
//
// 同时钉住 §9.114.4 量化出的「351 一个文件建 6 张表」：本门断言 6 张表都在，
// 否则只修 session_tags 会让另外 5 张继续缺，而它们各有生产读写方。
//
// 建池照抄产品 db/db.go 的 SimpleProtocol。零足迹：测试自建 session_id 前缀，
// cleanup 里 DELETE 自己的行。
package analysis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sessionTagsRealDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本门不构成 session_tags 写入链可用的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Skipf("parse dsn: %v", err)
	}
	// 生产全量 SimpleProtocol（db/db.go:72）。
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestMigration351TablesExistOnRealDB 断言 351 建的 5 张表都在。
//
// 为什么单独一个测试而不并进写入测试：缺表时本断言 Fatalf，若与写入断言
// 写在同一个函数里，它会**短路**掉后面那条 —— 于是我第一版只会看到
// "表不存在"，永远看不到「Project() 到底会不会报错」这个更要紧的问题。
// 拆开后两个失败各自暴露（我实测：拿掉 session_tags 时两条一起红）。
func TestMigration351TablesExistOnRealDB(t *testing.T) {
	pool := sessionTagsRealDBPool(t)

	// 351 建的六张表必须全在。少一张就说明这次登记只补了 session_tags，
	// 其余五张（各有生产读写方：domains/analysis/clusterer.go、
	// request_summary.go、optimizer.go、admin/session_clusters_handler.go、
	// admin/session_panorama_handler.go）仍缺 —— 那正是 §9.114.4 量化的那批。
	//
	// 这张清单是**数出来的**不是想出来的：第一版我写 5 张，实测发现 351 还建
	// session_optimization_suggestions（第 6 张）。
	for _, tbl := range []string{
		"session_tags", "session_request_summaries", "session_embeddings",
		"session_clusters", "session_cluster_members",
		"session_optimization_suggestions",
	} {
		var ok bool
		if err := pool.QueryRow(context.Background(),
			`SELECT to_regclass($1) IS NOT NULL`, "public."+tbl).Scan(&ok); err != nil {
			t.Fatalf("probe %s: %v", tbl, err)
		}
		if !ok {
			t.Errorf("public.%s 不存在 —— 迁移 351 未被应用（它一张文件建这 6 张表）", tbl)
		}
	}
}

// TestSessionStateProjectorWritesOnRealDB 证明「v6 审计状态 → session_tags」
// 这条生产写入链在真库上成立。**不**在这里检查表是否存在：Project() 自己的
// 返回值就是那个检查，而且比 to_regclass 更贴近生产（它走的是 hook 实际执行
// 的那条 INSERT）。
func TestSessionStateProjectorWritesOnRealDB(t *testing.T) {
	pool := sessionTagsRealDBPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("xtags-%d", time.Now().UnixNano())
	sessionID := "xtags-" + suffix
	tenantID := "xtags-tenant-" + suffix

	// v6 投影：approval_status + optimization_tag 应派生出至少两条 tag。
	proj := NewSessionStateProjector(NewPoolDB(pool), nil)
	derivable := deriveStateTags(SessionStateProjection{
		GwSessionID:     sessionID,
		TenantID:        tenantID,
		ApprovalStatus:  "approved",
		OptimizationTag: "compress_thinking",
	})
	if len(derivable) == 0 {
		t.Fatalf("夹具前提不成立：deriveStateTags 对 approved+compress_thinking 派生出 0 条，" +
			"Project() 会直接 return nil，断言将全部落空")
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM session_tags WHERE gw_session_id = $1 AND tenant_id = $2`,
			sessionID, tenantID); err != nil {
			t.Logf("cleanup session_tags rows: %v", err)
		}
	})

	// 这就是 hook 在生产里调用的那个方法。缺表时它返回 error，hook 吞掉。
	if err := proj.Project(ctx, SessionStateProjection{
		GwSessionID:     sessionID,
		TenantID:        tenantID,
		ApprovalStatus:  "approved",
		OptimizationTag: "compress_thinking",
	}); err != nil {
		t.Fatalf("Project() 在真库上失败（全新安装缺 session_tags 时这里是 42P01，"+
			"hook 会把它当 best-effort 吞掉，所以这条链只能这样直接观测）：%v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_tags WHERE gw_session_id = $1 AND tenant_id = $2`,
		sessionID, tenantID).Scan(&n); err != nil {
		t.Fatalf("count projected tags: %v", err)
	}
	if n != len(derivable) {
		t.Fatalf("投影落库 %d 行，deriveStateTags 派生 %d 条 —— 写入链未完整落库", n, len(derivable))
	}
}
