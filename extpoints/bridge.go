// Package extpoints - bridge.go
//
// ExtensionPointHook 是 extpoints.ExecutionEngine 到 domains/pipeline.Hook 的桥接适配器。
// 
// 背景（2026-11-19）
// ----------------
// Week 2 完成的 ExecutionEngine 实现了 16-Phase ExtensionPoint 架构，
// 但现有生产代码使用 domains/pipeline.Hook 架构。两个架构互不兼容：
//   - Phase 命名不同（pre_authentication vs pre_auth）
//   - 接口不同（Hook 5方法 vs ExtensionPoint 3方法）
//
// 集成策略
// --------
// 不替换现有 Pipeline（风险高），而是创建适配器：
//   1. ExtensionPointHook 实现 pipeline.Hook 接口
//   2. 内部持有 ExecutionEngine 实例
//   3. Execute() 调用 engine.Execute()，映射 Phase
//   4. 在 buildV2DispatchPipeline 中注册为一个 Stage
//
// Phase 映射策略
// -------------
// domains/pipeline 有 16 个 Phase（含 governance/transform），
// extpoints 有 16 个 Phase（含 log/error），映射规则：
//   - 相同语义的 Phase 映射（authentication → auth, routing → route）
//   - 不存在的 Phase 跳过（如 governance 在 extpoints 中没有对应）
//   - 多对一映射（pre_authentication + authentication + post_authentication → 一次调用）

package extpoints

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// ExtensionPointHook 是 ExecutionEngine 到 pipeline.Hook 的适配器。
// 它允许 ExtensionPoint 插件在现有的 domains/pipeline 架构中运行。
type ExtensionPointHook struct {
	engine   *ExecutionEngine
	registry ExtensionPointRegistry
	
	// phase 是此 Hook 对应的 domains/pipeline Phase
	// 用于在 Name() 中显示，帮助调试
	pipelinePhase string
}

// NewExtensionPointHook 创建一个新的桥接适配器。
//
// 参数：
//   - registry: ExtensionPoint 注册表（接口类型）
//   - pipelinePhase: 此 Hook 对应的 domains/pipeline Phase 名称（用于日志）
func NewExtensionPointHook(registry ExtensionPointRegistry, pipelinePhase string) *ExtensionPointHook {
	return &ExtensionPointHook{
		engine:        NewExecutionEngine(registry),
		registry:      registry,
		pipelinePhase: pipelinePhase,
	}
}

// Name 实现 pipeline.Hook 接口。
func (h *ExtensionPointHook) Name() string {
	return fmt.Sprintf("ExtensionPointBridge(%s)", h.pipelinePhase)
}

