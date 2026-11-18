package streaming

// 错误处理增强 - 2026-11-18
// 目标：保留上游错误信息，添加source字段，支持重试信息

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
)

// UpstreamErrorInfo 上游错误详情
type UpstreamErrorInfo struct {
	StatusCode int                    `json:"status_code"`
	Type       string                 `json:"type,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Code       string                 `json:"code,omitempty"`
	Body       string                 `json:"body,omitempty"` // 截断到4KB
	Details    map[string]interface{} `json:"details,omitempty"`
}

// RetryInfo 重试信息
type RetryInfo struct {
	Retryable    bool `json:"retryable"`
	RetryAfter   int  `json:"retry_after,omitempty"` // seconds
	AttemptsMade int  `json:"attempts_made"`
	MaxAttempts  int  `json:"max_attempts"`
}

// EnhancedErrorResponse 增强的错误响应
type EnhancedErrorResponse struct {
	Error struct {
		Type          string             `json:"type"`
		Message       string             `json:"message"`
		Code          string             `json:"code"`
		Source        string             `json:"source"` // "gateway" | "provider"
		ProviderID    *int               `json:"provider_id,omitempty"`
		ProviderName  string             `json:"provider_name,omitempty"`
		UpstreamError *UpstreamErrorInfo `json:"upstream_error,omitempty"`
		RetryInfo     *RetryInfo         `json:"retry_info,omitempty"`
	} `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// writeErrorJSONEnhanced 增强版错误写入函数（导出供测试）
// 保留上游错误信息，添加source字段和重试信息
func WriteErrorJSONEnhanced(
	w http.ResponseWriter,
	status int,
	requestID string,
	gatewayMsg string,
	errType string,
	code string,
	source string, // "gateway" | "provider"
	upstreamErr *UpstreamErrorInfo,
	providerID *int,
	providerName string,
	retryInfo *RetryInfo,
) {
	resp := EnhancedErrorResponse{RequestID: requestID}
	resp.Error.Type = errType
	resp.Error.Message = gatewayMsg
	resp.Error.Code = code
	resp.Error.Source = source

	// 设置供应商信息（只要有providerID就设置，不依赖upstreamErr）
	if providerID != nil {
		resp.Error.ProviderID = providerID
		resp.Error.ProviderName = providerName
	}

	// 设置上游错误信息
	if upstreamErr != nil {
		resp.Error.UpstreamError = upstreamErr
	}

	if retryInfo != nil {
		resp.Error.RetryInfo = retryInfo
	}

	w.Header().Set("Content-Type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-Id", requestID)
	}

	// 添加路由信息头
	if retryInfo != nil && retryInfo.AttemptsMade > 1 {
		w.Header().Set("X-Gateway-Failover", "true")
		w.Header().Set("X-Gateway-Attempts", fmt.Sprintf("%d", retryInfo.AttemptsMade))
	}

	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// ExtractUpstreamErrorInfo 从ExecuteError或响应体中提取上游错误信息（导出供测试）
func ExtractUpstreamErrorInfo(execErr error, respBody []byte, statusCode int) *UpstreamErrorInfo {
	if respBody == nil || len(respBody) == 0 {
		return nil
	}

	info := &UpstreamErrorInfo{
		StatusCode: statusCode,
	}

	// 尝试解析JSON错误体
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(respBody, &bodyMap); err == nil {
		// 尝试多种上游错误格式
		if errObj, ok := bodyMap["error"].(map[string]interface{}); ok {
			info.Type, _ = errObj["type"].(string)
			info.Message, _ = errObj["message"].(string)
			info.Code, _ = errObj["code"].(string)
			info.Details = errObj
		} else {
			info.Message, _ = bodyMap["message"].(string)
			info.Type, _ = bodyMap["type"].(string)
			info.Code, _ = bodyMap["code"].(string)
		}
	}

	// 保留原始响应体（截断到4KB）
	//
	// ★ D18.3（2026-11-19 审计轮）：`Body` 是上游响应原文，旧实现**完全不过
	// 滤**——上游把凭据放在 body 的任意位置（甚至 HTML 错误页里）时，会随
	// 错误响应整体下发到客户端。现与 Message 走同一套整值脱敏。
	if len(respBody) > 4096 {
		info.Body = sanitizeErrorMessage(string(respBody[:4096])) + "... (truncated)"
	} else {
		info.Body = sanitizeErrorMessage(string(respBody))
	}

	// 整值脱敏：Message / Type / Code 一并过一遍。
	//
	// ★ D18.3：旧实现只脱敏 Message，Type 与 Code 原样透传——上游把凭据塞
	// 在 `code` 字段里同样会泄露。
	if info.Message != "" {
		info.Message = sanitizeErrorMessage(info.Message)
	}
	if info.Type != "" {
		info.Type = sanitizeErrorMessage(info.Type)
	}
	if info.Code != "" {
		info.Code = sanitizeErrorMessage(info.Code)
	}
	// ★ D18.3：`Details` 是上游 error 对象的原样 map（旧实现零过滤）。
	if info.Details != nil {
		redactErrorDetails(info.Details)
	}

	return info
}

// sanitizeErrorMessage 对错误消息做**整值脱敏**。
//
// ★ D18.3（2026-11-19 审计轮）：旧实现是「前缀替换」——
//
//	msg = strings.ReplaceAll(msg, "sk-", "sk-***")
//	if strings.Contains(strings.ToLower(msg), "token") {
//	    msg = strings.ReplaceAll(msg, "Bearer ", "Bearer ***")
//	}
//
// 两处都不成立：
//
//	① `sk-ant-api03-XYZ` → `sk-***ant-api03-XYZ`——**密钥主体原样保留**，
//	   只换掉了前缀，等于没脱敏；
//	② `Bearer` 的替换**取决于消息里是否出现 "token" 字样**——
//	   `Bearer eyJhbGci…`（真 JWT）若消息里没提 "token" 就原样保留，
//	   条件与要保护的内容毫无关系。
//
// 现改为「命中即整体替换为 ***」，覆盖厂商密钥前缀、Bearer 令牌、JWT
// 三类形态，且**不依赖上下文关键词**。
//
// 本函数当前**零生产调用方**（生产错误出口仍是 handler.go 的
// writeErrorJSON）——本轮是「先修再接线」：接线前先把脱敏做对。
func sanitizeErrorMessage(msg string) string {
	if msg == "" {
		return msg
	}
	msg = redactJWT(msg)
	msg = redactBearer(msg)
	msg = redactVendorKey(msg)
	return msg
}

var (
	// redactVendorKeyRe 匹配厂商密钥/令牌（Anthropic、OpenAI、GitHub、
	// GitLab、Slack、Google、AWS、PyPI、HuggingFace 等）。
	redactVendorKeyRe = regexp.MustCompile(`(?i)\b(?:sk-ant-|sk-proj-|sk-or-v1-|sk-|ak-|ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|glpat-|xoxb-|xoxp-|xoxa-|xoxr-|AIza|AKIA|ASIA|npm_|hf_)[A-Za-z0-9_\-]{6,}`)
	// redactBearerRe 匹配 `Bearer <令牌>`（整个令牌一并替换）。
	redactBearerRe = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	// redactJWTRe 匹配 JWT（三段点分）。
	redactJWTRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}`)
)

// redactVendorKey 把厂商密钥形态的子串整体替换为 ***。
func redactVendorKey(s string) string {
	return redactVendorKeyRe.ReplaceAllString(s, "***")
}

// redactBearer 把 `Bearer <令牌>` 的令牌部分替换为 ***（保留 "Bearer " 前缀
// 以便客户端知道这是认证失败，而不是格式错误）。
func redactBearer(s string) string {
	return redactBearerRe.ReplaceAllString(s, "Bearer ***")
}

// redactJWT 把 JWT 整体替换为 ***。
func redactJWT(s string) string {
	return redactJWTRe.ReplaceAllString(s, "***")
}

// redactErrorDetails 递归脱敏上游错误对象里的所有字符串值。
//
// ★ D18.3：`info.Details` 是上游 error 对象的**原样 map**，旧实现完全不过
// 滤——上游把凭据放在 `details.request.headers.authorization` 这类嵌套
// 位置时，会随错误响应整体下发到客户端。
func redactErrorDetails(details map[string]interface{}) {
	for k, v := range details {
		switch tv := v.(type) {
		case string:
			details[k] = sanitizeErrorMessage(tv)
		case map[string]interface{}:
			redactErrorDetails(tv)
		case []interface{}:
			for i, item := range tv {
				switch ti := item.(type) {
				case string:
					tv[i] = sanitizeErrorMessage(ti)
				case map[string]interface{}:
					redactErrorDetails(ti)
				}
			}
		}
	}
}

// buildRetryInfoFromExecuteError 从ExecuteError构建重试信息
func buildRetryInfoFromExecuteError(execErr *executors.ExecuteError, maxAttempts int) *RetryInfo {
	if execErr == nil {
		return nil
	}

	retryInfo := &RetryInfo{
		Retryable:    !execErr.Exhausted,
		AttemptsMade: execErr.Tried,
		MaxAttempts:  maxAttempts,
	}

	if execErr.RetryAfterSeconds > 0 {
		retryInfo.RetryAfter = execErr.RetryAfterSeconds
	}

	return retryInfo
}

// determineErrorSource 判断错误来源
func determineErrorSource(execErr error) string {
	if execErr == nil {
		return "gateway"
	}

	// 如果是ExecuteError，通常来自供应商
	var ee *executors.ExecuteError
	if errors.As(execErr, &ee) {
		if ee.Tried > 0 {
			return "provider"
		}
	}

	// 其他类型的错误（路由、鉴权等）来自网关
	return "gateway"
}
