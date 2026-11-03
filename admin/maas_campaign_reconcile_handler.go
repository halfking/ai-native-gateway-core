package admin

// F-2 延伸（2026-11-04 审计修复轮，承接 0329 审计 §4.2 序号 12）：
// 活动额度双向对账的 admin 只读读数端点。
//
// 背景：ReconcileCampaigns / CampaignsBalanced（maas/campaign_reconcile.go）
// 此前**零生产调用方** —— 对账只在测试里跑，红线只落在测试里。本端点把
// 它暴露为运营看板读数。
//
// 鉴权档位：super-only，与同族 /api/admin/maas/bills/reconcile
// （maas.ReconcileTripartite 的 admin 对账读点）逐字同档 —— 两处都是
// 平台级对账数据（读 credit_ledger_with_current_month + promotion_redemptions，
// 不带租户谓词），不该降到 tenant_admin 可见面。
//
// ★ 只读承诺：本文件不新增任何写操作；maas/campaign_reconcile.go 的
//
//	SQL 与函数签名零改动（只调用）。
//
// 分页口径：ReconcileCampaigns 的签名只收 code（活动目录体量小，SQL 无
// LIMIT），页化在 handler 层对**已取回的全量**切片 ⇒ 无 OFFSET 放大面；
// page/page_size 仍然钳位（与 /api/maas/my/requests 同族口径：
// page 下界 1、page_size 越界回默认），防无界参数产出异常大响应。
// ★ days 参数刻意不收：对账查询没有时间窗维度（函数签名无 days），
// 加一个不参与计算的参数只会制造「看似可筛、实则无效」的假 API 面。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/maas"
)

// 分页钳位档位（handler 层切片分页）：
//   - page_size 上界 100 与 maas.QueryMyRequests 的同族上界对齐；
//   - page 上界只做响应尺寸护栏 —— 切片分页没有 OFFSET 扫描成本，
//     取一个宽但有限的值防手滑构造天量请求。
const (
	campaignReconcileDefaultPageSize = 50
	campaignReconcileMaxPageSize     = 100
	campaignReconcileMaxPage         = 1000
)

// campaignReconciliationResponse 组装端点响应（纯函数 ⇒ 判据可无 DB 单测）。
//
// ★ balanced 判 **全量**（CampaignsBalanced(recs)），不是页内 —— 看板的
//
//	红绿信号不能因翻页而变。
func campaignReconciliationResponse(recs []maas.CampaignReconciliation, page, pageSize int) map[string]any {
	items, total, page, pageSize := paginateCampaignReconciliation(recs, page, pageSize)
	return map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"balanced":  maas.CampaignsBalanced(recs),
	}
}

// paginateCampaignReconciliation 钳位 + 切片（纯函数，无 DB 依赖）。
// 钳位口径：page < 1 → 1、page > 上界 → 上界；page_size 越界（<1 或
// > 上界）→ 回默认 —— 与 QueryMyRequests 的「page_size 非法回默认」同族。
func paginateCampaignReconciliation(recs []maas.CampaignReconciliation, page, pageSize int) (items []maas.CampaignReconciliation, total, pageOut, sizeOut int) {
	if page < 1 {
		page = 1
	}
	if page > campaignReconcileMaxPage {
		page = campaignReconcileMaxPage
	}
	if pageSize < 1 || pageSize > campaignReconcileMaxPageSize {
		pageSize = campaignReconcileDefaultPageSize
	}
	total = len(recs)
	items = []maas.CampaignReconciliation{}
	if start := (page - 1) * pageSize; start < total {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = append(items, recs[start:end]...)
	}
	return items, total, page, pageSize
}

func (h *Handler) handleAdminMaasCampaignReconciliation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	svc := h.maasSvc()
	if svc == nil || !svc.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	// 参数解析与 /api/maas/my/requests 同族：解析失败保持默认，钳位集中在
	// paginateCampaignReconciliation 一处（单一钳位语义，单测直接钉它）。
	page, pageSize := 1, campaignReconcileDefaultPageSize
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			page = n
		}
	}
	if v := q.Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			pageSize = n
		}
	}

	recs, err := svc.ReconcileCampaigns(r.Context(), strings.TrimSpace(q.Get("code")))
	if err != nil {
		writeInternalErr(w, "internal error (see server logs)", err)
		return
	}
	writeJSON(w, http.StatusOK, campaignReconciliationResponse(recs, page, pageSize))
}

// registerMaasCampaignReconcileRoutes wires the platform-level campaign
// reconciliation readout (super-only, read-only; F-2 延伸).
func (h *Handler) registerMaasCampaignReconcileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/maas/campaign-reconciliation", h.superAdmin(h.handleAdminMaasCampaignReconciliation))
}
