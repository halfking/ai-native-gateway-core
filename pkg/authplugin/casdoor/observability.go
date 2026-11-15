package casdoor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsCollector collects authentication and authorization metrics.
type MetricsCollector struct {
	mu sync.RWMutex

	// Authentication metrics
	authAttempts     int64
	authSuccesses    int64
	authFailures     int64
	authLatencySum   int64 // in milliseconds
	authLatencyCount int64

	// Token metrics
	tokenValidations    int64
	tokenValidSuccesses int64
	tokenValidFailures  int64
	tokenRefreshes      int64

	// User metrics
	userCreations int64
	userUpdates   int64
	userDeletions int64
	userLookups   int64

	// MFA metrics
	mfaEnabled       int64
	mfaVerifications int64
	mfaSuccesses     int64
	mfaFailures      int64

	// Session metrics
	sessionCreations int64
	sessionRefreshes int64
	sessionDeletions int64
	activeSessions   int64

	// Rate limiting metrics
	rateLimitChecks  int64
	rateLimitBlocked int64

	// Error metrics
	errors   map[string]int64
	errorsMu sync.RWMutex

	// Start time
	startTime time.Time
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		errors:    make(map[string]int64),
		startTime: time.Now(),
	}
}

// Authentication Metrics

// RecordAuthAttempt records an authentication attempt.
func (m *MetricsCollector) RecordAuthAttempt(success bool, latency time.Duration) {
	atomic.AddInt64(&m.authAttempts, 1)
	if success {
		atomic.AddInt64(&m.authSuccesses, 1)
	} else {
		atomic.AddInt64(&m.authFailures, 1)
	}
	atomic.AddInt64(&m.authLatencySum, latency.Milliseconds())
	atomic.AddInt64(&m.authLatencyCount, 1)
}

// RecordTokenValidation records a token validation.
func (m *MetricsCollector) RecordTokenValidation(success bool) {
	atomic.AddInt64(&m.tokenValidations, 1)
	if success {
		atomic.AddInt64(&m.tokenValidSuccesses, 1)
	} else {
		atomic.AddInt64(&m.tokenValidFailures, 1)
	}
}

// RecordTokenRefresh records a token refresh.
func (m *MetricsCollector) RecordTokenRefresh() {
	atomic.AddInt64(&m.tokenRefreshes, 1)
}

// User Metrics

// RecordUserCreation records a user creation.
func (m *MetricsCollector) RecordUserCreation() {
	atomic.AddInt64(&m.userCreations, 1)
}

// RecordUserUpdate records a user update.
func (m *MetricsCollector) RecordUserUpdate() {
	atomic.AddInt64(&m.userUpdates, 1)
}

// RecordUserDeletion records a user deletion.
func (m *MetricsCollector) RecordUserDeletion() {
	atomic.AddInt64(&m.userDeletions, 1)
}

// RecordUserLookup records a user lookup.
func (m *MetricsCollector) RecordUserLookup() {
	atomic.AddInt64(&m.userLookups, 1)
}

// MFA Metrics

// RecordMFAEnabled records MFA being enabled.
func (m *MetricsCollector) RecordMFAEnabled() {
	atomic.AddInt64(&m.mfaEnabled, 1)
}

// RecordMFAVerification records an MFA verification.
func (m *MetricsCollector) RecordMFAVerification(success bool) {
	atomic.AddInt64(&m.mfaVerifications, 1)
	if success {
		atomic.AddInt64(&m.mfaSuccesses, 1)
	} else {
		atomic.AddInt64(&m.mfaFailures, 1)
	}
}

// Session Metrics

// RecordSessionCreation records a session creation.
func (m *MetricsCollector) RecordSessionCreation() {
	atomic.AddInt64(&m.sessionCreations, 1)
	atomic.AddInt64(&m.activeSessions, 1)
}

// RecordSessionRefresh records a session refresh.
func (m *MetricsCollector) RecordSessionRefresh() {
	atomic.AddInt64(&m.sessionRefreshes, 1)
}

// RecordSessionDeletion records a session deletion.
func (m *MetricsCollector) RecordSessionDeletion() {
	atomic.AddInt64(&m.sessionDeletions, 1)
	atomic.AddInt64(&m.activeSessions, -1)
}

// Rate Limiting Metrics

// RecordRateLimitCheck records a rate limit check.
func (m *MetricsCollector) RecordRateLimitCheck(blocked bool) {
	atomic.AddInt64(&m.rateLimitChecks, 1)
	if blocked {
		atomic.AddInt64(&m.rateLimitBlocked, 1)
	}
}

// Error Metrics

// RecordError records an error by type.
func (m *MetricsCollector) RecordError(errorType string) {
	m.errorsMu.Lock()
	defer m.errorsMu.Unlock()
	m.errors[errorType]++
}

