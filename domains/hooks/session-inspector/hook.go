package sessioninspector

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metadata keys used by InspectorHook.
const (
	// MetaKeySessionFindings session_findings 元数据键。
	// PipelineRequest.Metadata[MetaKeySessionFindings] = []*Finding
	MetaKeySessionFindings = "session_findings"

	// MetaKeyRequestCount request_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyRequestCount] = int
	MetaKeyRequestCount = "request_count"

	// MetaKeyTokenCount token_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyTokenCount] = int
	MetaKeyTokenCount = "token_count"

	// MetaKeyLastActiveAt last_active_at 元数据键。
	// PipelineRequest.Metadata[MetaKeyLastActiveAt] = time.Time
	MetaKeyLastActiveAt = "last_active_at"

	// MetaKeyStartedAt started_at 元数据键。
	// PipelineRequest.Metadata[MetaKeyStartedAt] = time.Time
	MetaKeyStartedAt = "started_at"

	// MetaKeyErrorRate error_rate 元数据键。
	// PipelineRequest.Metadata[MetaKeyErrorRate] = float64
	MetaKeyErrorRate = "error_rate"

	// MetaKeyBurstCount burst_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyBurstCount] = int
	MetaKeyBurstCount = "burst_count"

	// MetaKeyConcurrentCount concurrent_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyConcurrentCount] = int
	MetaKeyConcurrentCount = "concurrent_count"

	// MetaKeyTenantActiveCount tenant_active_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyTenantActiveCount] = int
	MetaKeyTenantActiveCount = "tenant_active_count"

	// MetaKeyModelSwitchCount model_switch_count 元数据键。
	// PipelineRequest.Metadata[MetaKeyModelSwitchCount] = int
	MetaKeyModelSwitchCount = "model_switch_count"
)

// Prometheus 指标（按 code / severity 打 label，便于 Grafana 切片）。
var (
	sessionInspectorFindingsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_inspector_findings_total",
			Help: "Total number of session inspector findings emitted by hooks",
		},
		[]string{"code", "severity"},
	)

	sessionInspectorRecycleTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llmgw_session_inspector_recycle_total",
			Help: "Total number of sessions recycled by the session inspector (idle or absolute expiry)",
		},
	)

	sessionInspectorBlockTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_session_inspector_block_total",
			Help: "Total number of session inspector findings that triggered a 429/403 block",
		},
		[]string{"code"},
	)

	sessionInspectorHookDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_session_inspector_hook_duration_seconds",
			Help:    "Duration of InspectorHook.Execute",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		},
	)
)

// EventBusPublisher 是事件发布接口（避免直接依赖 eventbus 包类型，便于测试）。
type EventBusPublisher interface {
	Publish(event eventbus.Event) error
}

// InspectorHook 检查器编排 Hook。
//
// 行为：
//   - Enabled: 读取 session_inspector.enabled 配置；env 非 nil 且 SessionID != ""
//   - Execute: 从 env 构造 SessionSnapshot，依次调用所有 Inspector；
//     收集 findings 后：①写入 env.Metadata[MetaKeySessionFindings]；
//     ②按 severity 写入 Prometheus 指标；③若告警启用则发布 EventBus 事件；
//     ④对 critical + 阻断类 finding 设置 env.StatusCode=429。
//   - OnError: 吞掉错误（检查器失败可降级，不影响主流程）。
//
// 适用阶段：PreRouting。
type InspectorHook struct {
	inspectors []Inspector
	config     *Config // 缓存最近一次 LoadConfig 的结果（仅用于 Enabled 同步判断）
	bus        EventBusPublisher
}

// NewInspectorHook 构造 Hook（兼容旧 API：传入固定 inspector 列表）。
func NewInspectorHook(inspectors ...Inspector) *InspectorHook {
	return &InspectorHook{inspectors: inspectors, config: DefaultConfig()}
}

// NewInspectorHookWithConfig 构造 Hook，按 Config 自动构建全套 inspector。
// 推荐使用：会与 settings 热更新保持同步。
func NewInspectorHookWithConfig(cfg *Config) *InspectorHook {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &InspectorHook{
		inspectors: BuildInspectorsFromConfig(cfg),
		config:     cfg,
	}
}

// SetEventBus 注入事件总线（用于发布告警事件）。传 nil 关闭事件发布。
func (h *InspectorHook) SetEventBus(bus EventBusPublisher) {
	if h == nil {
		return
	}
	h.bus = bus
}

// Add 追加一个 Inspector（允许运行时注册）。
func (h *InspectorHook) Add(i Inspector) {
	if i == nil {
		return
	}
	h.inspectors = append(h.inspectors, i)
}

// Inspectors 返回已注册的 inspector 列表（只读副本）。
func (h *InspectorHook) Inspectors() []Inspector {
	out := make([]Inspector, len(h.inspectors))
	copy(out, h.inspectors)
	return out
}

// ReloadConfig 重新读取配置并重建 inspector 列表。
// 可在 settings 热更新回调中调用。
func (h *InspectorHook) ReloadConfig() *Config {
	cfg := LoadConfig()
	h.config = cfg
	h.inspectors = BuildInspectorsFromConfig(cfg)
	return cfg
}

