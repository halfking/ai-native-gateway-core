// Package moduleregistry 定义系统中所有会话相关模块的标识符
//
// 这是会话模块执行记录系统（session_module_executions_hot）的基础。
// 所有需要被追踪执行情况的模块都必须使用此处定义的常量。
//
// 使用规范：
//   - 模块名必须全局唯一
//   - 模块名采用 snake_case 命名
//   - 模块版本用于在升级时自动失效旧结果
package moduleregistry

// 标准模块名称常量
const (
	// ═══════════════════════════════════════════════════════════════
	// 安全检测类
	// ═══════════════════════════════════════════════════════════════

	// ModuleSessionAudit 会话审计（综合审计）
	// 职责：内容安全检测、风险评分、阻断决策
	// 位置：domains/hooks/sessionaudit/
	// TTL：1小时
	ModuleSessionAudit = "session_audit"

	// ModuleSecurityScan 通用安全扫描
	// 职责：基础安全检查
	// 位置：domains/security/hook.go
	// TTL：30分钟
	ModuleSecurityScan = "security_scan"

	// ModulePromptInjection Prompt 注入检测
	// 职责：检测用户输入中的 prompt injection
	// 位置：domains/security/plugins/prompt_injection*.go
	// TTL：2小时
	ModulePromptInjection = "prompt_injection"

	// ModuleToxicityDetection 有毒内容检测
	// 职责：检测输出中的有毒/有害内容
	// TTL：1小时
	ModuleToxicityDetection = "toxicity_detection"

	// ModulePIIDetection PII 检测
	// 职责：检测个人身份信息
	// 位置：domains/outputcompliance/
	// TTL：1小时
	ModulePIIDetection = "pii_detection"

	// ═══════════════════════════════════════════════════════════════
	// 会话分析类
	// ═══════════════════════════════════════════════════════════════

	// ModuleSessionInspector 会话巡检
	// 职责：会话行为异常检测（频率、并发、错误率、突发）
	// 位置：domains/hooks/session-inspector/
	// TTL：5分钟（实时性要求高）
	ModuleSessionInspector = "session_inspector"

	// ModuleSessionHealth 会话健康度计算
	// 职责：计算会话健康分数和等级
	// 位置：bg/session_health_worker.go
	// TTL：1小时
	ModuleSessionHealth = "session_health"

	// ModuleSessionSummary LLM 摘要生成
	// 职责：生成会话标题、摘要、关键主题、用户意图
	// 位置：domains/sessionsummary/summarizer.go
	// TTL：24小时（生成成本高）
	ModuleSessionSummary = "session_summary"

	// ModuleIntentAnalysis 意图分析
	// 职责：分析用户意图
	// 位置：domains/hooks/intentanalysis/
	// TTL：1小时
	ModuleIntentAnalysis = "intent_analysis"

	// ModuleGoalAnalysis 目标分析
	// 职责：分析会话目标和偏离
	// 位置：domains/hooks/goal/
	// TTL：1小时
	ModuleGoalAnalysis = "goal_analysis"

	// ═══════════════════════════════════════════════════════════════
	// 优化类
	// ═══════════════════════════════════════════════════════════════

	// ModuleSessionCompression 会话压缩
	// 职责：智能上下文压缩（LLM 总结 + 机械裁剪）
	// 位置：domains/hooks/compression/
	// TTL：30分钟
	ModuleSessionCompression = "session_compression"

	// ModuleHandoffTrigger 切换触发检测
	// 职责：检测是否需要切换模型/供应商
	// 位置：domains/hooks/handoff/
	// TTL：10分钟
	ModuleHandoffTrigger = "handoff_trigger"

	// ModuleOptimizationAdvice 优化建议
	// 职责：生成会话优化建议
	// 位置：domains/analysis/workers/optimization_close_hook.go
	// TTL：24小时
	ModuleOptimizationAdvice = "optimization_advice"

	// ═══════════════════════════════════════════════════════════════
	// 合规类
	// ═══════════════════════════════════════════════════════════════

	// ModuleOutputCompliance 输出合规检查
	// 职责：输出脱敏与所有权控制
	// 位置：domains/hooks/outputcompliance/
	// TTL：1小时
	ModuleOutputCompliance = "output_compliance"

	// ModuleDataOwnership 数据所有权验证
	// 职责：验证数据所有者权限
	// 位置：cmd/gateway/output_compliance_control.go
	// TTL：30分钟
	ModuleDataOwnership = "data_ownership"
)

