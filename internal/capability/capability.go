// Package capability 实现会话能力的清单化（W4.1）：把会话侧能力
// （audit/forensics/summary/cluster/timeline/dimensions）打包为可售卖的
// Capability 单元，供后续 W4.2（能力绑 Key）消费。
//
// 设计对标灵果 Policies：能力有 ID、名称、提供方、依赖与权限要求；
// 注册表 fail-closed（未注册能力查询即失败），并防止重复注册与自环依赖。
//
// ⚠ 与 plugin-runtime/capability.go 的区别：那是「插件能调哪个 HTTP 面」的
// 路由权限表；本包是「客户买了哪个功能」的商业能力表。两者词表不相交是设计。
package capability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Capability 是一个可售卖的会话能力单元。
type Capability struct {
	// ID 是稳定的能力标识，形如 "session.audit"。API Key 的 capabilities
	// 列表（W4.2）将直接存储这些 ID。
	ID string `json:"id"`
	// Name 是面向售前的中文名。
	Name string `json:"name"`
	// Provider 是提供该能力的插件 ID（v1 只有 ai-session-manager）。
	Provider string `json:"provider"`
	// Description 一句话描述，用于 Key 创建表单的提示文案。
	Description string `json:"description"`
	// Dependencies 列出启用本能力时必须同时具备的能力 ID。
	Dependencies []string `json:"dependencies,omitempty"`
	// Permissions 是启用本能力时要求账号具备的权限位（v1 仅作展示）。
	Permissions []string `json:"permissions,omitempty"`
}

// Builtin 是 v1 内置的 6 个会话能力，全部由 ai-session-manager 提供。
// 这是《方案-…-2026-10-28》§W4.1 的验收清单，ID 一旦发布即为合同，不可改。
var Builtin = []Capability{
	{
		ID:          "session.audit",
		Name:        "会话审计",
		Provider:    "ai-session-manager",
		Description: "记录并检索全量会话活动，支持合规审计",
		Permissions: []string{"session.read", "audit.read"},
	},
	{
		ID:          "session.forensics",
		Name:        "会话取证",
		Provider:    "ai-session-manager",
		Description: "对指定会话做深度取证与证据导出",
		Dependencies: []string{"session.audit"},
		Permissions: []string{"session.read", "audit.read"},
	},
	{
		ID:          "session.summary",
		Name:        "会话摘要",
		Provider:    "ai-session-manager",
		Description: "AI 自动生成会话摘要与要点提炼",
		Permissions: []string{"session.read"},
	},
	{
		ID:          "session.cluster",
		Name:        "会话聚类",
		Provider:    "ai-session-manager",
		Description: "按语义聚类会话，发现批量行为模式",
		Dependencies: []string{"session.summary"},
		Permissions: []string{"session.read", "analytics.read"},
	},
	{
		ID:          "analytics.timeline",
		Name:        "时间线分析",
		Provider:    "ai-session-manager",
		Description: "会话事件按时间轴回放与对比分析",
		Permissions: []string{"analytics.read"},
	},
	{
		ID:          "analytics.dimensions",
		Name:        "多维分析",
		Provider:    "ai-session-manager",
		Description: "按模型/租户/渠道等维度做成本与用量切片",
		Permissions: []string{"analytics.read"},
	},
}

// Registry 保存已注册能力。零值不可用，必须用 NewRegistry。
type Registry struct {
	mu     sync.RWMutex
	byID   map[string]Capability
}

// NewRegistry 返回一个已注入 Builtin 清单的注册表。
func NewRegistry() *Registry {
	r := &Registry{byID: map[string]Capability{}}
	for _, c := range Builtin {
		// 内置清单是编译期常量，注册失败只可能是源码 bug，直接 panic 暴露。
		if err := r.Register(c); err != nil {
			panic(fmt.Sprintf("capability: builtin %q: %v", c.ID, err))
		}
	}
	return r
}

// Register 注册一个能力。能力 ID 必须为 [a-z0-9.]+ 小写词表；重复注册返回错误。
func (r *Registry) Register(c Capability) error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("capability id required")
	}
	if !isLowerDotted(c.ID) {
		return fmt.Errorf("invalid capability id %q: must match [a-z0-9.]+", c.ID)
	}
	if strings.TrimSpace(c.Provider) == "" {
		return fmt.Errorf("capability %q: provider required", c.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[c.ID]; exists {
		return fmt.Errorf("capability %q already registered", c.ID)
	}
	r.byID[c.ID] = c
	return nil
}

// Lookup 返回能力；未注册返回错误（fail-closed）。
func (r *Registry) Lookup(id string) (Capability, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byID[id]
	if !ok {
		return Capability{}, fmt.Errorf("capability %q not registered", id)
	}
	return c, nil
}

// ListByPlugin 返回某插件提供的全部能力，按 ID 排序。
func (r *Registry) ListByPlugin(provider string) []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Capability
	for _, c := range r.byID {
		if c.Provider == provider {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// List 返回全部已注册能力，按 ID 排序（供管理面下拉渲染）。
func (r *Registry) List() []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Capability, 0, len(r.byID))
	for _, c := range r.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ValidateSet 校验一组能力 ID（API Key 声明、请求检查共用）：
// 全部必须已注册，依赖闭包必须完整，无重复。
// 返回错误并指明第一条不合规的 ID。
func (r *Registry) ValidateSet(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate capability %q", id)
		}
		seen[id] = true
		if _, err := r.Lookup(id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		c, err := r.Lookup(id)
		if err != nil {
			return err // 不可达：上面已校验
		}
		for _, dep := range c.Dependencies {
			if !seen[dep] {
				return fmt.Errorf("capability %q requires %q to also be granted", id, dep)
			}
		}
	}
	return nil
}

// MissingDeps 返回 ids 中缺失依赖闭包需要补齐的能力 ID（W4.2 表单自动勾选用）。
func (r *Registry) MissingDeps(ids []string) []string {
	granted := map[string]bool{}
	for _, id := range ids {
		granted[id] = true
	}
	var need []string
	seenNeed := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		c, err := r.Lookup(id)
		if err != nil {
			return // 未注册能力在 ValidateSet 报错，这里不重复
		}
		for _, dep := range c.Dependencies {
			if !granted[dep] && !seenNeed[dep] {
				seenNeed[dep] = true
				need = append(need, dep)
			}
			walk(dep)
		}
	}
	for _, id := range ids {
		walk(id)
	}
	sort.Strings(need)
	return need
}

// isLowerDotted 匹配 ^[a-z0-9]+(\.[a-z0-9]+)*$。
func isLowerDotted(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') {
				return false
			}
		}
	}
	return true
}
