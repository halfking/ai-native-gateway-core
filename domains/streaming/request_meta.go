package streaming

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"                            //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	telemetryv1 "github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry" //nolint:depguard // RequestLogEntry struct lives here; aliased to avoid clash with /telemetry extractor package
	"github.com/kaixuan/llm-gateway-go/domains/identity"                                  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"                                       //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/settings"                                          //nolint:depguard // hot-reloadable prompt budget
	"github.com/kaixuan/llm-gateway-go/telemetry"                                         //nolint:depguard // canonical IP / agent / protocol extractors
)

var errBodyTooLarge = errors.New("request body too large")

// ── Prompt budget guard (2026-08-24, 245 memcg OOM) ────────────────────────
//
// 245 预发环境在长上下文流量下被 memcg OOM-kill 53 次（anon 1.95G 撑满
// MemoryMax=2G，单请求 max prompt_tokens=920179 —— 一份 ~4MB JSON 在
// 解析/转发/审计路径上被复制多份，数个并发即可把 2G 堆顶穿）。
// gateway.max_prompt_tokens 让网关在入口处拒绝超预算 prompt：
//
//	=0          → 不限制
//	>0           → estimateTokens(body) 超过即 413 prompt_too_large
//
// 默认值为 1048576（1M）。读取侧 5s TTL 缓存，系统配置 DB > env > default。
// 拒绝发生在 JSON 解析与上游转发之前。

const promptBudgetDefaultTokens = 1048576

// promptBudgetLimit resolves the hot-reloadable system setting. The env
// fallback keeps DB-less deployments and tests usable before settings specs
// are registered by the gateway composition root.
func promptBudgetLimit() int {
	if settings.Global != nil && settings.Global.Spec("gateway.max_prompt_tokens") != nil {
		return settings.CachedPlatformInt("gateway.max_prompt_tokens", promptBudgetDefaultTokens)
	}
	return promptBudgetLimitFromEnv()
}

func promptBudgetLimitFromEnv() int {
	v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_MAX_PROMPT_TOKENS"))
	if v == "" {
		return promptBudgetDefaultTokens
	}
	switch strings.ToLower(v) {
	case "0", "off", "false", "disabled":
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return promptBudgetDefaultTokens
	}
	return n
}

// promptBudgetExceeded estimates prompt tokens for the buffered body and
// reports whether the estimate exceeds the configured budget. Always
// (0, false) when the guard is off or the body is empty.
func promptBudgetExceeded(body []byte) (est int, over bool) {
	limit := promptBudgetLimit()
	if limit <= 0 || len(body) == 0 {
		return 0, false
	}
	est = estimateTokens(body)
	return est, est > limit
}

const defaultRequestBodyTimeout = 120 * time.Second

// requestAttemptMeta captures request-side facts as early as possible so
// request_logs rows stay useful even when auth or body read fails later.
//
// 2026-07-15 (docs/会话优化v2/07-08):  扩展为客户端感知的统一提取载体。
// fillAttemptMeta 现在一次性提取全部客户端感知字段（agent/ip/protocol/
// virtual-*/fingerprint 原材），供 request_context_attrs 侧表消费。
type requestAttemptMeta struct {
	APIKeyPrefix    string
	APIKeyOwnerUser string
	ApplicationCode string
	ClientProfile   string
	IdentityHash    string
	RequestMode     string
	KeyStatus       string // missing | valid | invalid_<db-status> | invalid_unknown
	LookupKeyID     *int

	// ─── 客户端感知（2026-07-15）─── 由 fillAttemptMeta 填充，供侧表写入。
	VirtualClientID   string         // "vc-" + hash[:16]
	VirtualIP         string         // 10.x.x.x 派生 IP
	VirtualMAC        string         // 02:xx:xx:xx:xx:xx
	AgentName         string         // claude-code/cursor/curl/...
	AgentType         string         // web/cli/api/bot/mobile/unknown
	ClientIP          string         // X-Real-IP > XFF[0] > RemoteAddr
	ForwardedFor      string         // 完整 XFF 链
	APIKeyFingerprint string         // SHA-256(rawKey)[:16]，在认证阶段设置
	ClientProtocol    string         // openai-chat/anthropic-messages/gemini-generate
	ProjectID         string         // X-Gw-Project-Id
	Namespace         string         // loaded Session.Namespace
	SourceChannel     string         // web/api/mcp/agent
	FingerprintRaw    map[string]any // 原始指纹字段（取证原材）

	// ─── 智能体兜底识别（2026-07-27）───
	// 由 EnsureCaptured 从已缓冲的 body 解析，fillAttemptMeta 阶段用作
	// DetectAgentFromSystemPrompt 的输入，覆盖 User-Agent 不暴露身份的
	// Claude Code / OpenCode / Codex / Cursor / RooCode / Windsurf / Zed /
	// Copilot / Cline / Aider / Continue / Kiro / ZCode 等智能体。
	SystemPrompt string
}

