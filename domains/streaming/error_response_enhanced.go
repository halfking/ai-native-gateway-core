package streaming

// 错误处理增强 - 2026-11-18
// 目标：保留上游错误信息，添加source字段，支持重试信息

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	if len(respBody) > 4096 {
		info.Body = string(respBody[:4096]) + "... (truncated)"
	} else {
		info.Body = string(respBody)
	}
	
	// 简单脱敏处理（移除可能的凭据信息）
	if info.Message != "" {
		info.Message = sanitizeErrorMessage(info.Message)
	}
	
	return info
}

// sanitizeErrorMessage 简单的错误消息脱敏
func sanitizeErrorMessage(msg string) string {
	// 移除可能的API key
	msg = strings.ReplaceAll(msg, "sk-", "sk-***")
	// 移除可能的token
	if strings.Contains(strings.ToLower(msg), "token") {
		msg = strings.ReplaceAll(msg, "Bearer ", "Bearer ***")
	}
	return msg
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
