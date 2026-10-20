package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// db_fresh_install_runtime_shape_test.go
//
// 「全新安装 + 启动一次」之后的**运行时形态**不变量。
//
// # 这道门补的是哪个盲点
//
// 2026-10-21（R61 轮）复核 R60 的「全新安装缺 23 个列」结论时实测发现：
//
//	R60 的测量口径是「**装完、ensure 链没跑**」的一次性库 ⇒ 量到 23 条
//	「生产有、新装没有」的列。但产品**启动时**的 ensure 链（db.Open）
//	会自己 `ADD COLUMN IF NOT EXISTS` 补掉其中 **20 条**：
//
//	  models_canonical.standard_iq / .standard_iq_source / .standard_iq_updated_at
//	  credentials.is_free_tier / .free_quota_limit / .free_quota_window_type
//	  provider_catalog.has_free_tier / .free_tier_notes / .official_free_docs_url
//	  _request_logs.outbound_body
//	  session_turns.host / .tags
//
//	⇒ 同口径复算（装完 **且启动过**）：新装 9245 列 / 生产 14122 列
//	⇒ 差 26 条，**产品读写真正依赖的 = 0 条** ⇒ 「新装开不了库」已不存在。
//
// # 为什么这道门必须存在
//
// 上面那 20 列的「补齐」**只由 ensure 链负责**（它们不在 installer's 迁移链里：
// 350 在 domain 通道、015/075 之类的在 root 通道、session_turns.host/tags
// 与 _request_logs.outbound_body 由 db.go 的 ensure 加）。
// ⇒ ★ **谁把那几段 ensure 的 ALTER 删了，缺口就会静默回来**，
// 而且**两道现有真库门都看不见**：
//   - installer 迁移链的连续性门：管的是「登记的迁移还在不在」，与列无关；
//   - `sql/schema/fresh_install_column_gaps.tsv` 那道门：量的是「生产 vs 新装」的差集，
//     而这些列**装完就没了、启动才有** ⇒ 它的 oracle 必须是 post-ensure 形态
//     （那道门自己有量具自检：库里没有 models_canonical.standard_iq ⇒ 跳过）。
//
// ⇒ 所以这里把「ensure 自愈清单」钉成**显式不变量**：列不在 ⇒ 红。
//
// # 口径说明
//
// 这道门是**有副作用的**：它在 TEST_DATABASE_URL 指向的库上跑 db.Open，
// 也就是**真的启动一次产品**（ensure 链 + 迁移守卫）。
// ⇒ 只能指向**一次性库**（scripts/audit/probe-onetime-db-shape.sh 建的那种），
//
//	绝不要指向生产。
func TestFreshInstallRuntimeShape_EnsureChain(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿" +
			"（⚠ 本门会在目标库上跑 db.Open ⇒ 只能指向一次性库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ① 先跑一次 db.Open = 产品启动一次（ensure 链 + 迁移守卫）。
	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open（= 产品启动一次）失败: %v", err)
	}
	if d == nil {
		t.Fatal("db.Open 返回 nil DB")
	}
	defer d.Close()

	// ② ensure 自愈清单：每一列都必须在（少一列 ⇒ 有个 ensure 的 ALTER 被删了）。
	want := []struct{ table, column string }{
		{"models_canonical", "standard_iq"},
		{"models_canonical", "standard_iq_source"},
		{"models_canonical", "standard_iq_updated_at"},
		{"credentials", "is_free_tier"},
		{"credentials", "free_quota_limit"},
		{"credentials", "free_quota_window_type"},
		{"provider_catalog", "has_free_tier"},
		{"provider_catalog", "free_tier_notes"},
		{"provider_catalog", "official_free_docs_url"},
		{"_request_logs", "outbound_body"},
		{"session_turns", "host"},
		{"session_turns", "tags"},
	}
	for _, w := range want {
		var n int
		if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
			w.table, w.column).Scan(&n); err != nil {
			t.Fatalf("查 %s.%s: %v", w.table, w.column, err)
		}
		if n == 0 {
			t.Errorf("ensure 自愈清单里的 %s.%s 在「装完 + 启动一次」之后仍然缺 ⇒ "+
				"对应的 ensure 段被删了或改了名 ⇒ 全新安装会回到「缺列」状态（R61 实测过这件事）",
				w.table, w.column)
		}
	}
}
