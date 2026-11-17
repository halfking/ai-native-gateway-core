// Package securityevent 是网关侧安全事件的统一契约。
//
// # 为什么需要它
//
// 网关里同时存在 8 个互不相通的安全检测面（异常流量、注入检测、护栏、敏感
// 信息、输出合规、Armor 判定、会话审计、IP 黑名单），它们各自：
//
//   - 用不同的结果结构（AnomalyResult / DetectionResult / Verdict /
//     GuardVerdict / SafetyResult / Decision …）；
//   - 用不同的严重度刻度（low-medium-high-critical / safe-warn-block /
//     pass-warn-block-rewrite / P0-P1-P2 / 0-3 / 1-10 整数）；
//   - 用不同的事件类型字面量（多数是裸字符串，没有常量表）；
//   - 各自决定"记到哪"：有的写自己的表，有的只进 slog。
//
// 结果是：**没有任何一个地方能回答"这个租户最近被攻击过几次、都是什么
// 模式"** —— 安全结果出了网关就散成了日志碎片。
//
// 本包只做三件事：
//
//  1. 定义一个**统一的安全事件契约**（Event）：同一套字段、同一套严重度、
//     同一套事件类型命名；
//  2. 定义**严重度归一化**：把各面的原始刻度折算到 info/low/medium/high/
//     critical，**同时把原始刻度原样透传**（raw_severity / raw_action）——
//     归一化是为了让消费侧能排序，不是为了抹掉各面的语义；
//  3. 提供**出网 sink**：非阻塞、有界、尽力而为地把事件写进 Memora，
//     让安全事件跨实例可查、可沉淀成威胁模式。
//
// # 刻意不做的事
//
//   - **不改各检测面的判定逻辑**。本包只归一化"结论的形状"，不重新裁决
//     "要不要拦"。
//   - **不往 ASM 的 outbox 里塞新的事件类型**。ASM 的 wire schema
//     （internal/outbox/testdata/gateway-event-schema-v1.json）的 `type`
//     是**封闭 enum**，加类型要跨仓同步（接收端 ai-session-manager 在另一个
//     仓），本仓单方面加会让契约校验立刻红。⇒ 出网走 Memora 这条**独立**
//     通道，不动既有跨仓契约。
package securityevent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Severity 是归一化后的严重度。
//
// 归一化的目的是让**跨检测面**可比（"哪个更严重"），不是为了替换各面的原始
// 语义。原始刻度一律保留在 Event.Metadata 的 raw_severity / raw_action 里。
type Severity string

const (
	// SeverityInfo 表示"看到了，但不是问题"（各面的 safe / pass / 0）。
	SeverityInfo Severity = "info"
	// SeverityLow 表示轻微异常，观察即可。
	SeverityLow Severity = "low"
	// SeverityMedium 表示需要注意但未达拦截级别。
	SeverityMedium Severity = "medium"
	// SeverityHigh 表示应当拦截或告警的级别。
	SeverityHigh Severity = "high"
	// SeverityCritical 表示最高级别（P0 / critical / 3）。
	SeverityCritical Severity = "critical"
)

// Action 是检测面对这次请求采取的动作。
type Action string

const (
	// ActionObserved 表示"只观察不执法"（observe-only 检测面）。
	ActionObserved Action = "observed"
	// ActionFlagged 表示"打了标记但放行"。
	ActionFlagged Action = "flagged"
	// ActionBlocked 表示"真的拦了"。
	ActionBlocked Action = "blocked"
)

// 事件类型常量。
//
// 命名沿用本仓出网契约的惯例：点分 + `.v1` 后缀
// （internal/outbox 的 request.completed.v1 / session.opened.v1）。
//
// ★ 注意：这些类型**不出现在 ASM 的 wire schema enum 里**（那是另一个仓的
// 契约）。本包的事件走 Memora 通道，与 ASM 的封闭 enum 互不影响。
const (
	// EventTypeAnomalyDetected 是流量/资源异常检测的结果。
	EventTypeAnomalyDetected = "security.anomaly_detected.v1"
	// EventTypeThreatDetected 是威胁检测的结果（注入、越权、敏感信息外泄）。
	EventTypeThreatDetected = "security.threat_detected.v1"
	// EventTypeComplianceViolation 是输出合规检测的结果。
	EventTypeComplianceViolation = "security.compliance_violation.v1"
)

