package admin

// 输出合规的**全局开关** + 拦截审核工作台的管理面（2026-10-29 输出合规误报轮）。
//
// ## 为什么要单独一个开关端点
//
// owner 口径（本需求原话）：「我们需要有一个开关，能在全局关闭或开启输出合规的
// 检查」。仓里其实早有这个开关（settings 的 `sanitize.output_check_enabled`，
// 平台级、热加载），但它只出现在**模块设置页**里 —— 运维在「清洗规则」页看不到
// 它，也看不出它关掉之后**具体**关掉了什么。
//
// ⇒ 本文件给「输出合规」这件事一个**专属的、可读的、带审计的**入口：
//
//	GET /api/admin/sanitize-rules/output-check   → 当前状态 + 语义说明
//	PUT /api/admin/sanitize-rules/output-check   → 开关（写 settings_kv + 审计日志）
//
// ★ 它写的是**同一个设置键**（sanitize.output_check_enabled）⇒ 与模块设置页
//   双向一致，不存在第二份真值。
//
// ## 审核工作台的三个新面
//
//	POST /api/admin/sanitize-rules/blocked-reviews/{id}/label
//	      → 人工标注（误报/真命中/规则太宽/缺规则）。★ 与 approve/reject 正交：
//	        状态回答「现在放不放行」，标注回答「这次拦得对不对」。
//	GET  /api/admin/sanitize-rules/blocked-reviews/insights
//	      → 按规则聚合的复核读数（规则优化的输入面）。
//	GET  /api/admin/sanitize-rules/blocked-reviews
//	      → 列表（已含规则名/原因/位置/摘要/**有界摘录**/会话 ID）。
//
// ## 权限
//
// 写（开关/标注）只给 super_admin；租户管理员可读自己租户的列表与读数
// （「为什么我被拦了」），不可写。

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// outputCheckSettingKey 是输出合规总开关的设置键（平台级、热加载）。
//
// ★ 与 settings/spec_modules.go 的 ModuleSpecs 里那一条**逐字一致** —— 同一份
// 真值，本端点不另立开关。
const outputCheckSettingKey = "sanitize.output_check_enabled"

// handleOutputCheckSwitch 处理输出合规总开关的读/写。
//
//	GET  /api/admin/sanitize-rules/output-check    （super_admin）
//	PUT  /api/admin/sanitize-rules/output-check    （super_admin）
//
// PUT body：{"enabled": true|false, "comment": "…" }。
//
// ★ 语义（前端必须原样展示，别让运维猜）：
//
//	开启：输出侧敏感检查全部生效（强制外传守卫 + 可选输出合规 + 写出前脱敏）。
//	关闭：**只关输出侧**。输入侧脱敏与占位符还原继续执行（不是整体旁路）。
//	★ 热加载：新值对**后续请求**立即生效；**已在流式途中的请求**在首帧
//	  就固定了检查状态 ⇒ 中途改开关不影响它（这是既有契约，不是本轮新增）。
func (h *Handler) handleOutputCheckSwitch(w http.ResponseWriter, r *http.Request) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "only super_admin can view or change the output check switch")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.outputCheckSwitchState())
	case http.MethodPut:
		var req struct {
			Enabled *bool  `json:"enabled"`
			Comment string `json:"comment"`
		}
		if !decodeSanitizeRuleBody(w, r, &req) {
			return
		}
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required (true = on, false = off)")
			return
		}
		if settings.Global == nil {
			writeError(w, http.StatusServiceUnavailable, "settings registry not initialised")
			return
		}
		sp := settings.Global.Spec(outputCheckSettingKey)
		if sp == nil {
			writeError(w, http.StatusServiceUnavailable, "output check setting is not registered")
			return
		}
		if err := sp.Validate(*req.Enabled); err != nil {
			writeError(w, http.StatusBadRequest, "validation: "+err.Error())
			return
		}
		store, ok := h.dbSettingsStore()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "settings store not available")
			return
		}
		if _, err := store.Set(settings.ScopePlatform, outputCheckSettingKey, *req.Enabled); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist the output check switch")
			return
		}
		// ★ 变更立刻对后续请求生效：设置层有缓存（InvalidatePlatformValue 在
		//   Set 里已调用），但保险起见再显式失效一次（同一口径只有一处实现：
		//   settings.StoreDB.Set 自己会 InvalidatePlatformValue）。
		comment := strings.TrimSpace(req.Comment)
		writeSanitizeRuleAudit("output_check_switch_changed", comment, nil)
		writeJSON(w, http.StatusOK, h.outputCheckSwitchState())
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// outputCheckSwitchState 组装开关的读数（当前值 + 生效来源 + 语义说明）。
//
// ★ 「来源」必须给出来：运维要知道这个值是**默认**、**库里存着**、还是**刚被
//
//	改过** —— 三者的可动性不同（默认的要写库才持久）。
func (h *Handler) outputCheckSwitchState() map[string]any {
	state := map[string]any{
		"key":   outputCheckSettingKey,
		"scope": string(settings.ScopePlatform),
		"semantics": "开启：输出侧敏感检查全部生效（强制外传守卫 / 可选输出合规 / 写出前脱敏）。" +
			"关闭：只关输出侧；输入侧脱敏与占位符还原继续执行。已在途的流式请求在首帧固定了检查状态，不受中途改开关影响。",
		"default": true,
	}
	if settings.Global == nil {
		state["enabled"] = true
		state["source"] = "unavailable"
		return state
	}
	raw, source, err := settings.Global.EffectiveValue(settings.ScopePlatform, outputCheckSettingKey, "")
	if err == nil {
		var enabled bool
		if json.Unmarshal([]byte(raw), &enabled) == nil {
			state["enabled"] = enabled
		} else {
			state["enabled"] = true
		}
	} else {
		state["enabled"] = true
	}
	state["source"] = source
	if sp := settings.Global.Spec(outputCheckSettingKey); sp != nil {
		state["description"] = sp.Description
		state["description_long"] = sp.DescriptionLong
		state["hot_reload"] = sp.HotReload
	}
	return state
}

