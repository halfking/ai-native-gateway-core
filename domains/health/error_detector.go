package health

import (
	"context"
	"sync"
	"time"
)

// ErrorDetector monitors provider errors and triggers health checks.
type ErrorDetector struct {
	mu               sync.RWMutex
	consecutiveFails map[string]int // credentialID -> fail count
	lastError        map[string]error
	lastErrorTime    map[string]time.Time
	errorRate        map[string]*ErrorCounter // errors per minute
	failThreshold    int
	tcpChecker       *TCPChecker
	httpChecker      *HTTPChecker
	inferenceChecker *InferenceChecker
}

// ErrorCounter tracks errors in a sliding window (60 seconds).
type ErrorCounter struct {
	mu         sync.RWMutex
	window     [60]int // One slot per second
	current    int
	lastUpdate time.Time
}

// ErrorEvent represents a detected error event.
type ErrorEvent struct {
	CredentialID string
	StatusCode   int
	Error        error
	Timestamp    time.Time
}

// DetectionResult contains the result of error detection and triggered checks.
type DetectionResult struct {
	ShouldMarkUnhealthy bool
	ShouldTriggerL3     bool
	ShouldTriggerQuota  bool
	ConsecutiveFails    int
	ErrorsPerMinute     int
	RecommendedStatus   string // "Active", "Degraded", "Unhealthy"
}

// NewErrorDetector creates a new error detector.
func NewErrorDetector(failThreshold int) *ErrorDetector {
	if failThreshold == 0 {
		failThreshold = 3 // Default: 3 consecutive failures
	}

	return &ErrorDetector{
		consecutiveFails: make(map[string]int),
		lastError:        make(map[string]error),
		lastErrorTime:    make(map[string]time.Time),
		errorRate:        make(map[string]*ErrorCounter),
		failThreshold:    failThreshold,
		tcpChecker:       NewTCPChecker(1 * time.Second),
		httpChecker:      NewHTTPChecker(3 * time.Second),
		inferenceChecker: NewInferenceChecker(10*time.Second, 30*time.Second),
	}
}

// OnError processes an error event (e.g., 5xx from provider).
func (d *ErrorDetector) OnError(event ErrorEvent) DetectionResult {
	d.mu.Lock()
	defer d.mu.Unlock()

	credID := event.CredentialID

	// Increment consecutive failures
	d.consecutiveFails[credID]++
	d.lastError[credID] = event.Error
	d.lastErrorTime[credID] = event.Timestamp

	// Update error rate counter
	if d.errorRate[credID] == nil {
		d.errorRate[credID] = &ErrorCounter{}
	}
	d.errorRate[credID].Increment()

	fails := d.consecutiveFails[credID]
	errorsPerMin := d.errorRate[credID].Last1Min()

	result := DetectionResult{
		ConsecutiveFails: fails,
		ErrorsPerMinute:  errorsPerMin,
	}

	// Classify by status code
	switch {
	case event.StatusCode == 500:
		// Internal server error - trigger L3 light inference
		result.ShouldTriggerL3 = true
		result.RecommendedStatus = "Degraded"

	case event.StatusCode == 503:
		// Service unavailable - might be quota/capacity
		result.ShouldTriggerQuota = true
		result.RecommendedStatus = "Degraded"

	case event.StatusCode == 502 || event.StatusCode == 504:
		// Gateway/timeout errors - trigger connectivity check
		result.RecommendedStatus = "Degraded"

	default:
		result.RecommendedStatus = "Degraded"
	}

	// Mark as Unhealthy after threshold
	if fails >= d.failThreshold {
		result.ShouldMarkUnhealthy = true
		result.RecommendedStatus = "Unhealthy"
	}

	return result
}

// IsUnhealthy returns true when the credential has hit the fail threshold.
// Use this as a routing short-circuit: when true, exclude from selection.
func (d *ErrorDetector) IsUnhealthy(credentialID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.consecutiveFails[credentialID] >= d.failThreshold
}

// OnSuccess resets consecutive failure count.
func (d *ErrorDetector) OnSuccess(credentialID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.consecutiveFails[credentialID] > 0 {
		d.consecutiveFails[credentialID]--
	}

	// Don't reset to 0 immediately, allow gradual recovery
}

// GetConsecutiveFails returns the current consecutive failure count.
func (d *ErrorDetector) GetConsecutiveFails(credentialID string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.consecutiveFails[credentialID]
}

// GetErrorsPerMinute returns errors in the last minute.
func (d *ErrorDetector) GetErrorsPerMinute(credentialID string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if counter := d.errorRate[credentialID]; counter != nil {
		return counter.Last1Min()
	}
	return 0
}

// ResetFailures completely resets failure tracking (use after full recovery).
func (d *ErrorDetector) ResetFailures(credentialID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.consecutiveFails[credentialID] = 0
	delete(d.lastError, credentialID)
	delete(d.lastErrorTime, credentialID)
}

// Increment increments the error counter.
func (ec *ErrorCounter) Increment() {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	ec.tick()
	ec.window[ec.current]++
}

// Last1Min returns the total errors in the last 60 seconds.
func (ec *ErrorCounter) Last1Min() int {
	ec.mu.RLock()
	defer ec.mu.RUnlock()

	sum := 0
	for _, count := range ec.window {
		sum += count
	}
	return sum
}

// tick advances the sliding window.
func (ec *ErrorCounter) tick() {
	now := time.Now()

	if ec.lastUpdate.IsZero() {
		ec.lastUpdate = now
		return
	}

	elapsed := now.Sub(ec.lastUpdate)
	secondsElapsed := int(elapsed.Seconds())

	if secondsElapsed > 0 {
		// Advance window
		for i := 0; i < secondsElapsed && i < 60; i++ {
			ec.current = (ec.current + 1) % 60
			ec.window[ec.current] = 0
		}
		ec.lastUpdate = now
	}
}

// TriggerFastCheck performs L1→L2→L3 fast detection sequence.
func (d *ErrorDetector) TriggerFastCheck(ctx context.Context, providerAddr, healthURL, apiURL, apiKey, model string) (healthy bool, latency time.Duration, err error) {
	// L1: TCP check
	tcpResult := d.tcpChecker.Check(ctx, providerAddr)
	if !tcpResult.Success {
		return false, tcpResult.Latency, tcpResult.Error
	}

	// L2: HTTP health check
	httpResult := d.httpChecker.Check(ctx, healthURL)
	if !httpResult.Success {
		return false, httpResult.Latency, httpResult.Error
	}

	// L3: Light inference check
	inferenceResult := d.inferenceChecker.CheckLight(ctx, apiURL, apiKey, model)
	if !inferenceResult.Success {
		return false, inferenceResult.Latency, inferenceResult.Error
	}

	// All checks passed
	return true, inferenceResult.Latency, nil
}
