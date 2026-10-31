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
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

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
			// 2026-10-29 拦截持久化轮：管理面保存的清单已落库（迁移 917 的
			// sanitize_hardblock_state），重启后仍是这份；env 只在从未用管理面
			// 保存过时才是生效值。
			"note": "runtime view; PUT replaces the whole list and persists to the database (restart keeps it). Without any managed save, the list comes from LLM_GATEWAY_HARD_BLOCK_RULES.",
		})
	case http.MethodPut:
		var req struct {
			Rules             []string `json:"rules"`
			Preset            string   `json:"preset"`
			Comment           string   `json:"comment"`
			ConfirmDisableAll bool     `json:"confirm_disable_all"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		// 2026-10-29 拦截持久化轮：preset 是「整批同步开/关」的一键形态——
		// all = 请求侧全量 + 输出侧全量 + egress 逐条内建规则（r1039 禁止
		// egress 通配，必须枚举）；none = 清空（等价 rules:[]，仍要求显式确认）。
		var allows []sanitizerules.HardBlockAllow
		switch req.Preset {
		case "":
			if len(req.Rules) == 0 && !req.ConfirmDisableAll {
				writeError(w, http.StatusBadRequest, "empty rules would disable every hard block including the input-side fail-closed guard; pass confirm_disable_all=true to do that explicitly")
				return
			}
			allows = make([]sanitizerules.HardBlockAllow, 0, len(req.Rules))
			for _, raw := range req.Rules {
				parsed, err := sanitizerules.ParseHardBlockAllows(raw)
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				allows = append(allows, parsed...)
			}
		case "all":
			allows = sanitizerules.BuiltinHardBlockAll()
		case "none":
			if !req.ConfirmDisableAll {
				writeError(w, http.StatusBadRequest, "preset none disables every hard block including the input-side fail-closed guard; pass confirm_disable_all=true to do that explicitly")
				return
			}
			allows = nil
		default:
			writeError(w, http.StatusBadRequest, "preset must be all or none")
			return
		}
		// 写穿透：进程内立即生效 + DB 持久化（重启后仍是这份清单，env 退化为
		// 没有管理面覆盖时的出厂默认）。
		sanitizerules.SaveHardBlockStatePersistent(r.Context(), allows, sanitizeRuleActor(GetAuthContext(r)))
		auditLog := strings.TrimSpace(req.Comment)
		writeSanitizeRuleAudit("hardblock_allowlist_updated", auditLog, allows)
		writeJSON(w, http.StatusOK, map[string]any{
			"rules":   sanitizerules.HardBlockAllows(),
			"comment": auditLog,
			"source":  "managed-state",
			"preset":  req.Preset,
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
	case path == "/insights":
		// ★ 2026-10-29 输出合规误报轮：按规则聚合的复核读数（规则优化的输入面）。
		h.handleBlockReviewInsights(w, r, auth)
		return
	case strings.HasPrefix(path, "/") && strings.HasSuffix(path, "/label"):
		// ★ 2026-10-29 输出合规误报轮：人工标注（与 approve/reject 正交）。
		h.handleBlockReviewLabel(w, r, auth)
		return
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
		// ★ 2026-10-29 输出合规误报轮（owner 反馈「看不到原始的上下文」）：
		// ★★ 给每条审核单补上**原始上下文**与**归因**（★ 读时联表，★ 不落库）
		//   —— 审核单自带的四个「为什么」字段在历史记录里全空 ⇒ ★★ 审核员
		//   无从判断「这次拦得对不对」★★。
		// ★ 补上下文有自己的**超时预算**（★ 读请求日志 + 邻近时段规则事件）：
		// ★ 补不齐不许拖垮列表（★ 超时 ⇒ 只丢上下文，★ 列表照常）★★
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		contexts := h.attachBlockReviewContexts(ctx, reviews)
		cancel()
		rows := make([]blockReviewRow, 0, len(reviews))
		for _, rv := range reviews {
			rows = append(rows, blockReviewRow{BlockReview: rv, Context: contexts[rv.ID]})
		}
		// 2026-10-29 持久化轮：一并返回放行表（租户过滤同 reviews），前端在
		// 已批准单上展示「放行有效期至」。持久化装载后含历史放行（含剩余期限）。
		releases := sanitizerules.EgressReleases()
		if !super {
			filtered := make([]sanitizerules.EgressRelease, 0, len(releases))
			for _, rel := range releases {
				if tenant != "" && rel.TenantID == tenant {
					filtered = append(filtered, rel)
				}
			}
			releases = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reviews": rows,
			// ★ 放行表口径不变（★ 租户过滤同 reviews）。
			"releases":     releases,
			"process_only": false,
			// ★ 2026-10-30 审计轮（迁移 926）：待审数改用「待决断」口径 ——
			//   降级放行单（响应已成功送达、无决断可做）不计入，否则
			//   211 实测 24h 内 226/226 条全是降级单 ⇒ 徽标整天闪假警报。
			"pending_count": sanitizerules.CountPendingBlockDecisions(tenant),
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
		// 2026-10-29 持久化轮：决定落库；批准写规则级 7 天放行（租户+规则，
		// 同一租户一周内同规则不再拦），进程内与 DB 同时生效。
		review, err := sanitizerules.DecideBlockReviewWithContext(r.Context(), id, approve, decidedBy)
		if err != nil {
			writeSanitizeRuleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, review)
	default:
		writeError(w, http.StatusNotFound, "sanitize rule endpoint not found")
	}
}
