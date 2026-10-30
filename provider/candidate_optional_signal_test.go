package provider

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 2026-10-31 实施包审计：可选信号降级的判据。
// 注意：这里钉的是 SQL 形态与状态机；"降级 SQL 在真库上可执行、函数缺失时候选仍可返回"
// 由 candidate_optional_signal_realdb_test.go 在有 DSN 时实测。

func TestCandidateCacheSignalCallShapeIsUnique(t *testing.T) {
	full := candidateQuerySQL()
	if n := strings.Count(full, candidateCacheSignalCallSQL); n != 1 {
		t.Fatalf("chr 调用形态在候选查询中出现 %d 次，必须恰为 1；模板被改写时降级会静默失效", n)
	}
}

func TestCandidateQuerySQLWithoutCacheSignal_RemovesOnlyTheChrCall(t *testing.T) {
	full := candidateQuerySQL()
	degraded, applied := candidateQuerySQLWithoutCacheSignal()

	// ★ 降级必须**真的生效**（★ 否则调用点会拿一条语义未知的 SQL 去查 ⇒ ⇒
	// ★ 而旧实现在这里不会红 ⇒ ⇒ ⇒ ★ 这条是「降级是否可用」的唯一出口）。
	if !applied {
		t.Fatalf("★ 降级未生效（模板调用形态不唯一）⇒ ⇒ 降级路径不可用")
	}
	if strings.Contains(degraded, "recent_cache_hit_rate(") {
		t.Fatal("降级 SQL 仍然调用 recent_cache_hit_rate")
	}
	if !strings.Contains(degraded, candidateCacheSignalAbsentSQL) {
		t.Fatal("降级 SQL 缺少 chr 中性投影")
	}
	// 只允许这一处差异：把降级投影还原回调用形态，必须逐字等于完整 SQL。
	if restored := strings.Replace(degraded, candidateCacheSignalAbsentSQL, candidateCacheSignalCallSQL, 1); restored != full {
		t.Fatal("降级 SQL 与完整 SQL 的差异不止 chr 一处")
	}
	// 可选维度之外的硬依赖（rsr，硬过滤信号）必须原样保留。
	if !strings.Contains(degraded, "recent_success_rate(c.id, mo.raw_model_name, 50)") {
		t.Fatal("降级不得移除 recent_success_rate（它是硬过滤信号，不是可选维度）")
	}
}

func TestCandidateQuerySQLWithoutCacheSignal_FollowsCurrentModalityGate(t *testing.T) {
	// ★★★ **2026-10-31 审计轮补的判据**：★ 降级 SQL **必须跟着当前口径走**。
	//
	// ★ 背景：★ 候选 SQL 不是常量 —— 它按运行时环境变量 `ModalityRoutingStrictEnv`
	// ★ 在【缺省档 / 严格档】两套模态判词闸门之间切换 ⇒ ⇒ ★ 而降级 SQL 曾被
	// ★ `sync.Once` **算一次就永久缓存** ⇒ ⇒ ⇒ ★★ **闸门在降级期间被切换 ⇒ ⇒
	// ★★ 降级路径会一直用旧闸门** ⇒ ⇒ ★★ ⇒ **路由策略与当前配置不一致，且完全静默**。
	//
	// ★ 判据形态：★ 严格档比缺省档多一段 `mmv3` 判词 ⇒ ⇒ ★ 用它当两档的区分标记。
	// ★ ★★ **为什么要两档各算一次**：★★ 只算一次的话，★ **缓存版与现算版会给出
	// ★ ★ 同一个结果**（★ 第一次算的就是当前档）⇒ ⇒ ⇒ ★ **判据会恒绿** ⇒ ⇒
	// ★ ⇒ ★★ 所以必须**换档再算** ⇒ ⇒ ⇒ ★ 换档后仍相等 ⇒ ⇒ 就是没跟上 ⇒ ⇒ ⇒ ★ 红。

	t.Setenv(ModalityRoutingStrictEnv, "")
	defaultDegraded, defaultApplied := candidateQuerySQLWithoutCacheSignal()
	defaultFull := candidateQuerySQL()

	t.Setenv(ModalityRoutingStrictEnv, "1")
	strictDegraded, strictApplied := candidateQuerySQLWithoutCacheSignal()
	strictFull := candidateQuerySQL()

	if !defaultApplied || !strictApplied {
		t.Fatalf("★ 降级未生效（default=%v strict=%v）⇒ ⇒ 本判据无法执行", defaultApplied, strictApplied)
	}

	// ① 两档的完整 SQL 必须真的不同（★ 否则下面的判据没有分辨力 ⇒ ⇒ 假绿）。
	if defaultFull == strictFull {
		t.Fatalf("★ 缺省档与严格档的候选 SQL 相同 ⇒ ⇒ 判据无分辨力（★ 闸门形态变了？）")
	}
	// ② 降级 SQL 必须跟着当前档走（★ **这条就是本次修的那个缺陷**）。
	if defaultDegraded == strictDegraded {
		t.Fatalf("★★ 降级 SQL 不随模态闸门档位变化 ⇒ ⇒ 【降级路径会一直用旧闸门】⇒ ⇒ " +
			"★★ ⇒ **路由策略与当前配置不一致，且完全静默** ⇒ ⇒ ★★ 降级 SQL 必须是【按当前 SQL 现算】的")
	}
	// ③ 两档各自的降级 SQL 都必须与【同档】的完整 SQL 只差 chr 一处（★ 逐字对拍）。
	for _, tc := range []struct {
		name           string
		degraded, full string
	}{
		{"缺省档", defaultDegraded, defaultFull},
		{"严格档", strictDegraded, strictFull},
	} {
		if restored := strings.Replace(tc.degraded, candidateCacheSignalAbsentSQL, candidateCacheSignalCallSQL, 1); restored != tc.full {
			t.Errorf("★ %s 的降级 SQL 与同档完整 SQL 的差异不止 chr 一处", tc.name)
		}
		if strings.Contains(tc.degraded, "recent_cache_hit_rate(") {
			t.Errorf("★ %s 的降级 SQL 仍然调用 recent_cache_hit_rate", tc.name)
		}
	}
	// ④ 严格档的降级 SQL 必须带严格档的判词（★ 反向证明它不是缺省档的残留）。
	if !strings.Contains(strictDegraded, "mmv3") {
		t.Errorf("★ 严格档的降级 SQL 里没有 `mmv3` 判词 ⇒ ⇒ 它不是当前档的 SQL")
	}
	if strings.Contains(defaultDegraded, "mmv3") {
		t.Errorf("★ 缺省档的降级 SQL 里出现了 `mmv3` 判词 ⇒ ⇒ 它混进了严格档的片段")
	}
}

