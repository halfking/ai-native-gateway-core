// Package agentecosystem 实现智能体生态系统。
//
// 提供 agent 发现、行为分析、能力集成、协作编排能力。
// 是 domain-refactoring-plan.md 定义的"16 号全新领域"。
package agentecosystem

import (
	"errors"
	"time"
)

// Agent 智能体元数据。
type Agent struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Version      string        `json:"version"`
	Capabilities []*Capability `json:"capabilities"`
	Endpoints    []Endpoint    `json:"endpoints"`
	Tags         []string      `json:"tags"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// Capability 智能体能力。
type Capability struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Returns     map[string]any `json:"returns,omitempty"`
}

// Endpoint 调用端点。
type Endpoint struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Method   string `json:"method"`
	AuthType string `json:"auth_type"` // "none" / "api_key" / "oauth"
}

// Behavior 行为记录（用于行为分析）。
type Behavior struct {
	AgentID    string         `json:"agent_id"`
	Action     string         `json:"action"` // "invoke" / "fail" / "timeout" / "discover"
	Target     string         `json:"target"`
	Success    bool           `json:"success"`
	LatencyMs  int64          `json:"latency_ms"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	RecordedAt time.Time      `json:"recorded_at"`
}

// Registry agent 注册表。
type Registry struct {
	agents map[string]*Agent
}

// NewRegistry 构造一个空的 Registry。
func NewRegistry() *Registry {
	return &Registry{agents: make(map[string]*Agent)}
}

// Register 注册一个 agent。
func (r *Registry) Register(agent *Agent) error {
	if agent == nil {
		return errors.New("agentecosystem: nil agent")
	}
	if agent.ID == "" {
		return errors.New("agentecosystem: agent ID required")
	}
	r.agents[agent.ID] = agent
	return nil
}

// Unregister 注销一个 agent。返回是否原本存在。
func (r *Registry) Unregister(id string) bool {
	if _, ok := r.agents[id]; !ok {
		return false
	}
	delete(r.agents, id)
	return true
}

// Get 按 ID 获取 agent。返回 (agent, found, error)。
// 与 cache 等领域保持一致：found=false 且 err=nil 表示未命中。
func (r *Registry) Get(id string) (*Agent, bool, error) {
	a, ok := r.agents[id]
	if !ok {
		return nil, false, nil
	}
	return a, true, nil
}

// List 列出所有已注册的 agent。
func (r *Registry) List() []*Agent {
	out := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	return out
}

// Count 返回已注册 agent 数量。
func (r *Registry) Count() int {
	return len(r.agents)
}

// FindByCapability 按能力名查找 agent。
// 返回所有拥有该 capability 的 agent（顺序不保证）。
func (r *Registry) FindByCapability(capName string) []*Agent {
	out := []*Agent{}
	for _, a := range r.agents {
		for _, c := range a.Capabilities {
			if c != nil && c.Name == capName {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// FindByTag 按 tag 查找 agent。
func (r *Registry) FindByTag(tag string) []*Agent {
	out := []*Agent{}
	for _, a := range r.agents {
		for _, t := range a.Tags {
			if t == tag {
				out = append(out, a)
				break
			}
		}
	}
	return out
}
