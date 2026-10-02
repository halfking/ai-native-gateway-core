package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// R65 真库门控集成回归：checkCredentialHealth 错误通道三门分类（R35-N1
// 登记遗留 doHealthCheck 复合 err 的收口）。
// ①真库 + 不存在的凭据 → 404 "credential not found"（ErrNoRows 原语义保留）；
// ②DB 故障（已关闭连接池）→ 500，而不是旧实现的一律 404（把 DB 故障伪装成
//   "凭据不存在"会把运维引向错误方向）。
// 同步 GET 路由（GET /api/providers/{id}/credentials/{cid}/check-health）在
// R65 才接线（此前 handler 是 //nolint:unused 死代码）。
func TestCheckCredentialHealth_NotFoundIs404(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	h := &Handler{db: pool}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/999999/credentials/999999/check-health", nil)
	h.checkCredentialHealth(rec, req, 999999, 999999)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing credential should be 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "credential not found") {
		t.Fatalf("404 must preserve original message, body=%s", rec.Body.String())
	}
}

func TestCheckCredentialHealth_ClosedPoolIs500Not404(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	closed, err := pgxpool.New(context.Background(), dsnOf(t))
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	closed.Close()

	hBroken := &Handler{db: closed}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/1/credentials/1/check-health", nil)
	hBroken.checkCredentialHealth(rec, req, 1, 1)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("DB failure must NOT be reported as 404 credential-not-found; body=%s", rec.Body.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DB failure should be 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestCheckCredentialHealth_GETRouteWired：R65 前该 handler 是
// //nolint:unused 死代码（dispatch 只接 POST），GET 面接线后经真实 dispatch
// 必须可达——不存在凭据 → 404（而非旧日的 405 method not allowed）。
func TestCheckCredentialHealth_GETRouteWired(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	h := &Handler{db: pool}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/999999/credentials/999999/check-health", nil)
	h.handleProviderCredentials(rec, req, 999999, "999999/check-health")
	if rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("GET check-health must be wired (was dead code pre-R65); got 405")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET check-health on missing credential should be 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// POST 分支保持原语义（后台任务 202）：用已关闭连接池证明 dispatch 可达
	//（insertBackgroundTask 失败 → 500，非 405），且不向真库写入任务行。
	closed, err := pgxpool.New(context.Background(), dsnOf(t))
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	closed.Close()
	hBroken := &Handler{db: closed}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/providers/999999/credentials/999999/check-health", nil)
	hBroken.handleProviderCredentials(rec2, req2, 999999, "999999/check-health")
	if rec2.Code == http.StatusMethodNotAllowed {
		t.Fatalf("POST check-health must stay wired; got 405")
	}
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("POST with closed pool should be 500 (task create failed), got %d body=%s", rec2.Code, rec2.Body.String())
	}
}