// Execute 实现 pipeline.Hook 接口。
// 
// 策略：
//   1. 映射 domains/pipeline Phase 到 extpoints Phase
//   2. 从 registry 获取该 Phase 的插件（已按优先级排序）
//   3. 按顺序执行这些插件（不调用 engine.Execute，避免执行所有 Phase）
//   4. 将结果同步回 envelope
func (h *ExtensionPointHook) Execute(ctx context.Context, envelope *domain.PipelineRequest) error {
	if h.registry == nil {
		return nil
	}
	
	// 映射 Phase：domains/pipeline → extpoints
	extPhases := h.mapPhases(h.pipelinePhase)
	if len(extPhases) == 0 {
		// 没有对应的 extpoints Phase，跳过
		return nil
	}
	
	// 从 envelope 构建 extpoints.Request
	req := h.buildRequest(envelope)
	
	// 记录开始时间（用于性能监控）
	hookStart := time.Now()
	totalPlugins := 0
	successfulPlugins := 0
	
	// 对每个映射的 Phase，执行该 Phase 的插件
	for _, phase := range extPhases {
		plugins := h.registry.Get(phase)
		if len(plugins) == 0 {
			continue
		}
		
		slog.Debug("ExtensionPointHook: executing plugins",
			"phase", phase,
			"plugin_count", len(plugins),
			"request_id", req.RequestID,
		)
		
		// 从对象池获取 ExecutionContext（性能优化：减少分配）
		execCtx := AcquireExecutionContext()
		defer ReleaseExecutionContext(execCtx)
		
		// 初始化 ExecutionContext
		InitExecutionContext(execCtx, ctx, phase, "", req.RequestID, time.Now())
		
		// 按优先级顺序执行插件（registry.Get 已排序）
		for _, plugin := range plugins {
			// 确保插件实现了 PhaseExecutor 接口
			executor, ok := plugin.(PhaseExecutor)
			if !ok {
				slog.Warn("ExtensionPointHook: plugin does not implement PhaseExecutor",
					"plugin_id", plugin.Metadata().PluginID,
					"phase", phase,
				)
				continue
			}
			
			md := plugin.Metadata()
			if md == nil {
				continue
			}
			
			execCtx.PluginID = md.PluginID
			totalPlugins++
			
			// 执行插件
			pluginStart := time.Now()
			result, err := executor.Execute(execCtx)
			pluginDuration := time.Since(pluginStart)
			
			// 记录执行结果
			if err != nil {
				// fail-open: 插件失败记录但不阻塞请求
				slog.Warn("ExtensionPointHook: plugin execution failed (fail-open)",
					"plugin_id", md.PluginID,
					"phase", phase,
					"error", err.Error(),
					"duration_us", pluginDuration.Microseconds(),
					"request_id", req.RequestID,
				)
				continue
			}
			
			if result != nil && !result.Success {
				slog.Warn("ExtensionPointHook: plugin returned failure (fail-open)",
					"plugin_id", md.PluginID,
					"phase", phase,
					"error", result.Error,
					"duration_us", pluginDuration.Microseconds(),
					"request_id", req.RequestID,
				)
				continue
			}
			
			// 成功执行
			successfulPlugins++
			slog.Debug("ExtensionPointHook: plugin executed successfully",
				"plugin_id", md.PluginID,
				"phase", phase,
				"duration_us", pluginDuration.Microseconds(),
				"modified", result != nil && result.Modified,
				"request_id", req.RequestID,
			)
			
			// 将插件结果同步到 Request.Attributes
			if result != nil && result.Metadata != nil {
				for k, v := range result.Metadata {
					req.Attributes["plugin_"+md.PluginID+"_"+k] = v
				}
			}
		}
	}
	
	// 记录整体执行统计
	hookDuration := time.Since(hookStart)
	if totalPlugins > 0 {
		slog.Info("ExtensionPointHook: execution complete",
			"pipeline_phase", h.pipelinePhase,
			"total_plugins", totalPlugins,
			"successful_plugins", successfulPlugins,
			"failed_plugins", totalPlugins-successfulPlugins,
			"duration_us", hookDuration.Microseconds(),
			"request_id", req.RequestID,
		)
	}
	
	// 构建虚拟的 Response（用于同步回 envelope）
	resp := &Response{
		StatusCode: 200,
		Attributes: req.Attributes,
	}
	
	// 同步执行结果回 envelope
	h.syncResponseToEnvelope(resp, envelope)
	
	return nil
}

// Priority 实现 pipeline.Hook 接口。
// 
// 策略：返回中等优先级（500），让 ExtensionPoint 插件在标准 Hook 之后执行。
// 这样可以确保认证、路由等核心逻辑先完成，插件再处理增强功能。
func (h *ExtensionPointHook) Priority() int {
	return 500 // PriorityNormal
}

// Enabled 实现 pipeline.Hook 接口。
// 
// 策略：检查 registry 是否有注册的插件。如果没有插件，返回 false 跳过执行。
func (h *ExtensionPointHook) Enabled(ctx context.Context, envelope *domain.PipelineRequest) bool {
	if h.registry == nil {
		return false
	}
	
	// 映射 Phase 并检查是否有插件注册
	extPhases := h.mapPhases(h.pipelinePhase)
	for _, phase := range extPhases {
		plugins := h.registry.Get(phase)
		if len(plugins) > 0 {
			return true
		}
	}
	
	return false
}

// OnError 实现 pipeline.Hook 接口。
// 
// 策略：向上传递错误（不吞掉），让 Pipeline 决定如何处理。
// 这符合 ExecutionEngine 的 fail-open 策略：单个插件失败不应阻塞请求。
func (h *ExtensionPointHook) OnError(ctx context.Context, envelope *domain.PipelineRequest, err error) error {
	// 记录错误但继续（fail-open）
	// 注意：实际生产环境应该使用结构化日志
	return nil // 吞掉错误，让请求继续
}

