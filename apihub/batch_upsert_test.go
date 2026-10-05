package apihub

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ── §10.30：批量 upsert 的门 ─────────────────────────────────────────────
//
// 为什么这组门必须有：批量路径与单行路径是**同一份契约的两个实现**。
// 它们若对「什么时候该写」产生分歧，分歧在数据上**完全不可见** ——
// n_tup_upd 只会显示一个「差不多」的数，没有任何表状态读数能发现。
// 所以这里钉的全部是**结构不变量**，不是行为结果。

// 门 1：多值 SQL 的占位符编号必须连续且无跳号
func TestBatchSQLPlaceholdersAreContiguous(t *testing.T) {
	for _, rows := range []int{1, 2, 7, maxUpsertBatchRows} {
		sql := buildUpsertAssetsBatchSQL(rows)
		got := map[int]bool{}
		max := 0
		for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(sql, -1) {
			n, _ := strconv.Atoi(m[1])
			got[n] = true
			if n > max {
				max = n
			}
		}
		// VALUES 每行 11 个 + 末尾 1 个共用窗口参数
		want := rows*upsertAssetsBatchRowParams + 1
		if max != want {
			t.Errorf("rows=%d：最大占位符 = $%d，期望 $%d", rows, max, want)
		}
		for i := 1; i <= want; i++ {
			if !got[i] {
				t.Errorf("rows=%d：占位符 $%d 缺失（跳号会让 PG 直接报错，但错误信息很难指向真因）", rows, i)
			}
		}
	}
}

// 门 2：分片上限必须让参数量远离 PG 的 65535 上限
//
// 这是一条**容量契约**：rows 一旦被调大而没人注意，撞上限的表现是
// 「偶发失败」而不是编译错误。
func TestBatchChunkSizeStaysUnderPostgresParamLimit(t *testing.T) {
	const pgParamLimit = 65535
	params := maxUpsertBatchRows*upsertAssetsBatchRowParams + 1
	if params >= pgParamLimit {
		t.Fatalf("单个分片的参数 %d 已达/超 PG 上限 %d", params, pgParamLimit)
	}
	// 记下余量倍数：改 maxUpsertBatchRows 时这个数会变，能一眼看出动了多少
	t.Logf("每片 %d 行 = %d 参数，距 PG 上限 %d 还有 %.1f 倍",
		maxUpsertBatchRows, params, pgParamLimit, float64(pgParamLimit)/float64(params))
	if float64(pgParamLimit)/float64(params) < 2 {
		t.Errorf("余量不足 2 倍（%.1f×），再上调 rows 就会逼近上限", float64(pgParamLimit)/float64(params))
	}
}

// ★ 门 3（本组最重要）：批量 SQL 的 DO UPDATE / WHERE 必须与单行版**逐字一致**
//
// 这是整组门里唯一能抓到「两条路径对写入条件产生分歧」的门。
// 变异方式：把批量版的 WHERE 里的 IS DISTINCT FROM 去掉 ——
// 那种分歧会让批量路径「比单行路径更爱写」，而 n_tup_upd 只会显示
// 一个略大的数，没有任何告警会响。
func TestBatchSQLWriteConditionIsIdenticalToSingleRow(t *testing.T) {
	single := normalizeWS(upsertAssetSQL)
	batch := normalizeWS(buildUpsertAssetsBatchSQL(1))

	// 只比较 DO UPDATE 之后的部分（INSERT ... VALUES 头本来就该不同）
	cut := func(s string) string {
		i := strings.Index(s, "ON CONFLICT")
		if i < 0 {
			t.Fatal("找不到 ON CONFLICT 子句")
		}
		return s[i:]
	}
	sc, bc := cut(single), cut(batch)
	// 把窗口参数编号归一化：单行是 $12，批量 1 行是 $12（恰好相同），
	// 多行才是 $12..$N+1 ⇒ 这里必须先把两者都抹成同一个占位符再比。
	norm := func(s string) string {
		return regexp.MustCompile(`\$\d+`).ReplaceAllString(s, "$?")
	}
	if norm(sc) != norm(bc) {
		t.Errorf("批量版的写入条件与单行版不一致：\n--- 单行 ---\n%s\n--- 批量 ---\n%s",
			sc, bc)
	}
}

