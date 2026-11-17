package securityevent

// Memora 落地面：把统一契约的安全事件写进 Memora。
//
// # 两条通道，怎么选
//
//	memoraV2Writer   —— /api/v2/memories/ingest（v2 fail-closed 组）
//	                    鉴权 = service JWT（自签 HS256）
//	                    优点：逐条幂等键（重投不写第二行）、scope_chain、
//	                          source_type 白名单
//	memoraLegacyWriter —— /api/memory/add（legacy 组，V1Auth）
//	                    鉴权 = ingest API key（X-API-Key 头）
//	                    优点：网关现役就配着这个凭据
//	                    缺点：没有幂等键、没有 scope_chain
//
// 装配时按"有没有 service JWT 密钥"决定用哪条：两条都写同一个 Writer 接口，
// 契约与 sink 不受选择影响。
//
// ★ 作用域的语义（两条通道一致）：
//
//   memora 的 **project scope** 回答的是"这条知识存在哪个知识项目里"，
//   不是"谁的流量触发的"。网关把安全事件写进**网关自己的安全知识项目**，
//   触发方的租户进 metadata 与 tags（tenant:<id>）⇒ 消费侧按租户过滤，
//   而不会把 A 租户的事件写进 B 租户的项目。

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/memory"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
	memv2 "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer/memora"
)

// SourceSystemGateway 是 memora 认可的写入方标识（v2 ingest 的白名单：
// pocket|redclaw|acc|gateway）。
const SourceSystemGateway = "gateway"

// SourceTypeSessionSummary 是 v2 ingest 的 source_type 白名单值之一
// （email|note|meeting|session_summary|artifact）。安全事件是一次会话/请求
// 的安全摘要 ⇒ session_summary。
const SourceTypeSessionSummary = "session_summary"

// ---- v2 通道 ----

// MemoraV2Writer 把安全事件写进 Memora 的 v2 typed ingest 面。
type MemoraV2Writer struct {
	client *memv2.Client
}

// NewMemoraV2Writer 构造 v2 落地面。
func NewMemoraV2Writer(client *memv2.Client) *MemoraV2Writer {
	return &MemoraV2Writer{client: client}
}

// WriteSecurityEvent 写出一条安全事件。
//
// 幂等：item 的幂等键取事件 id ⇒ 同一事件的重复投递（含 sink 的重试与
// 客户端内部重试）会被服务端去重，不会写出第二行。
func (w *MemoraV2Writer) WriteSecurityEvent(ctx context.Context, event Event) error {
	if w == nil || w.client == nil {
		return fmt.Errorf("securityevent: memora v2 writer not configured")
	}
	if err := event.validateForWrite(); err != nil {
		return err
	}

	tags := []string{
		TagSecurity,
		TagSeverityPrefix + string(event.Severity),
		TagActionPrefix + string(event.Action),
		TagSourcePrefix + string(event.Source),
	}
	if event.TenantID != "" {
		tags = append(tags, TagTenantPrefix+event.TenantID)
	}
	if event.Code != "" {
		tags = append(tags, TagCodePrefix+event.Code)
	}

	metadata := map[string]any{
		"event_id":    event.EventID,
		"event_type":  event.EventType,
		"code":        event.Code,
		"severity":    string(event.Severity),
		"action":      string(event.Action),
		"source":      string(event.Source),
		"tenant_id":   event.TenantID,
		"instance_id": event.InstanceID,
		"session_id":  event.SessionID,
		"request_id":  event.RequestID,
		"detected_at": event.DetectedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"api_key_id":  event.APIKeyID,
	}
	for key, value := range event.Metadata {
		metadata["raw_"+key] = value
	}
	// 跨实例提示写在**顶层**（不是 raw_* 里）：它是契约的一部分，不是
	// 某个检测面的原始细节。nil ⇒ 不写这个键（空提示会被读成"只有我见过"）。
	if fleet := FleetHintMap(event.FleetHint); fleet != nil {
		metadata["fleet_pattern"] = fleet
		tags = append(tags, FleetHintTags(event.FleetHint)...)
	}

	result, err := w.client.Ingest(ctx, SourceSystemGateway, []memv2.IngestItem{{
		IdempotencyKey: event.EventID,
		SourceType:     SourceTypeSessionSummary,
		SourceID:       "gateway/security/" + event.EventID,
		ContentPreview: FormatEvent(event),
		Tags:           tags,
		Metadata:       metadata,
		// scope_chain 的谱系字段：安全事件挂在哪个会话/请求上。
		TaskID:         event.SessionID,
		AgentSessionID: event.SessionID,
	}})
	if err != nil {
		return err
	}
	// 服务端的逐条拒收随 200 一起回来 —— 必须当成失败，否则"被拒"会被读成
	// "写成功"。
	if len(result.Failed) > 0 {
		return fmt.Errorf("securityevent: memora rejected the security event: %s", strings.Join(result.Failed, "; "))
	}
	if result.Accepted == 0 && result.Duplicates == 0 {
		return fmt.Errorf("securityevent: memora accepted nothing for %s (accepted=0 duplicates=0)", event.EventID)
	}
	return nil
}