// BuildInspectorsFromConfig 根据 Config 构造默认 inspector 套件。
// 新接入方请直接调用此函数，保持 inspector 集合与配置一致。
func BuildInspectorsFromConfig(cfg *Config) []Inspector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return []Inspector{
		NewTokenLimitInspectorWithConfig(cfg),
		NewInactiveInspectorWithConfig(cfg),
		NewHighFrequencyInspectorWithConfig(cfg),
		NewSessionLifecycleInspectorWithConfig(cfg),
		NewErrorRateInspectorWithConfig(cfg),
		NewModelSwitchInspectorWithConfig(cfg),
	}
}

// Name 返回 Hook 名。
func (h *InspectorHook) Name() string { return "session.inspect" }

// Priority 返回优先级（晚于认证/会话解析，早于路由决策）。
func (h *InspectorHook) Priority() int { return 100 }

// Enabled 仅当配置启用且 env 非 nil 且存在 SessionID 时启用。
func (h *InspectorHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil || env.SessionID == "" {
		return false
	}
	// 每次读最新配置（轻量级 — 仅 settings.Global map 读 + json unmarshal）
	cfg := LoadConfig()
	h.config = cfg
	return cfg.Enabled
}

// Execute 顺序执行所有 Inspector 并收集 findings。
func (h *InspectorHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil || env.SessionID == "" {
		return nil
	}
	start := time.Now()
	defer func() {
		sessionInspectorHookDuration.Observe(time.Since(start).Seconds())
	}()

	snap := h.buildSnapshot(env)
	cfg := h.config
	if cfg == nil {
		cfg = DefaultConfig()
	}

	var all []*Finding
	for _, ins := range h.inspectors {
		if ins == nil {
			continue
		}
		findings, err := ins.Inspect(snap)
		if err != nil {
			// 单一 inspector 故障不上抛，单独记录后继续
			slog.Warn("session_inspector: inspector failed",
				"inspector", ins.Name(),
				"session_id", env.SessionID,
				"error", err)
			continue
		}
		all = append(all, findings...)
	}

	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}
	env.Metadata[MetaKeySessionFindings] = all

	// Prometheus 指标 + 告警事件 + 阻断
	for _, f := range all {
		if cfg.AlertPrometheusEnabled {
			sessionInspectorFindingsTotal.WithLabelValues(f.Code, string(f.Severity)).Inc()
		}
		if cfg.ShouldAlertOnSeverity(f.Severity) && h.bus != nil {
			ev := &SessionInspectorFindingEvent{
				SessionID: env.SessionID,
				TenantID:  env.TenantID,
				Finding:   f,
				Source:    "hook",
				EventTime: time.Now(),
			}
			if err := h.bus.Publish(ev); err != nil {
				slog.Warn("session_inspector: publish finding event failed",
					"session_id", env.SessionID, "error", err)
			}
		}
		// Critical + 高频类阻断（仅当 observe_only=false）
		if f.Severity == SeverityCritical {
			shouldBlock := false
			switch f.Code {
			case "TOKEN_LIMIT_EXCEEDED":
				// 仅当 warn_action=block 才阻断
				shouldBlock = cfg.IsBlockAction()
			case "BURST_EXCEEDED", "CONCURRENT_EXCEEDED", "HIGH_REQUEST_RATE":
				shouldBlock = !cfg.IsObserveOnly()
			case "SESSION_EXPIRED":
				shouldBlock = true // 绝对超期一律阻断
			}
			if shouldBlock {
				if cfg.AlertPrometheusEnabled {
					sessionInspectorBlockTotal.WithLabelValues(f.Code).Inc()
				}
				env.StatusCode = 429
				if env.Metadata == nil {
					env.Metadata = make(map[string]any)
				}
				env.Metadata["session_inspector_block"] = f.Code
			}
		}
	}

	return nil
}

// buildSnapshot 从 PipelineRequest 构造 SessionSnapshot。
func (h *InspectorHook) buildSnapshot(env *domain.PipelineRequest) *SessionSnapshot {
	snap := &SessionSnapshot{
		SessionID: env.SessionID,
		TenantID:  env.TenantID,
		Metadata:  env.Metadata,
	}
	if env.Metadata == nil {
		return snap
	}
	if rc, ok := env.Metadata[MetaKeyRequestCount].(int); ok {
		snap.RequestCount = rc
	}
	if tc, ok := env.Metadata[MetaKeyTokenCount].(int); ok {
		snap.TokenCount = tc
	}
	if la, ok := env.Metadata[MetaKeyLastActiveAt].(time.Time); ok {
		snap.LastActiveAt = la
	}
	if sa, ok := env.Metadata[MetaKeyStartedAt].(time.Time); ok {
		snap.StartedAt = sa
	}
	if er, ok := env.Metadata[MetaKeyErrorRate].(float64); ok {
		snap.ErrorRate = er
	}
	if bc, ok := env.Metadata[MetaKeyBurstCount].(int); ok {
		snap.BurstCount = bc
	}
	if cc, ok := env.Metadata[MetaKeyConcurrentCount].(int); ok {
		snap.ConcurrentCount = cc
	}
	if tac, ok := env.Metadata[MetaKeyTenantActiveCount].(int); ok {
		snap.TenantActiveCount = tac
	}
	if msc, ok := env.Metadata[MetaKeyModelSwitchCount].(int); ok {
		snap.ModelSwitchCount = msc
	}
	return snap
}

// OnError 吞掉错误（检查器失败可降级）。
func (h *InspectorHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// 编译期断言。
var _ pipeline.Hook = (*InspectorHook)(nil)
