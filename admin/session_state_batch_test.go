package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/redis/go-redis/v9"
)

// adminRoundTripCounter 数**网络往返**（单命令一次、管道一次），不数命令条数。
// ★ 理由同 domains/session 侧：批量化不改变命令条数，只改变往返次数；
// 对着命令条数测量会得出「优化无效」的错误结论。
type adminRoundTripCounter struct {
	roundTrips int64
}

func (c *adminRoundTripCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (c *adminRoundTripCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		c.roundTrips++
		return next(ctx, cmd)
	}
}

func (c *adminRoundTripCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		c.roundTrips++
		return next(ctx, cmds)
	}
}

func newAdminSessionHandler(t *testing.T, n int, tenantID string) (*Handler, *adminRoundTripCounter, []string) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	counter := &adminRoundTripCounter{}
	client.AddHook(counter)
	t.Cleanup(func() { _ = client.Close() })

	mgr := session.NewManager(session.NewRedisClientFromClient(client), time.Hour)
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sess, err := mgr.Create(ctx, 7, tenantID, "device-1")
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		ids = append(ids, sess.SessionID)
	}

	h := &Handler{}
	h.SetSessionManager(mgr)
	return h, counter, ids
}

// TestListSessionsRoundTripsDoNotScaleWithSessionCount 是本轮优化的**端到端主判据**：
// 会话列表的 Redis 往返数**不许随会话数增长**。
//
// ★ 为什么这样判：优化前每会话 5 次往返（租户过滤 1 次 Get + 列表 Get 1 次 +
// GetStats 1 次，每次 SafeHGetAll 内部是 TYPE+HGETALL 两次）⇒ 往返数与
// 会话数**线性相关**。优化后是固定的几次（扫描 + 一次批量读）。
// 「往返数不随 N 增长」这一条比任何绝对阈值都更难被误读通过。
func TestListSessionsRoundTripsDoNotScaleWithSessionCount(t *testing.T) {
	const small, large = 5, 40

	hSmall, counterSmall, _ := newAdminSessionHandler(t, small, "default")
	// ★ 量具净化：建会话本身也走 Redis（每建一个 1 次往返），
	// 不清零的话测到的是「建会话 + 列表」的合计，会把 N+1 的增长算到
	// 错误的账上——这正是「读数要问它是在什么策略/环境下得到的」。
	counterSmall.roundTrips = 0
	wSmall := httptest.NewRecorder()
	rSmall := httptest.NewRequest(http.MethodGet, "/api/admin/sessions?limit=200", nil)
	hSmall.handleListSessions(wSmall, rSmall)
	if wSmall.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", wSmall.Code)
	}
	countSmall := counterSmall.roundTrips

	hLarge, counterLarge, _ := newAdminSessionHandler(t, large, "default")
	counterLarge.roundTrips = 0
	wLarge := httptest.NewRecorder()
	rLarge := httptest.NewRequest(http.MethodGet, "/api/admin/sessions?limit=200", nil)
	hLarge.handleListSessions(wLarge, rLarge)
	if wLarge.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", wLarge.Code)
	}
	countLarge := counterLarge.roundTrips

	// 会话数从 5 涨到 40（8 倍），往返数只许小幅变化。
	// 优化前这段差值 ≈ 5×35 = 175 次。
	const allowedGrowth = 8
	if growth := countLarge - countSmall; growth > allowedGrowth {
		t.Fatalf("round trips grew by %d when sessions grew from %d to %d (small=%d large=%d): "+
			"the list path still scales with session count (N+1 not fixed)",
			growth, small, large, countSmall, countLarge)
	}
	t.Logf("round trips: n=%d → %d, n=%d → %d (growth %d, allowed %d)",
		small, countSmall, large, countLarge, countLarge-countSmall, allowedGrowth)
}

// TestListSessionsReturnsAllSeededSessions 钉住「批量读不许漏会话」。
func TestListSessionsReturnsAllSeededSessions(t *testing.T) {
	const n = 12
	h, _, _ := newAdminSessionHandler(t, n, "default")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/sessions?limit=200", nil)
	h.handleListSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp sessionListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Sessions) != n {
		t.Fatalf("sessions = %d, want %d (body=%s)", len(resp.Sessions), n, w.Body.String())
	}
	for _, item := range resp.Sessions {
		if item.SessionID == "" {
			t.Fatalf("empty session_id in item: %+v", item)
		}
	}
}

// TestListSessionsTenantFilterStillFilters 钉住「租户过滤没被批量改坏」：
// 另一租户的会话不得出现在结果里。
//
// ★ 这里必须注入 super_admin 鉴权上下文，否则 `tenant_id` 查询参数会被
// `GetTenantID(r)` 覆盖成 "default"（无鉴权上下文时的既有行为，
// 见 admin/session_state_handlers.go 的 tenantID 取值段）——
// 测试前提写错会得到「0 条」的假失败，而不是真缺陷。
func TestListSessionsTenantFilterStillFilters(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	mgr := session.NewManager(session.NewRedisClientFromClient(client), time.Hour)
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		if _, err := mgr.Create(ctx, 7, "tenant-a", "device-1"); err != nil {
			t.Fatalf("Create tenant-a failed: %v", err)
		}
	}
	for i := 0; i < 6; i++ {
		if _, err := mgr.Create(ctx, 8, "tenant-b", "device-1"); err != nil {
			t.Fatalf("Create tenant-b failed: %v", err)
		}
	}
	_ = mr

	h := &Handler{}
	h.SetSessionManager(mgr)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/admin/sessions?limit=200&tenant_id=tenant-a", nil)
	r = SetAuthContext(r, &AuthContext{UserID: 1, TenantID: "tenant-a", Username: "admin", Role: "super_admin"})
	h.handleListSessions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp sessionListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Sessions) != 6 {
		t.Fatalf("sessions = %d, want 6 (tenant-a only; body=%s)", len(resp.Sessions), w.Body.String())
	}
	for _, item := range resp.Sessions {
		if item.TenantID != "tenant-a" {
			t.Fatalf("tenant leak: item tenant=%s", item.TenantID)
		}
	}
}

