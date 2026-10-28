package admin

// 2026-10-28 拦截反馈明确化轮：硬阻断清单的**运行时管理面** + 拦截审核队列的
// HTTP 入口。
//
// ## 为什么需要这一层
//
// owner 口径（本需求原话）：「或者可以在后台有个开关一次性关掉。或者在后台
// 有个列表，需要审核，审核完成后可以返回给前端。」
//
// 硬阻断清单（domains/sanitizerules/hardblock.go）此前只有环境变量一个入口
// ——改一次要改 env 文件并重启进程；被拦的现场只有容器日志可看，管理面
// 够不着。本文件补两件事：
//
//   - GET/PUT hardblock：运行时查看/整表替换「哪些规则允许终止回合」。
//     一次 PUT ["output:*","egress:*"] 或清空（显式确认）就是那个
//     「一次性关掉」的开关；无需重启，重启后回落到 env 装载的清单。
//   - blocked-reviews：拦截审核队列的列表与批准/拒绝（批准 ⇒ 服务端写入
//     放行记录，用户重试即拿到内容）。
//
// ## 权限
//
// 与 egress-releases 同一条线：写操作只给 super_admin；租户管理员可读
// 自己租户的审核列表（「为什么我被拦了」），不可写。

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// handleHardBlockRules 处理硬阻断清单的读/写。
//
//	GET  /api/admin/sanitize-rules/hardblock        （super_admin）
//	PUT  /api/admin/sanitize-rules/hardblock        （super_admin）
//
// PUT body：{"rules": ["egress:builtin.egress_sink", "output:*"], "comment": "…",
// "confirm_disable_all": false}。语义是**整表替换**（不是 env 装载的累加语义）：
// 运维在这里看到的就是进程当前生效的完整清单，改完即生效。清空清单（关掉
// 包括请求侧 fail-closed 在内的全部硬阻断）必须同时带 confirm_disable_all=true
// ——那是 env 语法里 "-" 的等价物，要求显式。
func (h *Handler) handleHardBlockRules(w http.ResponseWriter, r *http.Request) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "only super_admin can view or change the hard block allowlist")
		return
	}
	switch r.Method {
	case http.MethodGet:
		allows := sanitizerules.HardBlockAllows()
		writeJSON(w, http.StatusOK, map[string]any{
			"rules": allows,
			"note":   "runtime view; PUT replaces the whole list for this process lifetime, restart falls back to LLM_GATEWAY_HARD_BLOCK_RULES",
		})
	case http.MethodPut:
		var req struct {
			Rules             []string `json:"rules"`
			Comment           string   `json:"comment"`
			ConfirmDisableAll bool     `json:"confirm_disable_all"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		if len(req.Rules) == 0 && !req.ConfirmDisableAll {
			writeError(w, http.StatusBadRequest, "empty rules would disable every hard block including the input-side fail-closed guard; pass confirm_disable_all=true to do that explicitly")
			return
		}
		allows := make([]sanitizerules.HardBlockAllow, 0, len(req.Rules))
		for _, raw := range req.Rules {
			parsed, err := sanitizerules.ParseHardBlockAllows(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			allows = append(allows, parsed...)
		}
		sanitizerules.SetHardBlockAllows(allows)
		auditLog := strings.TrimSpace(req.Comment)
		writeSanitizeRuleAudit("hardblock_allowlist_updated", auditLog, allows)
		writeJSON(w, http.StatusOK, map[string]any{
			"rules":   sanitizerules.HardBlockAllows(),
			"comment": auditLog,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// writeSanitizeRuleAudit 把运行时清单变更打进结构化日志（进程内清单没有
// 版本表，日志是它的变更史）。
func writeSanitizeRuleAudit(event, comment string, allows []sanitizerules.HardBlockAllow) {
	items := make([]string, 0, len(allows))
	for _, a := range allows {
		items = append(items, a.Scope+":"+a.Rule)
	}
	slog.Warn("sanitize_hardblock_allowlist_changed",
		"event", event,
		"comment", comment,
		"allows", items)
}

// handleBlockedReviews 处理拦截审核队列的读/写。
//
//	GET   /api/admin/sanitize-rules/blocked-reviews?status=pending[&tenant_id=…]
//	POST  /api/admin/sanitize-rules/blocked-reviews/{id}/review
//	      body = {"decision": "approve"|"reject", "comment": "…"}
//
// 批准 ⇒ sanitizerules.DecideBlockReview 写入放行记录（绑定载荷摘要时只放行
// 这一次载荷），用户**重试**即拿到内容；拒绝 ⇒ 拦截维持。
func (h *Handler) handleBlockedReviews(w http.ResponseWriter, r *http.Request, auth *AuthContext) {
	super := IsSuperAdminOrLegacy(r)
	path := strings.TrimPrefix(r.URL.Path, sanitizeRulesPath+"/blocked-reviews")
	switch {
	case path == "" || path == "/":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status != "" && status != sanitizerules.BlockReviewPending && status != sanitizerules.BlockReviewApproved && status != sanitizerules.BlockReviewRejected {
			writeError(w, http.StatusBadRequest, "invalid review status filter")
			return
		}
		limit, ok := sanitizeRuleLimit(w, r)
		if !ok {
			return
		}
		tenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		if !super {
			// 租户管理员只看自己租户的队列。
			tenant = auth.TenantID
		}
		reviews := sanitizerules.ListBlockReviews(status, tenant, limit)
		writeJSON(w, http.StatusOK, map[string]any{
			"reviews":       reviews,
			"process_only":  true,
			"pending_count": len(sanitizerules.ListBlockReviews(sanitizerules.BlockReviewPending, tenant, 1000)),
		})
	case strings.HasPrefix(path, "/") && strings.HasSuffix(path, "/review"):
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !super {
			writeError(w, http.StatusForbidden, "only super_admin can decide a blocked review")
			return
		}
		idStr := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/review")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, "invalid review ID")
			return
		}
		var req struct {
			Decision string `json:"decision"`
			Comment  string `json:"comment"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		approve := req.Decision == "approve"
		if !approve && req.Decision != "reject" {
			writeError(w, http.StatusBadRequest, "decision must be approve or reject")
			return
		}
		decidedBy := sanitizeRuleActor(auth)
		if req.Comment != "" {
			decidedBy += " (" + req.Comment + ")"
		}
		review, err := sanitizerules.DecideBlockReview(id, approve, decidedBy)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, review)
	default:
		writeError(w, http.StatusNotFound, "sanitize rule endpoint not found")
	}
}
