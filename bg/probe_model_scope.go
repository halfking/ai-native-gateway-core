// Package bg — probe_model_scope.go
//
// Automatic probe model scope (2026-11-05):
//   - Featured models (routing_policy.featured ∪ usage Top-N cache), OR
//   - Models with real (non-probe) traffic visible in the session-family
//     HOT source — effectively the hot retention window (default 8h,
//     bg/partition_manager.go DefaultRetentionWindow). The 24h INV-3
//     constant (probeUsageWindowInterval) only upper-bounds the predicate;
//     the hot source is the binding constraint. ★ P-3（2026-11-06 审计）：
//     口径如实降级并在此注明——刻意不换双腿（SessionFamilyTurnsSourceSQL），
//     否则每次 scope 判定都变成跨分区扫描；daily_probe_audit 仍持有
//     request_logs_with_current_month 的真 24h 口径，两者用途不同不互相冒充。
//   - Everything else: manual probes only
//
// Credential/provider eligibility (automaticProbeEligibilityExistsSQL) stays
// orthogonal — this gate answers "is this MODEL allowed for automatic probe".
package bg

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/db"
)

// ErrProbeModelOutOfAutomaticScope marks automatic probes rejected because the
// model is neither featured nor recently used by real traffic.
var ErrProbeModelOutOfAutomaticScope = fmt.Errorf("automatic probe model out of scope")

// automaticProbeModelInScope reports whether an automatic probe may target
// rawModel. Manual tasks never call this.
//
// ★ 2026-11-05 审计修：失败**开放**（fail-open）—— DB 读错时按「在范围内」
// 放行，而不是拒绝。理由：这道门的**目的是**「别让恢复探测被卡住」
// （INV-3 收紧的是**常态**探测量，不是恢复路径）⇒ 若一次瞬时读抖动
// 就把恢复探测挡在门外，恢复探测反而被这道门废掉 ⇒ 与门的意图相反。
// ⇒ 因此读错时**放行 + 告警**，让调用方知道「本次判定不可信」；
// 不可信不等于「拒绝」。
//
// ★ 2026-11-05 审计修：原先的实现是 fail-**closed**
// （返回 err ⇒ 两个调用点都把它当「拒绝」⇒ 读错即拒绝），
// 与本注释宣称的 fail-open 相反 —— 注释与行为不一致，属「量具自伤」。
func (q *ProbeQueue) automaticProbeModelInScope(ctx context.Context, rawModel string) (bool, error) {
	if q == nil || q.db == nil {
		// 无 DB ⇒ 无法判定 ⇒ 按 fail-open 放行（仅记告警）。
		slog.Warn("probe model scope: database unavailable, treating model as in scope",
			"model", rawModel)
		return true, nil
	}
	model := strings.TrimSpace(rawModel)
	if model == "" {
		return false, nil
	}
	if globalIsFeaturedModel(model, "") {
		return true, nil
	}
	var ok bool
	err := q.db.QueryRow(ctx, automaticProbeRecentUsageExistsSQL(), model).Scan(&ok)
	if err != nil {
		// ★ fail-open：读失败 ⇒ 放行（恢复探测不能被一次读抖动卡死），
		// 但把错误原样返回给调用方，让它能落审计/告警。
		slog.Warn("probe model scope: usage check failed, treating model as in scope",
			"model", model, "error", err)
		return true, err
	}
	return ok, nil
}

// automaticProbeRecentUsageExistsSQL is model-level (not credential-pair):
// any real business traffic for the model name within the INV-3 window puts
// it in automatic scope. Probe traffic is excluded via the session-family
// predicate so probes cannot keep a model "in scope" forever.
// ★ P-3（2026-11-06 审计）：实际可见窗口 = 会话族热腿保留期（默认 8h），
// 不是名义上的 24h —— 见文件头注释；credential_selfcheck.go 同源查询
// 已按同一口径如实注释（「v1 侧本来就只看得到最近 8h」）。
//
// ★ 2026-11-05 审计修（真库实测 42P03）：原先比对的 `rl.model` **不是**
// 会话族投影里的列 —— 会话族投影只暴露 118 个 canonical 别名，其中
// 没有裸 `model`（它在基表叫 model，但投影把它 AS 成 outbound_model）。
// ⇒ 该 SQL 在真库上直接 `ERROR: column rl.model does not exist`
// ⇒ 每次判定都读失败 ⇒ 这道门会把**所有**自动探测挡掉。
// ⇒ 改用投影里**确实存在**的四个模型名列：
//   raw_model_name（会话族口径 = COALESCE(outbound_model, client_model)）、
//   outbound_model、client_model、canonical_model。
//
// ★ 这道门的 SQL 必须对着**真库**验过，不能只跑字符串断言 ——
// 本轮就是「字符串断言全绿、真库一跑就炸」。
func automaticProbeRecentUsageExistsSQL() string {
	excl := fmt.Sprintf(probeTrafficExclusionPredicateSession, "rl", "rl", "rl")
	return `
		SELECT EXISTS (
			SELECT 1
			FROM ` + db.SessionFamilyTurnsHotSourceSQL() + ` rl
			WHERE rl.ts >= now() - ` + probeUsageWindowInterval + `
			  AND ` + excl + `
			  AND (
				lower(COALESCE(rl.raw_model_name, '')) = lower($1)
				OR lower(COALESCE(rl.outbound_model, '')) = lower($1)
				OR lower(COALESCE(rl.client_model, '')) = lower($1)
				OR lower(COALESCE(rl.canonical_model, '')) = lower($1)
			  )
		)`
}
