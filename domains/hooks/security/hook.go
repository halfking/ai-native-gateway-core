package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/settings"
)

// security.mode 的两个合法取值（settings spec_modules.go 同一契约）：
//
//	observe = 仅记录不拦截（spec 默认值，描述原文「推荐测试期」）
//	enforce = 高风险请求直接阻断
const (
	securityModeObserve = "observe"
	securityModeEnforce = "enforce"
)

// securityMode 归一化配置里的 mode 取值（容忍大小写与首尾空白）。
func securityMode(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// SecurityConfig 安全检测引擎配置
type SecurityConfig struct {
	Enabled                bool
	Mode                   string
	IntentModel            string
	ThreatModel            string
	IntentEnabled          bool
	IntentConfidenceThresh float64
	IntentDriftThresh      float64
	ThreatEnabled          bool
	SeverityThreshold      int
	LowThreshold           int
	MediumThreshold        int
	HighThreshold          int
	ResponseLowRisk        string
	ResponseMediumRisk     string
	ResponseHighRisk       string
	AuditEnabled           bool
	AuditLogAll            bool
	AuditSamplingRate      float64
}

// securityConfigTTL 是配置缓存的刷新间隔。
//
// 2026-07-27 concurrency fix: Execute 原来每个请求都取 h.mu 的写锁去
// loadSecurityConfig（读 settings 后端），把所有请求串行化在一把互斥锁上。
// 现在配置放在 atomic.Pointer 里，热路径只做一次无锁 Load；刷新按这个 TTL
// 节流，由第一个发现过期的请求异步触发（不阻塞自己）。
const securityConfigTTL = 5 * time.Second

// SecurityHook 安全检查 Hook
type SecurityHook struct {
	intent   *IntentAnalyzer
	threat   *ThreatDetector
	config   atomic.Pointer[SecurityConfig]
	registry *settings.Registry
	// reloadAtUnixNano 是下一次允许重新加载配置的时间点（UnixNano）。
	reloadAtUnixNano atomic.Int64
	// reloading 保证同一时刻只有一个后台刷新在跑。
	reloading atomic.Bool
}

// NewSecurityHook 创建安全检查 Hook（从 settings 读取配置）
func NewSecurityHook(registry *settings.Registry) *SecurityHook {
	config := loadSecurityConfig(registry)

	intent := NewIntentAnalyzer(config.IntentConfidenceThresh)
	threat := NewThreatDetector(config.SeverityThreshold)

	h := &SecurityHook{
		intent:   intent,
		threat:   threat,
		registry: registry,
	}
	h.config.Store(config)
	h.reloadAtUnixNano.Store(time.Now().Add(securityConfigTTL).UnixNano())
	return h
}

// currentConfig 无锁读取当前配置，并在 TTL 到期时异步触发一次刷新。
func (h *SecurityHook) currentConfig() *SecurityConfig {
	config := h.config.Load()
	if h.registry == nil {
		return config
	}

	now := time.Now()
	deadline := h.reloadAtUnixNano.Load()
	if now.UnixNano() >= deadline &&
		h.reloadAtUnixNano.CompareAndSwap(deadline, now.Add(securityConfigTTL).UnixNano()) {
		// 刷新会读 settings 后端（可能有 I/O），放到后台跑，绝不阻塞请求。
		if h.reloading.CompareAndSwap(false, true) {
			go func() {
				defer h.reloading.Store(false)
				h.config.Store(loadSecurityConfig(h.registry))
			}()
		}
	}
	return config
}

// loadSecurityConfig 从 settings 加载安全配置
func loadSecurityConfig(reg *settings.Registry) *SecurityConfig {
	config := &SecurityConfig{
		// 默认值
		Enabled:                true,
		Mode:                   securityModeObserve,
		IntentModel:            "gpt-4o-mini",
		ThreatModel:            "gpt-4o-mini",
		IntentEnabled:          true,
		IntentConfidenceThresh: 0.7,
		IntentDriftThresh:      0.5,
		ThreatEnabled:          true,
		SeverityThreshold:      7,
		LowThreshold:           3,
		MediumThreshold:        5,
		HighThreshold:          8,
		ResponseLowRisk:        "log",
		ResponseMediumRisk:     "log",
		ResponseHighRisk:       "block",
		AuditEnabled:           true,
		AuditLogAll:            false,
		AuditSamplingRate:      0.1,
	}

	if reg == nil {
		return config
	}

	// 读取各个配置项
	//
	// 2026-11-18（211 生产实证）：security.enabled 此前无任何运行时消费者
	// —— 211 settings_kv 已写 false，钩子照常拦截 ⇒ 运维关不掉。spec 承诺
	// 「关闭后跳过所有安全检测」，此处落地该承诺。
	if sp := reg.Spec("security.enabled"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				config.Enabled = b
			}
		}
	}

	if sp := reg.Spec("security.mode"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.Mode = s
			}
		}
	}

	if sp := reg.Spec("security.llm.intent_model"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.IntentModel = s
			}
		}
	}

	if sp := reg.Spec("security.llm.threat_model"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.ThreatModel = s
			}
		}
	}

	if sp := reg.Spec("security.intent.enabled"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				config.IntentEnabled = b
			}
		}
	}

	if sp := reg.Spec("security.intent.confidence_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var f float64
			if json.Unmarshal(v, &f) == nil && f > 0 {
				config.IntentConfidenceThresh = f
			}
		}
	}

	if sp := reg.Spec("security.intent.drift_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var f float64
			if json.Unmarshal(v, &f) == nil && f > 0 {
				config.IntentDriftThresh = f
			}
		}
	}

	if sp := reg.Spec("security.threat.enabled"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				config.ThreatEnabled = b
			}
		}
	}

	if sp := reg.Spec("security.threat.severity_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var i int
			if json.Unmarshal(v, &i) == nil && i > 0 {
				config.SeverityThreshold = i
			}
		}
	}

	if sp := reg.Spec("security.threat.risk_level.low_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var i int
			if json.Unmarshal(v, &i) == nil && i > 0 {
				config.LowThreshold = i
			}
		}
	}

	if sp := reg.Spec("security.threat.risk_level.medium_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var i int
			if json.Unmarshal(v, &i) == nil && i > 0 {
				config.MediumThreshold = i
			}
		}
	}

	if sp := reg.Spec("security.threat.risk_level.high_threshold"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var i int
			if json.Unmarshal(v, &i) == nil && i > 0 {
				config.HighThreshold = i
			}
		}
	}

	if sp := reg.Spec("security.response.low_risk"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.ResponseLowRisk = s
			}
		}
	}

	if sp := reg.Spec("security.response.medium_risk"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.ResponseMediumRisk = s
			}
		}
	}

	if sp := reg.Spec("security.response.high_risk"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				config.ResponseHighRisk = s
			}
		}
	}

	if sp := reg.Spec("security.audit.enabled"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				config.AuditEnabled = b
			}
		}
	}

	if sp := reg.Spec("security.audit.log_all"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				config.AuditLogAll = b
			}
		}
	}

	if sp := reg.Spec("security.audit.sampling_rate"); sp != nil {
		if v, _, err := reg.EffectiveValue(sp.Scope, sp.Key, ""); err == nil {
			var f float64
			if json.Unmarshal(v, &f) == nil && f >= 0 && f <= 1 {
				config.AuditSamplingRate = f
			}
		}
	}

	return config
}

