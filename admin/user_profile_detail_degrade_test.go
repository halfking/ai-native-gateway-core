package admin

// admin/user_profile_detail_degrade_test.go —— 回归门：
// 「schema 落后于代码」时，用户画像**详情**端点必须降级成 200 + 空载荷 +
// 自报家门的 degraded 标记，而不是整页 500。
//
// ## 挡住的是什么
//
// 2026-10-20 部署后复验轮在 245/154 生产实测到：
// GET /api/admin/session-analytics/users/{owner} 恒 500，服务端原文
//
//	ERROR: relation "session_owners" does not exist (SQLSTATE 42P01)
//
// 根因不是代码写错，而是**生产共享库缺 session_owners**：建它的迁移 358
// 版本号低于部署通道的账本对账下限（DB_LEDGER_RECONCILE_FROM=412）⇒
// 存量生产库永远拿不到它 ⇒ 详情端点对着一个不存在的表聚合。
//
// 同文件的**列表**端点早在 2026-10-03 就为此做过降级（200 + 空列表 +
// degraded 标记），详情端点漏了 ⇒ 同一页面的两个入口一个能开一个不能开。
//
// ## 为什么必须带 degraded 标记
//
// 「这个 owner 没有会话」与「聚合表没迁移」在页面上都长成一个空列表。
// 不自报家门就是撒谎（admin/degrade_marker_test.go 立的正是这条规矩）。
//
// ## 判据的鉴别力（三条腿，缺一即恒绿）
//
// ① 静态腿（**不依赖数据库**，CI 永远跑）：错误分支必须调
//    IsSchemaBehindError 且在同窗口内写 Degraded: true + 200；
// ② 真库腿（一次性 database，从不写业务库）：
//    - 缺关系（全新库天然成立）⇒ 200 + degraded:true + 非空 reason；
//    - 有关系且有行 ⇒ 200 + degraded:**false**（恒发，不是「字段缺失」）；
//    - 普通错误（请求 ctx 已取消 ⇒ context.Canceled）⇒ **仍 500**，
//      因为「把所有错误都降级成 200 + 全 0」那是撒谎，不是降级
//      （同款警告见 admin/usage_enhanced.go）。
// ③ 形状腿：降级载荷的 JSON 形状必须与结构体同源（同一个结构体构造 ⇒
//    编译器保证；这条门把「少字段」显式钉住 —— 前端读到 undefined 会渲染出 NaN）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── ① 静态腿：不依赖数据库 ──────────────────────────────────────────

// TestUserProfileDetailDegradeBranchIsWired 钉住「错误分支真的接了降级」。
//
// 判据打在**源码文本**上（不是结构门也不是行为门）：这一腿的作用是
// 「CI 没有数据库时也能挡住回归」，所以它必须能单独变红。
func TestUserProfileDetailDegradeBranchIsWired(t *testing.T) {
	src, err := os.ReadFile("user_profile.go")
	if err != nil {
		t.Fatalf("read user_profile.go: %v", err)
	}
	body := string(src)

	// 找到详情端点里的降级判定点。
	idx := strings.Index(body, "IsSchemaBehindError(err)")
	if idx < 0 {
		t.Fatal("详情端点的错误分支没有调 IsSchemaBehindError —— schema 落后时会 500")
	}
	// 列表端点也有一个 IsSchemaBehindError；详情端点那个在**第二个**出现处。
	second := strings.Index(body[idx+1:], "IsSchemaBehindError(err)")
	if second < 0 {
		// 只有列表端点接了降级 ⇒ 详情端点漏了（这正是本轮要修的缺陷）。
		t.Fatal("只有列表端点接了降级；详情端点的错误分支仍会把 schema 落后变成 500")
	}
	window := body[idx+1+second : idx+1+second+1200]
	for _, needle := range []string{"Degraded:", "DegradedReason", "http.StatusOK"} {
		if !strings.Contains(window, needle) {
			t.Errorf("降级分支窗口内缺 %q（必须自报家门 + 回 200）", needle)
		}
	}
}

// ── ② 真库腿：一次性 database，从不写业务库 ──────────────────────────

func userProfileDegradeDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_ADMIN_500_DSN", "LLM_GATEWAY_DATABASE_URL", "DATABASE_URL", "TEST_DATABASE_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("set TEST_ADMIN_500_DSN (or LLM_GATEWAY_DATABASE_URL / TEST_DATABASE_URL) — " +
		"跳过**不构成**「端点可用」的证据")
	return ""
}

// newUserProfileDegradeDB 建一个一次性数据库并返回它的池。
// 全新库里**没有** session_owners ⇒ 「缺关系」是天然状态，不需要造。
func newUserProfileDegradeDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := userProfileDegradeDSN(t)
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open root pool: %v", err)
	}
	name := fmt.Sprintf("lgw_upd_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		root.Close()
		t.Fatalf("create database %s: %v", name, err)
	}
	idx := strings.LastIndex(dsn, "/")
	if idx < 0 {
		root.Close()
		t.Fatalf("cannot rewrite dsn %q", dsn)
	}
	rest := dsn[idx+1:]
	if q := strings.Index(rest, "?"); q >= 0 {
		rest = rest[q:]
	} else {
		rest = ""
	}
	isoDSN := dsn[:idx+1] + name + rest
	pool, err := pgxpool.New(ctx, isoDSN)
	if err != nil {
		root.Close()
		t.Fatalf("open isolated db pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := root.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		root.Close()
	})
	return pool, isoDSN
}

