package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"                 //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance"      // Verdict 类型
	"github.com/kaixuan/llm-gateway-go/domains/moduleexec"     // 模块执行记录器
	"github.com/kaixuan/llm-gateway-go/domains/moduleregistry" // 模块标识注册表
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/settings"
)

// securityConfigTTL 是配置缓存的刷新间隔。
//
// ★ 2026-11-23（12h 变更审计 B）：与 v1 hook（domains/hooks/security/hook.go
// 的 securityConfigTTL）**同值**，刻意不另立口径 —— 两处读的是同一批
// security.* 配置，TTL 不一致会让「改了配置什么时候生效」出现两种答案。
const securityConfigTTL = 5 * time.Second

// SecurityHook 把 Registry 接入 Pipeline。
//
// 行为：
//   - Enabled: env != nil 且 security.enabled（配置按 TTL 热加载，见
//     maybeReloadConfig）
//   - Execute: 调 Registry.RunAll 并把所有 verdict 写入 env.EnsureGovernance()
//   - OnError: 吞掉 error（verdict 已写入，interception 引擎接管决策；
//     若某 plugin 直接 return error，Registry.RunAll 已把错误转 verdict）
//
// 适用阶段：PhaseGovernance。
//
// 2026-07-10: 集成模块执行器，支持 Check-Execute-Record 模式。
// 2026-11-23: 添加 settings 支持，让 security.enabled 和 security.mode 配置生效。
// 2026-11-23（12h 变更审计 B）: 补热加载 —— 此前配置只在构造时快照一次，
// 运行时改 settings 不生效（而 spec 声明 HotReload:true）。
type SecurityHook struct {
	registry *Registry
	scope    Scope
	executor *moduleexec.Executor // 模块执行记录器（可选）
	settings *settings.Registry   // 配置访问器
	enabled  atomic.Bool          // security.enabled 配置缓存
	mode     atomic.Value         // security.mode 配置缓存 ("enforce" or "observe")
	// reloadAtUnixNano 是下一次允许重新加载配置的时间点（UnixNano）。
	reloadAtUnixNano atomic.Int64
	// reloading 保证同一时刻只有一个后台刷新在跑。
	reloading atomic.Bool
	// now 是时钟（测试可注入）；构造后不再改 ⇒ 并发读安全。
	now func() time.Time
}

// NewSecurityHook 构造 hook。
// settings 可选（nil 时默认启用）。
func NewSecurityHook(registry *Registry, scope Scope, settingsReg *settings.Registry) *SecurityHook {
	return newSecurityHook(registry, scope, settingsReg, time.Now)
}

// newSecurityHook 是带可注入时钟的构造（生产走 NewSecurityHook；测试用它把
// TTL 语义钉住，不必真等 5 秒）。
func newSecurityHook(registry *Registry, scope Scope, settingsReg *settings.Registry, now func() time.Time) *SecurityHook {
	if registry == nil {
		registry = NewRegistry()
	}
	if now == nil {
		now = time.Now
	}
	h := &SecurityHook{
		registry: registry,
		scope:    scope,
		settings: settingsReg,
		now:      now,
	}
	// ★ 构造时同步加载一次 ⇒ 「构造后立刻请求」用的是**当前**配置（不是等
	// 一个 TTL 周期）。
	h.loadConfig()
	h.reloadAtUnixNano.Store(h.now().Add(securityConfigTTL).UnixNano())
	return h
}

// maybeReloadConfig 在 TTL 到期时异步触发一次配置刷新（v1 同款范式）。
//
// ★ 2026-11-23（12h 变更审计 B）：v1 hook（domains/hooks/security/hook.go:
// currentConfig）已经用「TTL 节流 + 无锁 atomic Load + 后台刷新」这套做法，
// v2 沿用它而不是另造一套：读 settings 后端可能有 I/O ⇒ 绝不放在请求热路径
// 上同步做。
//
// 语义（与 v1 一致）：**触发刷新的那一次请求仍然用旧值**（刷新在后台跑）⇒
// 配置变更从「TTL 到期后的下一次请求」起生效。
func (h *SecurityHook) maybeReloadConfig() {
	if h == nil || h.settings == nil {
		return // 没有 settings 后端 ⇒ 没有可刷新的配置（保持构造时的快照）
	}
	now := h.now()
	deadline := h.reloadAtUnixNano.Load()
	if now.UnixNano() < deadline {
		return // TTL 未到
	}
	if !h.reloadAtUnixNano.CompareAndSwap(deadline, now.Add(securityConfigTTL).UnixNano()) {
		return // 别的请求已经抢到这次刷新
	}
	if !h.reloading.CompareAndSwap(false, true) {
		return // 已有刷新在途
	}
	go func() {
		defer h.reloading.Store(false)
		h.loadConfig()
	}()
}

