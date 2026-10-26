package admin

import (
	"context"
	"encoding/json"
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
