package errorplaybook

// 动作注册表：方案步骤只能调用这里的**闭集**原语。
//
// ★ 为什么是白名单而不是解释器：LLM 建议是攻击者/幻觉可影响的输入，
//
//	「执行一段动态脚本」绝不等于「执行任意代码」。任意 shell / 任意 SQL
//	被结构性排除——方案能做的只有注册表里的这几个保守动作，且每个有
//	副作用的动作都必须先产出旧值快照（可回滚）。
//
// ★ 闭集（v1）：
//
//	record_note      只读：把一条说明写进执行留痕（诊断/指引类方案的落地形态）。
//	set_settings_kv  变更：写 settings_kv 的一个键（**代码级 allowlist**，只允许
//	                  安全三开关族；写前快照旧值，可回滚）。
//	disable_pi_rule  变更：禁用一条 PI 检测规则（规则必须真实存在；写前快照
//	                  enabled/severity/pattern，可回滚——与 979 applied_actions
//	                  同纪律）。
//	call_admin_api   变更/只读：调用登记在册的内部管理端点（闭集路由表 +
//	                  装配侧注入的扩展；未登记的路径 fail-closed）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 动作名闭集（plan.go 的 isMutatingAction 与这里对表）。
const (
	ActionRecordNote    = "record_note"
	ActionSetSettingsKV = "set_settings_kv"
	ActionDisablePIRule = "disable_pi_rule"
	ActionCallAdminAPI  = "call_admin_api"
)

// settingsKVAllowlist 是 set_settings_kv 允许写入的键（代码级闭集）。
//
//	★ 全部是仓内 settings specs 里真实存在的键（settings/spec_modules.go），
//	且都是**安全/合规开关族**——运维在真实事故里手工拨过的那几个闸门
//	（2026-11-15 anomaly 自锁、2026-11-18 security 误拦、输出合规总开关）。
//	新增键必须同时满足：① spec 在场；② 语义是「事故止血开关」；
//	③ owner 拍板。不在表里的键一律拒绝。
var settingsKVAllowlist = map[string]bool{
	"security.enabled":              true,
	"security.mode":                 true,
	"sanitize.output_check_enabled": true,
}

// AdminRoute 是 call_admin_api 允许调用的内部端点（闭集路由表）。
type AdminRoute struct {
	Method string
	Path   string
	// Handler 执行该端点对应的内部动作；body 是请求体 JSON（可为 nil）。
	// 返回 (HTTP 语义状态码, 响应摘要, 错误)。
	Handler func(ctx context.Context, body json.RawMessage) (int, string, error)
}

// ActionRegistry 是方案步骤的执行面。
type ActionRegistry interface {
	// Has 报告动作名是否在白名单里。
	Has(action string) bool
	// Names 返回白名单闭集（错误提示与文档用）。
	Names() []string
	// Execute 执行一个动作。返回旧值快照（可回滚；nil = 本动作不可回滚）
	// 与结果摘要。
	Execute(ctx context.Context, action string, params map[string]interface{}, ev *Event) (snapshot json.RawMessage, result string, err error)
	// Revert 按快照回滚一次执行。
	Revert(ctx context.Context, action string, params map[string]interface{}, snapshot json.RawMessage) (result string, err error)
}

// BuiltinActions 是基于 DB 的内置注册表。
type BuiltinActions struct {
	db *sql.DB
	// extraRoutes 是装配侧注入的 call_admin_api 扩展路由（闭集的一部分）。
	extraRoutes map[string]AdminRoute
}

// NewBuiltinActions 创建注册表。db 可为 nil（此时变更类动作会如实失败）。
func NewBuiltinActions(db *sql.DB, extraRoutes []AdminRoute) *BuiltinActions {
	b := &BuiltinActions{db: db, extraRoutes: map[string]AdminRoute{}}
	for _, r := range extraRoutes {
		key := routeKey(r.Method, r.Path)
		if _, dup := b.extraRoutes[key]; dup {
			continue
		}
		b.extraRoutes[key] = r
	}
	return b
}

// builtinRoutes 是 call_admin_api 的内置路由（闭集）。
func (b *BuiltinActions) builtinRoutes() []AdminRoute {
	return []AdminRoute{
		{
			Method: "POST",
			Path:   "/api/admin/pi-analysis/trigger",
			Handler: func(ctx context.Context, body json.RawMessage) (int, string, error) {
				// 触发一轮 PI 错误分析（批大小默认 10）。真正的调用方由装配侧
				// 通过 extraRoutes 注入带 analyzer 的实现；这里是兜底占位——
				// 没有注入 ⇒ 如实失败（不假装成功）。
				return 0, "", fmt.Errorf("pi-analysis trigger route is not wired (no analyzer injected)")
			},
		},
	}
}

func routeKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimRight(strings.TrimSpace(path), "/")
}

// Has implements ActionRegistry.
func (b *BuiltinActions) Has(action string) bool {
	switch action {
	case ActionRecordNote, ActionSetSettingsKV, ActionDisablePIRule, ActionCallAdminAPI:
		return true
	}
	return false
}

// Names implements ActionRegistry.
func (b *BuiltinActions) Names() []string {
	return []string{ActionRecordNote, ActionSetSettingsKV, ActionDisablePIRule, ActionCallAdminAPI}
}

// Execute implements ActionRegistry.
func (b *BuiltinActions) Execute(ctx context.Context, action string, params map[string]interface{}, ev *Event) (json.RawMessage, string, error) {
	switch action {
	case ActionRecordNote:
		msg := strings.TrimSpace(strFrom(params, "message", "note", "text"))
		if msg == "" {
			return nil, "", &ErrPlanInvalid{msg: "record_note requires params.message"}
		}
		return nil, msg, nil

	case ActionSetSettingsKV:
		return b.executeSetSettingsKV(ctx, params)

	case ActionDisablePIRule:
		return b.executeDisablePIRule(ctx, params)

	case ActionCallAdminAPI:
		return b.executeCallAdminAPI(ctx, params)

	default:
		return nil, "", &ErrPlanInvalid{msg: fmt.Sprintf("action %q is not whitelisted (allowed: %s)", action, strings.Join(b.Names(), ", "))}
	}
}

// executeSetSettingsKV 写一个 allowlist 内的 settings_kv 键（写前快照旧值）。
func (b *BuiltinActions) executeSetSettingsKV(ctx context.Context, params map[string]interface{}) (json.RawMessage, string, error) {
	key := strings.TrimSpace(strFrom(params, "key"))
	if key == "" {
		return nil, "", &ErrPlanInvalid{msg: "set_settings_kv requires params.key"}
	}
	if !settingsKVAllowlist[key] {
		return nil, "", &ErrPlanInvalid{msg: fmt.Sprintf(
			"set_settings_kv: key %q is not in the safety allowlist %v", key, allowlistKeys())}
	}
	value, err := normalizeSettingsValue(params)
	if err != nil {
		return nil, "", err
	}
	if b.db == nil {
		return nil, "", fmt.Errorf("set_settings_kv: database not configured")
	}

	// 旧值快照（写前取）。
	var (
		prevValue  jsonb
		prevType   string
		hadPreview bool
	)
	err = b.db.QueryRowContext(ctx,
		`SELECT value, value_type FROM settings_kv WHERE key = $1`, key,
	).Scan(&prevValue, &prevType)
	switch {
	case err == sql.ErrNoRows:
		// 无旧行 ⇒ 快照记录「无」⇒ 回滚=删除该行。
		hadPreview = false
	case err != nil:
		return nil, "", fmt.Errorf("set_settings_kv: query prev value failed: %w", err)
	default:
		hadPreview = true
	}

	valueType := strings.TrimSpace(strFrom(params, "value_type"))
	if valueType == "" {
		valueType = inferValueType(value)
	}

	res, err := b.db.ExecContext(ctx, `
		INSERT INTO settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
		VALUES ($1, $2::jsonb, $3, 'platform', 'general', NOW(), 'error_playbook')
		ON CONFLICT (key) DO UPDATE SET
			value = EXCLUDED.value, value_type = EXCLUDED.value_type,
			prev_value = settings_kv.value, prev_updated_at = settings_kv.updated_at,
			updated_at = NOW(), updated_by = 'error_playbook'`,
		key, value, valueType)
	if err != nil {
		return nil, "", fmt.Errorf("set_settings_kv: upsert failed: %w", err)
	}
	_ = res

	snapshot, _ := json.Marshal(map[string]interface{}{
		"key":        key,
		"had_value":  hadPreview,
		"prev_value": json.RawMessage(prevValue),
		"prev_type":  prevType,
	})
	return snapshot, fmt.Sprintf("settings_kv[%s] set to %s (%s)", key, string(value), valueType), nil
}

