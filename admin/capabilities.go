// Package admin — capabilities.go
//
// 「功能能力位」端点：告诉前端**当前实例实际启用了哪些可选功能**，
// 使前端能**按能力隐藏入口**，而不是「点了必然失败」。
//
// # 为什么需要它
//
// 「费用对账」是一个**可选装配**的功能：它的端点只在开关打开时才注册
// （admin/handler.go:1096 —— `providerCostReconciliationHandler != nil` 才注册）。
// ⇒ ⇒ 而移动端那一席（`/m/reconciliation`）此前**无条件可见**
// （web-mobile/src/config/appNav.ts 的 `reconciliation` 席只带 `requiresRole`）
// ⇒ ⇒ ⇒ 开关关着时，super_admin 照样看得见那一席、点进去必然 404。
// ★ 404 与「端点不存在」同形 ⇒ **没有一层会说真话**（这正是 2026-10-21
// R72f 页面巡检挖出这条链时的现场）。
//
// # 单一真源
//
// ★ 能力位**不是**另开一份状态，而是由「handler 是否装配」**派生**：
// `providerCostReconciliationHandler != nil` ⇔ 开关打开 ⇔ 端点已注册
// ⇒ ⇒ ⇒ 所以「能力位说开着」与「端点真的在」**不可能不一致**。
//
// # 注册时机
//
// ★ 本端点**无条件注册**（即使功能全关也要在）⇒ ⇒ 否则前端**学不到
// 「关」** ⇒ ⇒ ⇒ 那样「按能力隐藏入口」就无从谈起（只能靠猜）。
package admin

import (
	"encoding/json"
	"net/http"
)

// FeatureCapabilities 描述当前实例实际启用的可选功能。
//
// ★ 字段名 = 前端 `NavItem.requiresCapability` 用的能力名（**逐字相等**）。
type FeatureCapabilities struct {
	// CostReconciliation：「费用对账」已装配（端点已注册）。
	CostReconciliation bool `json:"cost_reconciliation"`
}

// featureCapabilities 派生能力位。
//
// ★ 派生而非存储：与「端点是否注册」共用同一个真相来源
// （`providerCostReconciliationHandler != nil`）⇒ ⇒ ⇒ 不存在
// 「能力位说开着、端点却 404」的可能。
func (h *Handler) featureCapabilities() FeatureCapabilities {
	return FeatureCapabilities{
		CostReconciliation: h.providerCostReconciliationHandler != nil,
	}
}

// handleCapabilities GET /api/admin/capabilities
//
// ★ 鉴权用 `admin`（不是 `superAdmin`）：这是**只读元数据**（不敏感），
// 而 tenant_admin 的抽屉也会渲染 ⇒ ⇒ 若用 superAdmin，tenant_admin 取能力位
// 会拿到 403 ⇒ ⇒ ⇒ 前端就得把 403 当成「全部能力关」——那会让
// super_admin 之外的人看到**错误的**入口集。★ 所以这里用 admin 档。
func (h *Handler) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(h.featureCapabilities())
}

// RegisterCapabilityRoutes 注册能力位端点。
//
// ★ 无条件注册（**与功能开关无关**）—— 这是「前端能学到『关』」的前提。
func (h *Handler) RegisterCapabilityRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	// ★ 2026-10-22 12h 审计轮（A8）：改为**方法限定的路由模式**。
	// 原形态 `mux.HandleFunc("/api/admin/capabilities", …)` 对**任何**方法都注册
	// ⇒ POST / DELETE / PUT 也会进同一个 handler ⇒ 返回 200 + 能力位 JSON
	// ⇒ 一个**只读**端点被写方法打中也报「成功」⇒ 调用方会以为写成功了。
	// ⇒ Go 1.22+ 的 ServeMux 支持 "METHOD /path" 模式 ⇒ 直接限死 GET。
	mux.HandleFunc("GET /api/admin/capabilities", h.admin(h.handleCapabilities))
}