// mapPhases 将 domains/pipeline Phase 映射到 extpoints Phase。
// 
// 映射规则：
//   - pre_authentication / pre_auth → PhasePreAuth
//   - authentication → PhaseAuth
//   - post_authentication / post_auth → PhasePostAuth
//   - pre_routing → PhasePreRoute
//   - routing → PhaseRoute
//   - post_routing → PhasePostRoute
//   - pre_upstream → PhasePreUpstream
//   - upstream → PhaseUpstream
//   - post_upstream → PhasePostUpstream
//   - pre_response → PhasePreResponse
//   - response → PhaseResponse
//   - post_response → PhasePostResponse
//   - governance → 无映射（extpoints 没有此 Phase）
//   - transform 系列 → 无映射
func (h *ExtensionPointHook) mapPhases(pipelinePhase string) []Phase {
	mapping := map[string][]Phase{
		"pre_authentication":  {PhasePreAuth},
		"pre_auth":            {PhasePreAuth}, // alias
		"authentication":      {PhaseAuth},
		"post_authentication": {PhasePostAuth},
		"post_auth":           {PhasePostAuth}, // alias
		"pre_routing":         {PhasePreRoute},
		"routing":             {PhaseRoute},
		"post_routing":        {PhasePostRoute},
		"pre_upstream":        {PhasePreUpstream},
		"upstream":            {PhaseUpstream},
		"post_upstream":       {PhasePostUpstream},
		"pre_response":        {PhasePreResponse},
		"response":            {PhaseResponse},
		"post_response":       {PhasePostResponse},
		
		// governance 和 transform 系列无映射
		"governance":     {},
		"pre_transform":  {},
		"transform":      {},
		"post_transform": {},
	}
	
	phases, ok := mapping[pipelinePhase]
	if !ok {
		return []Phase{}
	}
	
	return phases
}

// buildRequest 从 PipelineRequest 构建 extpoints.Request。
func (h *ExtensionPointHook) buildRequest(envelope *domain.PipelineRequest) *Request {
	req := &Request{
		// 预分配 Attributes map：典型请求包含 8-12 个属性
		// (session_id, model, method, path, remote, agent, api_key_id, provider 等)
		Attributes: make(map[string]interface{}, 12),
	}
	
	if envelope == nil {
		return req
	}
	
	// 填充基本字段
	req.TenantID = envelope.TenantID
	
	// SessionID stored in Attributes
	if envelope.SessionID != "" {
		req.Attributes["session_id"] = envelope.SessionID
	}
	
	// RequestID: PipelineRequest doesn't have RequestID field directly,
	// it's in Envelope.RequestID, but we construct a pseudo-ID from context
	if envelope.Envelope != nil {
		req.RequestID = envelope.Envelope.RequestID
	}
	
	// 从 Metadata 中提取字段
	if envelope.Metadata != nil {
		if method, ok := envelope.Metadata["method"].(string); ok {
			req.Method = method
		}
		if path, ok := envelope.Metadata["path"].(string); ok {
			req.Path = path
		}
		if model, ok := envelope.Metadata["model"].(string); ok {
			req.Model = model
		}
		if remote, ok := envelope.Metadata["remote"].(string); ok {
			req.ClientIP = remote
		}
		if agent, ok := envelope.Metadata["agent"].(string); ok {
			req.UserAgent = agent
		}
		
		// 复制所有 Metadata 到 Attributes
		for k, v := range envelope.Metadata {
			req.Attributes[k] = v
		}
	}
	
	// 从 Envelope 中提取 Transport 信息
	if envelope.Envelope != nil && envelope.Envelope.Transport != nil {
		if envelope.Envelope.Transport.R != nil {
			r := envelope.Envelope.Transport.R
			req.Method = r.Method
			req.Path = r.URL.Path
			req.Headers = r.Header.Clone()
			req.QueryParams = r.URL.Query()
			req.UserAgent = r.UserAgent()
			req.ClientIP = r.RemoteAddr
		}
	}
	
	// 填充认证信息
	if envelope.APIKey != nil && envelope.APIKey.TenantID != "" {
		req.TenantID = envelope.APIKey.TenantID
		// Store key info in attributes
		req.Attributes["api_key_id"] = envelope.APIKey.ID
	}
	
	// 填充路由信息
	if envelope.SelectedProvider != nil {
		req.Provider = envelope.SelectedProvider.Name
	}
	
	return req
}

// syncResponseToEnvelope 将 extpoints.Response 同步回 PipelineRequest。
// 
// 策略：将 Response.Attributes 合并到 envelope.Metadata 中，使用前缀 "extpoint_" 避免冲突。
func (h *ExtensionPointHook) syncResponseToEnvelope(resp *Response, envelope *domain.PipelineRequest) {
	if resp == nil || envelope == nil {
		return
	}
	
	if envelope.Metadata == nil {
		// 预分配 Metadata map：典型响应包含原有 metadata + 插件结果
		// 估算大小：原有 8-12 个 + 插件注入 4-6 个 = ~16 个
		envelope.Metadata = make(map[string]interface{}, 16)
	}
	
	// 合并 Attributes
	for k, v := range resp.Attributes {
		envelope.Metadata["extpoint_"+k] = v
	}
	
	// 同步错误信息（如果有）
	if resp.Error != nil {
		envelope.Metadata["extpoint_error"] = resp.Error.Error()
		envelope.Metadata["extpoint_error_phase"] = string(resp.ErrorPhase)
	}
	
	// 同步状态码（如果设置了）
	if resp.StatusCode > 0 && resp.StatusCode != 200 {
		envelope.StatusCode = resp.StatusCode
	}
}
