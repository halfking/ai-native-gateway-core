package streaming

import (
	"context"
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"                //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	telemetryv1 "github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // aliased to avoid clash with /telemetry extractor package
)

// BuildContextAttrsEntry 把 RequestLogContext + keyInfo + meta 组装成
// telemetry.ContextAttrsEntry，供侧表 request_context_attrs 写入。
//
// 与 BuildFailureEntry/BuildSuccessEntry 同位，但**只构造侧表行**，不构造
// request_logs 行。两个入口是独立的（侧表失败不阻塞主请求日志）。
//
// 入参 ctx 用于桥接中间件后到达的兜底值（如 origin_mw 写入的 origin.client_ip
// 在 fillAttemptMeta 之后才有，故通过 ApplyAttrsFromContext 兜底）。
func BuildContextAttrsEntry(
	c *RequestLogContext,
	keyInfo *authentication.KeyInfo,
	meta *requestAttemptMeta,
	ctx context.Context,
) *telemetryv1.ContextAttrsEntry {
	if c == nil || c.RequestID == "" {
		return nil
	}

	entry := &telemetryv1.ContextAttrsEntry{
		RequestID: c.RequestID,
		TenantID:  tenantForCtx(keyInfo, meta),
	}

	// ─── 会话维度 ───
	if sid, tid := c.SessionTask(); sid != "" {
		s := sid
		entry.GwSessionID = &s
		if tid != "" {
			t := tid
			entry.GwTaskID = &t
			entry.TaskID = &t
		}
	}

	// ─── 客户端信息（来自 meta）───
	if meta != nil {
		fillFromMeta(entry, meta)
	}
	if keyInfo != nil {
		fillFromKeyInfo(entry, keyInfo)
	}

	// ─── 请求性质维度（来自 RequestLogContext）───
	fillFromRequestLogContext(entry, c)

	// ─── ctx 兜底（middleware 后到达的值）───
	if ctx != nil {
		entry.ApplyAttrsFromContext(ctx)
	}

	return entry
}

// fillFromMeta 把 requestAttemptMeta 的客户端感知字段映射到侧表行。
func fillFromMeta(entry *telemetryv1.ContextAttrsEntry, meta *requestAttemptMeta) {
	if meta.IdentityHash != "" {
		s := meta.IdentityHash
		entry.IdentityHash = &s
	}
	if meta.VirtualClientID != "" {
		s := meta.VirtualClientID
		entry.VirtualClientID = &s
	}
	if meta.VirtualIP != "" {
		s := meta.VirtualIP
		entry.VirtualIP = &s
	}
	if meta.VirtualMAC != "" {
		s := meta.VirtualMAC
		entry.VirtualMAC = &s
	}
	if meta.AgentName != "" {
		s := meta.AgentName
		entry.AgentName = &s
	}
	if meta.AgentType != "" {
		s := meta.AgentType
		entry.AgentType = &s
	}
	if meta.ClientIP != "" {
		s := meta.ClientIP
		entry.ClientIP = &s
	}
	if meta.ForwardedFor != "" {
		s := meta.ForwardedFor
		entry.ClientForwardedFor = &s
	}
	if meta.APIKeyFingerprint != "" {
		s := meta.APIKeyFingerprint
		entry.APIKeyFingerprint = &s
	}
	if meta.ClientProtocol != "" {
		s := meta.ClientProtocol
		entry.ClientProtocol = &s
	}
	if meta.ProjectID != "" {
		s := meta.ProjectID
		entry.ProjectID = &s
	}
	if meta.SourceChannel != "" {
		s := meta.SourceChannel
		entry.SourceChannel = &s
	}
	if len(meta.FingerprintRaw) > 0 {
		if raw, err := json.Marshal(meta.FingerprintRaw); err == nil {
			entry.FingerprintRaw = raw
		}
	}
}

// fillFromRequestLogContext 把 RequestLogContext 的请求性质字段映射到侧表行。
func fillFromRequestLogContext(entry *telemetryv1.ContextAttrsEntry, c *RequestLogContext) {
	if c == nil {
		return
	}
	if c.AttemptNo > 0 {
		n := c.AttemptNo
		entry.AttemptNo = &n
	}
	entry.IsRetry = c.IsRetry
	if c.TurnNo > 0 {
		n := c.TurnNo
		entry.TurnNo = &n
	}
	if c.OriginStage != "" {
		s := c.OriginStage
		entry.OriginStage = &s
		entry.IsProbe = isProbeOriginStage(s)
	}
	if c.ClientRequestID != "" {
		s := c.ClientRequestID
		entry.ClientRequestID = &s
	}
	if c.EndUser != "" {
		s := c.EndUser
		entry.EndUserID = &s
	}
}

// isProbeOriginStage 由 origin_stage 派生 is_probe（双轨之一，与 origin_stage 同义）。
func isProbeOriginStage(stage string) bool {
	switch stage {
	case "self_check", "node_probe", "system_health",
		"probe_direct", "probe_v2", "model_probe", "passive_probe", "manual":
		return true
	}
	return false
}

// fillFromKeyInfo 把 KeyInfo 的账户/应用/客户字段映射到侧表行。
func fillFromKeyInfo(entry *telemetryv1.ContextAttrsEntry, keyInfo *authentication.KeyInfo) {
	if keyInfo == nil {
		return
	}
	if keyInfo.ID > 0 {
		id := keyInfo.ID
		entry.APIKeyID = &id
	}
	if keyInfo.ApplicationID > 0 {
		appID := keyInfo.ApplicationID
		entry.ApplicationID = &appID
	}
	if keyInfo.ApplicationCode != "" {
		s := keyInfo.ApplicationCode
		entry.ApplicationCode = &s
	}
	if keyInfo.OwnerUser != nil && *keyInfo.OwnerUser != "" {
		s := *keyInfo.OwnerUser
		entry.OwnerUser = &s
	}
	if keyInfo.CustomerID != nil {
		cid := *keyInfo.CustomerID
		entry.CustomerID = &cid
	}
}

func tenantForCtx(keyInfo *authentication.KeyInfo, meta *requestAttemptMeta) string {
	if keyInfo != nil && keyInfo.TenantID != "" {
		return keyInfo.TenantID
	}
	if meta != nil && meta.KeyStatus == "valid" {
		return "default"
	}
	return "default"
}