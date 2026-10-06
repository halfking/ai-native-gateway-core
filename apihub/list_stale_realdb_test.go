package apihub

// listStale 分页的真库门（2026-10-06，runbook §10.34）
//
// ★ 这是**唯一**能区分「截断」与「完整」的证据。
//   源码门只能证明 OFFSET 存在、循环存在；它证明不了
//   「传进去的 offset 真的让 PG 返回了不同的行」。
//   改 OFFSET 顺序、写错排序键、RLS 把第二页也挡掉 —— 这些在源码里都看不出来。
//
// 打的是**真实的 public.assets 表**（SQL 里表名硬编码，改成临时表就等于改了被测物），
// 所以用独立租户 `staletest` + 独立 ref_id 段，并逐条清理。
//
// 未设 TEST_DATABASE_URL 时会 Skip。★ Skip 不等于通过：
//   配套的源码门 TestListStaleSQLIsPaged 保证那一刻仍有东西在守，
//   且它不需要真库。两者缺一，结论都不成立。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// staleTestRefBase 与 batchTestRefBase(99000000) 刻意不重叠。
const staleTestRefBase = 99100000

const staleTestTenant = "staletest"

func cleanupStaleTestRows(t *testing.T) {
	t.Helper()
	_, err := connectBatch(t).Exec(context.Background(),
		`DELETE FROM public.assets WHERE tenant_id = $1 AND ref_id >= $2 AND ref_id < $3`,
		staleTestTenant, staleTestRefBase, staleTestRefBase+5000)
	if err != nil {
		t.Fatalf("清理 stale 测试行失败: %v", err)
	}
}

// seedStale 造 n 个「早已停止上报」的资产。
// 先用生产写路径 UpsertBatch 落库（顺带保证这批行走的是真实插入路径），
// 再统一把 last_seen_at 回拨 —— 因为 upsert 会把它写成 now()。
func seedStale(t *testing.T, n int) {
	t.Helper()
	pool := connectBatch(t)
	assets := make([]Asset, 0, n)
	for i := 0; i < n; i++ {
		assets = append(assets, Asset{
			Kind:        KindLLMEndpoint,
			RefID:       int64(staleTestRefBase + i),
			TenantID:    staleTestTenant,
			Name:        "stale-" + itoa(int64(i)),
			HealthState: HealthUnknown,
			Version:     "0.0.0",
		})
	}
	// 2026-10-06（R48 轮遗留的编译破损）：UpsertBatch 的签名在
	// bc60cab71 改成 (int, error)，而本文件（95721459b 引入）
	// 仍按旧的单返回值调用 ⇒ `apihub` 包在 go test 下编译不过。
	// 注意 `go build ./...` 不编译 _test.go，所以只有这条门会红。
	//
	// 返回的 int 是本次 upsert 实际影响的行数；本用例只关心
	// 「行写进去了」，不关心 affected 的具体数值（它受 upsert 的
	// 心跳门控影响，见 pg_store.go 的 WHERE 两半），
	// 所以显式丢弃而不是拿它当断言。
	if _, err := NewPGStore(pool).UpsertBatch(context.Background(), assets); err != nil {
		t.Fatalf("seed UpsertBatch: %v", err)
	}
	// 回拨到 48 小时前，远超 6h 阈值。
	tag, err := pool.Exec(context.Background(),
		`UPDATE public.assets SET last_seen_at = now() - interval '48 hours'
		  WHERE tenant_id = $1 AND ref_id >= $2 AND ref_id < $3`,
		staleTestTenant, staleTestRefBase, staleTestRefBase+n)
	if err != nil {
		t.Fatalf("回拨 last_seen_at 失败: %v", err)
	}
	if int(tag.RowsAffected()) != n {
		t.Fatalf("回拨只影响 %d 行，期望 %d", tag.RowsAffected(), n)
	}
}

