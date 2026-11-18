package admin

// PI 错误分析「一键应用修复 / 回滚」（2026-11-18 PI 分析管理面优化轮）。
//
// ★ 背景：971 建出的 error_analysis_results 自带
//     auto_applied / applied_at / applied_by 三列，但全仓 Go 代码
//   **零写入方** ⇒ 分析器产出的 rule_adjustment（规则调整建议）
//   只能看、不能落地 ⇒「给出修正的建议。或者自动修正的操作」
//   这半条需求从未存在。
//
// ★ 安全设计（三条硬门，全部在后端强制）：
//
//	① **白名单动作**：LLM 的建议是自由形状 JSON，只允许归一化成三种
//	   保守动作 —— disable_rule / adjust_severity / update_pattern。
//	   LLM 建议里的任何其他键（改表、改策略、执行 shell 之类）
//	   一律忽略并在响应里如实列出「未识别字段」。
//	② **置信度门**：confidence_score >= 0.80 才允许动检测规则。
//	   低置信度的建议只能看（前端禁用按钮 + 展示原因）。
//	   LLM 解析失败落库的 confidence=0.00 天然过不了这道门。
//	③ **旧值快照 + 可回滚**：每条动作应用前把
//	   (enabled, severity, pattern) 旧值快照进 applied_actions，
//	   revert 按快照逐条恢复 ⇒ 改检测规则永远可撤销。
//
//	★ 附加门（实现层）：
//	  · 规则名必须真实存在（UPDATE 受影响行数 = 0 ⇒ 整个事务回滚），
//	    防 LLM 幻觉规则名；
//	  · update_pattern 的正则必须先在 Go 侧编译通过（RE2 语义 =
//	    运行时同一个引擎），非法正则拒绝；
//	  · severity clamp 到 1..10（表上 CHECK 约束同口径）；
//	  · 应用成功后触发规则热刷新（RefreshRules 接线），否则
//	    「改了库 ≠ 改了行为」（detector 内存里有规则缓存）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// piFixConfidenceFloor 是应用修复的置信度硬门。
// 0.80：LLM 正常分析通常 0.85+；解析失败落库 0.00。
const piFixConfidenceFloor = 0.80

// NullableJSON 是可空 JSONB 列的 Scan/Marshal 适配器。
//
// ★ 为什么需要它：Go 1.27 起 json.RawMessage = jsontext.Value，
//
//	**不能 Scan NULL**（"unsupported Scan, storing driver.Value type <nil>"）
//	而 error_analysis_queue/results 里的 JSONB 列（rule_adjustment /
//	matched_rules / error_details / …）在真实数据里**就是 NULL**
//	（LLM 没给建议 / 错误没带规则上下文时）⇒ 直接 Scan 会让详情端点
//	500 而不是返回「无此字段」的详情。原版 GetResult 在 252 真库的
//	rule_adjustment=NULL 行上就是打不开的（本轮实测复现）。
//	★ Marshal：NULL ⇒ JSON null（与 json.RawMessage 的输出形状一致，
//	前端只需判空）。
type NullableJSON []byte

// Scan implements sql.Scanner.
func (n *NullableJSON) Scan(src interface{}) error {
	if src == nil {
		*n = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		*n = append((*n)[:0], v...)
		return nil
	case string:
		*n = append((*n)[:0], v...)
		return nil
	}
	return fmt.Errorf("NullableJSON: unsupported scan source %T", src)
}

// Value implements driver.Valuer (NULL for empty).
func (n NullableJSON) Value() (driver.Value, error) {
	if len(n) == 0 {
		return nil, nil
	}
	return []byte(n), nil
}

// MarshalJSON outputs the stored JSON verbatim (or null).
func (n NullableJSON) MarshalJSON() ([]byte, error) {
	if len(n) == 0 {
		return []byte("null"), nil
	}
	return n, nil
}

// UnmarshalJSON accepts raw JSON (used when round-tripping through structs).
func (n *NullableJSON) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*n = nil
		return nil
	}
	*n = append((*n)[:0], b...)
	return nil
}

// piFixMaxPatternLen 限制 LLM 建议的 pattern 长度（防把超大正则写进库）。
const piFixMaxPatternLen = 4096

// PiNullString 是 sql.NullString 的 JSON 友好包装。
//
// ★ 为什么需要它：sql.NullString 直接 json.Marshal 输出
//
//	{"String":"","Valid":false} 对象 ⇒ 前端拿到对象而不是字符串/null
//	（详情页 session_id 显示 [object Object]）。NULL ⇒ JSON null。
type PiNullString struct {
	sql.NullString
}

