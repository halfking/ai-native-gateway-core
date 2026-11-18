// Package main - demo_extpoints.go
//
// Demo ExtensionPoint 插件注册示例
//
// 本文件演示如何在 gateway 启动时注册 ExtensionPoint 插件。
// 这些插件会在生产请求流经 Pipeline 时自动执行。
//
// 使用方式：
//   在 main.go 的 buildV2DispatchPipeline 调用之后，
//   调用 registerDemoExtensionPoints(deps) 注册 demo 插件。

package main

import (
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/extpoints"
)

// registerDemoExtensionPoints 注册演示用的 ExtensionPoint 插件。
//
// 这些插件展示了如何在各个 Phase 注入自定义逻辑：
//   - DemoAuthPlugin: 在 PhaseAuth 记录认证信息
//   - DemoHeaderPlugin: 在 PhasePreRoute 注入自定义 header
//   - DemoMetricsPlugin: 在 PhasePostResponse 记录请求指标
//
// 注意：这些是演示插件，生产环境应该根据实际需求实现专用插件。
func registerDemoExtensionPoints(deps *v2DispatchDeps) error {
	if deps == nil || deps.ExtensionPointRegistry == nil {
		slog.Warn("registerDemoExtensionPoints: registry not available, skipping demo plugins")
		return nil
	}

	registry := deps.ExtensionPointRegistry

	// 1. 认证阶段插件：记录认证事件
	authPlugin := NewDemoAuthPlugin()
	if err := registry.Register(extpoints.PhaseAuth, authPlugin); err != nil {
		slog.Error("failed to register DemoAuthPlugin", "error", err)
		return err
	}
	slog.Info("registered DemoAuthPlugin", "phase", extpoints.PhaseAuth, "priority", extpoints.PriorityNormal)

	// 2. 路由前插件：注入自定义 header
	headerPlugin := NewDemoHeaderPlugin()
	if err := registry.Register(extpoints.PhasePreRoute, headerPlugin); err != nil {
		slog.Error("failed to register DemoHeaderPlugin", "error", err)
		return err
	}
	slog.Info("registered DemoHeaderPlugin", "phase", extpoints.PhasePreRoute, "priority", extpoints.PriorityNormal)

	// 3. 响应后插件：记录请求指标
	metricsPlugin := NewDemoMetricsPlugin()
	if err := registry.Register(extpoints.PhasePostResponse, metricsPlugin); err != nil {
		slog.Error("failed to register DemoMetricsPlugin", "error", err)
		return err
	}
	slog.Info("registered DemoMetricsPlugin", "phase", extpoints.PhasePostResponse, "priority", extpoints.PriorityNormal)

	slog.Info("demo ExtensionPoint plugins registered successfully", "count", 3)
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// Demo Plugin 1: 认证事件记录
// ────────────────────────────────────────────────────────────────────────────

// DemoAuthPlugin 在认证阶段记录请求信息。
type DemoAuthPlugin struct {
	*extpoints.BaseExtensionPoint
}

// NewDemoAuthPlugin 创建一个认证演示插件。
func NewDemoAuthPlugin() *DemoAuthPlugin {
	metadata := &extpoints.PluginMetadata{
		PluginID:   "demo-auth-logger",
		Version:    "1.0.0",
		Phase:      extpoints.PhaseAuth,
		Priority:   extpoints.PriorityNormal,
		Capabilities: []extpoints.PluginCapability{
			extpoints.CapabilityObserveRequest,
			extpoints.CapabilityAuditLog,
		},
	}

	return &DemoAuthPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(metadata),
	}
}