// normalizeSettingsValue 把 params.value 归一化成 JSONB 值文本。
func normalizeSettingsValue(params map[string]interface{}) (string, error) {
	v, ok := params["value"]
	if !ok {
		return "", &ErrPlanInvalid{msg: "set_settings_kv requires params.value"}
	}
	switch tv := v.(type) {
	case bool:
		if tv {
			return "true", nil
		}
		return "false", nil
	case string:
		s := strings.TrimSpace(tv)
		if s == "" {
			return "", &ErrPlanInvalid{msg: "set_settings_kv: params.value is empty"}
		}
		// 字符串值按 JSON 字符串序列化。
		b, err := json.Marshal(s)
		if err != nil {
			return "", &ErrPlanInvalid{msg: "set_settings_kv: value is not encodable"}
		}
		return string(b), nil
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%v", tv)), nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", &ErrPlanInvalid{msg: "set_settings_kv: value is not JSON-encodable"}
		}
		return string(b), nil
	}
}

func inferValueType(value string) string {
	switch value {
	case "true", "false":
		return "bool"
	}
	return "json"
}

func allowlistKeys() []string {
	out := make([]string, 0, len(settingsKVAllowlist))
	for k := range settingsKVAllowlist {
		out = append(out, k)
	}
	return out
}

// executeDisablePIRule 禁用一条 PI 检测规则（快照 + 可回滚；规则必须存在）。
func (b *BuiltinActions) executeDisablePIRule(ctx context.Context, params map[string]interface{}) (json.RawMessage, string, error) {
	ruleName := strings.TrimSpace(strFrom(params, "rule_name", "rule"))
	if ruleName == "" {
		return nil, "", &ErrPlanInvalid{msg: "disable_pi_rule requires params.rule_name"}
	}
	if len(ruleName) > 256 {
		return nil, "", &ErrPlanInvalid{msg: "disable_pi_rule: rule_name too long"}
	}
	if b.db == nil {
		return nil, "", fmt.Errorf("disable_pi_rule: database not configured")
	}

	var (
		enabled  bool
		severity int
		pattern  string
	)
	err := b.db.QueryRowContext(ctx, `
		SELECT enabled, severity, pattern FROM prompt_injection_rules
		WHERE rule_name = $1`, ruleName,
	).Scan(&enabled, &severity, &pattern)
	if err == sql.ErrNoRows {
		// ★ 与 ApplyFix 同纪律：幻觉规则名 ⇒ 业务性失败（422），不是 500。
		return nil, "", &ErrPlanInvalid{msg: fmt.Sprintf("rule %q does not exist (LLM suggestion references unknown rule)", ruleName)}
	}
	if err != nil {
		return nil, "", fmt.Errorf("disable_pi_rule: query rule failed: %w", err)
	}

	snapshot, _ := json.Marshal(map[string]interface{}{
		"rule_name": ruleName,
		"enabled":   enabled,
		"severity":  severity,
		"pattern":   pattern,
	})
	if !enabled {
		return snapshot, fmt.Sprintf("rule %q already disabled (no-op)", ruleName), nil
	}
	res, err := b.db.ExecContext(ctx,
		`UPDATE prompt_injection_rules SET enabled = FALSE, updated_at = NOW() WHERE rule_name = $1`,
		ruleName)
	if err != nil {
		return nil, "", fmt.Errorf("disable_pi_rule: update failed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, "", fmt.Errorf("disable_pi_rule: rule %q not found when updating", ruleName)
	}
	return snapshot, fmt.Sprintf("rule %q disabled (was enabled, severity %d)", ruleName, severity), nil
}

// executeCallAdminAPI 调用登记在册的内部管理端点。
func (b *BuiltinActions) executeCallAdminAPI(ctx context.Context, params map[string]interface{}) (json.RawMessage, string, error) {
	method := strings.ToUpper(strings.TrimSpace(strFrom(params, "method")))
	if method == "" {
		method = "GET"
	}
	path := strings.TrimSpace(strFrom(params, "path", "url"))
	if path == "" {
		return nil, "", &ErrPlanInvalid{msg: "call_admin_api requires params.path"}
	}
	key := routeKey(method, path)

	var body json.RawMessage
	if raw, ok := params["body"]; ok && raw != nil {
		switch bv := raw.(type) {
		case json.RawMessage:
			body = bv
		case map[string]interface{}:
			enc, err := json.Marshal(bv)
			if err != nil {
				return nil, "", &ErrPlanInvalid{msg: "call_admin_api body is not encodable"}
			}
			body = enc
		case string:
			body = json.RawMessage(bv)
		}
	}

	route, ok := b.extraRoutes[key]
	if !ok {
		for _, r := range b.builtinRoutes() {
			if routeKey(r.Method, r.Path) == key {
				route = r
				ok = true
				break
			}
		}
	}
	if !ok || route.Handler == nil {
		known := make([]string, 0, len(b.extraRoutes)+1)
		for k := range b.extraRoutes {
			known = append(known, k)
		}
		known = append(known, routeKey("POST", "/api/admin/pi-analysis/trigger"))
		return nil, "", &ErrPlanInvalid{msg: fmt.Sprintf(
			"call_admin_api: route %q is not registered (known routes: %s)", key, strings.Join(known, ", "))}
	}

	// 超时闸（params.timeout_ms，默认 10s，封顶 60s）。
	timeout := 10 * time.Second
	if v, ok := params["timeout_ms"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Millisecond
		if timeout > 60*time.Second {
			timeout = 60 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	status, respSummary, err := route.Handler(ctx, body)
	if err != nil {
		return nil, "", fmt.Errorf("call_admin_api %s failed: %w", key, err)
	}
	if status == 0 {
		status = 200
	}
	return nil, fmt.Sprintf("%s -> %d: %s", key, status, truncateRunes(respSummary, 200)), nil
}

// Revert implements ActionRegistry：按快照逐动作回滚。
func (b *BuiltinActions) Revert(ctx context.Context, action string, params map[string]interface{}, snapshot json.RawMessage) (string, error) {
	if len(snapshot) == 0 || string(snapshot) == "null" {
		return "", fmt.Errorf("action %q has no snapshot to revert", action)
	}
	var snap map[string]interface{}
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		return "", fmt.Errorf("revert: snapshot is not valid JSON: %w", err)
	}

	switch action {
	case ActionSetSettingsKV:
		key := strings.TrimSpace(strFrom(snap, "key"))
		if key == "" {
			return "", fmt.Errorf("revert set_settings_kv: snapshot missing key")
		}
		if !settingsKVAllowlist[key] {
			return "", fmt.Errorf("revert set_settings_kv: key %q is not in the safety allowlist", key)
		}
		if b.db == nil {
			return "", fmt.Errorf("revert set_settings_kv: database not configured")
		}
		if had, _ := snap["had_value"].(bool); !had {
			res, err := b.db.ExecContext(ctx, `DELETE FROM settings_kv WHERE key = $1`, key)
			if err != nil {
				return "", fmt.Errorf("revert set_settings_kv: delete failed: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				slog.Warn("error_playbook: revert delete affected 0 rows", "key", key)
			}
			return fmt.Sprintf("settings_kv[%s] deleted (restored to no-value)", key), nil
		}
		prevValue := json.RawMessage(jsonbFrom(snap["prev_value"]))
		prevType := strings.TrimSpace(strFrom(snap, "prev_type"))
		if prevType == "" {
			prevType = "json"
		}
		if len(prevValue) == 0 {
			return "", fmt.Errorf("revert set_settings_kv: snapshot missing prev_value")
		}
		res, err := b.db.ExecContext(ctx, `
			UPDATE settings_kv SET value = $2::jsonb, value_type = $3, updated_at = NOW(), updated_by = 'error_playbook:revert'
			WHERE key = $1`, key, prevValue, prevType)
		if err != nil {
			return "", fmt.Errorf("revert set_settings_kv: update failed: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// ★ RowsAffected 纪律（D14）：0 行 = 键已被删除 = 回滚没生效，
			//   绝不能声称成功。
			return "", fmt.Errorf("revert set_settings_kv: key %q no longer exists", key)
		}
		return fmt.Sprintf("settings_kv[%s] restored", key), nil

	case ActionDisablePIRule:
		ruleName := strings.TrimSpace(strFrom(snap, "rule_name"))
		if ruleName == "" {
			return "", fmt.Errorf("revert disable_pi_rule: snapshot missing rule_name")
		}
		if b.db == nil {
			return "", fmt.Errorf("revert disable_pi_rule: database not configured")
		}
		enabled := true
		if v, ok := snap["enabled"].(bool); ok {
			enabled = v
		}
		res, err := b.db.ExecContext(ctx,
			`UPDATE prompt_injection_rules SET enabled = $2, updated_at = NOW() WHERE rule_name = $1`,
			ruleName, enabled)
		if err != nil {
			return "", fmt.Errorf("revert disable_pi_rule: update failed: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", fmt.Errorf("revert disable_pi_rule: rule %q no longer exists", ruleName)
		}
		return fmt.Sprintf("rule %q restored to enabled=%v", ruleName, enabled), nil

	default:
		return "", fmt.Errorf("action %q is not revertible", action)
	}
}

func jsonbFrom(v interface{}) []byte {
	if v == nil {
		return nil
	}
	switch tv := v.(type) {
	case string:
		return []byte(tv)
	case map[string]interface{}:
		b, err := json.Marshal(tv)
		if err != nil {
			return nil
		}
		return b
	}
	return nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
