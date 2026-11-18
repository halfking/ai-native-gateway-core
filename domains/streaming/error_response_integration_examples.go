// +build integration_examples

package streaming

// 错误处理增强集成示例 - 2026-11-18
// 展示如何在handler中使用writeErrorJSONEnhanced
// 注意：此文件仅作为参考实现，包含编译错误需要根据实际项目结构调整

import (
	"context"
	"errors"
	"net/http"
	
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 示例1：处理ExecuteError时使用增强错误响应
func (h *ChatHandler) handleExecuteErrorEnhanced(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	execErr error,
	candidates []provider.Candidate,
) {
	var ee *executors.ExecuteError
	if !errors.As(execErr, &ee) {
		// 非ExecuteError，使用原有方式
		writeErrorJSON(w, http.StatusInternalServerError, requestID,
			"Internal server error", "server_error", "internal_error")
		return
	}
	
	// 判断HTTP状态码
	status := http.StatusServiceUnavailable
	if ee.Exhausted {
		status = http.StatusServiceUnavailable
	}
	
	// 提取最后一次尝试的上游错误信息
	var upstreamErr *UpstreamErrorInfo
	var providerID *int
	var providerName string
	
	if len(ee.Attempts) > 0 {
		lastAttempt := ee.Attempts[len(ee.Attempts)-1]
		
		// 从Trace中提取响应体
		if ee.Trace != nil && ee.Trace.UpstreamResponseBody != nil {
			upstreamErr = extractUpstreamErrorInfo(
				execErr,
				ee.Trace.UpstreamResponseBody,
				lastAttempt.HTTPStatus,
			)
		}
		
		providerID = &lastAttempt.ProviderID
		// 从候选列表中查找供应商名称
		for _, c := range candidates {
			if c.ProviderID == lastAttempt.ProviderID {
				providerName = c.ProviderName
				break
			}
		}
	}
	
	// 构建重试信息
	retryInfo := buildRetryInfoFromExecuteError(ee, len(candidates))
	
	// 确定错误消息
	gatewayMsg := "All providers failed"
	if ee.LastErr != nil {
		gatewayMsg = ee.LastErr.Error()
	}
	
	// 确定错误类型和代码
	errType := "server_error"
	code := "no_available_provider"
	
	if ee.LastKind != "" {
		// 根据ErrorKind确定错误类型
		switch ee.LastKind {
		case errorsx.KindRateLimit:
			errType = "rate_limit_error"
			code = "rate_limit"
			status = http.StatusTooManyRequests
		case errorsx.KindAuth:
			errType = "authentication_error"
			code = "invalid_credentials"
			status = http.StatusUnauthorized
		case errorsx.KindQuota:
			errType = "insufficient_quota"
			code = "quota_exceeded"
			status = http.StatusPaymentRequired
		case errorsx.KindModelNotFound:
			errType = "invalid_request_error"
			code = "model_not_found"
			status = http.StatusNotFound
		case errorsx.KindContentFilter:
			errType = "content_filter_error"
			code = "content_policy_violation"
			status = http.StatusBadRequest
		}
	}
	
	// 使用增强的错误写入函数
	writeErrorJSONEnhanced(
		w,
		status,
		requestID,
		gatewayMsg,
		errType,
		code,
		"provider", // 来源是供应商
		upstreamErr,
		providerID,
		providerName,
		retryInfo,
	)
}

// 示例2：处理路由错误时使用增强错误响应
func (h *ChatHandler) handleRoutingErrorEnhanced(
	w http.ResponseWriter,
	requestID string,
	routingErr error,
) {
	// 分类路由错误
	class := classifyRoutingError(routingErr)
	
	// 路由错误来自网关
	writeErrorJSONEnhanced(
		w,
		class.httpStatus,
		requestID,
		class.message,
		"server_error",
		class.code,
		"gateway", // 来源是网关
		nil,       // 无上游错误
		nil,       // 无供应商ID
		"",        // 无供应商名称
		&RetryInfo{
			Retryable:    class.httpStatus >= 500, // 5xx可重试
			AttemptsMade: 0,
			MaxAttempts:  0,
		},
	)
}

// 示例3：处理鉴权错误时使用增强错误响应
func (h *ChatHandler) handleAuthErrorEnhanced(
	w http.ResponseWriter,
	requestID string,
	authErr error,
) {
	writeErrorJSONEnhanced(
		w,
		http.StatusUnauthorized,
		requestID,
		"Invalid authentication credentials",
		"authentication_error",
		"invalid_api_key",
		"gateway", // 来源是网关
		nil,
		nil,
		"",
		&RetryInfo{
			Retryable:    false, // 鉴权错误不可重试
			AttemptsMade: 1,
			MaxAttempts:  1,
		},
	)
}

// 示例4：在主处理流程中集成增强错误响应
func (h *ChatHandler) handleRequestWithEnhancedErrors(w http.ResponseWriter, r *http.Request) {
	requestID := generateRequestID()
	ctx := r.Context()
	
	// 1. 鉴权
	if err := h.authenticate(r); err != nil {
		h.handleAuthErrorEnhanced(w, requestID, err)
		return
	}
	
	// 2. 路由
	clientModel := getClientModel(r)
	profile := getProfile(r)
	candidates, err := h.resolver.GetCandidates(ctx, clientModel, profile)
	if err != nil {
		h.handleRoutingErrorEnhanced(w, requestID, err)
		return
	}
	
	// 3. 执行
	resp, execErr := h.executor.Execute(ctx, candidates, r)
	if execErr != nil {
		h.handleExecuteErrorEnhanced(w, r, requestID, execErr, candidates)
		return
	}
	
	// 4. 成功响应
	writeSuccessResponse(w, resp)
}

// 示例5：向后兼容 - 为现有调用点提供包装函数
// 可以逐步迁移现有的writeErrorJSON调用到增强版本
func writeErrorJSONCompat(w http.ResponseWriter, status int, requestID, msg, errType, code string) {
	// 确定来源（简单启发式）
	source := "gateway"
	if status >= 500 && status < 600 {
		// 5xx可能来自供应商
		source = "provider"
	}
	
	writeErrorJSONEnhanced(
		w,
		status,
		requestID,
		msg,
		errType,
		code,
		source,
		nil, // 无上游错误详情
		nil,
		"",
		nil, // 无重试信息
	)
}

// 实用函数：从provider.Candidate查找供应商名称
func findProviderName(providerID int, candidates []provider.Candidate) string {
	for _, c := range candidates {
		if c.ProviderID == providerID {
			return c.ProviderName
		}
	}
	return ""
}

// 实用函数：判断ErrorKind是否可重试
func isRetryableKind(kind errorsx.ErrorKind) bool {
	switch kind {
	case errorsx.KindTransient,
		errorsx.KindTimeout,
		errorsx.KindNetwork,
		errorsx.KindRateLimit,
		errorsx.KindUpstreamDown,
		errorsx.KindUpstreamOverloaded,
		errorsx.KindConcurrent:
		return true
	default:
		return false
	}
}
