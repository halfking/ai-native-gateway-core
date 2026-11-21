package errorplaybook

// 计划（Plan）的归一化与校验。
//
// ★ 为什么需要归一化层：LLM 的建议是自由形状 JSON，方案 payload 必须先过
//
//	本层才能落库/执行。任何未在白名单里的动作、非法正则、超大 payload
//	都在这里被拒绝（fail-closed），执行器拿到的是已验证的形状。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 方案 payload 尺寸上限（防 LLM 把超大 payload 写进库/执行日志）。
const (
	maxPlanPayloadBytes = 32 << 10 // 32KB
	maxPlanSteps        = 8
	maxPromptBytes      = 16 << 10 // 16KB
	maxPatternLen       = 1024
)

// ErrPlanInvalid 是方案校验失败的业务性错误（可安全回显给管理面）。
type ErrPlanInvalid struct{ msg string }

func (e *ErrPlanInvalid) Error() string { return e.msg }

func planInvalid(format string, args ...interface{}) error {
	return &ErrPlanInvalid{msg: fmt.Sprintf(format, args...)}
}

// RouteChecker 是 ActionRegistry 的**可选**扩展：报告某 (method, path)
// 是否已登记为可执行的内部路由。
//
//	★ 审计轮（2026-11-21）引入：api 形态方案在**归一化期**就 fail-fast
//	（此前要到执行期才发现路由未登记 ⇒ 条目每次标准执行必失败 ⇒ 失败
//	进提炼循环空转）。LLM 提案引用未登记路由时，归一化即拒绝 ⇒
//	AnalyzeUnknown 走 prompt 兜底（手册仍落库，不再制造执行循环）。
type RouteChecker interface {
	HasRoute(method, path string) bool
}

// NormalizePlan 把自由形状的方案 payload 归一化并校验。
//
//	kind 为空时尝试从 payload 推断（{"kind": "..."} 或 {"steps": [...]}）。
//	api 形态归一化成单个 call_admin_api 步骤 ⇒ 执行面统一走动作注册表。
//	registry 可为 nil（只做结构校验，不校验动作白名单）。
func NormalizePlan(kind string, raw json.RawMessage, registry ActionRegistry) (Plan, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return Plan{}, planInvalid("plan payload is empty")
	}
	if len(raw) > maxPlanPayloadBytes {
		return Plan{}, planInvalid("plan payload too large (%d bytes > %d)", len(raw), maxPlanPayloadBytes)
	}

	var top map[string]interface{}
	if err := json.Unmarshal(raw, &top); err != nil {
		return Plan{}, planInvalid("plan payload is not a JSON object: %v", err)
	}

	if kind == "" {
		kind = planKindFromPayload(top)
	}

	var plan Plan
	switch kind {
	case PlanKindScript:
		steps, err := normalizeScriptSteps(top, registry)
		if err != nil {
			return Plan{}, err
		}
		plan = Plan{Kind: PlanKindScript, Steps: steps}

	case PlanKindAPI:
		api, err := normalizeAPIShape(top)
		if err != nil {
			return Plan{}, err
		}
		// ★ 路由 fail-fast（审计轮 2026-11-21）：registry 支持路由查询时，
		//   未登记的 (method, path) 在归一化期即拒绝（执行期才发现会制造
		//   「失败 → 提炼 → 再失败」循环）。
		if rc, ok := registry.(RouteChecker); ok && !rc.HasRoute(api.Method, api.Path) {
			return Plan{}, planInvalid("api plan route %s %s is not registered as an executable internal route",
				api.Method, api.Path)
		}
		plan = api

	case PlanKindPrompt:
		prompt, expect, err := normalizePromptShape(top)
		if err != nil {
			return Plan{}, err
		}
		plan = Plan{Kind: PlanKindPrompt, Prompt: prompt, Expect: expect}

	default:
		return Plan{}, planInvalid("unknown plan kind %q (want script|api|prompt)", kind)
	}

	return plan, nil
}

