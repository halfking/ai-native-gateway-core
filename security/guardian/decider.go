package guardian

import "sync"

// GuardMode 守卫模式
type GuardMode string

const (
	ModeObserve GuardMode = "observe" // 仅记录，不干扰
	ModeWarn    GuardMode = "warn"    // 记录 + 告警
	ModeBlock   GuardMode = "block"   // 阻断违规请求/响应
)

// TenantGuardPolicy 租户级别守卫策略
type TenantGuardPolicy struct {
	// Mode 默认守卫模式
	Mode GuardMode
	// SkipChecks 跳过的检查项（按 guard Name）
	SkipChecks []string
	// CheckOverrides 单个检查的覆盖模式（guard Name → mode）
	CheckOverrides map[string]GuardMode
}

// ShouldSkip 是否应该跳过指定检查
func (p *TenantGuardPolicy) ShouldSkip(checkName string) bool {
	for _, name := range p.SkipChecks {
		if name == checkName {
			return true
		}
	}
	return false
}

// EffectiveMode 返回指定检查的有效模式
func (p *TenantGuardPolicy) EffectiveMode(checkName string) GuardMode {
	if p == nil {
		return ModeObserve
	}
	if p.ShouldSkip(checkName) {
		return ModeObserve
	}
	if mode, ok := p.CheckOverrides[checkName]; ok {
		return mode
	}
	return p.Mode
}

// GuardDecider 安全策略决策引擎
//
// 按租户+检查项决定守卫的生效模式。
// 默认模式：ModeObserve（观察），线上环境配置后才升级。
type GuardDecider struct {
	mu       sync.RWMutex
	defaults GuardMode
	tenants  map[string]*TenantGuardPolicy
}

// NewGuardDecider 创建决策引擎
//
//	defaults: 全局默认模式（建议 ModeObserve）
func NewGuardDecider(defaultMode GuardMode) *GuardDecider {
	if defaultMode == "" {
		defaultMode = ModeObserve
	}
	return &GuardDecider{
		defaults: defaultMode,
		tenants:  make(map[string]*TenantGuardPolicy),
	}
}

// Mode 返回指定租户+检查项的生效模式
func (d *GuardDecider) Mode(tenantID, checkName string) GuardMode {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if p, ok := d.tenants[tenantID]; ok {
		return p.EffectiveMode(checkName)
	}
	return d.defaults
}

// SetTenantPolicy 设置租户策略（线程安全）
func (d *GuardDecider) SetTenantPolicy(tenantID string, policy *TenantGuardPolicy) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tenants[tenantID] = policy
}

// SetDefaultMode 设置全局默认模式（线程安全）
func (d *GuardDecider) SetDefaultMode(m GuardMode) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.defaults = m
}

// DeleteTenantPolicy 删除租户策略（恢复默认）
func (d *GuardDecider) DeleteTenantPolicy(tenantID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tenants, tenantID)
}