// ModuleInfo 模块元信息
type ModuleInfo struct {
	Name        string
	Version     string
	Description string
	TTLSeconds  int
}

// ModuleRegistry 模块注册表
var ModuleRegistry = map[string]ModuleInfo{
	// 安全检测类
	ModuleSessionAudit: {
		Name:        ModuleSessionAudit,
		Version:     "v2.1",
		Description: "会话审计 - 内容安全检测、风险评分",
		TTLSeconds:  3600,
	},
	ModuleSecurityScan: {
		Name:        ModuleSecurityScan,
		Version:     "v1.3",
		Description: "通用安全扫描",
		TTLSeconds:  1800,
	},
	ModulePromptInjection: {
		Name:        ModulePromptInjection,
		Version:     "v1.5",
		Description: "Prompt 注入检测",
		TTLSeconds:  7200,
	},
	ModuleToxicityDetection: {
		Name:        ModuleToxicityDetection,
		Version:     "v1.0",
		Description: "有毒内容检测",
		TTLSeconds:  3600,
	},
	ModulePIIDetection: {
		Name:        ModulePIIDetection,
		Version:     "v1.2",
		Description: "PII 个人身份信息检测",
		TTLSeconds:  3600,
	},

	// 会话分析类
	ModuleSessionInspector: {
		Name:        ModuleSessionInspector,
		Version:     "v3.0",
		Description: "会话巡检 - 行为异常检测",
		TTLSeconds:  300,
	},
	ModuleSessionHealth: {
		Name:        ModuleSessionHealth,
		Version:     "v3.0",
		Description: "会话健康度计算",
		TTLSeconds:  3600,
	},
	ModuleSessionSummary: {
		Name:        ModuleSessionSummary,
		Version:     "v1.4",
		Description: "LLM 摘要生成",
		TTLSeconds:  86400,
	},
	ModuleIntentAnalysis: {
		Name:        ModuleIntentAnalysis,
		Version:     "v1.1",
		Description: "意图分析",
		TTLSeconds:  3600,
	},
	ModuleGoalAnalysis: {
		Name:        ModuleGoalAnalysis,
		Version:     "v1.0",
		Description: "目标分析",
		TTLSeconds:  3600,
	},

	// 优化类
	ModuleSessionCompression: {
		Name:        ModuleSessionCompression,
		Version:     "v3.2",
		Description: "会话压缩 - 智能上下文压缩",
		TTLSeconds:  1800,
	},
	ModuleHandoffTrigger: {
		Name:        ModuleHandoffTrigger,
		Version:     "v2.0",
		Description: "切换触发检测",
		TTLSeconds:  600,
	},
	ModuleOptimizationAdvice: {
		Name:        ModuleOptimizationAdvice,
		Version:     "v1.0",
		Description: "优化建议",
		TTLSeconds:  86400,
	},

	// 合规类
	ModuleOutputCompliance: {
		Name:        ModuleOutputCompliance,
		Version:     "v2.0",
		Description: "输出合规检查",
		TTLSeconds:  3600,
	},
	ModuleDataOwnership: {
		Name:        ModuleDataOwnership,
		Version:     "v1.0",
		Description: "数据所有权验证",
		TTLSeconds:  1800,
	},
}

// GetModuleInfo 获取模块元信息
func GetModuleInfo(name string) (ModuleInfo, bool) {
	info, ok := ModuleRegistry[name]
	return info, ok
}

// GetModuleVersion 获取模块版本
func GetModuleVersion(name string) string {
	if info, ok := ModuleRegistry[name]; ok {
		return info.Version
	}
	return "v0.0"
}

// GetModuleTTL 获取模块默认 TTL（秒）
func GetModuleTTL(name string) int {
	if info, ok := ModuleRegistry[name]; ok {
		return info.TTLSeconds
	}
	return 3600 // 默认 1 小时
}

// IsValidModule 检查模块名是否有效
func IsValidModule(name string) bool {
	_, ok := ModuleRegistry[name]
	return ok
}

// AllModules 返回所有注册的模块名
func AllModules() []string {
	modules := make([]string, 0, len(ModuleRegistry))
	for name := range ModuleRegistry {
		modules = append(modules, name)
	}
	return modules
}
