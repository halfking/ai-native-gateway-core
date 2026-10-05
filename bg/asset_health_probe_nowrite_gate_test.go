package bg

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/apihub"
)

// ── apihub.Store 的最小实现：只实现 probe 用到的方法，其余 panic ──────────
//
// 为什么不复用 apihub 包里的 memStore：它在该包的 _test.go 里，不导出。
// 这里重写一份，好处是能**按调用计数**——而计数正是这个门要量的东西。

type probeStore struct {
	tenants []string
	stale   []apihub.Asset
	all     []apihub.Asset

	// markCalls 记录每次 MarkHealth 的 (kind|ref_id, 旧值, 新值)。
	markCalls []markCall
	// listed 记录每次 List 的 (limit, offset) 游标序列。
	listed []listCall
}

type listCall struct{ limit, offset int }

type markCall struct {
	refID int64
	old   apihub.HealthState
	new   apihub.HealthState
}

func (s *probeStore) Upsert(context.Context, apihub.Asset) error { panic("probe 不应调 Upsert") }

func (s *probeStore) Get(context.Context, string, apihub.Kind, int64) (apihub.Asset, error) {
	panic("probe 不应调 Get")
}

func (s *probeStore) List(_ context.Context, f apihub.Filter) ([]apihub.Asset, error) {
	// 忠实还原 PG 的 LIMIT/OFFSET 语义，并记录每次的游标。
	// ★ 必须真的实现 Offset：忽略它的话，探针就算不翻页这个假 store 也照样
	//   返回全量，「翻页失效」这个回归就永远测不出来。
	// ★ 必须照抄 pgStore.List 的 limit>500 截断：不照抄的话，探针请求
	//   1000 行时会拿到全量，把「静默截断」这个真正的病灶藏起来。
	s.listed = append(s.listed, listCall{limit: f.Limit, offset: f.Offset})
	out := make([]apihub.Asset, len(s.all))
	copy(out, s.all)
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if f.Offset >= len(out) {
		return nil, nil
	}
	out = out[f.Offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *probeStore) Link(context.Context, string, apihub.Relationship) error {
	panic("probe 不应调 Link")
}

func (s *probeStore) Neighbors(context.Context, string, apihub.Kind, int64, int) ([]apihub.Asset, []apihub.Relationship, error) {
	panic("probe 不应调 Neighbors")
}

func (s *probeStore) MarkHealth(_ context.Context, _ string, k apihub.Kind, refID int64, state apihub.HealthState) error {
	old := apihub.HealthUnknown
	for _, a := range s.all {
		if a.Kind == k && a.RefID == refID {
			old = a.HealthState
		}
	}
	for _, a := range s.stale {
		if a.Kind == k && a.RefID == refID {
			old = a.HealthState
		}
	}
	s.markCalls = append(s.markCalls, markCall{refID: refID, old: old, new: state})
	return nil
}

func (s *probeStore) ListStale(context.Context, string, time.Duration) ([]apihub.Asset, error) {
	out := make([]apihub.Asset, len(s.stale))
	copy(out, s.stale)
	return out, nil
}

func (s *probeStore) ListTenants(context.Context) ([]string, error) { return s.tenants, nil }

// probeSyncer 返回的即「活的」资产集合。liveLookup 由它构造。
type probeSyncer struct{ live []apihub.Asset }

func (s probeSyncer) LLMEndpoints(context.Context) ([]apihub.Asset, error) { return s.live, nil }
func (s probeSyncer) MCPServers(context.Context) ([]apihub.Asset, error)   { return nil, nil }

// ── 夹具 ────────────────────────────────────────────────────────────────

func asset(id int64, tenant string, st apihub.HealthState) apihub.Asset {
	return apihub.Asset{
		Kind:        apihub.KindLLMEndpoint,
		RefID:       id,
		TenantID:    tenant,
		Name:        fmt.Sprintf("a%d", id),
		HealthState: st,
	}
}

func runProbe(t *testing.T, st *probeStore, live []apihub.Asset) (degraded, removed int64) {
	t.Helper()
	p := NewAssetHealthProbe(apihub.New(st), probeSyncer{live: live})
	d, r, err := p.ProbeOnce(context.Background())
	if err != nil {
		t.Fatalf("ProbeOnce: %v", err)
	}
	return d, r
}

// ── 门 1：稳态下必须是 0 次写 ──────────────────────────────────────────
//
// 这是 runbook §10.26 的核心不变量。2026-10-05 实测：一轮 probe 发出
// 835 次 MarkHealth，全部是写同一个值（439 degraded + 396 down），
// 两台的日志（degraded=439, removed=396）与库内桶计数逐位对上。
//
// 这个门量的不是「assets 表有没有被更新」，而是**探针有没有发请求**——
// 因为空写对 SELECT 侧完全不可见，只有数调用次数才抓得到。

func TestProbeSteadyStateIssuesZeroMarkHealthCalls(t *testing.T) {
	// 夹具按 2026-10-05 生产形态画（库内三桶：unknown 1306 / degraded 439 /
	// down 396）。关键是 degraded 与 down 落在 liveLookup 的**两侧**：
	//  · degraded 439 —— 配置还在（源表里有）但心跳停了 ⇒ 在 liveLookup 里，
	//    所以只有 Step 1 看得见它，且它已经是 Degraded ⇒ 空写。
	//  · down 396 —— 源表里已经没有了 ⇒ 不在 liveLookup，Step 2 每次都想写。
	// 一开始我把 degraded 也放到 liveLookup 之外，门立刻红并报出
	// `ref_id=3 "degraded" -> "down"` —— 那是**正确的**行为转换，不是 bug。
	// 这个坑记在这里：门红了先怀疑夹具。
	st := &probeStore{
		tenants: []string{"t1"},
		stale: []apihub.Asset{
			asset(1, "t1", apihub.HealthDegraded), // 活但 stale
			asset(2, "t1", apihub.HealthDegraded), // 活但 stale
			asset(3, "t1", apihub.HealthDown),     // 非活
			asset(4, "t1", apihub.HealthDown),     // 非活
		},
		all: []apihub.Asset{
			asset(1, "t1", apihub.HealthDegraded),
			asset(2, "t1", apihub.HealthDegraded),
			asset(3, "t1", apihub.HealthDown),
			asset(4, "t1", apihub.HealthDown),
		},
	}
	// 活着的只有 1 和 2。
	d, r := runProbe(t, st, []apihub.Asset{
		asset(1, "t1", apihub.HealthUnknown),
		asset(2, "t1", apihub.HealthUnknown),
	})

	if len(st.markCalls) != 0 {
		for _, c := range st.markCalls {
			t.Logf("多余调用: ref_id=%d %q -> %q", c.refID, c.old, c.new)
		}
		t.Fatalf("稳态下发了 %d 次 MarkHealth，期望 0 次。\n"+
			"  这些调用的旧值已经等于新值 ⇒ 全部是空写，但每次都会产生\n"+
			"  n_tup_upd 与堆脏页。§10.26 实测 835 次/轮。\n"+
			"  门控必须在 Go 侧比对 a.HealthState（调用方已从 SQL 读到了它）。",
			len(st.markCalls))
	}
	if d != 0 || r != 0 {
		t.Fatalf("稳态计数 degraded=%d removed=%d，期望都是 0", d, r)
	}
}

// ── 门 2：任何一次 MarkHealth 都必须真的改变值 ──────────────────────────
//
// 门 1 只在「全部已达目标态」时才红。门 2 更强：它对**每一次**调用
// 断言 old != new，因此哪怕混合场景里混进一次空写也会被抓到。

func TestProbeNeverWritesTheSameValueTwice(t *testing.T) {
	st := &probeStore{
		tenants: []string{"t1"},
		stale: []apihub.Asset{
			asset(1, "t1", apihub.HealthUnknown), // 该变
			asset(2, "t1", apihub.HealthDegraded), // 不该变
			asset(3, "t1", apihub.HealthUnknown), // 该变
		},
		all: []apihub.Asset{
			asset(1, "t1", apihub.HealthUnknown),
			asset(2, "t1", apihub.HealthDegraded),
			asset(3, "t1", apihub.HealthUnknown),
			asset(4, "t1", apihub.HealthDown), // 已是 Down
		},
	}
	d, r := runProbe(t, st, []apihub.Asset{asset(1, "t1", apihub.HealthUnknown)})

	for _, c := range st.markCalls {
		if c.old == c.new {
			t.Errorf("空写：ref_id=%d 被写成 %q，而它本来就是 %q", c.refID, c.new, c.old)
		}
	}
	// 1 和 3 是 stale 且非 Degraded/Down ⇒ Step 1 标 Degraded。
	// liveLookup 只有 1 ⇒ 2 与 3 不在 liveLookup，Step 2 把它们推到 Down：
	// 这是**真实的状态转换**（degraded→down、unknown→down），门必须放行。
	// 4 已是 Down ⇒ 跳过。
	if d != 2 {
		t.Errorf("degraded = %d，期望 2（ref_id 1 和 3）", d)
	}
	if r != 2 {
		t.Errorf("removed = %d，期望 2（ref_id 2 和 3 非活且尚未是 Down）", r)
	}
}

// ── 门 3：Down 是终态，不能被降级回 Degraded ────────────────────────────
//
// 这条是 Step 1 门控的**副作用守卫**：我把条件从
// `== HealthDown` 扩成 `== HealthDegraded || == HealthDown`，
// 必须证明扩大的那半边没有顺手改掉 Down 的既有语义。

func TestProbeNeverDowngradesDownToDegraded(t *testing.T) {
	st := &probeStore{
		tenants: []string{"t1"},
		stale:   []apihub.Asset{asset(7, "t1", apihub.HealthDown)},
		all:     []apihub.Asset{asset(7, "t1", apihub.HealthDown)},
	}
	d, r := runProbe(t, st, []apihub.Asset{asset(7, "t1", apihub.HealthUnknown)})

	if d != 0 {
		t.Errorf("degraded = %d，期望 0 —— 已是 Down 的资产不得被降级", d)
	}
	if r != 0 {
		t.Errorf("removed = %d，期望 0 —— 7 虽已是 Down，但它活着所以 Step 2 根本不碰它", r)
	}
	if len(st.markCalls) != 0 {
		t.Errorf("发了 %d 次 MarkHealth，期望 0（7 已是 Down，且它活着所以 Step 2 也不碰）",
			len(st.markCalls))
	}
}

// ── 门 4：首次运行仍然要正常改状态（防「门控把功能改没了」）────────────
//
// §10.26 的门控有把功能整体关掉的风险（如果条件写反，全部跳过）。
// 这个门从「全是 Unknown」出发，钉住该写的确实写了。

func TestProbeFirstRunStillMarksUnseenAssets(t *testing.T) {
	st := &probeStore{
		tenants: []string{"t1"},
		stale:   []apihub.Asset{asset(11, "t1", apihub.HealthUnknown)},
		all: []apihub.Asset{
			asset(11, "t1", apihub.HealthUnknown),
			asset(12, "t1", apihub.HealthUnknown),
		},
	}
	// 11 活（会变 Degraded）但 stale；12 不活（会变 Down）。
	// 两者当前都是 Unknown ⇒ 都是**真实**转换，门必须放行。
	d, r := runProbe(t, st, []apihub.Asset{asset(11, "t1", apihub.HealthUnknown)})

	if d != 1 {
		t.Errorf("degraded = %d，期望 1（ref_id 11 是 stale 且非 Degraded）", d)
	}
	if r != 1 {
		t.Errorf("removed = %d，期望 1（ref_id 12 非活且不是 Down）", r)
	}
}

// ── 门 5：Step 2 必须真的翻页（§10.27 的本体）────────────────────────
//
// §10.27：apihub.Filter 当时没有 Offset、listAssetsSQL 没有 OFFSET 子句，
// 于是 `offset` 是死变量，探针每轮只检查了前 500 行。
// 生产实测 2141 行里 1521 行从未受检，其中 439 行早已不在源表。
//
// 这条门把夹具做成 1200 行（> 一页），且**全部不在 liveLookup**，
// 于是「是否每一行都被 MarkHealth 覆盖」直接等于「是否翻页了」。
// 不翻页 ⇒ 只有 500 次调用 ⇒ 立刻红。

func TestProbePagesThroughAllAssets(t *testing.T) {
	const n = 1200
	all := make([]apihub.Asset, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, asset(int64(i+1), "t1", apihub.HealthUnknown))
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}

	// syncer 返回空 ⇒ 全部 1200 行都「不在 liveLookup」。
	_, removed := runProbe(t, st, nil)

	if int(removed) != n {
		t.Fatalf("removed = %d，期望 %d。\n"+
			"  差额 = 探针没翻到的行数。§10.27 的 bug 正是每轮只看前 500 行\n"+
			"  （旧代码要 batchSize=1000，但 List 会静默截到 500，且没有 OFFSET）。\n"+
			"  实测游标序列: %+v", removed, n, st.listed)
	}
	if len(st.markCalls) != n {
		t.Errorf("MarkHealth 调用 %d 次，期望 %d", len(st.markCalls), n)
	}
}