// bufferRequestBody reads the body into memory and replaces r.Body so later
// shouldOverrideAgentName reports whether the header-derived AgentName
// is weak enough that a system-prompt-based override should win.
//
// "unknown" (no UA match) and the generic client libraries (Go-http-client
// / python-requests / curl / postman / insomnia) are considered weak —
// they don't tell us anything about the AI agent using them, while the
// system prompt almost always does (agents self-identify in their first
// system message). Specific agent matches (claude-code / opencode /
// cursor / …) are kept as-is because they're more reliable than a
// prompt substring match.
func shouldOverrideAgentName(headerName, systemPrompt string) bool {
	if strings.TrimSpace(systemPrompt) == "" {
		return false
	}
	switch headerName {
	case "", "unknown",
		"go-client", "python-client",
		"curl", "postman", "insomnia":
		return true
	}
	return false
}

// handlers can re-read without losing bytes.
func bufferRequestBody(r *http.Request, limit int) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	buf, err := readRequestBody(r.Context(), r.Body, limit)
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, err
}

func readRequestBody(ctx context.Context, body io.ReadCloser, limit int) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestBodyTimeout())
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
		resultCh <- result{data: data, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.data, result.err
	case <-ctx.Done():
		_ = body.Close()
		result := <-resultCh
		return result.data, ctx.Err()
	}
}

func requestBodyTimeout() time.Duration {
	seconds, err := strconv.Atoi(os.Getenv("LLM_GATEWAY_ATTACHMENT_REQUEST_BODY_TIMEOUT_SEC"))
	if err != nil || seconds <= 0 {
		return defaultRequestBodyTimeout
	}
	return time.Duration(seconds) * time.Second
}

// ensureRequestBodyBuffered peeks the JSON body once for logging and model
// extraction. Safe to call multiple times.
//
// 2026-06-20 audit fix v2: When the body is buffered but has no "model"
// field (e.g. /v1/messages client omitted model, or body is `{}`),
// set client_model to "<unknown>" so request_logs never shows a blank
// client_model alongside a non-empty request_body. This distinguishes
// "empty body" from "body present but no model field" — both look
// the same otherwise, blocking the operator's diagnostic flow.
func ensureRequestBodyBuffered(r *http.Request, bodyOut *[]byte, modelOut *string) error {
	if bodyOut != nil && len(*bodyOut) > 0 {
		return nil
	}
	buf, err := bufferRequestBody(r, maxBodySize)
	if bodyOut != nil && len(buf) > 0 {
		*bodyOut = buf
	}
	if modelOut != nil && *modelOut == "" && len(buf) > 0 {
		*modelOut = extractModelFromBody(buf)
		// If body was captured but model extraction failed (no
		// "model" field in JSON), mark as <unknown> so the row is
		// unambiguous in the operator's filter queries.
		if *modelOut == "" {
			*modelOut = "<unknown>"
		}
	}
	if len(buf) > maxBodySize {
		return errBodyTooLarge
	}
	return err
}

func maskAPIKeyPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "无key"
	}
	if len(raw) <= 12 {
		return raw + "***"
	}
	return raw[:12] + "***"
}

func formatKeyPrefixDisplay(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	if strings.HasSuffix(prefix, "***") {
		return prefix
	}
	return prefix + "***"
}

