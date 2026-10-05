// Package sessionv2mirror — s1a_fields.go
//
// 存储优化方案 v2 S1a（migration 706/707）：把 telemetry.RequestLogEntry 上
// request_logs 独有的五类数据（计费/路由/诊断/检索·完整性/访问维度）补采进
// V2 会话族，使 session_turns 成为 turn 级唯一事实源（plan §3 D1）。
//
// 数据源事实（2026-09-14 审计，2026-10-02 订正）：RequestLogEntry 曾缺
// TraceEvents / SearchText / RequestChecksum / RawModelName —— 这些列已建
// （707）但保持零值；RawModelName 已于 2026-10-02 接线（entry.OutboundModel/
// ClientModel → req.RawModelName，见 s1a_raw_model_name_test.go），其余三项
// 仍零值。缺 StreamDoneSent，最接近的 StreamDoneReceived 作映射。缺源列由
// S2 视图 NULL 补位登记（plan §9）。
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
	// 2026-10-05 审计 §9.208: client_protocol 与 agent_name/agent_type 是同一组
	// 「客户端身份」列，但**只有它**没被带过来 ⇒ session_turns.client_protocol
	// 实测 30 天 0/1,659,271 非空，而它的两个同族列是 98.8%。
	// 后果不是「少一列数据」而是**用户可见的**：
	// storage.admin_logs_native_turns_read=true 时 admin 请求日志列表直读
	// session 族原生投影（admin/logs.go:204 选 rl.client_protocol），
	// 而该投影来自这批行 ⇒ 列表里这个字段**恒为空**。
	req.ClientProtocol = strVal(entry.ClientProtocol)

	// 730 会话角色归因三列（R50 F15 写入方）：role/父会话是 Mirror-only
	// 传输字段；父任务复用 GwTaskID 关联头。空串零值由 upsert 的
	// NULLIF/COALESCE 归一（'main' 列默认 / NULL）。
	req.AgentRole = strVal(entry.AgentRole)
	req.ParentSessionID = strVal(entry.ParentSessionID)
	req.ParentTaskID = strVal(entry.GwTaskID)

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
	// raw_model_name = 绑定解析出的**上游原始模型名**（2026-10-02 接线，审计 §9.60.8）。
	//
	// 取值口径与 §9.30.2 的比对口径逐字一致：COALESCE(outbound_model, client_model)。
	// 这**不是**一个新字段——telemetry.RequestLogEntry 本来就同时带 OutboundModel
	// 与 ClientModel（client.go:274-275），本文件早前的注释「RequestLogEntry 无此字段」
	// 是错的，§9.30.2 据此把「补源字段」当成端口前置也是错的。真实情况是**接线漏了**。
	//
	// 为什么不能拿 session_turns.model 顶替：那是 client_model 的同义列
	// （§9.12.1 实测 1138/1138）。252 生产库近 7 天实测 outbound_model <> client_model
	// 的行有 3,227/29,201 = 11.0%，且差异是**真实映射**而非噪声：
	// `minimax-m3 → MiniMax-M3`、`glm-5.3 → glm-5.3-flash`（大小写 + 别名）。
	// 在这 11% 的行上用 client_model 去比 provider_models.raw_model_name，
	// credential_recovery 的下架判定会系统性误判。
	//
	// 为什么必须 COALESCE：outbound_model 在 252 有 4,903/29,201 = 16.8% 为 NULL
	//（请求未走到解析出上游名，或走 passthrough），此时与 v1 的比对口径一致地
	// 回落到 client_model，而不是留空——留空会让这一列在两族之间**从「同义」变成
	// 「一半缺值」**，那才是真的不可比。
	req.RawModelName = firstNonEmpty(strVal(entry.OutboundModel), strVal(entry.ClientModel))

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

	// 检索/完整性组（§9.98：SearchText 的源已接上，RequestChecksum 仍缺源）
	//
	// SearchText 此前「缺源，保持零值」⇒ session_turns.search_text 252 实测 0% 填充，
	// 而 v1 侧 100% ⇒ **退役 v1 会静默杀掉全文检索**（§9.97 阻塞 #1）。
	// searchText(entry) 是纯函数，两侧结果逐字节相同。
	if st := telemetry.SearchText(entry); st != nil {
		req.SearchText = *st
	}
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
