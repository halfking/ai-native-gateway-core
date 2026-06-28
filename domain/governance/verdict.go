package governance

// Verdict 统一治理检查产出。
//
// PR-V4-02 引入共享 Verdict 类型，替代 domains/hooks/security 内的私有 Verdict，
// 使 PipelineRequest.GovernanceState 可以承载检查结果而无需反向依赖 domains/*。
//
// 字段语义：
//   - PluginName：产生本 verdict 的插件/hook 名称，用于决策可观测与回放。
//   - Allow：true 表示放行；false 表示阻断或需要 mutate。
//   - Severity：0=info, 1=warn, 2=block, 3=critical。
//   - Code：机器可读的判定码，例如 "prompt_injection.jailbreak"。
//   - Reason：人类可读的判定原因。
//   - Evidence：原始证据（命中片段、规则名、敏感字段等），序列化为 JSONB。
//   - FixAction：建议的修复动作（"redact_phone" / "abort_request" / ""）。
//
// PR-V4-03 会把 domains/hooks/security 私有 Verdict 替换为本类型；
// PR-V4-02 阶段保持双向兼容（两个 Verdict 类型共存，PR-V4-03 再做迁移）。
type Verdict struct {
	PluginName string
	Allow      bool
	Severity   int
	Code       string
	Reason     string
	Evidence   map[string]any
	FixAction  string
}