// ── 门：超过一页时必须拿全 ────────────────────────────────────────────
//
// 1234 > 1000（页大小）。改动前这个用例会拿到 1000 ——
// 而 1000 是一个「看起来完全正常」的数，不是 0，也不是空。
func TestListStaleRealDBReturnsMoreThanOnePage(t *testing.T) {
	connectBatch(t)
	cleanupStaleTestRows(t)
	t.Cleanup(func() { cleanupStaleTestRows(t) })

	const n = 1234
	seedStale(t, n)

	got, err := NewPGStore(connectBatch(t)).ListStale(context.Background(), staleTestTenant, 6*time.Hour)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	if len(got) != n {
		t.Fatalf("ListStale 返回 %d 行，期望 %d。\n"+
			"★ 拿到 %d（恰好等于页大小）说明被 LIMIT 截断了：超出部分的资产"+
			"永远不会被标成 Degraded，且**不报错、不留痕**。",
			len(got), n, len(got))
	}
	// 正向自证：这批行确实都是 stale 且都属本租户。
	// 不加这条，一个「返回 1234 行但全是别的租户的行」的回归也能骗过计数。
	seen := map[string]bool{}
	for _, a := range got {
		if a.TenantID != staleTestTenant {
			t.Fatalf("返回了不属于 %s 的行（tenant=%s）—— 分页把租户过滤弄丢了",
				staleTestTenant, a.TenantID)
		}
		seen[a.Name] = true
	}
	if len(seen) != n {
		t.Errorf("去重后只有 %d 个不同资产，期望 %d ⇒ 翻页过程中有重复行", len(seen), n)
	}
}

// ── 门：恰好整页时也必须收尾 ──────────────────────────────────────────
//
// n 恰好等于页大小时，最后一次查询返回 0 行。
// 终止条件若写成「读到 0 行才算结束」在这里仍然能过，
// 但若写成「读到 0 行才结束」且**同时**忘了处理空页，循环就会多转一轮；
// 更要命的是把「不满一页」写成「等于 0」时，n=1000 与 n=999 走出两条路径。
// 这条门把 n 正好等于页大小的边界钉住。
func TestListStaleRealDBHandlesExactPageMultiple(t *testing.T) {
	connectBatch(t)
	cleanupStaleTestRows(t)
	t.Cleanup(func() { cleanupStaleTestRows(t) })

	const n = listStalePageSize // 1000，整除
	seedStale(t, n)

	got, err := NewPGStore(connectBatch(t)).ListStale(context.Background(), staleTestTenant, 6*time.Hour)
	if err != nil {
		t.Fatalf("ListStale: %v", err)
	}
	if len(got) != n {
		t.Fatalf("恰好 %d 行（页大小整数倍）时期望 %d，实得 %d", n, n, len(got))
	}
}

// ── 门：保险丝触顶必须**报错**，不能静默返回部分结果 ──────────────────
//
// 这是防御分支的真库覆盖。★ 阈值必须调成 **1** 而不是 2：
// 1234 行正好 2 页，而保险丝在「发起第 maxPages+1 次查询前」判断，
// 于是 maxPages=2 时循环在第 2 页（234 行，不满页）正常收尾，保险丝**根本碰不到**。
// ⇒ 「把阈值设成刚好够用的数」是测不出保险丝的 —— 上一版就栽在这里。
//
// ★ 断言的重点不是「返回了错误」，而是**错误优先于数据**：
//
//	若实现写成触顶时 return 已取到的那部分，调用方就会拿到 1000 行
//	且 err == nil ⇒ 与修复前的静默截断**完全同形**，
//	保险丝就等于一个换了个位置的同款 bug。
func TestListStaleRealDBFuseErrorsInsteadOfTruncating(t *testing.T) {
	connectBatch(t)
	cleanupStaleTestRows(t)
	t.Cleanup(func() { cleanupStaleTestRows(t) })

	const n = 1234
	seedStale(t, n)

	orig := listStaleMaxPages
	listStaleMaxPages = 1 // 1234 行需要 2 页 ⇒ 第 2 页前就触顶
	t.Cleanup(func() { listStaleMaxPages = orig })

	got, err := NewPGStore(connectBatch(t)).ListStale(context.Background(), staleTestTenant, 6*time.Hour)
	if err == nil {
		t.Fatalf("保险丝触顶却没有报错，返回了 %d 行。\n"+
			"★ 返回数据 + err==nil 与修复前的静默截断同形：调用方无法分辨"+
			"「这是全部」与「这是前 2 页」。防御分支必须以错误收场。", len(got))
	}
	if got != nil {
		t.Errorf("保险丝触顶时不应同时返回部分数据，实得 %d 行", len(got))
	}
	if !strings.Contains(err.Error(), "游标没有推进") {
		t.Errorf("错误信息应说明是游标未推进（便于判断是数据异常还是代码回归），实得: %v", err)
	}
}