// MarshalJSON outputs the string or null.
func (n PiNullString) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.String)
}

// RuleFixAction 是白名单里的单个可执行动作（归一化后的规范形状）。
type RuleFixAction struct {
	Action   string `json:"action"`             // disable_rule | adjust_severity | update_pattern
	RuleName string `json:"rule_name"`          // 目标规则（必须真实存在）
	Severity int    `json:"severity,omitempty"` // adjust_severity 用
	Pattern  string `json:"pattern,omitempty"`  // update_pattern 用
	Reason   string `json:"reason,omitempty"`   // 建议理由（来自 LLM，仅展示）
}

// AppliedActionRecord 是应用后留痕的单条记录（迁移 979 的列形状）。
type AppliedActionRecord struct {
	Action   string                 `json:"action"`
	RuleName string                 `json:"rule_name"`
	Old      map[string]interface{} `json:"old"`
	New      map[string]interface{} `json:"new"`
	Reason   string                 `json:"reason,omitempty"`
}

// piRuleFixActionNames 是白名单闭集（前端展示动作名用）。
var piRuleFixActionNames = []string{"disable_rule", "adjust_severity", "update_pattern"}

// normalizeActionWord 把 LLM 产出的动作词归一到规范名（未识别返回空串）。
func normalizeActionWord(word string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	switch w {
	case "disable_rule", "disable", "disabled":
		return "disable_rule"
	case "adjust_severity", "severity", "set_severity", "lower_severity", "reduce_severity":
		return "adjust_severity"
	case "update_pattern", "pattern", "modify_pattern", "rewrite_pattern":
		return "update_pattern"
	}
	return ""
}

// NormalizeRuleAdjustment 把 LLM 产出的 rule_adjustment（自由形状 JSON）归一化成白名单动作列表。
//
// ★ 为什么需要归一化层：analyzer 的提示词只约定了「JSON 对象」，
//
//	没有锁定字段名 ⇒ 真实产出见过至少四种形状（单对象 / 顶层数组 /
//	rules 数组 / disable 列表）。直接挑一种形状解析会把其余形状
//	全部当「无建议」丢掉。归一化层识别常见形状，未识别的键**忽略但计数**。
//
// 返回：(动作列表, 未识别形状说明)。空列表 = 无可执行动作。
func NormalizeRuleAdjustment(raw json.RawMessage) ([]RuleFixAction, string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ""
	}

	var top interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Sprintf("rule_adjustment is not valid JSON: %v", err)
	}

	var actions []RuleFixAction
	unrecognized := 0
	addFromMap := func(m map[string]interface{}) {
		a, ok := piNormalizeOneAction(m)
		if !ok {
			unrecognized++
			return
		}
		actions = append(actions, a)
	}

	switch v := top.(type) {
	case map[string]interface{}:
		// 形状 1：规范单对象 {"action": "...", "rule_name": "...", ...}
		if _, hasAction := v["action"]; hasAction {
			addFromMap(v)
			break
		}
		// 形状 2：{"disable": ["rule_a", "rule_b"], "reason": "..."}
		if disables, ok := v["disable"].([]interface{}); ok {
			for _, d := range disables {
				if name, ok := d.(string); ok && strings.TrimSpace(name) != "" {
					actions = append(actions, RuleFixAction{
						Action:   "disable_rule",
						RuleName: strings.TrimSpace(name),
					})
				}
			}
		}
		// 形状 3：{"rules": [{...}, ...]}
		if rules, ok := v["rules"].([]interface{}); ok {
			for _, rr := range rules {
				if m, ok := rr.(map[string]interface{}); ok {
					addFromMap(m)
				}
			}
		}
		// 既没有 action 也没有 disable/rules ⇒ 整体未识别。
		if len(actions) == 0 && unrecognized == 0 {
			unrecognized++
		}
	case []interface{}:
		// 形状 4：顶层数组 [{"rule_name": "...", "action": "..."}, ...]
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				addFromMap(m)
			}
		}
	default:
		unrecognized++
	}

	note := ""
	if unrecognized > 0 {
		note = fmt.Sprintf("%d unrecognized rule_adjustment item(s) ignored", unrecognized)
	}
	return actions, note
}