// loadConfig 从 settings 读取 security.enabled 和 security.mode 配置。
//
// 背景（2026-11-23 211 生产实证）：v2 governance 管线的 SecurityHook
// 此前不读取 security.enabled 和 security.mode —— 211 settings_kv 已写配置，
// 但检测器照常拦截 ⇒ 运维关不掉。本次修复落地 spec 承诺：
//   - security.enabled = false: 跳过所有安全检测
//   - security.mode = "observe": 检测但不拦截，只记录
func (h *SecurityHook) loadConfig() {
	enabled := true      // 默认启用（向后兼容，settings == nil 时不改变行为）
	mode := "enforce"    // 默认拦截模式

	if h.settings != nil {
		// 读取 security.enabled
		if sp := h.settings.Spec("security.enabled"); sp != nil {
			if v, _, err := h.settings.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
				var b bool
				if json.Unmarshal(v, &b) == nil {
					enabled = b
				}
			}
		}
		
		// 读取 security.mode
		if sp := h.settings.Spec("security.mode"); sp != nil {
			if v, _, err := h.settings.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
				var m string
				if json.Unmarshal(v, &m) == nil {
					mode = m
				}
			}
		}
	}

	h.enabled.Store(enabled)
	h.mode.Store(mode)
}

// SetExecutor 注入模块执行器。
// 启用 Check-Execute-Record 模式，相同请求的安全扫描结果会被缓存。
func (h *SecurityHook) SetExecutor(exec *moduleexec.Executor) {
	if h == nil {
		return
	}
	h.executor = exec
}

// Name 返回 hook 名（用于日志/调试）。
func (h *SecurityHook) Name() string { return "security.plugins" }

// Priority 优先级（与 v3 同名 hook 一致；同 phase 内排序使用）。
func (h *SecurityHook) Priority() int { return 100 }

// Enabled 报告 hook 是否启用。
//
// 2026-11-23 修复：增加 security.enabled 配置检查。
// 当 security.enabled = false 时，跳过所有安全检测（落地 spec 承诺）。
//
// ★ 2026-11-23（12h 变更审计 B）：这里顺带触发 TTL 热加载（不阻塞请求），
// 使运行时改 security.enabled 能在 TTL 内生效（见 maybeReloadConfig）。
func (h *SecurityHook) Enabled(_ context.Context, env *domain.PipelineRequest) bool {
	if env == nil {
		return false
	}
	h.maybeReloadConfig()
	return h.enabled.Load()
}

// Execute 调 Registry.RunAll 并写入 env.Governance。
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// ★ 2026-11-23（12h 变更审计 B）：Execute 也触发一次 TTL 热加载 ——
	// security.mode 是 observe/enforce 的来源，只在 Enabled 里刷新的话，
	// 绕过 Enabled 直接调 Execute 的路径会一直用旧 mode。
	h.maybeReloadConfig()
	// 2026-07-10: 通过执行器执行安全扫描，结果会被缓存
	if h.executor != nil && env.SessionID != "" {
		return h.executeWithCache(ctx, env)
	}
	return h.executeDirectly(ctx, env)
}

// executeWithCache 通过执行器执行安全扫描（带缓存）
func (h *SecurityHook) executeWithCache(ctx context.Context, env *domain.PipelineRequest) error {
	params := securityParamsFromRequest(env)

	execResult, err := h.executor.CheckAndExecute(
		ctx, env.SessionID, env.TenantID,
		moduleregistry.ModuleSecurityScan,
		params, 0, // 使用模块默认 TTL（30分钟）
		func(ctx context.Context) (*moduleexec.ExecuteResult, error) {
			verdicts, err := h.registry.RunAll(ctx, env, h.scope)
			if err != nil {
				return nil, err
			}
			verdicts = h.applyObserveMode(verdicts)
			// 写入 Governance 状态
			state := env.EnsureGovernance()
			for _, v := range verdicts {
				state.RecordVerdict(v)
			}
			return &moduleexec.ExecuteResult{
				ResultSummary: verdictsToSummaryMap(verdicts),
				ResultDetail:  verdictsToDetailMap(verdicts),
			}, nil
		},
	)
	if err != nil {
		return err
	}

	// 如果是从缓存获取的，仍然需要写入 Governance 状态
	if execResult.FromCache {
		return h.applyCachedVerdicts(ctx, env, execResult)
	}
	return nil
}

// executeDirectly 直接执行安全扫描（不经过执行器）
func (h *SecurityHook) executeDirectly(ctx context.Context, env *domain.PipelineRequest) error {
	verdicts, err := h.registry.RunAll(ctx, env, h.scope)
	if err != nil {
		return err
	}
	verdicts = h.applyObserveMode(verdicts)
	state := env.EnsureGovernance()
	for _, v := range verdicts {
		state.RecordVerdict(v)
	}
	return nil
}

