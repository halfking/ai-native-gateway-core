package streaming

import (
	"context"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/diagnostics"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// 示例：如何在handler中集成诊断日志

// ServeHTTP 主处理函数示例
func (h *ChatHandler) ServeHTTPWithDiagnostics(w http.ResponseWriter, r *http.Request) {
	requestID := generateRequestID()
	
	// 1. 初始化诊断日志
	diagLog := diagnostics.NewSessionLog(requestID)
	diagLog.ClientModel = getClientModel(r)
	diagLog.TenantID = getTenantID(r)
	diagLog.SessionID = getSessionID(r)
	
	// 存入context
	ctx := diagnostics.ToContext(r.Context(), diagLog)
	r = r.WithContext(ctx)
	
	// 确保请求结束时保存日志
	defer func() {
		diagLog.Finalize(diagLog.FinalStatus, diagLog.Success)
		if err := h.diagnosticLogger.Save(ctx, diagLog); err != nil {
			// 日志保存失败不影响主流程
			slog.Error("failed to save diagnostic log", "error", err, "request_id", requestID)
		}
	}()
	
	// 2. 执行路由和请求处理
	h.handleRequestWithDiagnostics(w, r)
}

// handleRequestWithDiagnostics 处理请求并记录诊断信息
func (h *ChatHandler) handleRequestWithDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	diagLog := diagnostics.FromContext(ctx)
	
	clientModel := diagLog.ClientModel
	profile := getProfile(r)
	
	// 3. 路由阶段
	routingStart := time.Now()
	candidates, err := h.resolver.GetCandidates(ctx, clientModel, profile)
	routingLatency := time.Since(routingStart).Milliseconds()
	
	if err != nil {
		// 路由失败
		diagLog.FinalStatus = http.StatusServiceUnavailable
		diagLog.Success = false
		diagLog.SetFinalError(&diagnostics.ErrorRecord{
			Source:  "gateway",
			Type:    "routing_error",
			Message: err.Error(),
			Code:    classifyRoutingError(err).code,
		})
		
		writeErrorJSON(w, http.StatusServiceUnavailable, diagLog.RequestID,
			"Routing service error", "server_error", "routing_error")
		return
	}
	
	// 记录路由决策
	candidateInfos := make([]diagnostics.CandidateInfo, len(candidates))
	for i, c := range candidates {
		candidateInfos[i] = diagnostics.CandidateInfo{
			ProviderID:   c.ProviderID,
			ProviderName: c.ProviderName,
			CredentialID: c.CredentialID,
			Model:        c.Model,
			CatalogCode:  c.CatalogCode,
		}
	}
	diagLog.RecordRoutingDecision(candidateInfos, 0, routingLatency)
	
	// 4. 执行阶段
	var finalError error
	attemptNo := 1
	
	for _, candidate := range candidates {
		// 开始新的尝试
		attempt := diagLog.StartAttempt(attemptNo, diagnostics.ProviderInfo{
			ProviderID:   candidate.ProviderID,
			ProviderName: candidate.ProviderName,
			CredentialID: candidate.CredentialID,
			Model:        candidate.Model,
			CatalogCode:  candidate.CatalogCode,
		})
		attempt.UpstreamURL = buildUpstreamURL(candidate)
		
		// 调用上游
		resp, execErr := h.executor.Execute(ctx, candidate, r)
		
		// 记录尝试结果
		attempt.EndTime = time.Now()
		attempt.Duration = attempt.EndTime.Sub(attempt.StartTime).Milliseconds()
		attempt.Success = execErr == nil
		
		if resp != nil {
			attempt.StatusCode = resp.StatusCode
		}
		
		if execErr != nil {
			// 记录错误详情
			attempt.Error = extractErrorRecord(execErr, resp)
			
			// 分类错误
			kind := errorsx.ClassifyResponseStatus(resp.StatusCode, resp.Body)
			attempt.ErrorKind = string(kind)
			
			// 决策下一步
			decision := errorsx.DecideNextAction(errorsx.DecisionContext{
				Kind:              kind,
				HasAlternateNode:  attemptNo < len(candidates),
				RemainingAttempts: len(candidates) - attemptNo,
				// ... 其他上下文
			})
			attempt.NextAction = string(decision.Action)
			attempt.ActionReason = decision.ReasonCode
			
			diagLog.AddAttempt(attempt)
			
			// 根据决策执行
			if decision.Action == errorsx.ActionSwitchNode {
				// 切换到下一个节点
				attemptNo++
				finalError = execErr
				continue
			} else if decision.Action == errorsx.ActionFailTerminal {
				// 终止失败
				finalError = execErr
				break
			}
		} else {
			// 成功
			diagLog.AddAttempt(attempt)
			diagLog.FinalStatus = http.StatusOK
			diagLog.Success = true
			
			// 写入成功响应
			writeResponse(w, resp)
			return
		}
		
		attemptNo++
	}
	
	// 5. 所有尝试都失败
	diagLog.FinalStatus = http.StatusServiceUnavailable
	diagLog.Success = false
	diagLog.SetFinalError(&diagnostics.ErrorRecord{
		Source:  "provider",
		Type:    "no_available_provider",
		Message: "All providers failed",
		Code:    "exhausted",
	})
	
	// 使用增强的错误写入函数
	writeErrorJSONEnhanced(
		w,
		http.StatusServiceUnavailable,
		diagLog.RequestID,
		"All providers failed",
		"server_error",
		"no_available_provider",
		"provider",
		extractUpstreamErrorFromLastAttempt(diagLog),
		nil,
		"",
		&diagnostics.RetryInfo{
			Retryable:    false,
			AttemptsMade: diagLog.TotalAttempts,
			MaxAttempts:  len(candidates),
		},
	)
}