func (h *ChatHandler) fillAttemptMeta(r *http.Request, keyInfo *authentication.KeyInfo, meta *requestAttemptMeta) {
	if meta == nil || r == nil {
		return
	}
	if meta.RequestMode == "" {
		meta.RequestMode = requestModeFromPath(r.URL.Path)
	}
	h.resolveKeyMeta(r.Context(), extractBearerToken(r), keyInfo, meta)

	cp := clientProfileFromKey(keyInfo)
	if cp == "" {
		cp = strings.TrimSpace(meta.ClientProfile)
	}
	clientID := identity.BuildIdentityFromRequest(r, tenant(keyInfo), appID(keyInfo), apiKeyIDPtr(keyInfo), cp)
	if meta.ClientProfile == "" {
		meta.ClientProfile = cp
	}
	if meta.IdentityHash == "" {
		meta.IdentityHash = clientID.ShortID()
	}

	// 2026-07-15: 客户端感知字段——一次性从 *http.Request 提取，供侧表消费。
	// 复用 telemetry.Extract*（原死代码）+ identity.ClientIdentity（已算出但未落库）。
	if meta.VirtualClientID == "" {
		meta.VirtualClientID = clientID.VirtualClientID
	}
	if meta.VirtualIP == "" {
		meta.VirtualIP = clientID.VirtualIP
	}
	if meta.VirtualMAC == "" {
		meta.VirtualMAC = clientID.VirtualMAC
	}
	if meta.AgentName == "" {
		meta.AgentName = telemetry.ExtractAgentName(r)
	}
	// 2026-07-27: 智能体兜底识别 — 若 header 路径拿到 "unknown",回退到
	// system prompt 语义匹配 (telemetry.DetectAgentFromSystemPrompt)。
	// 这样 Claude Code / OpenCode / Codex / Cursor / RooCode / Windsurf / Zed /
	// Copilot / Cline / Aider / Continue / Kiro / ZCode 等自报身份的智能体,即使
	// 没在 User-Agent 里暴露身份也能被正确归类,统计面板才不会把它们全归为
	// "unknown"。meta.SystemPrompt 由 EnsureCaptured 在 body 缓冲后填充。
	// 2026-07-27: 智能体兜底识别 — 当 header 路径拿到 "unknown" 或低质量
	// 通用名 (go-client/python-client/curl/postman/insomnia) 时,回退到
	// system prompt 语义匹配 (telemetry.DetectAgentFromSystemPrompt)。
	// 这些通用 UA 是因为 Go/Python 的 HTTP 客户端库本身不带智能体身份,
	// 但 system prompt 里 "You are Claude Code / OpenCode / Codex / Cursor
	// / RooCode / Windsurf / Zed / Copilot / Cline / Aider / Continue / Kiro
	// / ZCode" 才是真实身份信号。直接覆盖低质量名,统计面板才能把真实
	// 智能体从客户端库里分离出来。
	if shouldOverrideAgentName(meta.AgentName, meta.SystemPrompt) {
		if name := telemetry.DetectAgentFromSystemPrompt(meta.SystemPrompt); name != "" {
			meta.AgentName = name
		}
	}
	if meta.AgentType == "" {
		meta.AgentType = telemetry.ExtractAgentType(r)
	}
	if meta.ClientIP == "" {
		meta.ClientIP = telemetry.ExtractClientIP(r)
	}
	if meta.ForwardedFor == "" {
		meta.ForwardedFor = telemetry.ExtractForwardedFor(r)
	}
	if meta.ClientProtocol == "" {
		// body 为空时 DetectProtocolByURL 退回 URL path 路由（openai-chat/
		// anthropic-messages/gemini-generate）。调用方在 body 已知后可覆盖。
		if proto, _, err := ir.DetectProtocolByURL(nil, r.URL.Path); err == nil && proto != "" {
			meta.ClientProtocol = proto
		}
	}
	if meta.ProjectID == "" {
		meta.ProjectID = strings.TrimSpace(r.Header.Get("X-Gw-Project-Id"))
	}
	if meta.SourceChannel == "" {
		meta.SourceChannel = sourceChannelFromRequest(r)
	}
	if meta.FingerprintRaw == nil {
		meta.FingerprintRaw = fingerprintRawMap(clientID.Fingerprint)
	}
}