// handleBlockReviewLabel 给一条拦截审核单打人工标注。
//
//	POST /api/admin/sanitize-rules/blocked-reviews/{id}/label
//	     body = {"label": "false_positive"|"true_positive"|"rule_too_broad"|"needs_rule",
//	             "comment": "…"}
//
// ★ 与 approve/reject **正交**：状态回答「现在放不放行」，标注回答「这次拦得
//
//	对不对」。一个已批准的审核单也可以补标（规则太宽 ⇒ 该改规则）。
//
// ★ 标成 false_positive / rule_too_broad 时会触发**自动优化**判定：
//
//	同租户同规则的误报标注累计到阈值 ⇒ 自动写入该租户该规则的 7 天放行。
func (h *Handler) handleBlockReviewLabel(w http.ResponseWriter, r *http.Request, auth *AuthContext) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "only super_admin can label a blocked review")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// ★ 路径是 /api/admin/sanitize-rules/blocked-reviews/{id}/label ⇒ 先剥
	// 前缀、再剥**后缀**（反了会得到 "/{id}/label" ⇒ 解析必失败）。
	rest := strings.TrimPrefix(r.URL.Path, sanitizeRulesPath+"/blocked-reviews")
	rest = strings.TrimSuffix(rest, "/label")
	idStr := strings.Trim(rest, "/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid review ID")
		return
	}
	var req struct {
		Label   string `json:"label"`
		Comment string `json:"comment"`
	}
	if !decodeSanitizeRuleBody(w, r, &req) {
		return
	}
	if !sanitizerules.ValidBlockReviewLabel(req.Label) {
		writeError(w, http.StatusBadRequest,
			"label must be one of: "+strings.Join(sanitizerules.BlockReviewLabels(), ", "))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	review, err := sanitizerules.LabelBlockReview(ctx, id, req.Label, req.Comment, sanitizeRuleActor(auth))
	if err != nil {
		writeSanitizeRuleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

// handleBlockReviewInsights 返回按规则聚合的复核读数（规则优化的输入面）。
//
//	GET /api/admin/sanitize-rules/blocked-reviews/insights?tenant_id=…&limit=…
//
// ★ 误报率的分母是**有判断的记录数**（标注过的），不是总量 —— 用总量做分母会让
//
//	「没人看的记录」把误报率稀释成假绿。
func (h *Handler) handleBlockReviewInsights(w http.ResponseWriter, r *http.Request, auth *AuthContext) {
	super := IsSuperAdminOrLegacy(r)
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if !super {
		tenant = auth.TenantID
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be within 1..200")
			return
		}
		limit = parsed
	}
	insights, err := sanitizerules.BlockReviewInsights(tenant, limit)
	if err != nil {
		// ★ 根因要进日志（对外消息保持不变）：只写「failed to read …」时，生产上
		// 看不到「哪一列不存在 / 哪个连接池被关 / 权限不足」⇒ 定位只能靠猜。
		slog.Warn("sanitize block review insights read failed",
			"error", err, "root_cause", sanitizerules.StoreErrorDetail(err))
		writeError(w, http.StatusServiceUnavailable, "failed to read block review insights")
		return
	}
	threshold := sanitizerules.AutoRelaxThreshold()
	writeJSON(w, http.StatusOK, map[string]any{
		"insights": insights,
		// 自动优化的阈值随读数一起给出 ⇒ 运维看得懂「为什么这条规则还没被
		// 自动放行」（还差几条误报标注）。
		"auto_relax_threshold": threshold,
		"auto_relax_enabled":   threshold > 0,
		"tenant_id":            tenant,
	})
}