// Metrics represents collected metrics.
type Metrics struct {
	// Authentication
	AuthAttempts     int64   `json:"auth_attempts"`
	AuthSuccesses    int64   `json:"auth_successes"`
	AuthFailures     int64   `json:"auth_failures"`
	AuthSuccessRate  float64 `json:"auth_success_rate"`
	AuthAvgLatencyMs float64 `json:"auth_avg_latency_ms"`

	// Tokens
	TokenValidations  int64   `json:"token_validations"`
	TokenValidSuccess int64   `json:"token_valid_successes"`
	TokenValidFailure int64   `json:"token_valid_failures"`
	TokenValidRate    float64 `json:"token_valid_rate"`
	TokenRefreshes    int64   `json:"token_refreshes"`

	// Users
	UserCreations int64 `json:"user_creations"`
	UserUpdates   int64 `json:"user_updates"`
	UserDeletions int64 `json:"user_deletions"`
	UserLookups   int64 `json:"user_lookups"`

	// MFA
	MFAEnabled       int64   `json:"mfa_enabled"`
	MFAVerifications int64   `json:"mfa_verifications"`
	MFASuccesses     int64   `json:"mfa_successes"`
	MFAFailures      int64   `json:"mfa_failures"`
	MFASuccessRate   float64 `json:"mfa_success_rate"`

	// Sessions
	SessionCreations int64 `json:"session_creations"`
	SessionRefreshes int64 `json:"session_refreshes"`
	SessionDeletions int64 `json:"session_deletions"`
	ActiveSessions   int64 `json:"active_sessions"`

	// Rate Limiting
	RateLimitChecks    int64   `json:"rate_limit_checks"`
	RateLimitBlocked   int64   `json:"rate_limit_blocked"`
	RateLimitBlockRate float64 `json:"rate_limit_block_rate"`

	// Errors
	Errors map[string]int64 `json:"errors"`

	// General
	Uptime time.Duration `json:"uptime"`
}

// GetMetrics returns current metrics snapshot.
func (m *MetricsCollector) GetMetrics() *Metrics {
	metrics := &Metrics{
		AuthAttempts:      atomic.LoadInt64(&m.authAttempts),
		AuthSuccesses:     atomic.LoadInt64(&m.authSuccesses),
		AuthFailures:      atomic.LoadInt64(&m.authFailures),
		TokenValidations:  atomic.LoadInt64(&m.tokenValidations),
		TokenValidSuccess: atomic.LoadInt64(&m.tokenValidSuccesses),
		TokenValidFailure: atomic.LoadInt64(&m.tokenValidFailures),
		TokenRefreshes:    atomic.LoadInt64(&m.tokenRefreshes),
		UserCreations:     atomic.LoadInt64(&m.userCreations),
		UserUpdates:       atomic.LoadInt64(&m.userUpdates),
		UserDeletions:     atomic.LoadInt64(&m.userDeletions),
		UserLookups:       atomic.LoadInt64(&m.userLookups),
		MFAEnabled:        atomic.LoadInt64(&m.mfaEnabled),
		MFAVerifications:  atomic.LoadInt64(&m.mfaVerifications),
		MFASuccesses:      atomic.LoadInt64(&m.mfaSuccesses),
		MFAFailures:       atomic.LoadInt64(&m.mfaFailures),
		SessionCreations:  atomic.LoadInt64(&m.sessionCreations),
		SessionRefreshes:  atomic.LoadInt64(&m.sessionRefreshes),
		SessionDeletions:  atomic.LoadInt64(&m.sessionDeletions),
		ActiveSessions:    atomic.LoadInt64(&m.activeSessions),
		RateLimitChecks:   atomic.LoadInt64(&m.rateLimitChecks),
		RateLimitBlocked:  atomic.LoadInt64(&m.rateLimitBlocked),
		Uptime:            time.Since(m.startTime),
	}

	// Calculate rates
	if metrics.AuthAttempts > 0 {
		metrics.AuthSuccessRate = float64(metrics.AuthSuccesses) / float64(metrics.AuthAttempts) * 100
	}
	if metrics.TokenValidations > 0 {
		metrics.TokenValidRate = float64(metrics.TokenValidSuccess) / float64(metrics.TokenValidations) * 100
	}
	if metrics.MFAVerifications > 0 {
		metrics.MFASuccessRate = float64(metrics.MFASuccesses) / float64(metrics.MFAVerifications) * 100
	}
	if metrics.RateLimitChecks > 0 {
		metrics.RateLimitBlockRate = float64(metrics.RateLimitBlocked) / float64(metrics.RateLimitChecks) * 100
	}

	// Calculate average latency
	latencyCount := atomic.LoadInt64(&m.authLatencyCount)
	if latencyCount > 0 {
		latencySum := atomic.LoadInt64(&m.authLatencySum)
		metrics.AuthAvgLatencyMs = float64(latencySum) / float64(latencyCount)
	}

	// Copy errors
	m.errorsMu.RLock()
	metrics.Errors = make(map[string]int64, len(m.errors))
	for k, v := range m.errors {
		metrics.Errors[k] = v
	}
	m.errorsMu.RUnlock()

	return metrics
}