// planKindFromPayload 从 payload 形状推断 kind（LLM 常见产出形状）。
func planKindFromPayload(top map[string]interface{}) string {
	if v, ok := top["kind"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := top["plan_kind"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.ToLower(strings.TrimSpace(v))
	}
	if _, ok := top["steps"]; ok {
		return PlanKindScript
	}
	if _, ok := top["path"]; ok {
		return PlanKindAPI
	}
	if _, ok := top["prompt"]; ok {
		return PlanKindPrompt
	}
	return ""
}

// normalizeScriptSteps 校验 script 形态的 steps 数组。
func normalizeScriptSteps(top map[string]interface{}, registry ActionRegistry) ([]PlanStep, error) {
	rawSteps, ok := top["steps"].([]interface{})
	if !ok || len(rawSteps) == 0 {
		return nil, planInvalid("script plan requires a non-empty \"steps\" array")
	}
	if len(rawSteps) > maxPlanSteps {
		return nil, planInvalid("script plan has %d steps (max %d)", len(rawSteps), maxPlanSteps)
	}
	steps := make([]PlanStep, 0, len(rawSteps))
	for i, rs := range rawSteps {
		m, ok := rs.(map[string]interface{})
		if !ok {
			return nil, planInvalid("step %d is not an object", i+1)
		}
		action := strings.ToLower(strings.TrimSpace(strFrom(m, "action", "type", "op")))
		if action == "" {
			return nil, planInvalid("step %d missing \"action\"", i+1)
		}
		if registry != nil && !registry.Has(action) {
			return nil, planInvalid("step %d action %q is not whitelisted (allowed: %s)",
				i+1, action, strings.Join(registry.Names(), ", "))
		}
		params, _ := m["params"].(map[string]interface{})
		if params == nil {
			params = map[string]interface{}{}
		}
		steps = append(steps, PlanStep{
			Action: action,
			Params: params,
			Note:   strings.TrimSpace(strFrom(m, "note", "reason")),
		})
	}
	return steps, nil
}

// normalizeAPIShape 校验 api 形态并归一化成单个 call_admin_api 步骤。
func normalizeAPIShape(top map[string]interface{}) (Plan, error) {
	method := strings.ToUpper(strings.TrimSpace(strFrom(top, "method", "http_method")))
	if method == "" {
		method = "GET"
	}
	switch method {
	case "GET", "POST", "PUT", "HEAD", "DELETE", "PATCH":
	default:
		return Plan{}, planInvalid("api plan method %q not allowed", method)
	}
	path := strings.TrimSpace(strFrom(top, "path", "url"))
	if path == "" {
		return Plan{}, planInvalid("api plan requires \"path\"")
	}
	// ★ 路径闸：只允许管理面前缀（api 方案的语义是「调一次管理面 API」，
	//   不是任意 URL —— 任意 URL 是 SSRF 面，被结构性排除）。
	if !strings.HasPrefix(path, "/api/admin/") {
		return Plan{}, planInvalid("api plan path must start with /api/admin/ (got %q)", path)
	}
	timeoutMS := 0
	if v, ok := top["timeout_ms"].(float64); ok && v > 0 {
		timeoutMS = int(v)
		if timeoutMS > 60000 {
			timeoutMS = 60000
		}
	}
	var body json.RawMessage
	if b, ok := top["body"]; ok && b != nil {
		raw, err := json.Marshal(b)
		if err != nil {
			return Plan{}, planInvalid("api plan body is not valid JSON: %v", err)
		}
		body = raw
	}
	params := map[string]interface{}{
		"method": method,
		"path":   path,
	}
	if len(body) > 0 {
		params["body"] = json.RawMessage(body)
	}
	if timeoutMS > 0 {
		params["timeout_ms"] = timeoutMS
	}
	return Plan{
		Kind:      PlanKindAPI,
		Method:    method,
		Path:      path,
		Body:      body,
		TimeoutMS: timeoutMS,
		Steps: []PlanStep{{
			Action: ActionCallAdminAPI,
			Params: params,
		}},
	}, nil
}

// normalizePromptShape 校验 prompt 形态。
func normalizePromptShape(top map[string]interface{}) (string, string, error) {
	prompt := strings.TrimSpace(strFrom(top, "prompt", "text", "instruction"))
	if prompt == "" {
		return "", "", planInvalid("prompt plan requires a non-empty \"prompt\"")
	}
	if len(prompt) > maxPromptBytes {
		return "", "", planInvalid("prompt too large (%d bytes > %d)", len(prompt), maxPromptBytes)
	}
	expect := strings.ToLower(strings.TrimSpace(strFrom(top, "expect")))
	switch expect {
	case "", "analysis", "proposal":
	default:
		return "", "", planInvalid("prompt plan expect %q not allowed (want analysis|proposal)", expect)
	}
	return prompt, expect, nil
}

// CompilePattern 编译条目的 match_pattern（"" 返回 nil）。
//
//	★ 非法正则按「不匹配」处理而不是 panic；长度上限防超大正则写进匹配热路径。
func CompilePattern(pattern string) (*regexp.Regexp, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return nil, nil
	}
	if len(p) > maxPatternLen {
		return nil, fmt.Errorf("pattern too long (%d > %d)", len(p), maxPatternLen)
	}
	return regexp.Compile(p)
}

// strFrom 依次取第一个非空字符串值。
func strFrom(m map[string]interface{}, keys ...string) string {
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

// MarshalPlan 把 Plan 序列化成 plan_payload 列的存储形状。
func MarshalPlan(p Plan) (json.RawMessage, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