// pipelineFailSwitch 让**流水线**调用失败、但**单命令**调用照常通过。
//
// ★ 为什么要这样一个开关：批量路径走流水线（`SafeHGetAllPipeline` 用
// `pipe.Exec`），而逐键路径走**单命令**（`SafeHGetAll` = TYPE + HGETALL
// 两次单命令往返）⇒ 只把流水线打掉，就能**精确**制造
// 「批量读失败 ⇒ 走逐键降级」这一场景，且降级路径仍能正常读出数据。
// ★ 不这样造的：整台 Redis 挂掉 ⇒ 连降级路径也读不到数据，
//
//	量具就会测成「0 条会话」而不是「往返数」。
type pipelineFailSwitch struct {
	armed bool
}

func (s *pipelineFailSwitch) DialHook(next redis.DialHook) redis.DialHook { return next }

func (s *pipelineFailSwitch) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

func (s *pipelineFailSwitch) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if s.armed {
			return errors.New("injected pipeline failure (test)")
		}
		return next(ctx, cmds)
	}
}

// TestListSessionsBatchFallbackReadsEachSessionHashOnce 钉住**降级路径**的
// Redis 往返数：批量读失败时，每个会话的 hash 只能被读**一次**。
//
// ★ 这条门锁的是上一轮审计转交的 R11 项（已修）：降级路径原先用
//
//	`Get` + `GetStats`，而两者读的是**同一份** `session:<id>` hash
//	⇒ 同一份数据取了两遍 ⇒ 每会话 **4 次往返**
//	（`SafeHGetAll` 内部 TYPE + HGETALL，各一次往返）。
//	★ 而降级路径恰恰在 **Redis 正出问题**时才会被走到 ⇒ 在最需要减轻
//	  Redis 压力的时候把往返翻倍，是最不该发生的事。
//	★ 主路径（批量读）只花 **2 次往返**走完全部会话 ⇒ 降级路径不该是它的
//	  N 倍。
//
// ★ 期望值怎么来的：`GetWithStats` 一次 `SafeHGetAll` = TYPE(1) + HGETALL(1)
//
//	= **2 次往返/会话** ⇒ N 个会话 = 2N。
//	★ 反向对照：若走的是批量路径（流水线），全程只有 2 次往返 ⇒
//	  2N（=12，N=6）与 2 相差一个数量级 ⇒ 这条门**不会**在批量路径上假绿。
func TestListSessionsBatchFallbackReadsEachSessionHashOnce(t *testing.T) {
	const n = 6

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := &adminRoundTripCounter{}
	client.AddHook(counter)
	failPipeline := &pipelineFailSwitch{}
	client.AddHook(failPipeline)

	mgr := session.NewManager(session.NewRedisClientFromClient(client), time.Hour)
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		sess, err := mgr.Create(ctx, 7, "default", "device-1")
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		ids = append(ids, sess.SessionID)
	}

	h := &Handler{}
	h.SetSessionManager(mgr)

	// 武装：此后所有流水线调用失败 ⇒ GetBatch 失败 ⇒ 走逐键降级路径。
	failPipeline.armed = true
	counter.roundTrips = 0

	sessions, stats := h.loadSessionsBatch(ctx, ids)

	// ① 降级语义不许被破坏：坏键跳过、好键照常渲染（一个都不能少）。
	if len(sessions) != n {
		t.Fatalf("sessions = %d, want %d (degradation path must not lose sessions)", len(sessions), n)
	}
	for _, sid := range ids {
		if sessions[sid] == nil {
			t.Fatalf("session %s missing from fallback result", sid)
		}
		// ② stats 恒非 nil：批量路径从同一份快照派生 stats，降级路径也必须同形
		//    （旧实现两次读之间 key 消失 ⇒ stats 变 nil）。
		if stats[sid] == nil {
			t.Fatalf("stats %s missing from fallback result (must be derived from the same snapshot)", sid)
		}
	}

	// ③ 主判据：每个会话的 hash 只读一次 ⇒ 2 次往返/会话。
	//    ★ 期望值 = 1（**失败的那次批量流水线**本身也算一次往返）+ 2×N。
	//      实测命令序列（trace）：
	//        00 PIPELINE([type ×6])   ← 批量尝试，失败（这 1 次无法省）
	//        01..12 type+hgetall ×6   ← 逐键降级，每个会话 TYPE+HGETALL
	//    ★ 修前同一场景是 1 + 4×N（Get 2 次往返 + GetStats 2 次往返，
	//      且两者读的是**同一份** hash）。
	const wantPerSession = 2
	want := int64(1 + n*wantPerSession)
	if counter.roundTrips != want {
		t.Fatalf("fallback round trips = %d, want %d (1 failed batch pipeline + n=%d × %d): "+
			"each session hash must be read exactly once — "+
			"Get+GetStats read the SAME hash twice (4 round trips/session)",
			counter.roundTrips, want, n, wantPerSession)
	}
	t.Logf("fallback round trips = %d (1 failed batch + n=%d × %d) — one hash read per session",
		counter.roundTrips, n, wantPerSession)
}
