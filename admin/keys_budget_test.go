package admin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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
	// 同一 request_id 只计一次，取 ts 最新（hot 那份）。
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO usage_ledger (request_id, tenant_id, ts, api_key_id, cost_usd)
		VALUES ($1, 'default', '2026-08-15T12:00:00Z', $2, 7.0)`,
		"zz-bgate-b-dup", keyID); err != nil {
		t.Fatalf("seed parent spend: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM usage_ledger WHERE request_id = $1`, "zz-bgate-b-dup")
	})
	insertHotSpend(t, pool, keyID, "zz-bgate-b-dup", 3.0)

	resp, rec := callBudgetCheck(h, keyID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !almostEqual(resp.SpentUSD, 3.0) {
		t.Fatalf("spent = %v, want 3.0 —— 双份 request_id 必须去重且取 ts 最新（对账读路径同款纪律），无去重会算成 10.0", resp.SpentUSD)
	}
}

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
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s —— 花费查询失败必须 fail-closed 回 500（R89-133 缺陷 B），不得 spent=0 回 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "usage query failed") {
		t.Fatalf("body = %s, want substring %q", rec.Body.String(), "usage query failed")
	}
	if resp.SpentUSD != 0 || resp.Exceeded {
		t.Fatalf("fail-closed 路径不应产出自洽的预算数字: %+v", resp)
	}
}