// Execute 实现插件逻辑：记录认证事件。
func (p *DemoAuthPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	start := time.Now()

	// 从 State 中读取请求信息（由 ExecutionEngine 填充）
	requestID := ctx.RequestID
	
	// 记录认证事件
	slog.Info("demo auth plugin executed",
		"plugin_id", p.Metadata().PluginID,
		"request_id", requestID,
		"phase", ctx.Phase,
	)

	// 将认证结果存入 State，供后续插件使用
	ctx.State["demo_auth_checked"] = true
	ctx.State["demo_auth_time"] = time.Now()

	return &extpoints.ExecutionResult{
		Success: true,
		Latency: time.Since(start),
		Metadata: map[string]interface{}{
			"auth_checked": true,
			"timestamp":    time.Now().Unix(),
		},
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Demo Plugin 2: Header 注入
// ────────────────────────────────────────────────────────────────────────────

// DemoHeaderPlugin 在路由前注入自定义 header。
type DemoHeaderPlugin struct {
	*extpoints.BaseExtensionPoint
}

// NewDemoHeaderPlugin 创建一个 header 注入演示插件。
func NewDemoHeaderPlugin() *DemoHeaderPlugin {
	metadata := &extpoints.PluginMetadata{
		PluginID:   "demo-header-injector",
		Version:    "1.0.0",
		Phase:      extpoints.PhasePreRoute,
		Priority:   extpoints.PriorityNormal,
		Capabilities: []extpoints.PluginCapability{
			extpoints.CapabilityMutateRequest,
		},
	}

	return &DemoHeaderPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(metadata),
	}
}

// Execute 实现插件逻辑：注入自定义 header。
func (p *DemoHeaderPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	start := time.Now()

	// 从 State 中读取上游插件的信息
	authChecked, _ := ctx.State["demo_auth_checked"].(bool)

	slog.Debug("demo header plugin executed",
		"plugin_id", p.Metadata().PluginID,
		"request_id", ctx.RequestID,
		"auth_checked", authChecked,
	)

	// 标记已注入 header（实际注入由 PipelineRequest 处理）
	ctx.State["demo_header_injected"] = true
	ctx.State["demo_custom_header_value"] = "x-demo-plugin-v1"

	return &extpoints.ExecutionResult{
		Success:  true,
		Modified: true, // 标记修改了请求
		Latency:  time.Since(start),
		Metadata: map[string]interface{}{
			"header_injected": true,
			"header_name":     "X-Demo-Plugin",
			"header_value":    "v1.0",
		},
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Demo Plugin 3: 请求指标记录
// ────────────────────────────────────────────────────────────────────────────

// DemoMetricsPlugin 在响应后记录请求指标。
type DemoMetricsPlugin struct {
	*extpoints.BaseExtensionPoint
}

// NewDemoMetricsPlugin 创建一个指标记录演示插件。
func NewDemoMetricsPlugin() *DemoMetricsPlugin {
	metadata := &extpoints.PluginMetadata{
		PluginID:   "demo-metrics-recorder",
		Version:    "1.0.0",
		Phase:      extpoints.PhasePostResponse,
		Priority:   extpoints.PriorityNormal,
		Capabilities: []extpoints.PluginCapability{
			extpoints.CapabilityMetrics,
		},
	}

	return &DemoMetricsPlugin{
		BaseExtensionPoint: extpoints.NewBaseExtensionPoint(metadata),
	}
}

// Execute 实现插件逻辑：记录请求指标。
func (p *DemoMetricsPlugin) Execute(ctx *extpoints.ExecutionContext) (*extpoints.ExecutionResult, error) {
	start := time.Now()

	// 从 State 中读取整个请求链路的信息
	authTime, _ := ctx.State["demo_auth_time"].(time.Time)
	headerInjected, _ := ctx.State["demo_header_injected"].(bool)

	// 计算总耗时
	var authLatency time.Duration
	if !authTime.IsZero() {
		authLatency = time.Since(authTime)
	}

	slog.Info("demo metrics plugin executed",
		"plugin_id", p.Metadata().PluginID,
		"request_id", ctx.RequestID,
		"auth_latency_ms", authLatency.Milliseconds(),
		"header_injected", headerInjected,
		"total_plugins_in_chain", len(ctx.State),
	)

	return &extpoints.ExecutionResult{
		Success: true,
		Latency: time.Since(start),
		Metadata: map[string]interface{}{
			"metrics_recorded": true,
			"request_duration": authLatency.Milliseconds(),
			"plugins_count":    len(ctx.State),
		},
	}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// 辅助函数：列出已注册插件
// ────────────────────────────────────────────────────────────────────────────

// listRegisteredPlugins 列出所有已注册的 ExtensionPoint 插件（调试用）。
func listRegisteredPlugins(registry extpoints.ExtensionPointRegistry) {
	if registry == nil {
		slog.Warn("listRegisteredPlugins: registry is nil")
		return
	}

	allPlugins := registry.List()
	total := 0

	for phase, plugins := range allPlugins {
		if len(plugins) == 0 {
			continue
		}

		slog.Info("plugins registered in phase",
			"phase", phase,
			"count", len(plugins),
		)

		for i, plugin := range plugins {
			md := plugin.Metadata()
			if md != nil {
				slog.Debug("plugin details",
					"index", i,
					"plugin_id", md.PluginID,
					"version", md.Version,
					"priority", md.Priority,
				)
				total++
			}
		}
	}

	slog.Info("total ExtensionPoint plugins registered", "count", total)
}