func (h *ChatHandler) resolveKeyMeta(ctx context.Context, rawKey string, keyInfo *authentication.KeyInfo, meta *requestAttemptMeta) {
	if meta == nil {
		return
	}
	if keyInfo != nil {
		meta.APIKeyPrefix = formatKeyPrefixDisplay(keyInfo.KeyPrefix)
		if meta.APIKeyPrefix == "" {
			meta.APIKeyPrefix = maskAPIKeyPrefix(rawKey)
		}
		if keyInfo.OwnerUser != nil {
			meta.APIKeyOwnerUser = strings.TrimSpace(*keyInfo.OwnerUser)
		}
		meta.ApplicationCode = keyInfo.ApplicationCode
		id := keyInfo.ID
		meta.LookupKeyID = &id
		meta.KeyStatus = "valid"
		if meta.APIKeyFingerprint == "" {
			meta.APIKeyFingerprint = telemetry.APIKeyFingerprint(rawKey)
		}
		return
	}
	if strings.TrimSpace(rawKey) == "" {
		meta.APIKeyPrefix = "无key"
		meta.KeyStatus = "missing"
		return
	}
	if h.keyVerifier != nil && h.keyVerifier.Enabled() {
		if lookup, err := h.keyVerifier.LookupKeyMeta(ctx, rawKey); err == nil && lookup != nil {
			meta.APIKeyPrefix = formatKeyPrefixDisplay(lookup.KeyPrefix)
			if lookup.OwnerUser != nil {
				meta.APIKeyOwnerUser = strings.TrimSpace(*lookup.OwnerUser)
			}
			meta.ApplicationCode = lookup.ApplicationCode
			id := lookup.ID
			meta.LookupKeyID = &id
			meta.KeyStatus = "invalid_" + strings.TrimSpace(lookup.Status)
			if lookup.DefaultClientProfile != nil && strings.TrimSpace(*lookup.DefaultClientProfile) != "" {
				meta.ClientProfile = strings.TrimSpace(*lookup.DefaultClientProfile)
			}
			return
		}
	}
	meta.APIKeyPrefix = maskAPIKeyPrefix(rawKey)
	meta.KeyStatus = "invalid_unknown"
}

func apiKeyIDForLog(keyInfo *authentication.KeyInfo, meta *requestAttemptMeta) *int {
	if keyInfo != nil {
		return apiKeyIDPtr(keyInfo)
	}
	if meta != nil && meta.LookupKeyID != nil {
		return meta.LookupKeyID
	}
	return nil
}

func applicationIDForLog(keyInfo *authentication.KeyInfo) *int {
	if keyInfo == nil {
		return nil
	}
	return appID(keyInfo)
}

// sourceChannelFromRequest derives the request source channel for the side
// table. Priority: explicit X-Client-Channel header > agent_type inference.
// Returns web/api/mcp/agent, or "" when nothing is available.
func sourceChannelFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ch := strings.TrimSpace(r.Header.Get("X-Client-Channel")); ch != "" {
		return ch
	}
	switch telemetry.ExtractAgentType(r) {
	case "web", "mobile":
		return "web"
	case "cli":
		return "agent"
	case "bot":
		return "api"
	case "api":
		return "api"
	}
	return ""
}

// fingerprintRawMap serialises the raw identity.ClientFingerprint into a
// map for the side table's fingerprint_raw JSONB column (forensic material).
func fingerprintRawMap(fp identity.ClientFingerprint) map[string]any {
	return map[string]any{
		"device_seed":     fp.DeviceSeed,
		"machine_id":      fp.MachineID,
		"runtime_name":    fp.RuntimeName,
		"runtime_version": fp.RuntimeVersion,
		"os_name":         fp.OSName,
		"os_arch":         fp.OSArch,
		"user_agent":      fp.UserAgent,
		"client_profile":  fp.ClientProfile,
	}
}

