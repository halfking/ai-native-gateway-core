package session

import (
	"sync/atomic"
	"time"
)

// AtomicSession 是 Session 的线程安全包装器
// 使用原子操作保护所有计数器字段，避免并发竞争
type AtomicSession struct {
	// 不可变字段（初始化后不再修改，无需同步）
	SessionID  string
	SessionKey string
	APIKeyID   int
	TenantID   string
	TaskID     string
	Namespace  string
	CreatedAt  time.Time
	ClientIP   string
	ClientFP   string

	// 原子计数器（使用 atomic 操作）
	totalTurns            atomic.Int64
	totalPromptTokens     atomic.Int64
	totalCompletionTokens atomic.Int64
	totalCostUSDCents     atomic.Int64
	currentCredTurns      atomic.Int64
	currentCredStartTurn  atomic.Int64

	// 原子指针（使用 atomic.Value）
	lastActive         atomic.Value // time.Time
	expiresAt          atomic.Value // time.Time
	firstRequestAt     atomic.Value // time.Time
	lastRequestAt      atomic.Value // time.Time
	currentCredStartAt atomic.Value // time.Time
	stoppedAt          atomic.Value // time.Time
	recoveredAt        atomic.Value // time.Time

	// 字符串字段（使用 atomic.Value）
	lastCredentialID atomic.Value // string
	status           atomic.Value // string
	stopReason       atomic.Value // string
	currentModel     atomic.Value // string
	currentProvider  atomic.Value // string
	title            atomic.Value // string
	annotation       atomic.Value // string
	tags             atomic.Value // string

	// 整数字段（使用 atomic）
	currentCredentialID atomic.Int64 // int
	fpSlotIndex         atomic.Int64 // int
	fpSlotCredentialID  atomic.Int64 // int

	// 复杂结构（需要深拷贝，使用 atomic.Value）
	devices       atomic.Value // []Device
	providerCache atomic.Value // CacheInfo
}

// NewAtomicSession 从普通 Session 创建线程安全的 AtomicSession
func NewAtomicSession(s *Session) *AtomicSession {
	if s == nil {
		return nil
	}

	as := &AtomicSession{
		SessionID:  s.SessionID,
		SessionKey: s.SessionKey,
		APIKeyID:   s.APIKeyID,
		TenantID:   s.TenantID,
		TaskID:     s.TaskID,
		Namespace:  s.Namespace,
		CreatedAt:  s.CreatedAt,
		ClientIP:   s.ClientIP,
		ClientFP:   s.ClientFP,
	}

	// 初始化原子计数器
	as.totalTurns.Store(s.TotalTurns)
	as.totalPromptTokens.Store(s.TotalPromptTokens)
	as.totalCompletionTokens.Store(s.TotalCompletionTokens)
	as.totalCostUSDCents.Store(s.TotalCostUSDCents)
	as.currentCredTurns.Store(s.CurrentCredTurns)
	as.currentCredStartTurn.Store(s.CurrentCredStartTurn)
	as.currentCredentialID.Store(int64(s.CurrentCredentialID))
	as.fpSlotIndex.Store(int64(s.FPSlotIndex))
	as.fpSlotCredentialID.Store(int64(s.FPSlotCredentialID))

	// 初始化时间字段
	as.lastActive.Store(s.LastActive)
	as.expiresAt.Store(s.ExpiresAt)
	as.firstRequestAt.Store(s.FirstRequestAt)
	as.lastRequestAt.Store(s.LastRequestAt)
	as.currentCredStartAt.Store(s.CurrentCredStartAt)
	as.stoppedAt.Store(s.StoppedAt)
	as.recoveredAt.Store(s.RecoveredAt)

	// 初始化字符串字段
	as.lastCredentialID.Store(s.LastCredentialID)
	as.status.Store(s.Status)
	as.stopReason.Store(s.StopReason)
	as.currentModel.Store(s.CurrentModel)
	as.currentProvider.Store(s.CurrentProvider)
	as.title.Store(s.Title)
	as.annotation.Store(s.Annotation)
	as.tags.Store(s.Tags)

	// 初始化复杂结构（深拷贝）
	devicesCopy := make([]Device, len(s.Devices))
	copy(devicesCopy, s.Devices)
	as.devices.Store(devicesCopy)
	as.providerCache.Store(s.ProviderCache)

	return as
}

