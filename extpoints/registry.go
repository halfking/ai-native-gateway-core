package extpoints

import (
	"fmt"
	"sort"
	"sync"
)

// registryImpl 是 ExtensionPointRegistry 的默认**内存实现**。
//
// ★ 2026-11-17（12h 审计）：本包此前只给了**接口**（ExtensionPointRegistry），
//
//	没有具体实现 ⇒ 同窗口新增的 demo（plugin-runtime/demo）引用
//	`NewExtensionPointRegistry()` / `NewExecutionEngine()` 时**编译不过**
//	⇒ `go build ./...` 看不出来（它不编译 _test.go），
//	而 `go test ./...` 与 `go vet ./...`（verify.sh 的两道门）直接失败
//	⇒ **主分支的门是红的**。这里补上具体实现。
//
// ★ 线程安全：Register/Unregister 写、Get/List 读 ⇒ 用 RWMutex 保护。
// ★ Get 返回**副本**（切片头），调用方改不到内部状态。
type registryImpl struct {
	mu      sync.RWMutex
	byPhase map[Phase][]ExtensionPoint
	byID    map[string]ExtensionPoint
}

// NewExtensionPointRegistry 返回一个空的扩展点注册表。
func NewExtensionPointRegistry() ExtensionPointRegistry {
	return &registryImpl{
		byPhase: make(map[Phase][]ExtensionPoint),
		byID:    make(map[string]ExtensionPoint),
	}
}

// Register 注册一个插件的扩展点。
//
// ★ 同一 pluginID 重复注册 ⇒ **替换**（而不是叠加两个同 ID 的条目），
//
//	否则 GetByID 的结果取决于注册顺序、而 List 会出现重复条目。
func (r *registryImpl) Register(phase Phase, plugin ExtensionPoint) error {
	if plugin == nil {
		return fmt.Errorf("extpoints: register %q: plugin is nil", phase)
	}
	md := plugin.Metadata()
	if md == nil {
		return fmt.Errorf("extpoints: register %q: plugin metadata is nil", phase)
	}
	if md.PluginID == "" {
		return fmt.Errorf("extpoints: register %q: plugin id is empty", phase)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 同 ID 已注册 ⇒ 先摘掉旧条目（可能挂在别的 phase 上）。
	if old, ok := r.byID[md.PluginID]; ok {
		r.removeLocked(old.Metadata().Phase, md.PluginID)
	}
	r.byPhase[phase] = append(r.byPhase[phase], plugin)
	r.byID[md.PluginID] = plugin
	r.sortLocked(phase)
	return nil
}

// Unregister 摘掉某个 phase 下的指定 pluginID。
func (r *registryImpl) Unregister(phase Phase, pluginID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[pluginID]; !ok {
		return fmt.Errorf("extpoints: unregister %q: plugin %q not registered", phase, pluginID)
	}
	r.removeLocked(phase, pluginID)
	return nil
}

// Get 返回该 phase 下按**优先级升序**排好的扩展点（值越小越先执行：
// PriorityHighest=0 最先，PriorityLowest=1000 最后）。
func (r *registryImpl) Get(phase Phase) []ExtensionPoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.byPhase[phase]
	out := make([]ExtensionPoint, len(src))
	copy(out, src)
	return out
}

// GetByID 按 pluginID 取扩展点。
func (r *registryImpl) GetByID(pluginID string) (ExtensionPoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[pluginID]
	if !ok {
		return nil, fmt.Errorf("extpoints: plugin %q not registered", pluginID)
	}
	return p, nil
}

// List 返回所有已注册扩展点（按 phase 分组）。
func (r *registryImpl) List() map[Phase][]ExtensionPoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[Phase][]ExtensionPoint, len(r.byPhase))
	for phase, src := range r.byPhase {
		cp := make([]ExtensionPoint, len(src))
		copy(cp, src)
		out[phase] = cp
	}
	return out
}

// removeLocked 摘掉一条（调用方持锁）。
func (r *registryImpl) removeLocked(phase Phase, pluginID string) {
	if list, ok := r.byPhase[phase]; ok {
		kept := list[:0]
		for _, p := range list {
			if md := p.Metadata(); md != nil && md.PluginID == pluginID {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == 0 {
			delete(r.byPhase, phase)
		} else {
			r.byPhase[phase] = kept
		}
	}
	delete(r.byID, pluginID)
}

// sortLocked 按优先级升序排（稳定排序 ⇒ 同优先级保持注册顺序）。
func (r *registryImpl) sortLocked(phase Phase) {
	list, ok := r.byPhase[phase]
	if !ok {
		return
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Metadata().Priority < list[j].Metadata().Priority
	})
}
