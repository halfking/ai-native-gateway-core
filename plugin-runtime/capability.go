package pluginruntime

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// CapabilityRoute is the single source of truth for the capability required by
// a plugin-facing route. Routes are matched most-specifically by path prefix.
type CapabilityRoute struct {
	Method     string
	PathPrefix string
	Capability string
}

var defaultCapabilityRoutes = []CapabilityRoute{
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/sessions", Capability: "session.read"},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/analytics", Capability: "analytics.read"},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/bodies", Capability: "body.fetch"},
	{Method: http.MethodGet, PathPrefix: "/_gateway/plugin/v1/interception-policies", Capability: "policy.serve"},
	{Method: http.MethodPost, PathPrefix: "/_gateway/plugin/v1/events", Capability: "events.ingest"},
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

func (r *CapabilityRegistry) RegisterManifest(m *Manifest) {
	if r == nil || m == nil || m.PluginID == "" { return }
	set := make(map[string]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities { set[strings.TrimSpace(capability)] = struct{}{} }
	r.mu.Lock(); r.declared[m.PluginID] = set; r.mu.Unlock()
}

func (r *CapabilityRegistry) RegisterRoute(route CapabilityRoute) {
	if r == nil || route.PathPrefix == "" || route.Capability == "" { return }
	r.mu.Lock(); r.routes = append(r.routes, route); r.mu.Unlock()
}

func (r *CapabilityRegistry) Required(method, path string) string {
	if r == nil { return "" }
	r.mu.RLock(); defer r.mu.RUnlock()
	best := ""
	bestLen := -1
	for _, route := range r.routes {
		if route.Method != "" && route.Method != method { continue }
		if strings.HasPrefix(path, route.PathPrefix) && len(route.PathPrefix) > bestLen {
			best, bestLen = route.Capability, len(route.PathPrefix)
		}
	}
	return best
}

func (r *CapabilityRegistry) Allows(pluginID, capability string) bool {
	if r == nil || capability == "" { return true }
	r.mu.RLock(); defer r.mu.RUnlock()
	set, ok := r.declared[pluginID]
	if !ok { return false }
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
		if pluginID == "" { pluginID = req.PathValue("pluginId") }
		if reg == nil || !reg.Allows(pluginID, capability) { CapabilityDenied(w, capability); return }
		next.ServeHTTP(w, req)
	})
}

// RequireRouteCapability applies the capability declared by the route table.
func RequireRouteCapability(reg *CapabilityRegistry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capability := reg.Required(req.Method, req.URL.Path)
		if capability != "" {
			pluginID := req.Header.Get("X-Gateway-Plugin-ID")
			if pluginID == "" { pluginID = req.PathValue("pluginId") }
			if !reg.Allows(pluginID, capability) { CapabilityDenied(w, capability); return }
		}
		next.ServeHTTP(w, req)
	})
}
