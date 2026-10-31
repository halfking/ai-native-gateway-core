package admin

import (
	"net/http"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1039（D4 收尾，2026-10-20）：egress 放行路径的**管理面写入点**。
//
// ## 为什么需要这一层
//
// owner 口径：「在被阻断时，需要给客户端提示，**由用户来选择是否执行**。」
//
// 客户端提示那一半已经落地（domains/streaming 的 writeOutputPolicyBlockedJSON
// 带上 rule/reason/field），「由用户选择」那一半需要一个**能被用户操作**的入口：
// 运维/管理员在管理面看到「某租户被 X 规则拦了」，于是对该租户在该规则下放行。
//
// 放行表本体（domains/sanitizerules/egress_release.go）只有**程序内**的
// AddEgressRelease / RemoveEgressRelease，没有任何 HTTP 入口 ⇒ 运维够不着。
// 本文件补上这个入口。
//
// ## 安全边界（不要在这条路上走样）
//
//   - 放行决定**只能由服务端持有**：客户端自带的头/参数**不得**影响它。
//     这正是 hop-header 豁免翻车过的形态（见 security/sanitize/hop.go
//     与 AGENTS.md 的「hop-header 豁免教训」）。
//   - 租户必须**精确**给��：空租户 = 全网放行 ⇒ 直接 400。
//   - 规则**精确匹配**：不接受通配/前缀族。
//   - 只有 **super_admin** 能写放行表：租户管理员只能读自己租户的那几条。
//     （与本文件既有的角色口径一致：tenant_admin 只放行 events 读+复核。）
//
// ## 持久化（2026-10-29 更新）
//
// 放行表已落库（迁移 917 的 sanitize_egress_releases）：授予/撤销写穿透
// （进程内 + DB），启动时由 InstallReviewPersistence 装载——跨重启存活。
// source=review 的行由审核批准产生（带 7 天过期），manual 的行由本端点产生。

// handleEgressReleases 处理放行表的读/写。
//
//	GET    /api/admin/sanitize-rules/egress-releases[?tenant_id=…]
//	POST   /api/admin/sanitize-rules/egress-releases        （body = EgressRelease）
//	DELETE /api/admin/sanitize-rules/egress-releases?tenant_id=…&scope=…&rule=…
func (h *Handler) handleEgressReleases(w http.ResponseWriter, r *http.Request, auth *AuthContext) {
	super := IsSuperAdminOrLegacy(r)
	switch r.Method {
	case http.MethodGet:
		releases := sanitizerules.EgressReleases()
		tenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
		if tenant != "" {
			filtered := make([]sanitizerules.EgressRelease, 0, len(releases))
			for _, rel := range releases {
				if rel.TenantID == tenant {
					filtered = append(filtered, rel)
				}
			}
			releases = filtered
		} else if !super {
			// 非超管不给看全局表：租户管理员只看自己租户。
			tenant = auth.TenantID
			filtered := make([]sanitizerules.EgressRelease, 0, len(releases))
			for _, rel := range releases {
				if tenant != "" && rel.TenantID == tenant {
					filtered = append(filtered, rel)
				}
			}
			releases = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"releases": releases})
	case http.MethodPost:
		if !super {
			writeError(w, http.StatusForbidden, "only super_admin can grant an egress release")
			return
		}
		var release sanitizerules.EgressRelease
		if !decodeSanitizeRuleBody(w, r, &release) {
			return
		}
		if release.ApprovedBy == "" {
			release.ApprovedBy = sanitizeRuleActor(auth)
		}
		if err := sanitizerules.ValidateEgressRelease(release); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if release.Source == "" {
			release.Source = "manual"
		}
		// 2026-10-29 持久化轮：放行写穿透（进程内 + DB）。管理员手动授予默认
		// 不过期；要给时限就在 body 里带 expires_at。
		if err := sanitizerules.AddEgressReleasePersistent(r.Context(), release); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"message":     "egress release granted",
			"tenant_id":   release.TenantID,
			"scope":       release.Scope,
			"rule":        release.Rule,
			"approved_by": release.ApprovedBy,
			"expires_at":  release.ExpiresAt,
			"source":      release.Source,
		})
	case http.MethodDelete:
		if !super {
			writeError(w, http.StatusForbidden, "only super_admin can revoke an egress release")
			return
		}
		q := r.URL.Query()
		tenant := strings.TrimSpace(q.Get("tenant_id"))
		scope := strings.TrimSpace(q.Get("scope"))
		rule := strings.TrimSpace(q.Get("rule"))
		digest := strings.TrimSpace(q.Get("payload_digest"))
		if tenant == "" || scope == "" || rule == "" {
			writeError(w, http.StatusBadRequest, "tenant_id, scope and rule are required")
			return
		}
		// 2026-10-29 持久化轮：撤销写穿透（进程内 + DB；digest 为空删该键下
		// 全部摘要粒度的行）。
		removed := sanitizerules.RemoveEgressReleasePersistent(r.Context(), tenant, scope, rule, digest)
		if !removed {
			writeError(w, http.StatusNotFound, "no such egress release")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "egress release revoked"})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
