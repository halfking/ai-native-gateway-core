package pluginruntime

import (
	"fmt"
	"sync"
)

const defaultPluginGroup = "plugins"

// ViewerOpts 描述当前请求者的角色维度，供菜单按角色过滤。
type ViewerOpts struct {
	IsSuper        bool
	IsPlatformOps  bool
	IsTenantPortal bool
}

// Registry 是插件菜单与状态的内存索引，P0 不持久化。
type Registry struct {
	mu       sync.RWMutex
	plugins  map[string]*PluginState
	nav      map[string][]Page
	versions map[string]string
	bindings *BindingRegistry
}

// NewRegistry 构造一个空的内存 registry。
func NewRegistry() *Registry {
	return &Registry{
		plugins:  map[string]*PluginState{},
		nav:      map[string][]Page{},
		versions: map[string]string{},
		bindings: NewBindingRegistry(),
	}
}

// SetPlugin 注册或覆盖一个插件的 PluginState。
func (r *Registry) SetPlugin(st *PluginState) {
	if r == nil || st == nil {
		return
	}
	copy := *st
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins[copy.PluginID] = &copy
	r.versions[copy.PluginID] = copy.PluginVersion
}

// SetPluginStatus 仅更新单个插件的状态（用于 degraded/ready 切换）。
func (r *Registry) SetPluginStatus(pluginID, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.plugins[pluginID]; ok {
		copy := *st
		copy.Status = status
		r.plugins[pluginID] = &copy
	}
}

// SetBindings registers lifecycle bindings separately from the menu/status index.
func (r *Registry) SetBindings(pluginID string, bindings []PluginBinding, opts BindingValidationOptions) error {
	if r == nil || r.bindings == nil {
		return fmt.Errorf("plugin binding registry unavailable")
	}
	return r.bindings.Register(pluginID, bindings, opts)
}

func (r *Registry) Bindings(pluginID string) []PluginBinding {
	if r == nil || r.bindings == nil {
		return nil
	}
	return r.bindings.Bindings(pluginID)
}

// SetNav 注册某个插件的页面/菜单集合并记录版本。
func (r *Registry) SetNav(pluginID, version string, pages []Page) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nav[pluginID] = pages
	r.versions[pluginID] = version
}

// RemovePlugin 从索引里彻底移除一个插件。
func (r *Registry) RemovePlugin(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, pluginID)
	delete(r.nav, pluginID)
	delete(r.versions, pluginID)
}

// NavEntries 按角色过滤出可见的菜单条目。非 ready 状态的插件被隐藏。
func (r *Registry) NavEntries(opts ViewerOpts) []NavEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []NavEntry
	for pluginID, pages := range r.nav {
		st, ok := r.plugins[pluginID]
		if !ok || st.Status != "ready" {
			continue
		}
		version := r.versions[pluginID]
		for _, p := range pages {
			if p.Nav == nil {
				continue
			}
			if !visible(p.Nav, opts) {
				continue
			}
			out = append(out, NavEntry{
				PluginID:      pluginID,
				PluginVersion: version,
				PagePath:      p.Path,
				PageType:      p.Type,
				NavGroup:      groupOrDefault(p.Nav.Group),
				LabelKey:      p.Nav.LabelKey,
				Super:         p.Nav.Super,
				PlatformOps:   p.Nav.PlatformOps,
				TenantOnly:    p.Nav.TenantOnly,
				Order:         p.Nav.Order,
				RouteURL:      fmt.Sprintf("/plugins/%s/%s", pluginID, p.Path),
			})
		}
	}
	return out
}

func visible(n *Nav, opts ViewerOpts) bool {
	if n.Super && !opts.IsSuper {
		return false
	}
	if n.PlatformOps && !(opts.IsSuper && opts.IsPlatformOps) {
		return false
	}
	if n.TenantOnly && !opts.IsTenantPortal {
		return false
	}
	return true
}

func groupOrDefault(g string) string {
	if g == "" {
		return defaultPluginGroup
	}
	return g
}