// ToSession 转换回普通 Session（用于序列化）
func (as *AtomicSession) ToSession() *Session {
	if as == nil {
		return nil
	}

	return &Session{
		SessionID:             as.SessionID,
		SessionKey:            as.SessionKey,
		APIKeyID:              as.APIKeyID,
		TenantID:              as.TenantID,
		TaskID:                as.TaskID,
		Namespace:             as.Namespace,
		CreatedAt:             as.CreatedAt,
		ClientIP:              as.ClientIP,
		ClientFP:              as.ClientFP,
		TotalTurns:            as.totalTurns.Load(),
		TotalPromptTokens:     as.totalPromptTokens.Load(),
		TotalCompletionTokens: as.totalCompletionTokens.Load(),
		TotalCostUSDCents:     as.totalCostUSDCents.Load(),
		CurrentCredTurns:      as.currentCredTurns.Load(),
		CurrentCredStartTurn:  as.currentCredStartTurn.Load(),
		CurrentCredentialID:   int(as.currentCredentialID.Load()),
		FPSlotIndex:           int(as.fpSlotIndex.Load()),
		FPSlotCredentialID:    int(as.fpSlotCredentialID.Load()),
		LastActive:            as.loadTime(as.lastActive),
		ExpiresAt:             as.loadTime(as.expiresAt),
		FirstRequestAt:        as.loadTime(as.firstRequestAt),
		LastRequestAt:         as.loadTime(as.lastRequestAt),
		CurrentCredStartAt:    as.loadTime(as.currentCredStartAt),
		StoppedAt:             as.loadTime(as.stoppedAt),
		RecoveredAt:           as.loadTime(as.recoveredAt),
		LastCredentialID:      as.loadString(as.lastCredentialID),
		Status:                as.loadString(as.status),
		StopReason:            as.loadString(as.stopReason),
		CurrentModel:          as.loadString(as.currentModel),
		CurrentProvider:       as.loadString(as.currentProvider),
		Title:                 as.loadString(as.title),
		Annotation:            as.loadString(as.annotation),
		Tags:                  as.loadString(as.tags),
		Devices:               as.loadDevices(),
		ProviderCache:         as.loadProviderCache(),
	}
}

// 原子操作方法 - 计数器增加

// AddTurn 原子地增加轮次计数
func (as *AtomicSession) AddTurn(delta int64) int64 {
	return as.totalTurns.Add(delta)
}

// AddPromptTokens 原子地增加提示词 token 计数
func (as *AtomicSession) AddPromptTokens(delta int64) int64 {
	return as.totalPromptTokens.Add(delta)
}

// AddCompletionTokens 原子地增加完成 token 计数
func (as *AtomicSession) AddCompletionTokens(delta int64) int64 {
	return as.totalCompletionTokens.Add(delta)
}

// AddCost 原子地增加成本（美分）
func (as *AtomicSession) AddCost(delta int64) int64 {
	return as.totalCostUSDCents.Add(delta)
}

// AddCredTurn 原子地增加当前凭证的轮次
func (as *AtomicSession) AddCredTurn(delta int64) int64 {
	return as.currentCredTurns.Add(delta)
}

// 原子操作方法 - 读取

// GetTotalTurns 原子地读取总轮次
func (as *AtomicSession) GetTotalTurns() int64 {
	return as.totalTurns.Load()
}

// GetTotalPromptTokens 原子地读取总提示词 tokens
func (as *AtomicSession) GetTotalPromptTokens() int64 {
	return as.totalPromptTokens.Load()
}

// GetTotalCompletionTokens 原子地读取总完成 tokens
func (as *AtomicSession) GetTotalCompletionTokens() int64 {
	return as.totalCompletionTokens.Load()
}

// GetTotalCost 原子地读取总成本
func (as *AtomicSession) GetTotalCost() int64 {
	return as.totalCostUSDCents.Load()
}

