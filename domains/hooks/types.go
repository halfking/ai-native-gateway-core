package hooks

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// Phase 定义Hook执行阶段
type Phase string

const (
	// PhasePreRouting 路由前阶段（认证、审计）
	PhasePreRouting Phase = "pre_routing"
	// PhaseRouting 路由中阶段（模型选择）
	PhaseRouting Phase = "routing"
	// PhasePreUpstream 上游前阶段（压缩、转换）
	PhasePreUpstream Phase = "pre_upstream"
	// PhasePostUpstream 上游后阶段（缓存、脱敏）
	PhasePostUpstream Phase = "post_upstream"
	// PhasePostResponse 响应后阶段（异步处理）
	PhasePostResponse Phase = "post_response"
)

// String 返回Phase的字符串表示
func (p Phase) String() string {
	return string(p)
}

// IsValid 检查Phase是否有效
func (p Phase) IsValid() bool {
	switch p {
	case PhasePreRouting, PhaseRouting, PhasePreUpstream, PhasePostUpstream, PhasePostResponse:
		return true
	default:
		return false
	}
}

// Hook 定义了Hook插件必须实现的接口
type Hook interface {
	// Name 返回Hook名称（全局唯一）
	Name() string

	// Priority 返回执行优先级（0-1000，越小越先执行）
	Priority() int

	// Enabled 返回是否启用
	Enabled() bool

	// Phase 返回执行阶段
	Phase() Phase

	// Execute 执行Hook逻辑
	Execute(ctx context.Context, env *Environment) error
}

// ConfigurableHook 定义了支持配置热更新的Hook接口
type ConfigurableHook interface {
	Hook
	// OnConfigChange 当配置变更时被调用
	OnConfigChange(config map[string]interface{}) error
}

// Environment 定义Hook执行环境，包含请求、响应、会话等信息
type Environment struct {
	// 请求标识
	RequestID  string
	TenantID   string
	SessionKey string
	TaskID     string

	// 请求响应数据
	Request          interface{} // 客户端请求（具体类型由调用方决定）
	Response         interface{} // 客户端响应
	UpstreamRequest  interface{} // 上游请求
	UpstreamResponse interface{} // 上游响应

	// 会话信息
	Session *session.Session

	// Hook间共享数据
	Metadata map[string]interface{}

	// 时间戳
	StartTime time.Time

	// 控制标志
	Skip        bool   // 跳过后续Hook
	Abort       bool   // 中止请求
	AbortReason string // 中止原因
}

// NewEnvironment 创建新的Environment实例
func NewEnvironment(requestID string) *Environment {
	return &Environment{
		RequestID: requestID,
		Metadata:  make(map[string]interface{}),
		StartTime: time.Now(),
	}
}

// SetSkip 设置跳过后续Hook的标志
func (e *Environment) SetSkip() {
	e.Skip = true
}

// SetAbort 设置中止请求的标志
func (e *Environment) SetAbort(reason string) {
	e.Abort = true
	e.AbortReason = reason
}

// ShouldContinue 返回是否应该继续执行后续Hook
func (e *Environment) ShouldContinue() bool {
	return !e.Skip && !e.Abort
}

// Registry 定义Hook注册中心接口
type Registry interface {
	// Register 注册Hook到Registry
	Register(hook Hook) error

	// Execute 执行指定Phase的Hook链
	Execute(ctx context.Context, phase Phase, env *Environment) error

	// GetHooks 获取指定Phase的所有Hook
	GetHooks(phase Phase) []Hook

	// ReloadConfig 重新加载配置
	ReloadConfig() error

	// GetHookByName 根据名称获取Hook
	GetHookByName(name string) (Hook, bool)
}

// ConfigManager 定义配置管理器接口
type ConfigManager interface {
	// Load 加载配置
	Load() error

	// GetHookConfig 获取指定Hook的配置
	GetHookConfig(hookName string) map[string]interface{}

	// GetHookTimeout 获取指定Hook的超时时间
	GetHookTimeout(hookName string) time.Duration

	// IsHookEnabled 检查Hook是否启用
	IsHookEnabled(hookName string) bool

	// Watch 监控配置文件变化
	Watch(callback func()) error

	// Stop 停止监控
	Stop()
}

// MetricsCollector 定义指标收集器接口
type MetricsCollector interface {
	// RecordHookExecution 记录Hook执行
	RecordHookExecution(hookName string, phase Phase, duration time.Duration, success bool)

	// RecordHookFailure 记录Hook失败
	RecordHookFailure(hookName string, phase Phase, errorType string)

	// RecordHookSkipped 记录Hook被跳过
	RecordHookSkipped(hookName string, phase Phase)

	// RecordHookTimeout 记录Hook超时
	RecordHookTimeout(hookName string, phase Phase)
}

// Logger 定义日志接口
type Logger interface {
	Info(msg string, args ...interface{})
	Error(msg string, args ...interface{})
	Debug(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
}
