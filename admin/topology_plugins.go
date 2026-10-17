package admin

// topology_plugins.go — 实时拓扑页「网关在用插件」只读投影。
//
//	GET /api/admin/topology/plugins
//
// 2026-10-18：为实时拓扑网关大方块的插件带供数。两类真值来源：
//   - builtin：domains/security Registry（治理管线在用的内置插件，
//     Name/Direction 来自插件自身，不在此处复写第二份清单 —— 双清单必漂移）；
//   - external：plugin-runtime Registry（外部插件进程的 ready/degraded/failed
//     状态，由 health loop 实时改写）。
//
// 装配缺失（SetTopologyPlugins 未被调用，如单测/精简启动）时返回
// 200 + degraded 信封 —— 与 providers-today 同契约：前端渲染成
// 「插件状态不可用」，绝不渲染成「没有插件」（那会让用户以为治理层是空的）。

import (
	"net/http"
	"time"
)

// TopologyBuiltinPlugin 是内置治理插件的一行（security.Plugin 的投影面）。
type TopologyBuiltinPlugin struct {
	Name      string `json:"name"`
	Direction string `json:"direction"` // input | output | both
}

// TopologyExternalPlugin 是外部插件进程的一行（pluginruntime.PluginState 的投影面）。
type TopologyExternalPlugin struct {
	PluginID      string `json:"plugin_id"`
	PluginVersion string `json:"plugin_version"`
	Status        string `json:"status"` // discovered | starting | ready | degraded | failed | stopped
}

type topologyPluginsResponse struct {
	GeneratedAt time.Time                `json:"generated_at"`
	Builtin     []TopologyBuiltinPlugin  `json:"builtin"`
	External    []TopologyExternalPlugin `json:"external"`
	Degraded    bool                     `json:"degraded"`
	ErrorCode   string                   `json:"error_code,omitempty"`
}

// topologyPluginsSource 是两类注册中心的窄投影接口。刻意不 import
// domains/security 与 plugin-runtime：结构化接口（Name/Direction/Snapshot
// 形状不同）让 admin 包保持零新依赖，装配侧（cmd/gateway）负责适配。
type topologyPluginsSource interface {
	BuiltinPlugins() []TopologyBuiltinPlugin
	ExternalPlugins() []TopologyExternalPlugin
}

// SetTopologyPlugins 在启动期注入插件清单来源（main.go 适配两个真实注册中心）。
// 只调一次；nil 源 = 端点走 degraded 信封。
func (h *Handler) SetTopologyPlugins(src topologyPluginsSource) {
	if h == nil || src == nil {
		return
	}
	h.topologyPlugins = src
}

// handleTopologyPlugins 服务实时拓扑网关大方块的「在用插件」清单。
func (h *Handler) handleTopologyPlugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h == nil || h.topologyPlugins == nil {
		writeJSON(w, http.StatusOK, topologyPluginsResponse{
			GeneratedAt: time.Now().UTC(),
			Builtin:     []TopologyBuiltinPlugin{},
			External:    []TopologyExternalPlugin{},
			Degraded:    true,
			ErrorCode:   "TOPOLOGY_PLUGINS_UNAVAILABLE",
		})
		return
	}
	src := h.topologyPlugins
	builtin := src.BuiltinPlugins()
	external := src.ExternalPlugins()
	if builtin == nil {
		builtin = []TopologyBuiltinPlugin{}
	}
	if external == nil {
		external = []TopologyExternalPlugin{}
	}
	writeJSON(w, http.StatusOK, topologyPluginsResponse{
		GeneratedAt: time.Now().UTC(),
		Builtin:     builtin,
		External:    external,
	})
}