// applyCachedVerdicts 从缓存结果还原 verdicts 并写入 Governance
func (h *SecurityHook) applyCachedVerdicts(ctx context.Context, env *domain.PipelineRequest, result *moduleexec.ExecuteResult) error {
	// 从缓存结果中还原 verdicts
	verdicts, err := mapToVerdicts(result.ResultDetail)
	if err != nil {
		// 降级：如果缓存数据损坏，重新执行扫描
		return h.executeDirectly(ctx, env)
	}

	verdicts = h.applyObserveMode(verdicts)
	// 写入 Governance 状态
	state := env.EnsureGovernance()
	for _, v := range verdicts {
		state.RecordVerdict(v)
	}

	return nil
}

// mapToVerdicts 从 detail map 还原 verdicts
func mapToVerdicts(detail map[string]interface{}) ([]*governance.Verdict, error) {
	if detail == nil {
		return []*governance.Verdict{}, nil
	}

	verdictsRaw, ok := detail["verdicts"]
	if !ok {
		return []*governance.Verdict{}, nil
	}

	verdictsArray, ok := verdictsRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("verdicts is not an array, got %T", verdictsRaw)
	}

	verdicts := make([]*governance.Verdict, 0, len(verdictsArray))
	for i, vr := range verdictsArray {
		vm, ok := vr.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("verdict[%d] is not a map, got %T", i, vr)
		}

		verdict := &governance.Verdict{}

		// 安全的类型转换
		if v, ok := vm["plugin_name"].(string); ok {
			verdict.PluginName = v
		}
		if v, ok := vm["allow"].(bool); ok {
			verdict.Allow = v
		}
		if v, ok := vm["severity"].(float64); ok {
			verdict.Severity = int(v)
		}
		if v, ok := vm["code"].(string); ok {
			verdict.Code = v
		}
		if v, ok := vm["reason"].(string); ok {
			verdict.Reason = v
		}
		if v, ok := vm["fix_action"].(string); ok {
			verdict.FixAction = v
		}

		verdicts = append(verdicts, verdict)
	}

	return verdicts, nil
}

// securityParamsFromRequest 从请求构造缓存参数
func securityParamsFromRequest(env *domain.PipelineRequest) map[string]interface{} {
	// 使用请求内容的哈希作为缓存键
	var contentHash string
	if env.Envelope != nil && len(env.Envelope.Transport.BodyBytes) > 0 {
		h := sha256.Sum256(env.Envelope.Transport.BodyBytes)
		contentHash = hex.EncodeToString(h[:])[:16]
	}
	return map[string]interface{}{
		"content_hash": contentHash,
	}
}

// verdictsToSummaryMap 将 verdicts 转换为 summary map
func verdictsToSummaryMap(verdicts []*governance.Verdict) map[string]interface{} {
	allowCount, blockCount := 0, 0
	for _, v := range verdicts {
		if v.Allow {
			allowCount++
		} else {
			blockCount++
		}
	}
	return map[string]interface{}{
		"total":       len(verdicts),
		"allow_count": allowCount,
		"block_count": blockCount,
	}
}

// verdictsToDetailMap 将 verdicts 转换为 detail map
func verdictsToDetailMap(verdicts []*governance.Verdict) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(verdicts))
	for _, v := range verdicts {
		items = append(items, map[string]interface{}{
			"plugin_name": v.PluginName,
			"allow":       v.Allow,
			"severity":    v.Severity,
			"code":        v.Code,
			"reason":      v.Reason,
			"fix_action":  v.FixAction,
		})
	}
	return map[string]interface{}{"verdicts": items}
}

// OnError 吞掉错误（verdict 已写入；interception 引擎在后续阶段做决策）。
func (h *SecurityHook) OnError(_ context.Context, _ *domain.PipelineRequest, err error) error {
	return nil
}

// Registry 返回内部 registry（用于测试与 telemetry）。
func (h *SecurityHook) Registry() *Registry { return h.registry }

// applyObserveMode 在 observe 模式下将所有 deny verdict 转为 allow。
//
// 背景（2026-11-23）：security.mode = "observe" 时，所有安全检测继续运行
// 但不拦截请求，只在日志中记录威胁信息。这允许在生产环境中：
//   1. 保持安全监控能力
//   2. 收集威胁情报
//   3. 不影响正常业务
func (h *SecurityHook) applyObserveMode(verdicts []*governance.Verdict) []*governance.Verdict {
	mode := h.mode.Load()
	if mode == nil {
		return verdicts
	}

	modeStr, ok := mode.(string)
	if !ok || modeStr != "observe" {
		return verdicts
	}

	// observe 模式：将所有 deny 改为 allow，但保留原因说明
	for _, v := range verdicts {
		if !v.Allow {
			v.Allow = true
			v.Reason = fmt.Sprintf("[observe] %s", v.Reason)
		}
	}
	return verdicts
}

// 编译期断言。
var _ pipeline.Hook = (*SecurityHook)(nil)
