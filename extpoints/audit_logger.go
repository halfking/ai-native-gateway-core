package extpoints

import (
	"time"
)

// AuditEvent represents an event to be logged/audited.
type AuditEvent struct {
	// Event identification
	EventID   string
	Timestamp time.Time
	EventType string

	// Request/Response context
	RequestID  string
	Method     string
	Path       string
	StatusCode int

	// User context
	UserID   string
	TenantID string
	ClientIP string

	// Timing information
	Duration       time.Duration
	UpstreamTime   time.Duration
	PluginTime     map[string]time.Duration // Time spent in each plugin

	// Result information
	Success      bool
	Error        string
	FailedPhase  Phase
	FailedPlugin string

	// Request/Response details (optional, can be large)
	RequestHeaders  map[string][]string
	ResponseHeaders map[string][]string
	RequestBody     []byte
	ResponseBody    []byte

	// Custom attributes
	Attributes map[string]interface{}

	// Plugin execution details
	PluginExecutions []PluginExecution
}

// PluginExecution represents the execution of a single plugin in the pipeline.
type PluginExecution struct {
	PluginID  string
	Phase     Phase
	Duration  time.Duration
	Success   bool
	Error     string
	Modified  bool // Whether the plugin modified the request/response
	Skipped   bool // Whether the plugin was skipped (e.g., circuit breaker open)
	Metadata  map[string]interface{}
}

// AuditLogger defines the interface for audit logging plugins.
// These plugins execute asynchronously in the log phase and do not block the response.
type AuditLogger interface {
	PhaseExecutor

	// LogEvent logs an audit event asynchronously.
	// This method should return quickly and perform actual logging in the background.
	LogEvent(ctx *ExecutionContext, event *AuditEvent) error

	// Flush ensures all pending log entries are written.
	// Called during graceful shutdown.
	Flush() error
}

// MetricsCollector defines the interface for metrics collection plugins.
type MetricsCollector interface {
	PhaseExecutor

	// RecordMetric records a metric value.
	RecordMetric(ctx *ExecutionContext, name string, value float64, tags map[string]string) error

	// RecordCounter increments a counter metric.
	RecordCounter(ctx *ExecutionContext, name string, delta int64, tags map[string]string) error

	// RecordHistogram records a histogram value (e.g., latency).
	RecordHistogram(ctx *ExecutionContext, name string, value float64, tags map[string]string) error

	// RecordGauge sets a gauge value.
	RecordGauge(ctx *ExecutionContext, name string, value float64, tags map[string]string) error
}

// TracingProvider defines the interface for distributed tracing plugins.
type TracingProvider interface {
	PhaseExecutor

	// StartSpan starts a new tracing span.
	StartSpan(ctx *ExecutionContext, name string) (spanID string, err error)

	// EndSpan ends a tracing span.
	EndSpan(ctx *ExecutionContext, spanID string, err error) error

	// AddSpanTags adds tags to a span.
	AddSpanTags(ctx *ExecutionContext, spanID string, tags map[string]string) error

	// GetTraceID returns the current trace ID for correlation.
	GetTraceID(ctx *ExecutionContext) string
}

// AccessLogger defines the interface for access logging plugins.
type AccessLogger interface {
	PhaseExecutor

	// LogAccess logs an access entry in standard format (e.g., CLF, JSON).
	LogAccess(ctx *ExecutionContext, req *Request, resp *Response, duration time.Duration) error
}

// ErrorLogger defines the interface for error logging plugins.
type ErrorLogger interface {
	PhaseExecutor

	// LogError logs an error with context.
	LogError(ctx *ExecutionContext, err error, phase Phase, pluginID string) error
}

// SecurityAuditor defines the interface for security audit plugins.
type SecurityAuditor interface {
	PhaseExecutor

	// AuditSecurityEvent logs security-related events (e.g., auth failures, suspicious requests).
	AuditSecurityEvent(ctx *ExecutionContext, eventType string, severity string, details map[string]interface{}) error
}

// ComplianceLogger defines the interface for compliance logging plugins.
type ComplianceLogger interface {
	PhaseExecutor

	// LogComplianceEvent logs events required for regulatory compliance.
	LogComplianceEvent(ctx *ExecutionContext, event *ComplianceEvent) error
}

// ComplianceEvent represents a compliance-related event.
type ComplianceEvent struct {
	EventID     string
	Timestamp   time.Time
	EventType   string
	DataAccess  []DataAccessRecord
	ConsentID   string
	LegalBasis  string
	RetentionID string
	Attributes  map[string]interface{}
}

// DataAccessRecord represents access to sensitive data.
type DataAccessRecord struct {
	DataType     string
	DataCategory string
	Purpose      string
	UserID       string
	Accessed     time.Time
}

// DebugLogger defines the interface for debug logging plugins.
type DebugLogger interface {
	PhaseExecutor

	// LogDebug logs debug information (only in debug mode).
	LogDebug(ctx *ExecutionContext, message string, data map[string]interface{}) error
}

// AlertManager defines the interface for alerting plugins.
type AlertManager interface {
	PhaseExecutor

	// SendAlert sends an alert based on conditions (e.g., error rate, latency).
	SendAlert(ctx *ExecutionContext, alert *Alert) error
}

// Alert represents an alert to be sent.
type Alert struct {
	AlertID   string
	Timestamp time.Time
	Severity  string // critical, warning, info
	Title     string
	Message   string
	Tags      map[string]string
	Metadata  map[string]interface{}
}

// LogLevel defines the severity of a log entry.
type LogLevel string

const (
	LogLevelDebug   LogLevel = "debug"
	LogLevelInfo    LogLevel = "info"
	LogLevelWarning LogLevel = "warning"
	LogLevelError   LogLevel = "error"
	LogLevelCritical LogLevel = "critical"
)

// StructuredLogger defines the interface for structured logging plugins.
type StructuredLogger interface {
	PhaseExecutor

	// Log logs a structured log entry.
	Log(ctx *ExecutionContext, level LogLevel, message string, fields map[string]interface{}) error
}

// LogAggregator defines the interface for log aggregation plugins.
type LogAggregator interface {
	PhaseExecutor

	// Aggregate accumulates log entries and flushes them in batches.
	Aggregate(ctx *ExecutionContext, entries []*AuditEvent) error

	// GetBatchSize returns the current batch size for tuning.
	GetBatchSize() int

	// SetBatchSize sets the batch size for aggregation.
	SetBatchSize(size int) error
}

// LogSampler defines the interface for log sampling plugins.
type LogSampler interface {
	PhaseExecutor

	// ShouldSample determines if an event should be logged based on sampling rules.
	ShouldSample(ctx *ExecutionContext, event *AuditEvent) bool

	// GetSampleRate returns the current sampling rate (0.0 to 1.0).
	GetSampleRate() float64

	// SetSampleRate sets the sampling rate.
	SetSampleRate(rate float64) error
}