// GetCurrentCredTurns 原子地读取当前凭证轮次
func (as *AtomicSession) GetCurrentCredTurns() int64 {
	return as.currentCredTurns.Load()
}

// 原子操作方法 - 设置

// SetLastActive 原子地设置最后活跃时间
func (as *AtomicSession) SetLastActive(t time.Time) {
	as.lastActive.Store(t)
}

// GetLastActive 原子地读取最后活跃时间
func (as *AtomicSession) GetLastActive() time.Time {
	return as.loadTime(as.lastActive)
}

// SetCurrentModel 原子地设置当前模型
func (as *AtomicSession) SetCurrentModel(model string) {
	as.currentModel.Store(model)
}

// GetCurrentModel 原子地读取当前模型
func (as *AtomicSession) GetCurrentModel() string {
	return as.loadString(as.currentModel)
}

// SetCurrentProvider 原子地设置当前提供商
func (as *AtomicSession) SetCurrentProvider(provider string) {
	as.currentProvider.Store(provider)
}

// GetCurrentProvider 原子地读取当前提供商
func (as *AtomicSession) GetCurrentProvider() string {
	return as.loadString(as.currentProvider)
}

// SetCurrentCredentialID 原子地设置当前凭证ID
func (as *AtomicSession) SetCurrentCredentialID(id int) {
	as.currentCredentialID.Store(int64(id))
}

// GetCurrentCredentialID 原子地读取当前凭证ID
func (as *AtomicSession) GetCurrentCredentialID() int {
	return int(as.currentCredentialID.Load())
}

// SetStatus 原子地设置状态
func (as *AtomicSession) SetStatus(status string) {
	as.status.Store(status)
}

// GetStatus 原子地读取状态
func (as *AtomicSession) GetStatus() string {
	return as.loadString(as.status)
}

// UpdateTokensAndCost 原子地更新 tokens 和成本（批量操作）
// 这是一个便利方法，用于在单个请求完成后更新所有计数器
func (as *AtomicSession) UpdateTokensAndCost(promptTokens, completionTokens, costCents int64) {
	as.totalPromptTokens.Add(promptTokens)
	as.totalCompletionTokens.Add(completionTokens)
	as.totalCostUSDCents.Add(costCents)
	as.totalTurns.Add(1)
	as.currentCredTurns.Add(1)
	as.lastRequestAt.Store(time.Now())
}

// ResetCredentialCounters 原子地重置凭证计数器（凭证轮转时调用）
func (as *AtomicSession) ResetCredentialCounters(newCredentialID int) {
	as.currentCredentialID.Store(int64(newCredentialID))
	as.currentCredTurns.Store(0)
	as.currentCredStartTurn.Store(as.totalTurns.Load())
	as.currentCredStartAt.Store(time.Now())
}

// 辅助方法

func (as *AtomicSession) loadTime(v atomic.Value) time.Time {
	if t, ok := v.Load().(time.Time); ok {
		return t
	}
	return time.Time{}
}

func (as *AtomicSession) loadString(v atomic.Value) string {
	if s, ok := v.Load().(string); ok {
		return s
	}
	return ""
}

func (as *AtomicSession) loadDevices() []Device {
	if devices, ok := as.devices.Load().([]Device); ok {
		// 返回深拷贝，避免外部修改
		result := make([]Device, len(devices))
		copy(result, devices)
		return result
	}
	return nil
}

func (as *AtomicSession) loadProviderCache() CacheInfo {
	if cache, ok := as.providerCache.Load().(CacheInfo); ok {
		return cache
	}
	return CacheInfo{}
}

// SetDevices 原子地设置设备列表（深拷贝）
func (as *AtomicSession) SetDevices(devices []Device) {
	devicesCopy := make([]Device, len(devices))
	copy(devicesCopy, devices)
	as.devices.Store(devicesCopy)
}

// SetProviderCache 原子地设置提供商缓存信息
func (as *AtomicSession) SetProviderCache(cache CacheInfo) {
	as.providerCache.Store(cache)
}