func enrichRequestLogFromMeta(reqLog *telemetryv1.RequestLogEntry, keyInfo *authentication.KeyInfo, meta *requestAttemptMeta) {
	if reqLog == nil || meta == nil {
		return
	}
	// 2026-07-27: 把 meta 中的客户端感知字段透传到 reqLog。
	// 之前只有侧表 request_context_attrs 拿到 agent_name,主表 request_logs_hot
	// 永远是 NULL,导致 GROUP BY agent_name 统计为 0。这里补齐主表写入。
	if meta.AgentName != "" && reqLog.AgentName == nil {
		reqLog.AgentName = strPtr(meta.AgentName)
	}
	if meta.AgentType != "" && reqLog.AgentType == nil {
		reqLog.AgentType = strPtr(meta.AgentType)
	}
	if meta.ClientProtocol != "" && reqLog.ClientProtocol == nil {
		reqLog.ClientProtocol = strPtr(meta.ClientProtocol)
	}
	if meta.VirtualClientID != "" && reqLog.VirtualClientID == nil {
		reqLog.VirtualClientID = strPtr(meta.VirtualClientID)
	}
	if meta.ProjectID != "" && reqLog.ProjectID == nil {
		reqLog.ProjectID = strPtr(meta.ProjectID)
	}
	if meta.Namespace != "" && reqLog.Namespace == nil {
		reqLog.Namespace = strPtr(meta.Namespace)
	}
	if meta.APIKeyPrefix != "" {
		reqLog.APIKeyPrefix = strPtr(meta.APIKeyPrefix)
	}
	if meta.APIKeyOwnerUser != "" {
		reqLog.APIKeyOwnerUser = strPtr(meta.APIKeyOwnerUser)
	}
	if meta.ApplicationCode != "" {
		reqLog.ApplicationCode = strPtr(meta.ApplicationCode)
	}
	if reqLog.APIKeyID == nil {
		reqLog.APIKeyID = apiKeyIDForLog(keyInfo, meta)
	}
	if reqLog.ApplicationID == nil {
		reqLog.ApplicationID = applicationIDForLog(keyInfo)
	}
	if meta.ClientProfile != "" && (reqLog.ClientProfile == nil || strings.TrimSpace(*reqLog.ClientProfile) == "") {
		reqLog.ClientProfile = strPtr(meta.ClientProfile)
	}
	if meta.IdentityHash != "" && (reqLog.IdentityHash == nil || strings.TrimSpace(*reqLog.IdentityHash) == "") {
		reqLog.IdentityHash = strPtr(meta.IdentityHash)
	}
	if meta.RequestMode != "" && (reqLog.RequestMode == nil || strings.TrimSpace(*reqLog.RequestMode) == "") {
		reqLog.RequestMode = strPtr(meta.RequestMode)
	}
}

func keyMetaFromKeyInfo(keyInfo *authentication.KeyInfo) (prefix, owner, appCode string) {
	if keyInfo == nil {
		return "", "", ""
	}
	prefix = formatKeyPrefixDisplay(keyInfo.KeyPrefix)
	if prefix == "" && keyInfo.ID > 0 {
		prefix = fmt.Sprintf("key#%d", keyInfo.ID)
	}
	if keyInfo.OwnerUser != nil {
		owner = strings.TrimSpace(*keyInfo.OwnerUser)
	}
	appCode = strings.TrimSpace(keyInfo.ApplicationCode)
	return prefix, owner, appCode
}

// applyKeyInfoToRequestLog fills api key display fields on a telemetry row.
func applyKeyInfoToRequestLog(reqLog *telemetryv1.RequestLogEntry, keyInfo *authentication.KeyInfo) {
	if reqLog == nil || keyInfo == nil {
		return
	}
	prefix, owner, appCode := keyMetaFromKeyInfo(keyInfo)
	if prefix != "" {
		reqLog.APIKeyPrefix = strPtr(prefix)
	}
	if owner != "" {
		reqLog.APIKeyOwnerUser = strPtr(owner)
	}
	if appCode != "" {
		reqLog.ApplicationCode = strPtr(appCode)
	}
	if reqLog.APIKeyID == nil {
		id := keyInfo.ID
		reqLog.APIKeyID = &id
	}
	if reqLog.ApplicationID == nil {
		reqLog.ApplicationID = appID(keyInfo)
	}
	if reqLog.TenantID == "" {
		reqLog.TenantID = keyInfo.TenantID
	}
}
