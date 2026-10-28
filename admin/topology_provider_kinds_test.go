package admin

// topology_provider_kinds_test.go — 「内部直连供应商判定」端点判据
// （2026-10-28 选路层轮）。
//
// 三条：
//  1. 正常路径：code/kind 两列投影，软删除行由 SQL 过滤（这里验投影形状）；
//  2. DB 不可用 → 200 + degraded（不是 500，不是空清单冒充「没有直连供应商」）；
//  3. 非 GET 拒绝。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestTopologyProviderKindsProjectsCodeAndKind(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	rows := pgxmock.NewRows([]string{"code", "kind"}).
		AddRow("deepseek", "cloud").
		AddRow("local-ollama", "local")
	mock.ExpectQuery(`(?s)SELECT code, COALESCE\(kind, ''\) FROM providers`).
		WillReturnRows(rows)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/provider-kinds", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-x", UserID: 9})
	rec := httptest.NewRecorder()
	// ★ tenant_admin 且非 default 租户：这正是本端点存在的理由 ——
	// /api/providers 会 403，这里必须照常工作。
	(&Handler{topologyProviderKindsDB: mock}).handleTopologyProviderKinds(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var out topologyProviderKindsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.False(t, out.Degraded)
	require.Len(t, out.Items, 2)
	require.Equal(t, "local-ollama", out.Items[1].Code)
	require.Equal(t, "local", out.Items[1].Kind)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTopologyProviderKindsDegradedWithoutDB(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/topology/provider-kinds", nil)
	req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "tenant-x", UserID: 9})
	rec := httptest.NewRecorder()
	(&Handler{db: nil}).handleTopologyProviderKinds(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var out topologyProviderKindsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.True(t, out.Degraded)
	require.NotEmpty(t, out.ErrorCode)
}

func TestTopologyProviderKindsRejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/topology/provider-kinds", nil)
	rec := httptest.NewRecorder()
	(&Handler{}).handleTopologyProviderKinds(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
