package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// budgetCheck 的三条钉测（R89-133 收口，2026-10-01 第二十轮）：
//
//  1. hot 表的花费必须被计入——裸 usage_ledger 父表不含 hot，而两条台账
//     写方都直接写 hot、promote 按批调度，读裸父表等于对「最近 7 天」这个
//     最可能超支的滚动窗口完全失明（实测最近 8h 裸父表 0 行、hot 2,162 行）。
//  2. 同一 request_id 在 hot 与父表各残留一份时只计一次（与对账读路径
//     pg_reconciliation_store 的 DISTINCT ON 同款纪律）。
//  3. 花费查询失败必须 fail-closed 回 5xx——spent 保持零值回 200 会把
//     「不知道超没超」当成「没超」放行。

type budgetCheckResp struct {
	APIKeyID     int      `json:"api_key_id"`
	BudgetUSD    *float64 `json:"budget_usd"`
	SpentUSD     float64  `json:"spent_usd"`
	RemainingUSD *float64 `json:"remaining_usd"`
	Exceeded     bool     `json:"exceeded"`
}

func seedBudgetKey(t *testing.T, pool *pgxpool.Pool, code string, budget float64) int {
	t.Helper()
	ctx := context.Background()
	var appID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO applications (code, display_name) VALUES ($1, 'budget gate test') RETURNING id`,
		code).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	var keyID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, key_hash, key_prefix, enabled, budget_usd, created_at)
		VALUES ($1, 'zz-budget-hash', $2, true, $3, now())
		RETURNING id`, appID, code, budget).Scan(&keyID); err != nil {
		t.Fatalf("seed api_key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM usage_ledger_hot WHERE api_key_id = $1`, keyID)
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1`, keyID)
		_, _ = pool.Exec(ctx, `DELETE FROM applications WHERE id = $1`, appID)
	})
	return keyID
}

func insertHotSpend(t *testing.T, pool *pgxpool.Pool, keyID int, requestID string, cost float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO usage_ledger_hot (request_id, tenant_id, ts, api_key_id, cost_usd)
		VALUES ($1, 'default', now(), $2, $3)`, requestID, keyID, cost)
	if err != nil {
		t.Fatalf("seed hot spend: %v", err)
	}
}