func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// 门 4：RegisterBatch 与逐个 Register **最终状态等价**
//
// ★ 这条门存在的理由：memStore 的 UpsertBatch 如果图省事写成「只写最后一行」，
//
//	绝大多数行为测试仍会绿（它们只断言最终状态）。这里显式对拍。
func TestRegisterBatchEquivalentToRepeatedRegister(t *testing.T) {
	mk := func(n int) []Asset {
		out := make([]Asset, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, Asset{
				Kind: KindLLMEndpoint, RefID: int64(i), TenantID: "t1",
				Name: "a", Tags: map[string]string{"k": "v"},
			})
		}
		return out
	}

	batchAssets := mk(50)
	batchStore := newMemStore()
	batchSvc := New(batchStore)
	if err := batchSvc.RegisterBatch(context.Background(), batchAssets); err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}

	singleStore := newMemStore()
	singleSvc := New(singleStore)
	for _, a := range batchAssets {
		if err := singleSvc.Register(context.Background(), a); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	if len(batchStore.assets) != len(singleStore.assets) {
		t.Fatalf("行数不等：批量 %d vs 逐行 %d", len(batchStore.assets), len(singleStore.assets))
	}
	for k, want := range singleStore.assets {
		got, ok := batchStore.assets[k]
		if !ok {
			t.Fatalf("批量路径缺 key %v", k)
		}
		if got.Name != want.Name || got.TenantID != want.TenantID ||
			got.HealthState != want.HealthState || len(got.Tags) != len(want.Tags) {
			t.Errorf("key %v 内容不等：批量 %+v vs 逐行 %+v", k, got, want)
		}
	}

	// 语句条数：这才是本轮改动要买的东西
	if batchStore.batchCalls != 1 {
		t.Errorf("batchCalls = %d，期望 1（50 行应该塌成 1 次调用）", batchStore.batchCalls)
	}
	if singleStore.upsertCalls != 50 {
		t.Errorf("对照组 upsertCalls = %d，期望 50（确认逐行路径没被顺带改掉）", singleStore.upsertCalls)
	}
}

// 门 5：跨租户不得共用一条语句（RLS 会在事务级生效）
//
// ★ 这不是「性能」门而是「正确性」门：单行 Upsert 走
//
//	withTenantTx(a.TenantID) 逐行设 RLS，若批量把两个租户塞进一个事务，
//	第二组的行会被 RLS 策略挡掉 —— **而且不报错，只是静默不写**。
func TestBatchGroupsAcrossTenants(t *testing.T) {
	m := newMemStore()
	svc := New(m)
	assets := []Asset{
		{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "a"},
		{Kind: KindLLMEndpoint, RefID: 2, TenantID: "t2", Name: "b"},
		{Kind: KindLLMEndpoint, RefID: 3, TenantID: "t1", Name: "c"},
		{Kind: KindLLMEndpoint, RefID: 4, TenantID: "t3", Name: "d"},
	}
	if err := svc.RegisterBatch(context.Background(), assets); err != nil {
		t.Fatalf("RegisterBatch: %v", err)
	}
	if len(m.assets) != 4 {
		t.Fatalf("写入 %d 行，期望 4（跨租户必须都落库）", len(m.assets))
	}
	seen := map[string]int{}
	for _, a := range m.assets {
		seen[a.TenantID]++
	}
	if seen["t1"] != 2 || seen["t2"] != 1 || seen["t3"] != 1 {
		t.Errorf("按租户分布不对: %v", seen)
	}
}

// 门 6：坏行被隔离，不得中断整批
//
// 这是 §10.30 约束③ 的落点：改动前是逐行 log+continue，
// 批量化后若一条坏行炸掉整片，就会从「跳过 1 个」变成「丢掉 499 个」。
func TestBatchIsolatesBadRows(t *testing.T) {
	m := newMemStore()
	svc := New(m)
	assets := []Asset{
		{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "good1"},
		{Kind: "not-a-kind", RefID: 2, TenantID: "t1", Name: "bad-kind"},
		{Kind: KindLLMEndpoint, RefID: 3, TenantID: "", Name: "no-tenant"},
		{Kind: KindLLMEndpoint, RefID: 4, TenantID: "t1", Name: "good2"},
	}
	if err := svc.RegisterBatch(context.Background(), assets); err != nil {
		t.Fatalf("RegisterBatch 不该因为坏行整体返回错误: %v", err)
	}
	if len(m.assets) != 2 {
		t.Fatalf("写入 %d 行，期望 2（只落两条合法行）", len(m.assets))
	}
}

// 门 7：空批次是 no-op，且不发语句
func TestBatchEmptyIsNoop(t *testing.T) {
	m := newMemStore()
	svc := New(m)
	if err := svc.RegisterBatch(context.Background(), nil); err != nil {
		t.Fatalf("空批次不该报错: %v", err)
	}
	if m.batchCalls != 0 {
		t.Errorf("batchCalls = %d，期望 0（空批次连语句都不该发）", m.batchCalls)
	}
}