// GetConfig 获取当前配置（用于测试和调试）
func (h *SecurityHook) GetConfig() *SecurityConfig {
	current := h.config.Load()
	if current == nil {
		return nil
	}
	config := *current
	return &config
}

// Name 返回 Hook 名称
func (h *SecurityHook) Name() string { return "security.check" }

// Priority 返回优先级
func (h *SecurityHook) Priority() int { return 100 }

// Enabled 是否启用
func (h *SecurityHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 执行安全检查
//
// PR-V4-04 收口：除保留 env.Metadata["security_verdict"] 以兼容既有读取方外，
// 同时把判定写入 env.EnsureGovernance()，使 domains/interception.Engine
// 能与 v4 安全插件 verdicts 一同参与决策。
//
// 2026-11-18（211 生产实证）：本钩子此前**不读 security.mode**——spec 默认
// observe（「仅记录不拦截」），实现却无条件 block；叠加同日 P0（b2c43d044）
// 把 deny verdict 首次真正收口到客户端 ⇒ 规则库里子串级的误报（"dan"、
// 「忽略/绕过/扮演」、tel+数字 …）第一次以 403 的形式砸到用户脸上。
// 修复后 observe/enforce 契约生效：只有 enforce 才拦，observe 留痕放行。
func (h *SecurityHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	// 热加载配置：无锁 atomic Load + TTL 节流刷新（见 currentConfig）
	config := h.currentConfig()
	severityThreshold := h.threat.severityThreshold
	mode := securityModeObserve
	if config != nil {
		// security.enabled=false ⇒ 跳过所有安全检测（spec 承诺，见 loadSecurityConfig 注释）
		if !config.Enabled {
			return nil
		}
		if h.registry != nil && config.SeverityThreshold > 0 {
			severityThreshold = config.SeverityThreshold
		}
		if securityMode(config.Mode) == securityModeEnforce {
			mode = securityModeEnforce
		}
	}
	intent := h.intent
	threat := h.threat

	content, _ := env.Metadata["user_content"].(string)
	if content == "" {
		return nil // 无内容跳过
	}

	verdict := &Verdict{Allow: true}
	verdict.Intent = intent.Analyze(content)
	verdict.Threats = threat.Detect(content)

	critical := isCriticalAt(verdict.Threats, severityThreshold)
	if critical {
		// 拦截/观测留痕：修复前命中判定零日志 ⇒ 4 笔 403 只能靠 body 重放反推
		// 误报来源。evidence 只含规则形态（正则/关键词），不含用户原文。
		action := "observe"
		if mode == securityModeEnforce {
			action = "block"
		}
		slog.Warn("security: threat detected",
			"action", action,
			"threshold", severityThreshold,
			"threats", describeThreats(verdict.Threats),
		)
	}

	if critical && mode == securityModeEnforce {
		verdict.Allow = false
		verdict.Reason = "critical threat detected"
	}

	// 向后兼容：保留 metadata 路径，给旧代码读
	env.Metadata["security_verdict"] = verdict
	env.Metadata["security_checked_at"] = time.Now()

	// V4 收口：写入 governance 子结构
	gv := toGovernanceVerdict(verdict)
	if gv != nil {
		env.EnsureGovernance().RecordVerdict(gv)
	}

	if !verdict.Allow {
		return errors.New("security: request blocked: " + verdict.Reason)
	}
	return nil
}

// describeThreats 把命中清单压成一行可读日志形态：type(sev):evidence;…
// evidence 截断到 120 字符（规则形态足够定位，不需要全文）。
func describeThreats(threats []*Threat) string {
	parts := make([]string, 0, len(threats))
	for _, t := range threats {
		ev := t.Evidence
		if len(ev) > 120 {
			ev = ev[:120] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s(sev%d):%s", t.Type, t.Severity, ev))
	}
	return strings.Join(parts, "; ")
}

// toGovernanceVerdict 把 v3 私有 Verdict 转写到共享 governance.Verdict。
func toGovernanceVerdict(v *Verdict) *governance.Verdict {
	if v == nil {
		return nil
	}
	gv := &governance.Verdict{
		PluginName: "security.legacy",
		Allow:      v.Allow,
		Severity:   0,
		Code:       "legacy_bridge",
		Reason:     v.Reason,
	}
	if !v.Allow {
		gv.Severity = 3
		gv.Code = "legacy.critical"
	}
	return gv
}

// OnError 错误处理（安全阻断必须上报）
func (h *SecurityHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil {
		env.StatusCode = 403
	}
	return err
}

var _ pipeline.Hook = (*SecurityHook)(nil)