func callBudgetCheck(h *Handler, keyID int) (budgetCheckResp, *httptest.ResponseRecorder) {
	body := `{"api_key_id":` + strconv.Itoa(keyID) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/keys/budget-check", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.budgetCheck(rec, req)
	var resp budgetCheckResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp, rec
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBudgetCheck_SeesHotSpendingWithinPromoteBlindWindow(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	keyID := seedBudgetKey(t, pool, "zz-bgate-a", 10.0)

	insertHotSpend(t, pool, keyID, "zz-bgate-a-r1", 3.5)
	resp, rec := callBudgetCheck(h, keyID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !almostEqual(resp.SpentUSD, 3.5) {
		t.Fatalf("spent = %v, want 3.5 —— hot 表的花费必须计入（R89-133 缺陷 A：裸父表对 promote 盲窗口失明）", resp.SpentUSD)
	}
	if resp.Exceeded {
		t.Fatalf("spent 3.5 < budget 10, exceeded 应为 false")
	}
	if resp.RemainingUSD == nil || !almostEqual(*resp.RemainingUSD, 6.5) {
		t.Fatalf("remaining = %v, want 6.5", resp.RemainingUSD)
	}

	insertHotSpend(t, pool, keyID, "zz-bgate-a-r2", 8.5)
	resp, rec = callBudgetCheck(h, keyID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !almostEqual(resp.SpentUSD, 12.0) {
		t.Fatalf("spent = %v, want 12.0", resp.SpentUSD)
	}
	if !resp.Exceeded {
		t.Fatalf("spent 12.0 >= budget 10.0, exceeded 必须为 true")
	}
}

func TestBudgetCheck_DedupesRequestIDDuplicatedAcrossHotAndParent(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	keyID := seedBudgetKey(t, pool, "zz-bgate-b", 100.0)

	// 父表一份（2026_08 分区内、ts 较早、7.0）+ hot 一份（ts=now 较新、3.0）：
	// 同一 request_id 只计一次，取 ts 最新（hot 那份）。request_id 带纳秒
	// 后缀：本库是共享 dev 实例，cleanup 是 best-effort（errcheck 豁免），
	// 固定字面量会撞上历史残留行 23505（第三十轮实测）。
	dupRequestID := fmt.Sprintf("zz-bgate-b-dup-%d", time.Now().UnixNano())
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO usage_ledger (request_id, tenant_id, ts, api_key_id, cost_usd)
		VALUES ($1, 'default', '2026-08-15T12:00:00Z', $2, 7.0)`,
		dupRequestID, keyID); err != nil {
		t.Fatalf("seed parent spend: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM usage_ledger WHERE request_id = $1`, dupRequestID)
	})
	insertHotSpend(t, pool, keyID, dupRequestID, 3.0)

	resp, rec := callBudgetCheck(h, keyID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !almostEqual(resp.SpentUSD, 3.0) {
		t.Fatalf("spent = %v, want 3.0 —— 双份 request_id 必须去重且取 ts 最新（对账读路径同款纪律），无去重会算成 10.0", resp.SpentUSD)
	}
}

// classifyBudgetSpendErr 的**纯函数**判据。不需要 LLM_GATEWAY_PG_URL。
//
// 为什么必须单独有一层：keys_budget_test.go 里那两条集成测试靠改名视图触发
// 真 42P01，没设 LLM_GATEWAY_PG_URL 时**整条 SKIP** —— 实测 5 条全 SKIP、
// exit 0。也就是说「42P01 → 503 / 其余 → 500」这个分类在默认环境下
// 一次都没被执行过，而它恰恰是本轮唯一的行为改动。
func TestClassifyBudgetSpendErr(t *testing.T) {
	missingRel := &pgconn.PgError{
		Code:      "42P01",
		Message:   `relation "usage_ledger_with_current_month" does not exist`,
		TableName: "usage_ledger_with_current_month",
	}

	t.Run("42P01 拆成 503 + 缺哪个视图族", func(t *testing.T) {
		status, code, detail := classifyBudgetSpendErr(missingRel)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", status)
		}
		if code != "usage_ledger_view_missing" {
			t.Fatalf("code = %q, want %q", code, "usage_ledger_view_missing")
		}
		if !strings.Contains(detail, "usage_ledger_with_current_month") {
			t.Fatalf("detail 未点名缺失的视图: %s", detail)
		}
		// ★ 这条是本轮最容易犯的错：照抄 analyticsViewMigrationHint 会带上 357。
		// 357 是 session-analytics 族的迁移号，运维照着跑完视图仍然缺。
		if !strings.Contains(detail, "344_usage_ledger_hot_independence.sql") {
			t.Fatalf("detail 必须指向 344（usage_ledger 族）: %s", detail)
		}
		if strings.Contains(detail, "357") {
			t.Fatalf("detail 混进了 357（那是 session-analytics 族的迁移号）: %s", detail)
		}
		// 同条件下认证面是 fail-open —— 不说清楚，运维会以为支出仍被拦着。
		if !strings.Contains(detail, "fail-open") {
			t.Fatalf("detail 必须点明认证面同条件下是 fail-open: %s", detail)
		}
	})

	t.Run("42P01 被包裹也要认出来", func(t *testing.T) {
		wrapped := errors.Join(errors.New("outer"), missingRel)
		status, code, _ := classifyBudgetSpendErr(wrapped)
		if status != http.StatusServiceUnavailable || code != "usage_ledger_view_missing" {
			t.Fatalf("包裹过的 42P01 必须仍走 503: status=%d code=%q", status, code)
		}
	})

	t.Run("只有消息没有 TableName 也能点名", func(t *testing.T) {
		// ExtractMissingRelationName 会退回正则；这条守的是「点名能力」不依赖 TableName。
		noTable := &pgconn.PgError{
			Code:    "42P01",
			Message: `relation "usage_ledger_with_current_month" does not exist`,
		}
		_, _, detail := classifyBudgetSpendErr(noTable)
		if !strings.Contains(detail, "usage_ledger_with_current_month") {
			t.Fatalf("TableName 为空时仍应从消息解析出视图名: %s", detail)
		}
	})

	t.Run("非 42P01 维持 500 原语义", func(t *testing.T) {
		for name, err := range map[string]error{
			"连接断":      errors.New("conn refused"),
			"42703 列缺": &pgconn.PgError{Code: "42703", Message: `column "foo" does not exist`},
			"nil":      nil,
		} {
			status, code, detail := classifyBudgetSpendErr(err)
			if status != http.StatusInternalServerError {
				t.Errorf("%s: status = %d, want 500", name, status)
			}
			if code != "" {
				t.Errorf("%s: 非 42P01 不得带 code（否则会被误当可修复环境态），实得 %q", name, code)
			}
			if detail != "usage query failed" {
				t.Errorf("%s: detail = %q, want %q", name, detail, "usage query failed")
			}
		}
	})

	t.Run("两类失败必须真的不同（防恒绿）", func(t *testing.T) {
		// 只分别断言各自分支的话，把整个函数改成「永远 503」也能过。
		// 这里显式要求两者在三个维度上都不同。
		s1, c1, d1 := classifyBudgetSpendErr(missingRel)
		s2, c2, d2 := classifyBudgetSpendErr(errors.New("boom"))
		if s1 == s2 || c1 == c2 || d1 == d2 {
			t.Fatalf("两类失败被判成同形: 42P01=(%d,%q,%q) 其余=(%d,%q,%q)", s1, c1, d1, s2, c2, d2)
		}
	})
}