func userProfileDegradeRequest(ctx context.Context) *http.Request {
	req := httptest.NewRequest("GET", "/api/admin/session-analytics/users/owner-degrade-probe", nil).WithContext(ctx)
	return SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "default",
		Username: "admin",
		Role:     "super_admin",
		IsJWT:    true,
	})
}

// TestUserProfileDetailDegradesWhenRelationMissing：缺关系 ⇒ 200 + degraded:true。
func TestUserProfileDetailDegradesWhenRelationMissing(t *testing.T) {
	pool, _ := newUserProfileDegradeDB(t)
	ctx := context.Background()

	// 前置断言：这张表**确实不存在**（否则本门测的不是「缺关系」）。
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.session_owners') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe session_owners: %v", err)
	}
	if exists {
		t.Fatal("一次性库里不该有 session_owners —— 本门的前提失效")
	}

	h := &Handler{db: pool}
	w := httptest.NewRecorder()
	h.handleUserAnalyticsDetail(w, userProfileDegradeRequest(ctx))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200（schema 落后 ⇒ 降级而不是 500），body = %s",
			w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("降级载荷不是 JSON: %v (body=%s)", err, w.Body.String())
	}
	if degraded, _ := payload["degraded"].(bool); !degraded {
		t.Errorf("降级载荷必须自报家门 degraded:true，实际: %s", w.Body.String())
	}
	if reason, _ := payload["degraded_reason"].(string); strings.TrimSpace(reason) == "" {
		t.Errorf("降级载荷必须给出 degraded_reason（为什么降级），实际: %s", w.Body.String())
	}
	if owner, _ := payload["owner_user"].(string); owner != "owner-degrade-probe" {
		t.Errorf("降级载荷必须回显 owner_user，实际 %q", owner)
	}
}

// TestUserProfileDetailDoesNotDegradeOnOrdinaryError：普通错误**不得**降级。
//
// 制造手段：请求 ctx 在进 handler 前就已被取消 ⇒ DB 调用返回 context.Canceled
// （不是 schema 落后）⇒ 端点必须仍 500，否则运维会以为页面正常。
func TestUserProfileDetailDoesNotDegradeOnOrdinaryError(t *testing.T) {
	pool, _ := newUserProfileDegradeDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消 ⇒ 查询失败于传输层，不是 schema 落后

	h := &Handler{db: pool}
	w := httptest.NewRecorder()
	h.handleUserAnalyticsDetail(w, userProfileDegradeRequest(ctx))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d，期望 500（普通错误不得降级），body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"degraded":true`) {
		t.Errorf("普通错误不得被降级成 200 + 空载荷，body = %s", w.Body.String())
	}
}

// TestUserProfileDetailAlwaysEmitsDegradedField：正常路径恒发 degraded:false。
func TestUserProfileDetailAlwaysEmitsDegradedField(t *testing.T) {
	pool, _ := newUserProfileDegradeDB(t)
	ctx := context.Background()

	// 造出「有这张表且该 owner 有行」的形态。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.session_owners (
			id BIGSERIAL PRIMARY KEY,
			gw_session_id VARCHAR(128),
			tenant_id VARCHAR(64),
			owner_user VARCHAR(128),
			end_user_id VARCHAR(128),
			api_key_id BIGINT,
			application_id BIGINT,
			application_code VARCHAR(64),
			client_id VARCHAR(128),
			request_count INT,
			total_cost_usd NUMERIC,
			success_count INT,
			error_count INT,
			first_seen_at TIMESTAMPTZ,
			last_seen_at TIMESTAMPTZ
		)`); err != nil {
		t.Fatalf("create session_owners: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_owners
			(gw_session_id, tenant_id, owner_user, request_count, total_cost_usd,
			 success_count, error_count, first_seen_at, last_seen_at)
		VALUES ('gw_probe', 'default', 'owner-degrade-probe', 2, 1.5, 2, 0, now(), now())`); err != nil {
		t.Fatalf("insert session_owners: %v", err)
	}

	h := &Handler{db: pool}
	w := httptest.NewRecorder()
	h.handleUserAnalyticsDetail(w, userProfileDegradeRequest(ctx))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d，期望 200，body = %s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON: %v (body=%s)", err, w.Body.String())
	}
	v, ok := payload["degraded"]
	if !ok {
		t.Fatalf("正常路径必须**恒发** degraded 字段（字段缺失与 false 无法区分），body = %s", w.Body.String())
	}
	if b, _ := v.(bool); b {
		t.Errorf("正常路径 degraded 必须是 false，body = %s", w.Body.String())
	}
	if n, _ := payload["session_count"].(float64); n != 1 {
		t.Errorf("正常路径必须真的查到数据（session_count=1），实际 %v —— "+
			"本门若测不到数据，降级与否都验不出区别", payload["session_count"])
	}
}

// TestUserProfileDegradedPayloadKeepsStructShape：降级载荷形状与结构体同源。
func TestUserProfileDegradedPayloadKeepsStructShape(t *testing.T) {
	raw, err := json.Marshal(&UserProfileDetailResponse{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := make([]string, 0, len(shape))
	for k := range shape {
		want = append(want, k)
	}
	sort.Strings(want)

	pool, _ := newUserProfileDegradeDB(t)
	ctx := context.Background()
	h := &Handler{db: pool}
	w := httptest.NewRecorder()
	h.handleUserAnalyticsDetail(w, userProfileDegradeRequest(ctx))

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal degraded payload: %v", err)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("降级载荷缺字段 %q（结构体形状 = %v）", k, want)
		}
	}
}
