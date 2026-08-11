package bg

// auto_index_refresher_dedup_test.go — 回归测试：credential_model_index_hot
// rollup 不得因 UNION ALL 两半内部的重复 (bucket, credential_id, raw_model) 行
// 触发 unique-key 冲突。
//
// 背景：154 生产日志持续报
//   rollup credential_model_index: insert: ERROR: duplicate key value
//   violates unique constraint "credential_model_index_hot_unique_key"
// 根因：Half 2（cold-start fallback）从 v_routable_credential_models 多表 JOIN，
// 当一个 credential 对同一 raw_model 有多个 binding 时产生重复行；Half 1 的
// GROUP BY 也可能因 canonical_id/billing_mode 维度产生重复。UNION ALL 不去重，
// INSERT 时违反 (bucket, credential_id, raw_model) 唯一约束。
//
// 修复：fresh CTE 加 half_no 列，最终 SELECT 用 DISTINCT ON 去重。
// 本测试需要真实 PG（v_routable_credential_models 视图 + 多表 JOIN 无法在内存里
// 完整复现），无 TEST_DATABASE_URL 时 skip。它是回归基线：修复后 rollup 必须无
// duplicate-key 错误地完成。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRollupCredentialModelIndex_NoDuplicateKey(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping PG-backed rollup regression test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	r := NewAutoIndexRefresher(pool, nil)
	// 用当前 5-min bucket 跑一次真实 rollup。修复前在有 multi-binding 的库里会
	// 返回 "duplicate key value violates unique constraint" 错误。
	bucket := time.Now().Truncate(5 * time.Minute)
	rows, err := r.rollupCredentialModelIndex(ctx, bucket)
	if err != nil {
		t.Fatalf("rollupCredentialModelIndex failed (expected no duplicate-key error after DISTINCT ON fix): %v", err)
	}
	if rows < 0 {
		t.Fatalf("rows affected = %d, want >= 0", rows)
	}
	t.Logf("rollup OK: %d rows for bucket %s (DISTINCT ON dedup held)", rows, bucket.Format(time.RFC3339))
}