// 标签常量。
//
// ★ 写面与读面必须用**同一套字面量**：读面靠 filters.tags 精确筛选，
// 写面靠这些标签产生可被筛选的条目。两边各写一份字符串，早晚会漂移。
// 检索侧（Qdrant）对标签是数组精确匹配（buildQdrantFilter 把每个 tag
// 变成一条 must 条件）。
const (
	// TagSecurity 标记"这是一条安全事件"。读面按它把安全事件从项目里挑出来。
	TagSecurity = "security"
	// TagSeverityPrefix + 严重度（如 severity:high）。
	TagSeverityPrefix = "severity:"
	// TagActionPrefix + 处置动作（如 action:blocked）。
	TagActionPrefix = "action:"
	// TagSourcePrefix + 检测面（如 source:anomaly）。
	TagSourcePrefix = "source:"
	// TagTenantPrefix + 租户（如 tenant:kaixuan）。
	TagTenantPrefix = "tenant:"
	// TagCodePrefix + 检测码（如 code:pii_leak）。
	TagCodePrefix = "code:"
)

// Source 标识检测面（哪个子系统产生的这条结论）。
type Source string

const (
	// SourceAnomaly 是 internal/anomaly 的异常检测面。
	SourceAnomaly Source = "anomaly"
	// SourceArmor 是 security/armor 的判定面。
	SourceArmor Source = "armor"
	// SourcePromptInjection 是 domains/promptinjection 的注入检测面。
	SourcePromptInjection Source = "prompt_injection"
	// SourceOutputCompliance 是 domains/outputcompliance 的输出合规面。
	SourceOutputCompliance Source = "output_compliance"
	// SourceGuardian 是 security/guardian 的护栏面。
	SourceGuardian Source = "guardian"
)

// Scope 是检测发生的那一刻能拿到的环境标识。
//
// 全部可选：拿不到的字段留空即可，**不要用猜的值填充**（空值在消费侧能
// 读成"当时没有"，猜出来的值会污染检索）。
type Scope struct {
	TenantID   string
	ProjectID  string
	InstanceID string
	SessionID  string
	RequestID  string
	// CorrelationID 是跨系统的关联 id（数据面从客户端 X-Request-Id 透传）。
	CorrelationID string
	// APIKeyID 是触发这次检测的密钥 id。
	APIKeyID int64
}

