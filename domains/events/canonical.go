// Package events 定义跨域共享的 canonical metadata 值对象。
//
// 这些类型是 omni-ref2 GW-00 交付的字段白名单：它们是路由、压缩、计费
// 等数据面结论对外（审计、事件、跨仓 ASM 投影）的唯一允许形态。
//
// 设计约束（README §5 Phase 0 / 02-CROSS-REPO-EVENT-CONTRACT.md §3）：
//   - 不得承载 prompt/response/system prompt/tool arguments/attachment 正文。
//   - 不得承载 API key/Bearer/cookie/secret/credential token 或高熵 token。
//   - 不得用 tenant_id 覆盖 signed tenant。
//   - 不得暴露内部 credential ID 数值、上游 URL、绝对文件路径或 SQL 错误。
//
// 本包只定义值对象，不定义事件总线事件类型，也不发布到 eventbus。
// 现有 eventbus.Event 接口（Type()+Timestamp()）和各领域事件包
// （analysis/sessionaudit/clientprofile/session-inspector）保持不动；
// 这些 canonical 类型作为被引用的值对象嵌入。
package events

import "time"

// 当前 canonical schema 版本。非向后兼容变化必须升版本号，
// 不能静默改变字段含义（02-CROSS-REPO-EVENT-CONTRACT.md §4）。
const DecisionVersionV1 = "v1"

// ProviderRef 是对外/审计/事件里唯一允许的 provider 引用形态。
//
// 刻意只暴露公开标识：catalog code、protocol、tier、vendor、credential 的
// 公开 label。CredentialID 用指针便于 omitempty；它只是数据库主键引用，
// 不是 secret。
//
// 禁止字段：API key、Bearer、完整 credential token、上游 BaseURL、绝对路径。
type ProviderRef struct {
	CatalogCode     string `json:"catalog_code"`
	ProviderID      *int64 `json:"provider_id,omitempty"`
	Protocol        string `json:"protocol"`
	Tier            string `json:"tier,omitempty"`
	VendorName      string `json:"vendor_name,omitempty"`
	CredentialID    *int64 `json:"credential_id,omitempty"`    // 数据库主键引用，非 secret
	CredentialLabel string `json:"credential_label,omitempty"` // 公开 label，非 secret
}

// RoutingDecision 是路由决策的 canonical 解释，低敏、可审计。
//
// 与 domains/routeincident.RouteSnapshot 互补：RouteSnapshot 是请求日志快照
// （含 model mapping、retry path），RoutingDecision 是可向外发的决策解释。
// RouteSnapshot 的 RoutingDecision 字符串字段保留不动；本类型是结构化补充。
//
// 禁止字段：prompt、内部 SQL 错误、credential secret。
type RoutingDecision struct {
	Strategy        string      `json:"strategy"`         // p2c|bandit|cost-optimized|cache-optimized|context-aware|headroom
	DecisionVersion string      `json:"decision_version"` // DecisionVersionV1
	Provider        ProviderRef `json:"provider"`
	ExplanationCode string      `json:"explanation_code"` // p2c_low_penalty|bandit_sample|...
	Timestamp       time.Time   `json:"timestamp"`
}

// CompressionEvent 是压缩结果的 canonical 事件字段。
//
// 字段定义与 ASM 跨仓契约 02-CROSS-REPO-EVENT-CONTRACT.md §3 的未来
// compression 扩展草案对齐（mode/stages/input_chars/output_chars/
// saved_chars/reason_code）。该扩展尚未在 ASM validator 批准，故当前
// 仅用于 Gateway 内部审计/telemetry，不直接投递到 ASM。
//
// 禁止字段：prompt 正文、response 正文、system prompt、tool arguments。
type CompressionEvent struct {
	Mode        string    `json:"mode"`   // lite|mechanical_trim|llm_summary|memora_l1_inject|noop
	Stages      []string  `json:"stages"` // whitespace|system-dedup|tool-compress|redundant-remove|image-placeholder
	InputChars  int       `json:"input_chars"`
	OutputChars int       `json:"output_chars"`
	SavedChars  int       `json:"saved_chars"`
	ReasonCode  string    `json:"reason_code"` // context_headroom|mode_1_auto_threshold|mode_2_on_4xx
	Timestamp   time.Time `json:"timestamp"`
}