// assertNoBudgetNumbersProduced 是本文件第 3 条钉测的**意图**所在：
// 花费读不出来时，响应里不得出现任何看起来自洽的预算数字。
// 抽成独立函数是因为 42P01 与非 42P01 两条路径现在分开，
// 但「不得产出数字」这条对两者同样成立 —— 意图不该跟着状态码一起被拆没。
func assertNoBudgetNumbersProduced(t *testing.T, resp budgetCheckResp, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200 —— 花费读不出来时回 200 就是把「不知道超没超」讲成「没超」"+
			"（R89-133 缺陷 B），body = %s", rec.Body.String())
	}
	if resp.SpentUSD != 0 || resp.Exceeded {
		t.Fatalf("fail-closed 路径不应产出自洽的预算数字: %+v", resp)
	}
}

// 花费查询因 42P01（usage_ledger 视图缺失）失败：必须 fail-closed，
// 且**必须**与「DB 真挂了」区分开 —— 2026-10-03（第八轮）之前两者同码同文案，
// 运维从响应无法区分该补迁移还是该查连接。沿用 admin 包三门约定：42P01 → 503。
func TestBudgetCheck_FailClosedWhenSpendingUnreadable(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	keyID := seedBudgetKey(t, pool, "zz-bgate-c", 10.0)
	insertHotSpend(t, pool, keyID, "zz-bgate-c-r1", 3.0)

	// 让花费查询确定性失败：视图临时改名（一次性库；Cleanup 恢复。
	// 本包测试顺序执行，恢复先于后续用例）。api_keys 查询不受影响，
	// 恰好只命中第二跳。
	if _, err := pool.Exec(context.Background(),
		`ALTER VIEW usage_ledger_with_current_month RENAME TO usage_ledger_with_current_month_bak`); err != nil {
		t.Fatalf("rename view: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`ALTER VIEW usage_ledger_with_current_month_bak RENAME TO usage_ledger_with_current_month`)
	})

	resp, rec := callBudgetCheck(h, keyID)
	assertNoBudgetNumbersProduced(t, resp, rec)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s —— 42P01（视图缺失，可修复环境态）必须 503，"+
			"不能与 DB 故障共用 500", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// 迁移号必须是真的那个。analyticsViewMigrationHint 写死 357
	// （session-analytics 族），抄过来会让运维去跑错的迁移 —— 比不给更坏。
	if !strings.Contains(body, "usage_ledger_view_missing") {
		t.Fatalf("body = %s, want code %q —— 42P01 必须自报缺的是哪个视图族", body, "usage_ledger_view_missing")
	}
	if !strings.Contains(body, "344_usage_ledger_hot_independence.sql") {
		t.Fatalf("body = %s —— 引导文案必须指向 344（usage_ledger 族），"+
			"不能是 357（那是 session-analytics 族的号）", body)
	}
	// 这条 detail 是本轮最要紧的一句：同一条件下认证面是 fail-open，
	// 只说「本端点 fail-closed」会让人以为支出仍然被拦着。
	if !strings.Contains(body, "fail-open") {
		t.Fatalf("body = %s —— 必须点明认证面在同条件下是 fail-open（放行不拦支出），"+
			"否则运维会以为预算仍在被拦截", body)
	}
}

