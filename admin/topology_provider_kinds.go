package admin

// topology_provider_kinds.go — 实时拓扑页「内部直连供应商」判定的只读投影。
//
//	GET /api/admin/topology/provider-kinds
//
// 2026-10-28（live 拓扑·选路层轮）：网关大方块「选路扇出带」的「内部直连」
// 出口与供应端节点的直连徽标，都需要判断 providers.kind === 'local'。
//
// ★ 为什么不复用 GET /api/providers：那个端点走 ProviderConsoleMiddleware
// （super_admin / **default 租户** tenant_admin），而 /live 是任意租户管理员
// 可进 —— 非 default 租户打开 /live 会 403，直连分桶恒为「未测量」
// （批判式审计修正：页面不该为一个辅助信息去要求更高权限）。
// 本端点与 /api/admin/topology/plugins 同级（admin 权限，任意 JWT 管理员），
// 只读两列，不泄凭据/URL 等敏感字段。
//
// 失败语义与 plugins 端点同契约：DB 不可用 → 200 + degraded 信封，前端渲染
// 成「未测量」而不是「没有直连供应商」。

import (
	"net/http"
	"time"
)

// TopologyProviderKind 是一行的投影面（code + kind 两列，刻意不放别的）。
type TopologyProviderKind struct {
	Code string `json:"code"`
	Kind string `json:"kind"`
}

type topologyProviderKindsResponse struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Items       []TopologyProviderKind  `json:"items"`
	Degraded    bool                    `json:"degraded"`
	ErrorCode   string                  `json:"error_code,omitempty"`
}

// handleTopologyProviderKinds 投影 providers.code/kind 全表。
// 表行数量级是几十（供应商目录），全量返回不需要分页/过滤参数。
func (h *Handler) handleTopologyProviderKinds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 注入口优先（单测 pgxmock），回落真实连接池 —— topologyStatsQuerier 同款。
	var querier topologyStatsDB
	if h.topologyProviderKindsDB != nil {
		querier = h.topologyProviderKindsDB
	} else if h.db != nil {
		querier = h.db
	}
	out := topologyProviderKindsResponse{
		GeneratedAt: time.Now().UTC(),
		Items:       []TopologyProviderKind{},
	}
	if querier == nil {
		out.Degraded = true
		out.ErrorCode = "db_unavailable"
		writeJSON(w, http.StatusOK, out)
		return
	}
	// tenant_id='default'：供应商目录挂在 default 租户（与 providers.go 全部
	// 既有查询同口径）；软删除行（deleted_at 非空）不投影。
	rows, err := querier.Query(r.Context(), `SELECT code, COALESCE(kind, '') FROM providers WHERE tenant_id = 'default' AND deleted_at IS NULL ORDER BY code`)
	if err != nil {
		out.Degraded = true
		out.ErrorCode = "query_failed"
		writeJSON(w, http.StatusOK, out)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item TopologyProviderKind
		if err := rows.Scan(&item.Code, &item.Kind); err != nil {
			out.Degraded = true
			out.ErrorCode = "scan_failed"
			out.Items = []TopologyProviderKind{}
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Items = append(out.Items, item)
	}
	if err := rows.Err(); err != nil {
		out.Degraded = true
		out.ErrorCode = "rows_iter_failed"
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
