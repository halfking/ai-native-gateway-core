package domain

// AuditContext 封装审计/可观测性领域的上下文。
type AuditContext struct {
	Builder       any
	StreamCapture any
	Tags          []string
	SamplingRate  float64
	ShouldEmit    bool
	RequestID     string
	StartTimeUnix int64
}

// AuditResponseContext 审计响应上下文。
type AuditResponseContext struct {
	Emitted        bool
	EndTimeUnix    int64
	FailureReason  string
	StatusCategory string
}
