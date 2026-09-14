// Package sessionv2mirror — s1a_fields.go
//
// 存储优化方案 v2 S1a（migration 706/707）：把 telemetry.RequestLogEntry 上
// request_logs 独有的五类数据（计费/路由/诊断/检索·完整性/访问维度）补采进
// V2 会话族，使 session_turns 成为 turn 级唯一事实源（plan §3 D1）。
//
// 数据源事实（2026-09-14 审计）：RequestLogEntry 缺 TraceEvents / SearchText /
// RequestChecksum / RawModelName —— 这些列已建（707）但保持零值；缺
// StreamDoneSent，最接近的 StreamDoneReceived 作映射。缺源列由 S2 视图
// NULL 补位登记（plan §9）。
//
// 全部指针安全：nil 字段零值跳过，落库时由 turn_writer 的 nilIf* 助手转
// SQL NULL，保住方案 §8-B 填充率验收的语义。
package sessionv2mirror

import (
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// applyStorageS1AFields copies the plan-v2 S1A backfill groups from a
// telemetry entry onto a ProcessedRequest. Called from
// entryToProcessedRequest after the legacy fields.
func applyStorageS1AFields(req *v2.ProcessedRequest, entry *telemetry.RequestLogEntry) {
	// 访问维度（sessions 落首值做归属，turns 落每轮值做计费到轮）
	req.APIKeyID = intStrPtr(entry.APIKeyID)
	req.ApplicationID = intStrPtr(entry.ApplicationID)
	req.EndUserID = strVal(entry.EndUserID)
	if entry.CustomerID != nil {
		req.CustomerID = *entry.CustomerID
	}
	req.OwnerUser = strVal(entry.APIKeyOwnerUser)
	req.ClientIP = strVal(entry.ClientIP)
	req.ClientForwardedFor = strVal(entry.ClientForwardedFor)
	req.AgentName = strVal(entry.AgentName)
	req.AgentType = strVal(entry.AgentType)
	req.VirtualClientID = strVal(entry.VirtualClientID)

	// client_type 断供修复（plan §1 sessions 行④）：mirror bridge 从不填
	// ClientType，sessions.client_type 因此长期为空。以 agent 名/型推导
	// （agent_name 是客户端自报身份，最贴近 client_type 的
	// cursor|roocode|…语义）。
	req.ClientType = clientTypeOf(entry)

	// 计费组（credits_charged 计费事实源，D7）
	if entry.CreditsCharged != nil {
		req.CreditsCharged = *entry.CreditsCharged
	}
	if entry.CostDisplay != nil {
		req.CostDisplay = *entry.CostDisplay
	}
	req.CostCurrency = strVal(entry.CostCurrency)
	req.WorkType = strVal(entry.WorkType)
	req.TokenBand = strVal(entry.TokenBand)
	req.UsageSource = strVal(entry.UsageSource)

	// 路由组
	if entry.IsAutoRequest != nil {
		req.IsAutoRequest = *entry.IsAutoRequest
	}
	req.AutoDecision = strVal(entry.AutoDecision)
	if entry.AutoConfidence != nil {
		req.AutoConfidence = *entry.AutoConfidence
	}
	req.TaskTypeChosen = strVal(entry.TaskTypeChosen)
	if len(entry.RoutingAttempts) > 0 && string(entry.RoutingAttempts) != "null" {
		req.RoutingAttempts = append(req.RoutingAttempts[:0], entry.RoutingAttempts...)
	}
	req.RoutingSummary = strVal(entry.RoutingSummary)
	if entry.CanonicalID != nil {
		req.CanonicalID = int64(*entry.CanonicalID)
	}
	req.CanonicalModel = strVal(entry.CanonicalModel)
	// req.RawModelName：RequestLogEntry 无此字段，保持零值（707 列已建）

	// 诊断组
	// req.TraceEvents：RequestLogEntry 无此字段，保持零值
	req.FailureStage = strVal(entry.FailureStage)
	req.FailureDetailCode = strVal(entry.FailureDetailCode)
	if entry.UpstreamStatusCode != nil {
		req.UpstreamStatusCode = *entry.UpstreamStatusCode
	}
	req.UpstreamFinishReason = strVal(entry.UpstreamFinishReason)
	if entry.StreamFirstChunkMs != nil {
		req.StreamFirstChunkMs = *entry.StreamFirstChunkMs
	}
	if entry.StreamChunkCount != nil {
		req.StreamChunkCount = *entry.StreamChunkCount
	}
	if entry.StreamInterrupted != nil {
		req.StreamInterrupted = *entry.StreamInterrupted
	}
	if entry.StreamDoneReceived != nil {
		// plan 列名 stream_done_sent；entry 最接近的信号是 done 事件接收
		req.StreamDoneSent = *entry.StreamDoneReceived
	}
	req.ClientRequestID = strVal(entry.ClientRequestID)
	req.ClientEndpoint = strVal(entry.ClientEndpoint)
	if entry.ClientTimeout != nil {
		req.ClientTimeout = *entry.ClientTimeout
	}
	req.EgressProtocol = strVal(entry.EgressProtocol)

	// 检索/完整性组（SearchText/RequestChecksum 缺源，保持零值）
	req.RequestPreview = strVal(entry.RequestPreview)
	req.ResponsePreview = strVal(entry.ResponsePreview)
	req.TransformSummary = strVal(entry.TransformSummary)
	req.IdentityHash = strVal(entry.IdentityHash)
	req.ResponseChecksum = strVal(entry.ResponseChecksum)
	req.SystemFingerprint = strVal(entry.SystemFingerprint)
	req.OriginStage = strVal(entry.OriginStage)
	req.OriginActor = strVal(entry.OriginActor)
}

// clientTypeOf derives the session client_type from the entry's agent
// identity, falling back to agent type (session_dim 的 client_id 语义最近邻)。
func clientTypeOf(entry *telemetry.RequestLogEntry) string {
	if entry == nil {
		return ""
	}
	if name := strVal(entry.AgentName); name != "" {
		return name
	}
	return strVal(entry.AgentType)
}

func intStrPtr(v *int) string {
	if v == nil || *v == 0 {
		return ""
	}
	return intStr(*v)
}