// extractErrorRecord 从executor错误中提取ErrorRecord
func extractErrorRecord(execErr error, resp *http.Response) *diagnostics.ErrorRecord {
	record := &diagnostics.ErrorRecord{
		Source:  "provider",
		Message: execErr.Error(),
	}
	
	// 尝试从ExecuteError中提取详细信息
	var ee *ExecuteError
	if errors.As(execErr, &ee) {
		record.Code = ee.Code
		record.Type = ee.Type
		
		if ee.UpstreamError != nil {
			record.UpstreamError = &diagnostics.UpstreamErrorInfo{
				StatusCode: resp.StatusCode,
				Type:       ee.UpstreamError.Type,
				Message:    ee.UpstreamError.Message,
				Code:       ee.UpstreamError.Code,
				Body:       truncateBody(ee.UpstreamError.Body, 4096),
			}
		}
	}
	
	return record
}

// extractUpstreamErrorFromLastAttempt 从最后一次尝试提取上游错误
func extractUpstreamErrorFromLastAttempt(log *diagnostics.SessionDiagnosticLog) *UpstreamErrorInfo {
	if len(log.Attempts) == 0 {
		return nil
	}
	
	lastAttempt := log.Attempts[len(log.Attempts)-1]
	if lastAttempt.Error == nil || lastAttempt.Error.UpstreamError == nil {
		return nil
	}
	
	return &UpstreamErrorInfo{
		StatusCode: lastAttempt.Error.UpstreamError.StatusCode,
		Type:       lastAttempt.Error.UpstreamError.Type,
		Message:    lastAttempt.Error.UpstreamError.Message,
		Code:       lastAttempt.Error.UpstreamError.Code,
	}
}

// truncateBody 截断响应体
func truncateBody(body string, maxLen int) string {
	if len(body) <= maxLen {
		return body
	}
	return body[:maxLen] + "... (truncated)"
}

// 增强的错误写入函数（新增）
type RetryInfo struct {
	Retryable    bool `json:"retryable"`
	RetryAfter   int  `json:"retry_after,omitempty"` // seconds
	AttemptsMade int  `json:"attempts_made"`
	MaxAttempts  int  `json:"max_attempts"`
}

type UpstreamErrorInfo struct {
	StatusCode int                    `json:"status_code"`
	Type       string                 `json:"type,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Code       string                 `json:"code,omitempty"`
	Body       string                 `json:"body,omitempty"`
	Details    map[string]interface{} `json:"details,omitempty"`
}

func writeErrorJSONEnhanced(
	w http.ResponseWriter,
	status int,
	requestID string,
	gatewayMsg string,
	errType string,
	code string,
	source string,
	upstreamErr *UpstreamErrorInfo,
	providerID *int,
	providerName string,
	retryInfo *RetryInfo,
) {
	type ErrorResponse struct {
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
	
	resp := ErrorResponse{RequestID: requestID}
	resp.Error.Type = errType
	resp.Error.Message = gatewayMsg
	resp.Error.Code = code
	resp.Error.Source = source
	
	if upstreamErr != nil {
		resp.Error.UpstreamError = upstreamErr
		resp.Error.ProviderID = providerID
		resp.Error.ProviderName = providerName
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