// 非 42P01 的查询失败必须**维持 500 原语义**。这条与上面那条成对：
// 只钉 42P01 → 503 的话，把所有错误一律改成 503 也照样过（恒绿）。
// 造一个 42703（undefined_column）：视图存在，但列不存在。
func TestBudgetCheck_NonMissingRelationErrorKeeps500(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	keyID := seedBudgetKey(t, pool, "zz-bgate-d", 10.0)
	insertHotSpend(t, pool, keyID, "zz-bgate-d-r1", 3.0)

	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`ALTER VIEW usage_ledger_with_current_month RENAME TO usage_ledger_with_current_month_bak`); err != nil {
		t.Fatalf("rename view: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`CREATE VIEW usage_ledger_with_current_month AS SELECT no_such_column_zz FROM usage_ledger_hot`); err != nil {
		t.Fatalf("create broken view: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP VIEW IF EXISTS usage_ledger_with_current_month`)
		_, _ = pool.Exec(ctx,
			`ALTER VIEW usage_ledger_with_current_month_bak RENAME TO usage_ledger_with_current_month`)
	})

	resp, rec := callBudgetCheck(h, keyID)
	assertNoBudgetNumbersProduced(t, resp, rec)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s —— 非 42P01 的失败必须维持 500 原语义"+
			"（本次造的是 42703 undefined_column，不是缺关系）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "usage query failed") {
		t.Fatalf("body = %s, want substring %q", rec.Body.String(), "usage query failed")
	}
}

// 第三十轮钉测：budgetCheck 租户围栏。旧实现硬编码 tenant_id='default'，
// 而 AdminMiddleware 只验 JWT 不验角色——任意租户的 tenant_admin 可用数字
// ID 枚举 default 租户任意 key 的预算与实时花费（跨租户泄露面），非 default
// 租户自己的 key 反而一律 404。修复后与 verifyKey（R46 形态）同款：
// tenant_admin 钉本租户，super_admin/admin_key 放行全租户。
func TestBudgetCheck_TenantScoping(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	defaultKeyID := seedBudgetKey(t, pool, "zz-bgate-td", 10.0)

	var appID int
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO applications (code, display_name) VALUES ('zz-bgate-tenant-x', 'budget tenant scoping test') RETURNING id`).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM api_keys WHERE application_id = $1`, appID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM applications WHERE id = $1`, appID)
	})
	var tenantKeyID int
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO api_keys (application_id, key_hash, key_prefix, enabled, budget_usd, created_at, tenant_id)
		VALUES ($1, 'zz-budget-hash-tx', 'zz-bgate-tx', true, 10.0, now(), 'zz-tenant-x')
		RETURNING id`, appID).Scan(&tenantKeyID); err != nil {
		t.Fatalf("seed tenant api_key: %v", err)
	}

	callWithRole := func(role, tenant string, keyID int) (int, *httptest.ResponseRecorder) {
		t.Helper()
		body := `{"api_key_id":` + strconv.Itoa(keyID) + `}`
		req := httptest.NewRequest(http.MethodPost, "/api/keys/budget-check", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), authContextKey{}, &AuthContext{
			UserID: 1, TenantID: tenant, Username: "scope-test", Role: role, IsJWT: true,
		})
		rec := httptest.NewRecorder()
		h.budgetCheck(rec, req.WithContext(ctx))
		return rec.Code, rec
	}

	// tenant_admin(zz-tenant-x) 查本租户 key → 200。
	if code, rec := callWithRole("tenant_admin", "zz-tenant-x", tenantKeyID); code != http.StatusOK {
		t.Fatalf("tenant_admin own key: status = %d, body = %s —— 本租户 key 必须可查（旧实现恒 404）", code, rec.Body.String())
	}
	// tenant_admin(zz-tenant-x) 查 default 租户 key → 404（围栏拦截）。
	if code, rec := callWithRole("tenant_admin", "zz-tenant-x", defaultKeyID); code != http.StatusNotFound {
		t.Fatalf("tenant_admin cross-tenant: status = %d, body = %s —— 跨租户预算/花费探测必须 404（旧实现 200 泄露）", code, rec.Body.String())
	}
	// super_admin 查 default 租户 key → 200（全租户放行）。
	if code, rec := callWithRole("super_admin", "default", defaultKeyID); code != http.StatusOK {
		t.Fatalf("super_admin default key: status = %d, body = %s", code, rec.Body.String())
	}
	// 无租户上下文（legacy admin_key 形态）→ IsTenantAdmin=false → 全租户放行 → 200。
	if code, rec := callWithRole("", "", defaultKeyID); code != http.StatusOK {
		t.Fatalf("no-auth-context (legacy admin_key): status = %d, body = %s", code, rec.Body.String())
	}
}
