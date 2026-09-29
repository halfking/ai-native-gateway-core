package admin

import (
	"context"
	"testing"
	"time"
)

// dimensionNames 的三条 SQL 必须能在**真库**上跑通。
//
// 这不是形式检查：`SELECT id, name FROM api_keys` 曾经恒报错（public.api_keys
// 没有 name 列，information_schema 里能看到的那个 name 属于 orchestrator 下的
// 另一张同名表），而 idLabelMap 把错误吞了，于是 apikey 一列永远显示裸 id、
// 页面不报错、日志无痕。scratch schema 里自造的同名表带 name 列，所以既有
// E2E 全绿也照样漏掉了它——这类 bug 只能在真表上验。
//
// 用真库而不是桩：只有真表能证明列存在。TEST_DATABASE_URL 未设置时跳过，
// 保持 CI 无库环境绿。
func TestDimensionNamesQueriesRunAgainstRealSchema(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h := &Handler{db: pool}
	names := h.dimensionNames(ctx)

	// 三张表都必须真的查到行；任一张为空说明查询写错了列（表存在但恒报错
	// 会被 idLabelMap 吞掉）。
	if len(names.Providers) == 0 {
		t.Error("providers 名称映射为空——display_name 查询可能写错了列")
	}
	if len(names.Credentials) == 0 {
		t.Error("credentials 名称映射为空——label 查询可能写错了列")
	}
	if len(names.APIKeys) == 0 {
		t.Error("api_keys 名称映射为空——key_alias/key_prefix 查询可能写错了列")
	}

	// 抽查一条 apikey 映射必须是人可读的名称，不能是空串。
	for id, label := range names.APIKeys {
		if label == "" {
			t.Fatalf("apikey id=%d 映射到空串（COALESCE 回落链没接上）", id)
		}
		break
	}
}
