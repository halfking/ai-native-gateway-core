package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// HookRegistry 实现了Registry接口，管理所有Hook的注册和执行
type HookRegistry struct {
	// hooks 存储按Phase分组的Hook列表
	hooks map[Phase][]Hook

	// hooksByName 存储Hook名称到Hook的映射
	hooksByName map[string]Hook

	// configManager 配置管理器
	configManager ConfigManager

	// metrics 指标收集器
	metrics MetricsCollector

	// logger 日志记录器
	logger Logger

	// mu 保护并发访问
	mu sync.RWMutex
}

// NewHookRegistry 创建新的HookRegistry实例
func NewHookRegistry(configManager ConfigManager, metrics MetricsCollector, logger Logger) *HookRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = NewNoOpMetricsCollector()
	}

	return &HookRegistry{
		hooks:         make(map[Phase][]Hook),
		hooksByName:   make(map[string]Hook),
		configManager: configManager,
		metrics:       metrics,
		logger:        logger,
	}
}

// Register 注册Hook到Registry
func (r *HookRegistry) Register(hook Hook) error {
	if hook == nil {
		return fmt.Errorf("hook cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := hook.Name()
	if name == "" {
		return fmt.Errorf("hook name cannot be empty")
	}

	// 检查是否已注册
	if _, exists := r.hooksByName[name]; exists {
		return fmt.Errorf("hook %s already registered", name)
	}

	phase := hook.Phase()
	if !phase.IsValid() {
		return fmt.Errorf("invalid phase %s for hook %s", phase, name)
	}

	// 添加到按名称索引的map
	r.hooksByName[name] = hook

	// 添加到按Phase分组的列表
	r.hooks[phase] = append(r.hooks[phase], hook)

	// 按Priority排序
	sort.Slice(r.hooks[phase], func(i, j int) bool {
		return r.hooks[phase][i].Priority() < r.hooks[phase][j].Priority()
	})

	r.logger.Info("hook registered",
		"name", name,
		"phase", phase,
		"priority", hook.Priority(),
		"enabled", hook.Enabled())

	return nil
}

// Execute 执行指定Phase的Hook链
func (r *HookRegistry) Execute(ctx context.Context, phase Phase, env *Environment) error {
	if env == nil {
		return fmt.Errorf("environment cannot be nil")
	}

	if !phase.IsValid() {
		return fmt.Errorf("invalid phase: %s", phase)
	}

	r.mu.RLock()
	hooks := r.hooks[phase]
	r.mu.RUnlock()

	if len(hooks) == 0 {
		return nil
	}

	r.logger.Debug("executing hook chain",
		"phase", phase,
		"hook_count", len(hooks),
		"request_id", env.RequestID)

	for _, hook := range hooks {
		// 检查是否应该继续
		if !env.ShouldContinue() {
			r.logger.Debug("hook chain interrupted",
				"phase", phase,
				"skip", env.Skip,
				"abort", env.Abort)
			break
		}

		// 检查Hook是否启用
		if !hook.Enabled() {
			r.metrics.RecordHookSkipped(hook.Name(), phase)
			r.logger.Debug("hook skipped (disabled)",
				"name", hook.Name(),
				"phase", phase)
			continue
		}

		// 执行Hook
		if err := r.executeHook(ctx, hook, phase, env); err != nil {
			// 根据Phase决定错误处理策略
			if r.isCriticalPhase(phase) {
				// 关键阶段，返回错误
				return fmt.Errorf("hook %s failed: %w", hook.Name(), err)
			}
			// 非关键阶段，记录错误但继续
			r.logger.Error("hook failed (non-critical)",
				"name", hook.Name(),
				"phase", phase,
				"error", err)
			r.metrics.RecordHookFailure(hook.Name(), phase, "execution_error")
		}
	}

	return nil
}

// executeHook 执行单个Hook，包含超时控制和指标记录
func (r *HookRegistry) executeHook(ctx context.Context, hook Hook, phase Phase, env *Environment) error {
	hookName := hook.Name()
	startTime := time.Now()

	// 获取超时配置
	timeout := r.getHookTimeout(hookName)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 在goroutine中执行Hook
	done := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- fmt.Errorf("hook panicked: %v", rec)
			}
		}()
		done <- hook.Execute(ctx, env)
	}()

	// 等待完成或超时
	var err error
	select {
	case err = <-done:
		duration := time.Since(startTime)
		success := err == nil
		r.metrics.RecordHookExecution(hookName, phase, duration, success)

		if success {
			r.logger.Debug("hook executed successfully",
				"name", hookName,
				"phase", phase,
				"duration_ms", duration.Milliseconds())
		} else {
			r.logger.Error("hook execution failed",
				"name", hookName,
				"phase", phase,
				"duration_ms", duration.Milliseconds(),
				"error", err)
		}

	case <-ctx.Done():
		duration := time.Since(startTime)
		r.metrics.RecordHookTimeout(hookName, phase)
		r.logger.Error("hook timeout",
			"name", hookName,
			"phase", phase,
			"timeout", timeout,
			"duration_ms", duration.Milliseconds())
		err = fmt.Errorf("hook %s timeout after %v", hookName, timeout)
	}

	return err
}

// GetHooks 获取指定Phase的所有Hook
func (r *HookRegistry) GetHooks(phase Phase) []Hook {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hooks := r.hooks[phase]
	// 返回副本，避免外部修改
	result := make([]Hook, len(hooks))
	copy(result, hooks)
	return result
}

// GetHookByName 根据名称获取Hook
func (r *HookRegistry) GetHookByName(name string) (Hook, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hook, exists := r.hooksByName[name]
	return hook, exists
}

// ReloadConfig 重新加载配置
func (r *HookRegistry) ReloadConfig() error {
	if r.configManager == nil {
		return nil
	}

	// 重新加载配置文件
	if err := r.configManager.Load(); err != nil {
		return fmt.Errorf("failed to reload config: %w", err)
	}

	r.logger.Info("configuration reloaded")

	// 通知支持配置热更新的Hook
	r.mu.RLock()
	defer r.mu.RUnlock()

	for name, hook := range r.hooksByName {
		if configurableHook, ok := hook.(ConfigurableHook); ok {
			config := r.configManager.GetHookConfig(name)
			if err := configurableHook.OnConfigChange(config); err != nil {
				r.logger.Error("hook config change failed",
					"name", name,
					"error", err)
			} else {
				r.logger.Debug("hook config updated",
					"name", name)
			}
		}
	}

	return nil
}

// getHookTimeout 获取Hook的超时时间
func (r *HookRegistry) getHookTimeout(hookName string) time.Duration {
	if r.configManager != nil {
		return r.configManager.GetHookTimeout(hookName)
	}
	// 默认超时5秒
	return 5 * time.Second
}

// isCriticalPhase 判断是否为关键阶段
func (r *HookRegistry) isCriticalPhase(phase Phase) bool {
	// PreRouting和Routing阶段的失败会中止请求
	return phase == PhasePreRouting || phase == PhaseRouting
}

// NoOpLogger 实现无操作的Logger
type NoOpLogger struct{}

func (l *NoOpLogger) Info(msg string, args ...interface{})  {}
func (l *NoOpLogger) Error(msg string, args ...interface{}) {}
func (l *NoOpLogger) Debug(msg string, args ...interface{}) {}
func (l *NoOpLogger) Warn(msg string, args ...interface{})  {}
