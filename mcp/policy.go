package mcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// matchPattern 判断工具名是否命中模式。
//
// 支持：
//   - 精确名："memory__search"
//   - 前缀通配："memory__*"
//   - 全通配："*"
func matchPattern(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if pattern == name || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(name, prefix)
	}
	re, err := regexp.Compile("^" + regexp.QuoteMeta(pattern) + "$")
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

// matchAny 判断名称是否命中任一模式。
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchPattern(p, name) {
			return true
		}
	}
	return false
}

// toolVisible 判断工具对某身份是否可见（ContextForge 的 L1 可见性层）。
//
//   - private：仅该工具所属的 agent（或同租户下的显式主体）可见。
//   - team：命中 key 的组织/团队即可见。
//   - public：所有已认证主体可见。
func toolVisible(t *Tool, id *Identity) bool {
	switch t.Visibility {
	case "", VisibilityPublic:
		return true
	case VisibilityTeam:
		if len(t.Teams) == 0 {
			return false
		}
		for _, team := range t.Teams {
			if team == id.OrgID || team == id.TenantID {
				return true
			}
		}
		return false
	case VisibilityPrivate:
		if t.AgentID != "" && id.AgentID != "" && t.AgentID == id.AgentID {
			return true
		}
		// 私有工具默认只对同租户可见（同租户即同治理域）。
		return t.TenantID == "" || t.TenantID == id.TenantID
	default:
		return false
	}
}

// keyAllowsTool 判断 key 的允许/拒绝表是否放行该工具。
//
// 拒绝表优先级高于允许表（deny-wins，与企业安全惯例一致）。
func keyAllowsTool(k *Key, toolName string) bool {
	if k == nil {
		return false
	}
	if matchAny(k.DeniedTools, toolName) {
		return false
	}
	if len(k.AllowedTools) == 0 {
		return true
	}
	return matchAny(k.AllowedTools, toolName)
}

// keyAllowsAdapter 判断 key 是否允许访问该适配器。
func keyAllowsAdapter(k *Key, adapter string) bool {
	if k == nil {
		return false
	}
	if len(k.AllowedAdapters) == 0 {
		return true
	}
	return matchAny(k.AllowedAdapters, adapter)
}

// bindingEnabled 判断 key 对该后端的绑定是否启用。
//
// 未登记该后端视为启用（交由上游自己校验），与既有 tools/mcp-gateway 的
// bindings 执法语义一致。
func bindingEnabled(k *Key, backend string) bool {
	if k == nil || k.Bindings == nil {
		return true
	}
	v, ok := k.Bindings[backend]
	if !ok {
		return true
	}
	return v
}

// classOf 返回工具的调用类别。
//
// 显式 Class 优先；否则按敏感度推导（read→read，write/high→write）。
func classOf(t *Tool) ToolClass {
	if t == nil {
		return ClassRead
	}
	if t.Class.ValidToolClass() {
		return t.Class
	}
	switch t.Sensitivity {
	case SensitivityWrite, SensitivityHigh:
		return ClassWrite
	default:
		return ClassRead
	}
}

// validateParams 校验工具入参。
//
// 两道闸：
//  1. 参数白名单（Tool.AllowedParams 非空时，只允许这些键）。
//  2. JSON Schema 校验（Tool.InputSchema 非空时按 schema 校验）。
func validateParams(t *Tool, args map[string]any) error {
	if t == nil {
		return nil
	}
	if len(t.AllowedParams) > 0 {
		for k := range args {
			if !containsStr(t.AllowedParams, k) {
				return fmt.Errorf("%w: parameter %q is not in the allowed list", ErrForbidden, k)
			}
		}
	}
	if len(t.InputSchema) == 0 {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(t.InputSchema)))
	if err != nil {
		// schema 本身坏了属于服务端问题：不阻断调用（fail-open），但要能被观测到。
		return nil
	}
	compiler := jsonschema.NewCompiler()
	const resourceURL = "mem://tool-schema.json"
	if err := compiler.AddResource(resourceURL, doc); err != nil {
		return nil
	}
	schema, err := compiler.Compile(resourceURL)
	if err != nil {
		return nil
	}
	if err := schema.Validate(args); err != nil {
		return fmt.Errorf("%w: invalid parameters: %s", ErrForbidden, err.Error())
	}
	return nil
}

// decodeArgs 把 JSON-RPC params.arguments 解码为 map。
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: arguments must be a JSON object", ErrForbidden)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
