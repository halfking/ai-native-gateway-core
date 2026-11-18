package diagnostics

import (
	"context"
	"encoding/json"
	"time"
)

// SessionDiagnosticLog 会话诊断日志 - 记录单个请求的完整执行过程
type SessionDiagnosticLog struct {
	RequestID   string    `json:"request_id"`
	SessionID   string    `json:"session_id,omitempty"`
	ClientModel string    `json:"client_model"`
	TenantID    string    `json:"tenant_id"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	FinalStatus int       `json:"final_status"`
	Success     bool      `json:"success"`

	// 路由信息
	RoutingDecision *RoutingDecision `json:"routing_decision,omitempty"`

	// 执行历史
	Attempts []*AttemptRecord `json:"attempts"`

	// 最终结果
	FinalError *ErrorRecord `json:"final_error,omitempty"`

	// 统计
	TotalAttempts int   `json:"total_attempts"`
	Switches      int   `json:"switches"`
	Retries       int   `json:"retries"`
	TotalDuration int64 `json:"total_duration_ms"`
}

// RoutingDecision 路由决策信息
type RoutingDecision struct {
	Candidates     []CandidateInfo `json:"candidates"`
	SelectedIndex  int             `json:"selected_index"`
	SelectionTime  time.Time       `json:"selection_time"`
	RoutingLatency int64           `json:"routing_latency_ms"`
}

// CandidateInfo 候选信息
type CandidateInfo struct {
	ProviderID   int    `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
	CatalogCode  string `json:"catalog_code,omitempty"`
	Priority     int    `json:"priority,omitempty"`
}

// AttemptRecord 单次尝试记录
type AttemptRecord struct {
	AttemptNo   int          `json:"attempt_no"`
	Provider    ProviderInfo `json:"provider"`
	StartTime   time.Time    `json:"start_time"`
	EndTime     time.Time    `json:"end_time"`
	Duration    int64        `json:"duration_ms"`
	UpstreamURL string       `json:"upstream_url,omitempty"`

	// 响应信息
	StatusCode int  `json:"status_code"`
	Success    bool `json:"success"`

	// 错误信息
	Error *ErrorRecord `json:"error,omitempty"`

	// 决策信息
	ErrorKind    string `json:"error_kind,omitempty"`
	NextAction   string `json:"next_action,omitempty"`
	ActionReason string `json:"action_reason,omitempty"`
}

// ProviderInfo 供应商信息
type ProviderInfo struct {
	ProviderID   int    `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
	CatalogCode  string `json:"catalog_code,omitempty"`
}

// ErrorRecord 错误详情
type ErrorRecord struct {
	Source        string             `json:"source"` // "gateway" | "provider"
	Type          string             `json:"type"`
	Message       string             `json:"message"`
	Code          string             `json:"code"`
	UpstreamError *UpstreamErrorInfo `json:"upstream_error,omitempty"`
}

// UpstreamErrorInfo 上游错误详情
type UpstreamErrorInfo struct {
	StatusCode int                    `json:"status_code"`
	Type       string                 `json:"type,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Code       string                 `json:"code,omitempty"`
	Body       string                 `json:"body,omitempty"` // 截断到4KB
	Details    map[string]interface{} `json:"details,omitempty"`
}

// NewSessionLog 创建新的会话诊断日志
func NewSessionLog(requestID string) *SessionDiagnosticLog {
	return &SessionDiagnosticLog{
		RequestID: requestID,
		StartTime: time.Now(),
		Attempts:  make([]*AttemptRecord, 0),
	}
}

// RecordRoutingDecision 记录路由决策
func (s *SessionDiagnosticLog) RecordRoutingDecision(candidates []CandidateInfo, selectedIndex int, latency int64) {
	s.RoutingDecision = &RoutingDecision{
		Candidates:     candidates,
		SelectedIndex:  selectedIndex,
		SelectionTime:  time.Now(),
		RoutingLatency: latency,
	}
}

// StartAttempt 开始新的尝试
func (s *SessionDiagnosticLog) StartAttempt(attemptNo int, provider ProviderInfo) *AttemptRecord {
	attempt := &AttemptRecord{
		AttemptNo: attemptNo,
		Provider:  provider,
		StartTime: time.Now(),
	}
	return attempt
}

// AddAttempt 添加尝试记录
func (s *SessionDiagnosticLog) AddAttempt(attempt *AttemptRecord) {
	s.Attempts = append(s.Attempts, attempt)
	s.TotalAttempts++

	if attempt.NextAction == "switch_node" {
		s.Switches++
	} else if attempt.NextAction == "retry_same_node" {
		s.Retries++
	}
}

// SetFinalError 设置最终错误
func (s *SessionDiagnosticLog) SetFinalError(err *ErrorRecord) {
	s.FinalError = err
}

// Finalize 完成日志记录
func (s *SessionDiagnosticLog) Finalize(status int, success bool) {
	s.EndTime = time.Now()
	s.FinalStatus = status
	s.Success = success
	s.TotalDuration = s.EndTime.Sub(s.StartTime).Milliseconds()
}

// ToJSON 转换为JSON
func (s *SessionDiagnosticLog) ToJSON() ([]byte, error) {
	return json.Marshal(s)
}

// FromJSON 从JSON解析
func FromJSON(data []byte) (*SessionDiagnosticLog, error) {
	var log SessionDiagnosticLog
	err := json.Unmarshal(data, &log)
	return &log, err
}

// contextKey 用于在context中存储诊断日志
type contextKey int

const diagnosticLogKey contextKey = 0

// ToContext 将诊断日志存入context
func ToContext(ctx context.Context, log *SessionDiagnosticLog) context.Context {
	return context.WithValue(ctx, diagnosticLogKey, log)
}

// FromContext 从context中获取诊断日志
func FromContext(ctx context.Context) *SessionDiagnosticLog {
	if log, ok := ctx.Value(diagnosticLogKey).(*SessionDiagnosticLog); ok {
		return log
	}
	return nil
}