// piNormalizeOneAction 归一化单个动作对象（map 形状）。
func piNormalizeOneAction(m map[string]interface{}) (RuleFixAction, bool) {
	getString := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						return s
					}
				}
			}
		}
		return ""
	}
	getInt := func(keys ...string) (int, bool) {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				switch n := v.(type) {
				case float64:
					return int(n), true
				case string:
					if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
						return i, true
					}
				}
			}
		}
		return 0, false
	}

	ruleName := getString("rule_name", "rule", "name", "ruleName")
	if ruleName == "" {
		return RuleFixAction{}, false
	}
	action := normalizeActionWord(getString("action", "type", "operation"))
	if action == "" {
		return RuleFixAction{}, false
	}

	out := RuleFixAction{
		Action:   action,
		RuleName: ruleName,
		Reason:   getString("reason", "rationale", "why"),
	}
	switch action {
	case "adjust_severity":
		sev, ok := getInt("severity", "new_severity", "severity_level", "value")
		if !ok {
			return RuleFixAction{}, false
		}
		if sev < 1 {
			sev = 1
		}
		if sev > 10 {
			sev = 10
		}
		out.Severity = sev
	case "update_pattern":
		pat := getString("pattern", "new_pattern", "value", "regex")
		if pat == "" || len(pat) > piFixMaxPatternLen {
			return RuleFixAction{}, false
		}
		// ★ 正则必须先编译通过（RE2 = 运行时同一引擎）。
		if _, err := regexp.Compile(pat); err != nil {
			return RuleFixAction{}, false
		}
		out.Pattern = pat
	}
	return out, true
}

// piFixResp 是 apply/revert 的响应形状。
type piFixResp struct {
	QueueID  int64                 `json:"queue_id"`
	Applied  []AppliedActionRecord `json:"applied_actions"`
	Note     string                `json:"note,omitempty"`
	Reverted bool                  `json:"reverted,omitempty"`
}