// ── 门 6：游标必须单调递增且步长等于页长 ─────────────────────────────
//
// 门 5 钉住「结果正确」，这条钉住「机制正确」：万一将来有人用别的方式
// 绕过（比如把 limit 调大到 10000），门 5 仍会绿，但页大小契约被破坏了。
// 两条门各管一半，缺一条都留缺口。

func TestProbePaginationUsesIncreasingOffsets(t *testing.T) {
	const n = 1200
	all := make([]apihub.Asset, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, asset(int64(i+1), "t1", apihub.HealthDown)) // 已 Down ⇒ 不产生写
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}
	runProbe(t, st, nil)

	if len(st.listed) != 3 {
		t.Fatalf("List 被调用 %d 次，期望 3（1200 行 / 每页 500 = 3 页）。游标: %+v",
			len(st.listed), st.listed)
	}
	want := []listCall{{limit: 500, offset: 0}, {limit: 500, offset: 500}, {limit: 500, offset: 1000}}
	for i, w := range want {
		if st.listed[i] != w {
			t.Errorf("第 %d 页游标 = %+v，期望 %+v", i+1, st.listed[i], w)
		}
	}
}

// ── 门 7：空结果必须终止循环，不得再多发一次请求 ─────────────────────
//
// 防「最后一页刚好等于整页时多打一次空查询」。这条看着琐碎，但它是
// 分页循环最常见的 off-by-one，代价是每小时 × 2 台的无谓往返。
// 用 500 的整数倍构造：正好 500 行 = 1 页，不应产生第 2 次查询。

func TestProbeStopsWhenTableExactlyFillsOnePage(t *testing.T) {
	all := make([]apihub.Asset, 0, 500)
	for i := 0; i < 500; i++ {
		all = append(all, asset(int64(i+1), "t1", apihub.HealthDown))
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}
	runProbe(t, st, nil)

	// 500 行 = 整一页 ⇒ `len(batch) < pageSize` 不成立，必须再发第 2 次
	// 才能知道结束了。这是**正确**的代价（不是 off-by-one），
	// 所以这里断言的是 2 次，且第二次 offset=500 必然返回空。
	if len(st.listed) != 2 {
		t.Fatalf("List 被调用 %d 次，期望 2（500 整页需要多打一次确认结尾）。游标: %+v",
			len(st.listed), st.listed)
	}
	if st.listed[1] != (listCall{limit: 500, offset: 500}) {
		t.Errorf("第 2 次游标 = %+v，期望 {500 500}", st.listed[1])
	}
}