func TestIsCacheSignalAbsent(t *testing.T) {
	undefinedChr := &pgconn.PgError{
		Code:    "42883",
		Message: "function recent_cache_hit_rate(bigint, text, integer) does not exist",
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"chr 函数缺失", undefinedChr, true},
		{"包装后的 chr 函数缺失", fmt.Errorf("query candidates failed: %w", undefinedChr), true},
		{"其它函数缺失（rsr 是硬信号，不降级）", &pgconn.PgError{
			Code: "42883", Message: "function recent_success_rate(bigint, text, integer) does not exist"}, false},
		{"同名但非 42883（表缺失）", &pgconn.PgError{
			Code: "42P01", Message: "relation recent_cache_hit_rate does not exist"}, false},
		{"纯文本含函数名但无 SQLSTATE", errors.New("recent_cache_hit_rate does not exist"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCacheSignalAbsent(tc.err); got != tc.want {
				t.Fatalf("isCacheSignalAbsent=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCacheSignalDegrade_WindowBoundary(t *testing.T) {
	var d cacheSignalDegrade
	now := time.Unix(1_700_000_000, 0)
	if d.degradedAt(now) {
		t.Fatal("零值不得处于降级态")
	}
	if !d.markDegraded(now) {
		t.Fatal("★ 首次 markDegraded 必须赢得状态切换（★ 它是「降级次数」的那个 1）")
	}
	if !d.degradedAt(now.Add(candidateCacheSignalRecheck - time.Second)) {
		t.Fatal("窗口内应处于降级态")
	}
	if d.degradedAt(now.Add(candidateCacheSignalRecheck + time.Second)) {
		t.Fatal("窗口到期后必须重新探测全量 SQL（函数补齐后自愈）")
	}
	// ★★★ **CAS 语义（★ 2026-10-31 12h 审计轮加）**：★★ ★ 窗口内重复调用**不得**
	// ★★★ 再次赢得切换（★ 否则并发同窗的每个请求都会各打一次点 ⇒ ⇒ 「降级次数」
	// ★★★ 这个口径被并发放大 ⇒ ⇒ ⇒ ★ 指标就读错了）。
	if d.markDegraded(now.Add(time.Second)) {
		t.Fatal("★ 窗口内重复 markDegraded 必须返回 false（★ 否则指标随并发放大）")
	}
	// ★ 窗口过期后必须能**重新抢到**（★ 它是下一轮的「首个撞上者」）。
	if !d.markDegraded(now.Add(candidateCacheSignalRecheck + time.Second)) {
		t.Fatal("★ 窗口过期后必须能重新取得状态切换权（★ 否则永不重新探测全量 SQL）")
	}
	// ★ 并发下**只有一个**赢家（★ 用 -race 跑才有意义 ⇒ ⇒ 这里是逻辑层的确定性断言）。
	var d2 cacheSignalDegrade
	winners := 0
	for i := 0; i < 64; i++ {
		if d2.markDegraded(now) {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("★ 64 次 markDegraded 里赢家数 = %d ⇒ ⇒ 必须恰为 1（★ CAS）", winners)
	}
}
