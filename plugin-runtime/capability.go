package pluginruntime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// CapabilityRoute is the single source of truth for the capability required by
// a plugin-facing route. Routes are matched most-specifically by path prefix.
//
// # ★ 2026-10-24 magpie 刷新轮（§6-P0-2）：本表新增 Implemented 字段
//
// 审计发现（docs/research/2026-10-24-magpie-v0.1.1367-…md §3.6）：
// **5 条 capability 路由只挂了 2 条** —— `bodies` / `interception-policies` /
// `events` 三条**在全仓找不到任何 handler**（只有本表与它自己的单测引用）。
// ⇒ 「插件抓取请求体」「插件下发拦截策略」「插件投递事件」这三件事
// **在能力表里存在，在运行时不存在**。
//
// ⇒ 处置（计划书 §6-P0-2 的**第二个选项**：明确判死）：
//
//	① 三条路由在表里标 `Implemented: false`（**声明与实现同源**）；
//	② manifest 声明这三条能力 ⇒ **声明即拒**（`RegisterManifest` 返回错误、
//	   该能力**永远不被授予**）⇒ 插件访问这些路径必然 403 capability_denied；
//	③ 门禁（scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go）
//	   **扫 cmd/gateway 的路由注册**逐条对拍：标了 implemented 就必须有 handler，
//	   没标就**不许**有 handler ⇒ 表不会骗人。
//
// ⚠ 为什么这三条**不实现**（而不是补实现）：
//
//		① `body.fetch`（插件抓请求体）与 `interception-policies`（插件下发拦截策略）
//		   都是**数据面**能力，而本仓的沙箱策略明确拒绝一切数据面写能力
//		   （plugin-runtime/sandbox.go:11-17：`data-plane write capability
//		   (mutate/block/tool.execute) is refused until a real sandbox is available`，
//		   且 `DefaultSandboxPolicy().Available = false`）。
//		   ⇒ 在**没有真沙箱**之前实现它们，等于把「声明了但执行被策略全拒」
//		   的老问题换个位置再犯一次。
//		② `events.ingest`（插件投递事件）需要事件投递面，而本仓**没有**该面。
//	  ⇒ 三条都属于 P3（进程内脚本引擎 / 真沙箱）的前置范围。
//
// ⇒ **诚实边界**：这是「明确判死」，不是「补齐」。能力表与实现的差集被**显式化**
//
//	并由门禁锁住，而不是继续藏在一张看起来很全的表里。
type CapabilityRoute struct {
	Method     string
	PathPrefix string
	Capability string
	// Implemented 表示这条路由**真的有 handler**。
	//
	// ★ false ⇒ 该路径没有实现，且 manifest 声明对应能力会被拒。
	Implemented bool
}

// 插件侧 HTTP 路由的能力名（**单一事实源**：路由表与 manifest 校验都引用它们）。
//
// ★ 注意这组词表与 `types.go` 的**绑定能力**词表（request.* / session.* / …）
//
//	是**两个不同的词表**：绑定能力管"插件能挂到哪个阶段"，
//	路由能力管"插件能调哪个 HTTP 面"。
//	⇒ 两者不相交是**设计**（不是笔误），门禁会断言这一点。
const (
	// CapabilitySessionRead 读取会话（/sessions）。
	CapabilitySessionRead = "session.read"
	// CapabilityAnalyticsRead 读分析面（/analytics）。
	CapabilityAnalyticsRead = "analytics.read"
	// CapabilityBodyFetch 抓取请求体（/bodies）—— **未实现**。
	CapabilityBodyFetch = "body.fetch"
	// CapabilityPolicyServe 下发拦截策略（/interception-policies）—— **未实现**。
	CapabilityPolicyServe = "policy.serve"
	// CapabilityEventsIngest 投递事件（/events）—— **未实现**。
	CapabilityEventsIngest = "events.ingest"
)

var defaultCapabilityRoutes = []CapabilityRoute{
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/sessions", Capability: CapabilitySessionRead, Implemented: true},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/analytics", Capability: CapabilityAnalyticsRead, Implemented: true},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/bodies", Capability: CapabilityBodyFetch, Implemented: false},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/interception-policies", Capability: CapabilityPolicyServe, Implemented: false},
	{Method: http.MethodPost, PathPrefix: "/_gateway/plugin/v1/events", Capability: CapabilityEventsIngest, Implemented: false},
}

// CapabilityRegistry stores the capabilities declared by each installed plugin.
type CapabilityRegistry struct {
	mu       sync.RWMutex
	declared map[string]map[string]struct{}
	routes   []CapabilityRoute
}

func NewCapabilityRegistry() *CapabilityRegistry {
	r := &CapabilityRegistry{declared: make(map[string]map[string]struct{})}
	r.routes = append([]CapabilityRoute(nil), defaultCapabilityRoutes...)
	return r
}

// CapabilityRoutes 返回插件侧 HTTP 路由的能力表（**单一事实源**，只读副本）。
//
// ★ 门禁（scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go）用它
//
//	逐条对拍**真实的路由注册** ⇒ 表与运行时不同源就红。
func CapabilityRoutes() []CapabilityRoute {
	return append([]CapabilityRoute(nil), defaultCapabilityRoutes...)
}

// UnimplementedCapabilities 返回**没有 handler 实现**的路由能力。
//
// ★ 单一事实源：直接读路由表的 `Implemented` 标记 ⇒ 表与实现同源。
func UnimplementedCapabilities() []string {
	var out []string
	for _, r := range defaultCapabilityRoutes {
		if !r.Implemented {
			out = append(out, r.Capability)
		}
	}
	return out
}