// ---- legacy 通道 ----

// MemoraLegacyWriter 把安全事件写进 Memora 的 legacy 记忆入口。
//
// 适用面：网关现役配的是 ingest API key（LLM_GATEWAY_MEMORA_API_KEY），
// 而 legacy 组正好认它（X-API-Key 头）。
//
// 代价：没有幂等键 ⇒ 重试可能写出重复条目；没有 scope_chain。
type MemoraLegacyWriter struct {
	client *memclient.Client
	// defaultUserID 是没有租户可用时的记忆归属者（memora 要求 user_id 非空，
	// 否则 AddMessage 会静默跳过）。
	defaultUserID string
}

// NewMemoraLegacyWriter 构造 legacy 落地面。
func NewMemoraLegacyWriter(client *memclient.Client, defaultUserID string) *MemoraLegacyWriter {
	return &MemoraLegacyWriter{
		client:        client,
		defaultUserID: strings.TrimSpace(defaultUserID),
	}
}

// WriteSecurityEvent 把一条安全事件写进 Memora。
//
// 记忆归属（user_id）取**触发方的租户** ⇒ 消费侧按租户检索时能直接命中；
// 项目作用域仍是网关配置的知识项目。
func (w *MemoraLegacyWriter) WriteSecurityEvent(ctx context.Context, event Event) error {
	if w == nil || w.client == nil || w.client.Disabled() {
		return fmt.Errorf("securityevent: memora legacy writer not configured")
	}
	if err := event.validateForWrite(); err != nil {
		return err
	}
	userID := strings.TrimSpace(event.TenantID)
	if userID == "" {
		userID = w.defaultUserID
	}
	if userID == "" {
		// memora 的 user_id 为空会静默跳过 —— 那等于"写成功但什么都没写"。
		return fmt.Errorf("securityevent: security event %s has no tenant and no default user id", event.EventID)
	}

	info := map[string]any{
		"source_type": "security_event",
		"event_type":  event.EventType,
		"event_id":    event.EventID,
		"code":        event.Code,
		"severity":    string(event.Severity),
		"action":      string(event.Action),
		"source":      string(event.Source),
		"tenant_id":   event.TenantID,
		"detected_at": event.DetectedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if event.ProjectID != "" {
		// 客户端会优先用 info 里的 project_id（见 client.setProjectHeader）。
		info["project_id"] = event.ProjectID
	}
	if event.SessionID != "" {
		info["session_id"] = event.SessionID
	}
	if event.RequestID != "" {
		info["request_id"] = event.RequestID
	}
	if event.CorrelationID != "" {
		info["correlation_id"] = event.CorrelationID
	}
	if event.InstanceID != "" {
		info["instance_id"] = event.InstanceID
	}
	if event.APIKeyID != 0 {
		info["api_key_id"] = event.APIKeyID
	}
	for key, value := range event.Metadata {
		info["meta_"+key] = value
	}
	if fleet := FleetHintMap(event.FleetHint); fleet != nil {
		info["fleet_pattern"] = fleet
	}

	return w.client.AddMessage(ctx, userID, []memory.Message{{
		Role:    "assistant",
		Content: FormatEvent(event),
	}}, info)
}

// FormatEvent 把一条事件渲染成一行人类可读的正文。
//
// 正文要能被人在检索结果里一眼看懂"发生了什么"，所以带上严重度、代码、
// 结论与关键定位信息。原始细节在 metadata 里。
func FormatEvent(event Event) string {
	var b strings.Builder
	b.WriteString("[Security][")
	b.WriteString(string(event.Severity))
	b.WriteString("][")
	b.WriteString(string(event.Action))
	b.WriteString("] ")
	b.WriteString(event.Code)
	if event.Message != "" {
		b.WriteString(": ")
		b.WriteString(event.Message)
	}
	locator := make([]string, 0, 4)
	if event.SessionID != "" {
		locator = append(locator, "session="+event.SessionID)
	}
	if event.RequestID != "" {
		locator = append(locator, "request="+event.RequestID)
	}
	if event.InstanceID != "" {
		locator = append(locator, "instance="+event.InstanceID)
	}
	if len(locator) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(locator, " "))
		b.WriteString(")")
	}
	return b.String()
}