// ApplyFix 应用某条分析结果的修复建议。
//
//	POST /api/admin/pi-analysis/results/{queue_id}/apply
func (h *PIAnalysisHandler) ApplyFix(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	// ── 1. 读分析结果（含建议与门控材料）────────────────────────────
	// ★ 可空 JSONB 列用 []byte 中转：json.RawMessage（jsontext.Value）
	//   不能 Scan NULL，而 rule_adjustment 在真实数据里就是 NULL
	//   （LLM 没给建议时）——直接 Scan 会让门控路径 500 而不是 422。
	var (
		ruleAdjustment []byte
		confidence     float64
		isFalsePos     bool
		alreadyApplied bool
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT rule_adjustment, confidence_score, is_false_positive, auto_applied
		FROM error_analysis_results WHERE queue_id = $1`, queueID,
	).Scan(&ruleAdjustment, &confidence, &isFalsePos, &alreadyApplied)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "analysis result not found (queue item may not be analyzed yet)", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalTextErr(w, "query analysis result failed", err)
		return
	}
	if alreadyApplied {
		http.Error(w, "fix already applied (revert first to re-apply)", http.StatusConflict)
		return
	}

	// ── 2. 门控：建议非空 + 置信度达标 ─────────────────────────────
	actions, note := NormalizeRuleAdjustment(ruleAdjustment)
	if len(actions) == 0 {
		reason := "rule_adjustment contains no executable whitelisted action"
		if note != "" {
			reason = note
		}
		if len(ruleAdjustment) == 0 || string(ruleAdjustment) == "null" {
			reason = "rule_adjustment is empty for this result"
		}
		http.Error(w, reason, http.StatusUnprocessableEntity)
		return
	}
	if confidence < piFixConfidenceFloor {
		http.Error(w, fmt.Sprintf(
			"confidence %.2f below floor %.2f: suggestion is view-only",
			confidence, piFixConfidenceFloor), http.StatusUnprocessableEntity)
		return
	}

	// ── 3. 事务：逐条应用 + 旧值快照 ───────────────────────────────
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalTextErr(w, "begin apply transaction failed", err)
		return
	}
	defer tx.Rollback()

	records, err := piApplyActions(r.Context(), tx, actions)
	if err != nil {
		// ★ D13（2026-11-19 审计轮）：区分业务性失败与驱动失败。
		//
		//	· 业务性失败（规则不存在 / 行数=0 / 动作未在白名单）⇒ 422 +
		//	  可读文案，前端能展示原因、能自愈；
		//	· 驱动失败（pgx 错误：表名/列名/约束名）⇒ 500 + 固定文案，
		//	  内部错误只进服务端日志，**不得**回显给客户端。
		var bizErr *piApplyBusinessError
		if errors.As(err, &bizErr) {
			http.Error(w, bizErr.msg, http.StatusUnprocessableEntity)
			return
		}
		writeInternalTextErr(w, "apply rule actions failed", err)
		return
	}

	appliedBy := piAppliedBy(r)
	recordsJSON, err := json.Marshal(records)
	if err != nil {
		writeInternalTextErr(w, "marshal applied records failed", err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `
		UPDATE error_analysis_results
		SET auto_applied = TRUE, applied_at = NOW(), applied_by = $1, applied_actions = $2
		WHERE queue_id = $3 AND auto_applied = FALSE`, appliedBy, recordsJSON, queueID)
	if err != nil {
		writeInternalTextErr(w, "mark applied failed", err)
		return
	}
	// 并发竞态双保险：两个管理员同时点「应用」⇒ 只有一个成功。
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "fix already applied by a concurrent request", http.StatusConflict)
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternalTextErr(w, "commit apply transaction failed", err)
		return
	}

	// ── 4. 规则热刷新（改了库 ≠ 改了行为）─────────────────────────
	h.refreshRules("apply")

	slog.Info("pi_analysis: fix applied",
		"queue_id", queueID, "actions", len(records), "applied_by", appliedBy,
		"is_false_positive", isFalsePos)
	for _, rec := range records {
		slog.Info("pi_analysis: rule fix",
			"action", rec.Action, "rule", rec.RuleName, "old", rec.Old, "new", rec.New)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(piFixResp{QueueID: queueID, Applied: records, Note: note})
}

// RevertFix 回滚已应用的修复（按 applied_actions 里的旧值快照逐条恢复）。
//
//	POST /api/admin/pi-analysis/results/{queue_id}/revert
func (h *PIAnalysisHandler) RevertFix(w http.ResponseWriter, r *http.Request, queueID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.db == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}

	var (
		alreadyApplied bool
		// ★ 同 ApplyFix：applied_actions 可空（未应用过 ⇒ NULL）。
		actionsRaw []byte
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT auto_applied, applied_actions FROM error_analysis_results WHERE queue_id = $1`, queueID,
	).Scan(&alreadyApplied, &actionsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "analysis result not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternalTextErr(w, "query applied actions failed", err)
		return
	}
	if !alreadyApplied {
		http.Error(w, "fix was not applied; nothing to revert", http.StatusConflict)
		return
	}

	var records []AppliedActionRecord
	if len(actionsRaw) > 0 && string(actionsRaw) != "null" {
		if err := json.Unmarshal(actionsRaw, &records); err != nil {
			writeInternalTextErr(w, "unmarshal applied actions failed", err)
			return
		}
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalTextErr(w, "begin revert transaction failed", err)
		return
	}
	defer tx.Rollback()

	// 按旧值快照恢复：只回写快照里有的键 ⇒ apply 时没动过的键不会被误改。
	for _, rec := range records {
		old := rec.Old
		setClauses := []string{"updated_at = NOW()"}
		args := []interface{}{}
		pos := 1
		addSet := func(col string, val interface{}) {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		if v, ok := old["enabled"].(bool); ok {
			addSet("enabled", v)
		}
		if v, ok := old["severity"].(float64); ok {
			addSet("severity", int(v))
		}
		if v, ok := old["pattern"].(string); ok {
			addSet("pattern", v)
		}
		if len(setClauses) == 1 {
			continue
		}

		q := fmt.Sprintf("UPDATE prompt_injection_rules SET %s WHERE rule_name = $%d",
			strings.Join(setClauses, ", "), pos)
		args = append(args, rec.RuleName)
		if _, err := tx.ExecContext(r.Context(), q, args...); err != nil {
			writeInternalTextErr(w, fmt.Sprintf("revert rule %q failed", rec.RuleName), err)
			return
		}
	}

	_, err = tx.ExecContext(r.Context(), `
		UPDATE error_analysis_results
		SET auto_applied = FALSE, applied_at = NULL, applied_by = NULL
		WHERE queue_id = $1`, queueID)
	if err != nil {
		writeInternalTextErr(w, "clear applied marks failed", err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeInternalTextErr(w, "commit revert transaction failed", err)
		return
	}

	h.refreshRules("revert")

	slog.Info("pi_analysis: fix reverted", "queue_id", queueID,
		"records", len(records), "by", piAppliedBy(r))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(piFixResp{QueueID: queueID, Reverted: true})
}

// piApplyBusinessError 标记**业务性失败**（可安全回显给客户端）。
//
// ★ D13（2026-11-19 审计轮）：apply 的错误路径原本把 piApplyActions 的
// 返回值原样 `http.Error(w, err.Error(), 422)` 回显。而 piApplyActions 的
// 错误里**混着驱动失败**（`%w` 包了 pgx 错误 ⇒ 表名/列名/约束名会到达
// 客户端）。
//
// 现在两类错误分流：
//
//	· 业务性失败（本类型）⇒ 422 + 可读文案（客户端能据此自愈）；
//	· 驱动失败 ⇒ 走 writeInternalTextErr（500 + 固定文案 + 服务端日志）。
type piApplyBusinessError struct{ msg string }

func (e *piApplyBusinessError) Error() string { return e.msg }

// piApplyActions 在事务里逐条应用白名单动作，返回留痕记录。
// 业务性失败（规则不存在）返回 error ⇒ 调用方回滚整个事务。
func piApplyActions(ctx context.Context, tx *sql.Tx, actions []RuleFixAction) ([]AppliedActionRecord, error) {
	records := make([]AppliedActionRecord, 0, len(actions))
	for _, act := range actions {
		// 旧值快照（同时验证规则存在）。
		var oldEnabled bool
		var oldSeverity int
		var oldPattern string
		err := tx.QueryRowContext(ctx, `
			SELECT enabled, severity, pattern FROM prompt_injection_rules
			WHERE rule_name = $1 FOR UPDATE`, act.RuleName,
		).Scan(&oldEnabled, &oldSeverity, &oldPattern)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &piApplyBusinessError{msg: fmt.Sprintf("rule %q does not exist (LLM suggestion references unknown rule)", act.RuleName)}
		}
		if err != nil {
			return nil, fmt.Errorf("query rule %q failed: %w", act.RuleName, err)
		}

		rec := AppliedActionRecord{
			Action:   act.Action,
			RuleName: act.RuleName,
			Old: map[string]interface{}{
				"enabled":  oldEnabled,
				"severity": oldSeverity,
				"pattern":  oldPattern,
			},
			Reason: act.Reason,
		}

		switch act.Action {
		case "disable_rule":
			res, err := tx.ExecContext(ctx, `
				UPDATE prompt_injection_rules SET enabled = FALSE, updated_at = NOW()
				WHERE rule_name = $1`, act.RuleName)
			if err != nil {
				return nil, fmt.Errorf("disable rule %q failed: %w", act.RuleName, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil, &piApplyBusinessError{msg: fmt.Sprintf("rule %q not found when applying disable", act.RuleName)}
			}
			rec.New = map[string]interface{}{"enabled": false}

		case "adjust_severity":
			res, err := tx.ExecContext(ctx, `
				UPDATE prompt_injection_rules SET severity = $1, updated_at = NOW()
				WHERE rule_name = $2`, act.Severity, act.RuleName)
			if err != nil {
				return nil, fmt.Errorf("adjust severity of %q failed: %w", act.RuleName, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil, &piApplyBusinessError{msg: fmt.Sprintf("rule %q not found when adjusting severity", act.RuleName)}
			}
			rec.New = map[string]interface{}{"severity": act.Severity}

		case "update_pattern":
			res, err := tx.ExecContext(ctx, `
				UPDATE prompt_injection_rules SET pattern = $1, updated_at = NOW()
				WHERE rule_name = $2`, act.Pattern, act.RuleName)
			if err != nil {
				return nil, fmt.Errorf("update pattern of %q failed: %w", act.RuleName, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil, &piApplyBusinessError{msg: fmt.Sprintf("rule %q not found when updating pattern", act.RuleName)}
			}
			rec.New = map[string]interface{}{"pattern": act.Pattern}

		default:
			return nil, &piApplyBusinessError{msg: fmt.Sprintf("action %q is not whitelisted", act.Action)}
		}
		records = append(records, rec)
	}
	return records, nil
}

// piAppliedBy 从鉴权上下文取应用者标识（无身份时 'manual'）。
func piAppliedBy(r *http.Request) string {
	if ac := GetAuthContext(r); ac != nil && strings.TrimSpace(ac.Username) != "" {
		return "manual:" + strings.TrimSpace(ac.Username)
	}
	return "manual"
}

// refreshRules 触发规则热刷新（改库后让在役 detector 立即生效）。
func (h *PIAnalysisHandler) refreshRules(cause string) {
	if h == nil || h.ruleRefresher == nil {
		return
	}
	if err := h.ruleRefresher(); err != nil {
		// 规则已提交进库；刷新失败只 WARN（重启后必然加载新规则）。
		slog.Warn("pi_analysis: rule refresh failed after fix", "cause", cause, "error", err)
		return
	}
	slog.Info("pi_analysis: rules refreshed", "cause", cause)
}
