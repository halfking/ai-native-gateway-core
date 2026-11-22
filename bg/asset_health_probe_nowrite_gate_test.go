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
	// missingDownCalls 记录每次 MarkMissingDown（集合差口径）的调用。
	missingDownCalls []missingDownCall
}

type listCall struct{ limit, offset int }

type markCall struct {
	refID int64
	old   apihub.HealthState
	new   apihub.HealthState
}

func (s *probeStore) Upsert(context.Context, apihub.Asset) error { panic("probe 不应调 Upsert") }
func (s *probeStore) UpsertBatch(context.Context, []apihub.Asset) (int, error) {
	panic("probe 不应调 UpsertBatch")
}

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

// MarkMissingDown 忠实还原 pgStore 的 SQL 语义：
//
//	UPDATE assets SET health_state='down'
//	 WHERE tenant_id=$1 AND kind=$2
//	   AND health_state IS DISTINCT FROM 'down'
//	   AND NOT (ref_id = ANY($3))
//
// ★ 关键：它**不受任何页数上界约束** —— 扫全量 s.all。
//
//	旧实现（探针分页 20 页 × 500 行）只看得见前 10000 行，
//	超过的行静默漏检 —— 新门就是钉住这条不再发生。
func (s *probeStore) MarkMissingDown(_ context.Context, tenantID string, k apihub.Kind, liveRefIDs []int64) (int64, error) {
	live := make(map[int64]bool, len(liveRefIDs))
	for _, id := range liveRefIDs {
		live[id] = true
	}
	var n int64
	for _, a := range s.all {
		if a.TenantID != tenantID || a.Kind != k {
			continue
		}
		if live[a.RefID] {
			continue
		}
		if a.HealthState == apihub.HealthDown {
			continue // 已是终态 ⇒ 不算变化（不重复写同一个值）
		}
		n++
	}
	s.missingDownCalls = append(s.missingDownCalls, missingDownCall{
		kind: k, tenant: tenantID, live: len(liveRefIDs), marked: n,
	})
	return n, nil
}

type missingDownCall struct {
	kind   apihub.Kind
	tenant string
	live   int
	marked int64
}

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
			asset(1, "t1", apihub.HealthUnknown),  // 该变
			asset(2, "t1", apihub.HealthDegraded), // 不该变
			asset(3, "t1", apihub.HealthUnknown),  // 该变
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

// ── 门 5：Step 2 必须覆盖**全表**，且不得有页数上界（§10.27 + L4 的本体）──
//
// §10.27 的原意：探针每轮只检查了前 500 行（Filter 没有 Offset、limit 被截到 500），
// 生产实测 2141 行里 1521 行从未受检。
//
// 2026-11-23（L4）把机制换成了**集合差**（一条 UPDATE ... NOT (ref_id = ANY(...))），
// 于是「有没有上界」这个问题本身消失了：不再有分页，就没有「只看前 N 行」。
//
// ★ 这条门用 **10,050 行**（超过旧实现的 20 页 × 500 行 = 10000 行上界）
//
//	来钉住这一点：旧实现在这里会漏掉 50 行（而且**静默**——它那条上界告警
//	的判据还是反的，见门 10）。新实现必须一行不漏。
func TestProbeCoversAllAssetsBeyondOldPaginationCeiling(t *testing.T) {
	const n = 10050
	all := make([]apihub.Asset, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, asset(int64(i+1), "t1", apihub.HealthUnknown))
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}

	// syncer 返回空 ⇒ 全部 n 行都「不在活着的集合里」。
	_, removed := runProbe(t, st, nil)

	if int(removed) != n {
		t.Fatalf("removed = %d，期望 %d。\n"+
			"  差额 = 探针没看到的行数。旧实现有 20 页 × 500 行 = 10000 行的上界，\n"+
			"  超过上界的行**静默漏检**；新实现是集合差，没有上界。\n"+
			"  旧游标序列: %+v", removed, n, st.listed)
	}
}

// ── 门 6：机制必须是「集合差」，不得回退成列表聚合 ────────────────────
//
// 门 5 钉住「结果正确」，这条钉住「机制正确」：万一将来有人为了「省一次查询」
// 又把全表拉进 Go 侧比对，门 5 仍会绿，但「统计面禁止列表聚合」又回来了
// （AGENTS.md 已登记：limit 截断后的逐行累加是静默欠数制造机）。
//
// 断言：探针**不再分页**（一次 List 都不发），Step 2 走 MarkMissingDown 一次成型。

func TestProbeUsesSetDifferenceInsteadOfListAggregation(t *testing.T) {
	const n = 1200
	all := make([]apihub.Asset, 0, n)
	for i := 0; i < n; i++ {
		all = append(all, asset(int64(i+1), "t1", apihub.HealthDown)) // 已 Down ⇒ 不产生写
	}
	st := &probeStore{tenants: []string{"t1"}, all: all}
	runProbe(t, st, nil)

	if len(st.listed) != 0 {
		t.Errorf("List 被调用 %d 次，期望 0（Step 2 必须是集合差，不得再分页拉全表）。游标: %+v",
			len(st.listed), st.listed)
	}
	// 两个 kind 各一次（llm_endpoint / mcp_server），mcp 没有活着的资产 ⇒ live=0。
	if len(st.missingDownCalls) != 2 {
		t.Fatalf("MarkMissingDown 被调用 %d 次，期望 2（每个 kind 一次）。调用: %+v",
			len(st.missingDownCalls), st.missingDownCalls)
	}
	if st.missingDownCalls[0].kind != apihub.KindLLMEndpoint {
		t.Errorf("第 1 次 MarkMissingDown 的 kind = %q，期望 %q",
			st.missingDownCalls[0].kind, apihub.KindLLMEndpoint)
	}
}

// ── 门 7：集合差口径下，「表有多大」与「请求数」无关 ──────────────────
//
// 旧的门 7 防的是分页循环的 off-by-one（整页时多打一次空查询）。
// 集合差口径下这个病**不存在**：无论表里有 0 行还是 10 万行，
// 每个 kind 只有**一条**语句。
func TestProbeIssuesOneStatementPerKindRegardlessOfTableSize(t *testing.T) {
	for _, n := range []int{0, 500, 5000} {
		all := make([]apihub.Asset, 0, n)
		for i := 0; i < n; i++ {
			all = append(all, asset(int64(i+1), "t1", apihub.HealthDown))
		}
		st := &probeStore{tenants: []string{"t1"}, all: all}
		runProbe(t, st, nil)
		if len(st.listed) != 0 {
			t.Errorf("n=%d：List 被调用 %d 次，期望 0", n, len(st.listed))
		}
		if len(st.missingDownCalls) != 2 {
			t.Errorf("n=%d：MarkMissingDown 被调用 %d 次，期望 2（每个 kind 一次）", n, len(st.missingDownCalls))
		}
	}
}