// ImplementedCapabilities 返回**有 handler 实现**的路由能力。
func ImplementedCapabilities() []string {
	var out []string
	for _, r := range defaultCapabilityRoutes {
		if r.Implemented {
			out = append(out, r.Capability)
		}
	}
	return out
}

// RejectUnimplementedCapabilities 把 manifest 的能力声明里**未实现**的那些挑出来。
//
// ★ 2026-10-24 P0-2（§6-P0-2 选项②）：「在能力表标注未实现并**禁止 manifest
//
//	声明未实现的能力**」⇒ 未实现的能力**永远不能被授予**。
//
// ⇒ 返回值是**被拒的能力名**（已去重、已排序）；空切片表示全部可授。
func RejectUnimplementedCapabilities(capabilities []string) []string {
	blocked := make(map[string]struct{})
	for _, c := range UnimplementedCapabilities() {
		blocked[c] = struct{}{}
	}
	var rejected []string
	seen := map[string]struct{}{}
	for _, c := range capabilities {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := blocked[c]; !ok {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		rejected = append(rejected, c)
	}
	sort.Strings(rejected)
	return rejected
}

// RegisterManifest 登记一个 manifest 声明的能力。
//
// ★ 2026-10-24 P0-2：**未实现的能力声明即拒** —— 该能力**不会**进入允许集，
//
//	于是走这条路由时 `Allows` 返回 false ⇒ 403 capability_denied（失败关闭）。
//
// ★ 返回值非 nil 表示"这个 manifest 声明了未实现的能力"；**其余能力照常登记**
//
//	（一个插件不该因为多声明了一条用不了的能力，就失去它本来能用的能力）。
//
// ⚠ 与绑定层的能力白名单（knownBindingCapabilities）是**两个词表**：
//
//	本函数管的是**路由能力**（session.read / analytics.read / …），
//	绑定层管的是**绑定能力**（request.* / session.* / …）。
//	manifest 的 capabilities 走路由词表这条路径；
//	绑定能力由 `ValidateBinding` 单独校验。
func (r *CapabilityRegistry) RegisterManifest(m *Manifest) error {
	if r == nil || m == nil || m.PluginID == "" {
		return nil
	}
	rejected := RejectUnimplementedCapabilities(m.Capabilities)
	set := make(map[string]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		c := strings.TrimSpace(capability)
		if c == "" {
			continue
		}
		set[c] = struct{}{}
	}
	// 去掉被拒的能力（它们**永远**不会被授予）。
	for _, bad := range rejected {
		delete(set, bad)
	}
	r.mu.Lock()
	r.declared[m.PluginID] = set
	r.mu.Unlock()
	if len(rejected) > 0 {
		return fmt.Errorf("manifest %q declares unimplemented plugin capabilities %v (declared-but-absent)",
			m.PluginID, rejected)
	}
	return nil
}

func (r *CapabilityRegistry) RegisterRoute(route CapabilityRoute) {
	if r == nil || route.PathPrefix == "" || route.Capability == "" {
		return
	}
	r.mu.Lock()
	r.routes = append(r.routes, route)
	r.mu.Unlock()
}

func (r *CapabilityRegistry) Required(method, path string) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := ""
	bestLen := -1
	for _, route := range r.routes {
		if route.Method != "" && route.Method != method {
			continue
		}
		if strings.HasPrefix(path, route.PathPrefix) && len(route.PathPrefix) > bestLen {
			best, bestLen = route.Capability, len(route.PathPrefix)
		}
	}
	return best
}

func (r *CapabilityRegistry) Allows(pluginID, capability string) bool {
	if r == nil || capability == "" {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	set, ok := r.declared[pluginID]
	if !ok {
		return false
	}
	_, ok = set[capability]
	return ok
}

func CapabilityDenied(w http.ResponseWriter, capability string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "capability_denied", "capability": capability})
}

// RequireCapability checks a verified plugin identity against the route table.
// It must be placed after VerifyPluginContext (canonical) or after admin auth
// plus plugin identity resolution (browser proxy).
func RequireCapability(reg *CapabilityRegistry, capability string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		pluginID := req.Header.Get("X-Gateway-Plugin-ID")
		if pluginID == "" {
			pluginID = req.PathValue("pluginId")
		}
		if reg == nil || !reg.Allows(pluginID, capability) {
			CapabilityDenied(w, capability)
			return
		}
		next.ServeHTTP(w, req)
	})
}

// RequireRouteCapability applies the capability declared by the route table.
func RequireRouteCapability(reg *CapabilityRegistry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capability := reg.Required(req.Method, req.URL.Path)
		if capability != "" {
			pluginID := req.Header.Get("X-Gateway-Plugin-ID")
			if pluginID == "" {
				pluginID = req.PathValue("pluginId")
			}
			if !reg.Allows(pluginID, capability) {
				CapabilityDenied(w, capability)
				return
			}
		}
		next.ServeHTTP(w, req)
	})
}

// IsImplementedCapability 报告某个路由能力**是否有 handler 实现**。
func IsImplementedCapability(capability string) bool {
	for _, r := range defaultCapabilityRoutes {
		if r.Capability == capability {
			return r.Implemented
		}
	}
	// 不在路由表里的能力（例如绑定能力）不是路由能力。
	return false
}
