package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// R35-N1 真库门控集成回归：分类三门在真实 handler 路径上的行为。
// ①真库 + 不存在的主键 → 404 原语义（ErrNoRows 分类正确）；
// ②DB 故障（已关闭连接池）→ 500，而不是旧实现的一律 404。
func TestWriteLookupErr_IntegrationClosedPoolIs500Not404(t *testing.T) {
	pool := setupTestDB(t) // TEST_DATABASE_URL 门控，缺省 skip
	defer pool.Close()

	h := &Handler{db: pool}

	// — 真库正常路径：随机 UUID 真不存在 → 404 "application not found"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/keys/applications/nonexistent/approve", nil)
	h.approveKeyApplication(rec, req, uuid.New())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing application should be 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// — DB 故障路径：已关闭连接池 → 连接错误（非 ErrNoRows）→ 必须 500
	closed, err := pgxpool.New(context.Background(), dsnOf(t))
	if err != nil {
		t.Fatalf("open second pool: %v", err)
	}
	closed.Close()

	hBroken := &Handler{db: closed}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/keys/applications/broken/approve", nil)
	hBroken.approveKeyApplication(rec2, req2, uuid.New())
	if rec2.Code == http.StatusNotFound {
		t.Fatalf("DB failure must NOT be reported as 404 (misleads operators); body=%s", rec2.Body.String())
	}
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("DB failure should be 500, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestWriteLookupErr_RejectBranchClassification：reject 路径同族验证。
func TestWriteLookupErr_RejectBranchClassification(t *testing.T) {
	pool := setupTestDB(t)
	defer pool.Close()

	h := &Handler{db: pool}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/keys/applications/nonexistent/reject", nil)
	h.rejectKeyApplication(rec, req, uuid.New())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing application should be 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// dsnOf 返回 TEST_DATABASE_URL（gate 已在 setupTestDB 内校验非空）。
func dsnOf(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	return dsn
}