// Event 是一条统一的安全事件。
//
// 字段集刻意保持窄：能被索引的（租户/项目/会话/请求/严重度/代码/动作）
// 放在顶层，其余一律进 Metadata。
type Event struct {
	// EventID 是全局唯一的事件 id（evt-security-<hex>）。
	EventID string `json:"event_id"`
	// EventType 是事件类型常量（见上）。
	EventType string `json:"event_type"`
	// TenantID 是事件归属的租户。
	TenantID string `json:"tenant_id"`
	// ProjectID 是知识项目作用域（Memora 侧 fail-closed，没有它写不进去）。
	ProjectID string `json:"project_id,omitempty"`
	// InstanceID 是产生事件的网关实例。
	InstanceID string `json:"instance_id,omitempty"`
	// SessionID / RequestID / CorrelationID 是关联标识。
	SessionID     string `json:"session_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
	// APIKeyID 是触发检测的密钥 id。
	APIKeyID int64 `json:"api_key_id,omitempty"`
	// Code 是检测器的原始代码（如 attachment_replay_loop、pii_leak）。
	Code string `json:"code"`
	// Severity 是归一化后的严重度。
	Severity Severity `json:"severity"`
	// Action 是检测面的处置动作。
	Action Action `json:"action"`
	// Source 是检测面标识。
	Source Source `json:"source"`
	// Message 是人类可读的一句话结论。
	Message string `json:"message"`
	// DetectedAt 是检测发生的时间。
	DetectedAt time.Time `json:"detected_at"`
	// Metadata 承载各检测面的原始细节（raw_severity / raw_action / 指标 …）。
	Metadata map[string]any `json:"metadata,omitempty"`
	// FleetHint 是**跨实例**的威胁模式提示（M2-3）。
	//
	// 它回答的是"这个模式别的网关实例也见过吗"：签名 + 观察次数 + 见过它的
	// 实例列表 + 这份快照的取回时间。
	//
	// ★ 它**不参与**任何拦截判定 —— 共享域的内容可能被污染，让它影响
	// "要不要拦"等于把一个外部系统的可信度接到请求路径上。它只是**观测
	// 增强**：写进共享域时让这条事件自带全网视角，消费侧可以据此加权、
	// 告警、排查，但网关自己的处置结论不因它改变。
	FleetHint *FleetHint `json:"fleet_hint,omitempty"`
}

// FleetHint 是一条事件所属模式的**全网视角**。
//
// 数据来自共享知识项目里的其它实例写入的安全事件（M2-3 的读面）。
type FleetHint struct {
	// Signature 是模式签名（事件类型 + 检测码）。
	Signature string `json:"signature"`
	// Observations 是**这份快照里**观察到这个模式的事件条数。
	//
	// ★ 不是累计计数器：同步是整份替换快照 ⇒ 它反映"共享域里现在有多少
	//   条这个模式的事件"。写成累计会让人以为它单调增长。
	Observations int `json:"observations"`
	// Instances 是见过它的实例 id（去重、排序）。
	Instances []string `json:"instances,omitempty"`
	// InstanceCount 是 Instances 的长度（消费侧不必自己数）。
	InstanceCount int `json:"instance_count"`
	// FetchedAt 是这份快照的取回时间（UTC）。
	//
	// 它让消费侧能判断"这个提示有多旧" —— 共享域不可达时，快照会停在
	// 上一次成功取回的时刻，而不是看起来永远是新鲜的。
	FetchedAt time.Time `json:"fetched_at"`
}

// NewEvent 按统一契约构造一条事件，并补齐事件 id 与时间戳。
//
// idKey 是事件的业务身份（同一检测面 + 同一对象 + 同一时刻 ⇒ 同一个 id），
// 用来让"同一条检测"在多次投递中保持同一个 id。
func NewEvent(eventType string, source Source, scope Scope, code string, severity Severity, action Action, message string, idKey string, detectedAt time.Time, metadata map[string]any) Event {
	if detectedAt.IsZero() {
		detectedAt = time.Now().UTC()
	}
	return Event{
		EventID:       EventID(scope.TenantID, eventType, idKey),
		EventType:     eventType,
		TenantID:      strings.TrimSpace(scope.TenantID),
		ProjectID:     strings.TrimSpace(scope.ProjectID),
		InstanceID:    strings.TrimSpace(scope.InstanceID),
		SessionID:     strings.TrimSpace(scope.SessionID),
		RequestID:     strings.TrimSpace(scope.RequestID),
		CorrelationID: strings.TrimSpace(scope.CorrelationID),
		APIKeyID:      scope.APIKeyID,
		Code:          strings.TrimSpace(code),
		Severity:      severity,
		Action:        action,
		Source:        source,
		Message:       strings.TrimSpace(message),
		DetectedAt:    detectedAt.UTC(),
		Metadata:      metadata,
	}
}

// EventID 按本仓 outbox 的惯例派生事件 id：
// sha256(tenant_id \x00 event_type \x00 bizkey) 的前 16 字节 hex，
// 形如 evt-security-<hex>。
//
// 为什么不用随机 id：同一条检测在重试/重复投递时必须保持同一个 id，否则
// 消费侧会去重失败。
func EventID(tenantID, eventType, bizKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tenantID) + "\x00" + eventType + "\x00" + bizKey))
	return "evt-security-" + hex.EncodeToString(sum[:])[:16]
}

// validateForWrite 是**落地面**用的最小校验。
//
// 与 Validate 的差别只有一条：不要求 tenant_id。
//
// 为什么：Validate 是**发布闸门**（sink 收事件时的准入）—— 没有租户的
// 事件不可归因，不该进队列。而落地面（尤其是 legacy 通道）可以用配置
// 里的默认归属者兜底，没有租户不等于写不了。
//
// 落地面真正依赖的是：事件 id（v2 的幂等键）与代码（检索与展示）。
func (e Event) validateForWrite() error {
	var problems []string
	if strings.TrimSpace(e.EventID) == "" {
		problems = append(problems, "event_id is required")
	}
	if strings.TrimSpace(e.Code) == "" {
		problems = append(problems, "code is required")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("securityevent: event not writable: %s", strings.Join(problems, "; "))
}

// Validate 校验事件是否满足出网所需的最小条件。
//
// 失败原因会逐条列出来，而不是笼统地说"无效" —— 运维要能照着改。
func (e Event) Validate() error {
	var problems []string
	if strings.TrimSpace(e.EventID) == "" {
		problems = append(problems, "event_id is required")
	}
	if strings.TrimSpace(e.EventType) == "" {
		problems = append(problems, "event_type is required")
	}
	if strings.TrimSpace(e.TenantID) == "" {
		problems = append(problems, "tenant_id is required")
	}
	if strings.TrimSpace(e.Code) == "" {
		problems = append(problems, "code is required")
	}
	switch e.Severity {
	case SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
	default:
		problems = append(problems, fmt.Sprintf("severity %q is not one of the normalized levels", e.Severity))
	}
	switch e.Action {
	case ActionObserved, ActionFlagged, ActionBlocked:
	default:
		problems = append(problems, fmt.Sprintf("action %q is not one of observed/flagged/blocked", e.Action))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("securityevent: invalid event: %s", strings.Join(problems, "; "))
}