// Reset resets all metrics.
func (m *MetricsCollector) Reset() {
	atomic.StoreInt64(&m.authAttempts, 0)
	atomic.StoreInt64(&m.authSuccesses, 0)
	atomic.StoreInt64(&m.authFailures, 0)
	atomic.StoreInt64(&m.authLatencySum, 0)
	atomic.StoreInt64(&m.authLatencyCount, 0)
	atomic.StoreInt64(&m.tokenValidations, 0)
	atomic.StoreInt64(&m.tokenValidSuccesses, 0)
	atomic.StoreInt64(&m.tokenValidFailures, 0)
	atomic.StoreInt64(&m.tokenRefreshes, 0)
	atomic.StoreInt64(&m.userCreations, 0)
	atomic.StoreInt64(&m.userUpdates, 0)
	atomic.StoreInt64(&m.userDeletions, 0)
	atomic.StoreInt64(&m.userLookups, 0)
	atomic.StoreInt64(&m.mfaEnabled, 0)
	atomic.StoreInt64(&m.mfaVerifications, 0)
	atomic.StoreInt64(&m.mfaSuccesses, 0)
	atomic.StoreInt64(&m.mfaFailures, 0)
	atomic.StoreInt64(&m.sessionCreations, 0)
	atomic.StoreInt64(&m.sessionRefreshes, 0)
	atomic.StoreInt64(&m.sessionDeletions, 0)
	atomic.StoreInt64(&m.activeSessions, 0)
	atomic.StoreInt64(&m.rateLimitChecks, 0)
	atomic.StoreInt64(&m.rateLimitBlocked, 0)

	m.errorsMu.Lock()
	m.errors = make(map[string]int64)
	m.errorsMu.Unlock()

	m.startTime = time.Now()
}

// HealthChecker performs health checks for the auth system.
type HealthChecker struct {
	casdoorEndpoint string
	dbPool          interface{} // *pgxpool.Pool
	redisClient     interface{} // *redis.Client
}

// NewHealthChecker creates a new health checker.
func NewHealthChecker(casdoorEndpoint string, dbPool, redisClient interface{}) *HealthChecker {
	return &HealthChecker{
		casdoorEndpoint: casdoorEndpoint,
		dbPool:          dbPool,
		redisClient:     redisClient,
	}
}

// HealthStatus represents health check status.
type HealthStatus struct {
	Healthy    bool              `json:"healthy"`
	Status     string            `json:"status"` // "healthy", "degraded", "unhealthy"
	Components map[string]string `json:"components"`
	Timestamp  time.Time         `json:"timestamp"`
}

// Check performs a comprehensive health check.
func (h *HealthChecker) Check(ctx context.Context) *HealthStatus {
	status := &HealthStatus{
		Healthy:    true,
		Status:     "healthy",
		Components: make(map[string]string),
		Timestamp:  time.Now(),
	}

	// Check Casdoor
	if h.casdoorEndpoint != "" {
		status.Components["casdoor"] = "healthy"
		// In production, ping Casdoor endpoint
	} else {
		status.Components["casdoor"] = "not_configured"
	}

	// Check database
	if h.dbPool != nil {
		status.Components["database"] = "healthy"
		// In production, ping database
	} else {
		status.Components["database"] = "not_configured"
		status.Healthy = false
		status.Status = "unhealthy"
	}

	// Check Redis (optional)
	if h.redisClient != nil {
		status.Components["redis"] = "healthy"
		// In production, ping Redis
	} else {
		status.Components["redis"] = "not_configured"
		// Redis is optional, so don't mark as unhealthy
		if status.Status == "healthy" {
			status.Status = "degraded"
		}
	}

	return status
}

// LogEntry represents a structured log entry.
type LogEntry struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"` // "debug", "info", "warn", "error"
	Message   string                 `json:"message"`
	UserID    int                    `json:"user_id,omitempty"`
	Username  string                 `json:"username,omitempty"`
	Action    string                 `json:"action,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Latency   time.Duration          `json:"latency,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Logger provides structured logging for auth operations.
type Logger struct {
	mu      sync.Mutex
	entries []LogEntry
	maxSize int
}

// NewLogger creates a new logger.
func NewLogger(maxSize int) *Logger {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &Logger{
		entries: make([]LogEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Log adds a log entry.
func (l *Logger) Log(entry LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry.Timestamp = time.Now()
	l.entries = append(l.entries, entry)

	// Keep only last maxSize entries
	if len(l.entries) > l.maxSize {
		l.entries = l.entries[len(l.entries)-l.maxSize:]
	}
}

// GetRecentLogs returns recent log entries.
func (l *Logger) GetRecentLogs(count int) []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	if count <= 0 || count > len(l.entries) {
		count = len(l.entries)
	}

	// Return last N entries
	start := len(l.entries) - count
	result := make([]LogEntry, count)
	copy(result, l.entries[start:])

	return result
}

// Clear clears all log entries.
func (l *Logger) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make([]LogEntry, 0, l.maxSize)
}
