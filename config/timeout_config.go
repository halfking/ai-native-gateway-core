package config

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBQuerier is an interface for database query operations
// Supports both *sql.DB and *pgxpool.Pool
type DBQuerier interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (RowsScanner, error)
}

// RowsScanner is an interface for scanning query results
type RowsScanner interface {
	Scan(dest ...interface{}) error
	Next() bool
	Err() error
	Close() error
}

// sqlDBAdapter wraps *sql.DB to implement DBQuerier
type sqlDBAdapter struct {
	db *sql.DB
}

func (a *sqlDBAdapter) QueryContext(ctx context.Context, query string, args ...interface{}) (RowsScanner, error) {
	return a.db.QueryContext(ctx, query, args...)
}

// pgxPoolAdapter wraps *pgxpool.Pool to implement DBQuerier
type pgxPoolAdapter struct {
	pool *pgxpool.Pool
}

type pgxRowsAdapter struct {
	rows interface {
		Scan(dest ...interface{}) error
		Next() bool
		Err() error
		Close()
	}
}

func (r *pgxRowsAdapter) Scan(dest ...interface{}) error {
	return r.rows.Scan(dest...)
}

func (r *pgxRowsAdapter) Next() bool {
	return r.rows.Next()
}

func (r *pgxRowsAdapter) Err() error {
	return r.rows.Err()
}

func (r *pgxRowsAdapter) Close() error {
	r.rows.Close()
	return nil
}

func (a *pgxPoolAdapter) QueryContext(ctx context.Context, query string, args ...interface{}) (RowsScanner, error) {
	rows, err := a.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRowsAdapter{rows: rows}, nil
}

// TimeoutMode defines the dynamic timeout calculation mode
type TimeoutMode string

const (
	TimeoutModeStatic       TimeoutMode = "static"
	TimeoutModeContextAware TimeoutMode = "context_aware"
	TimeoutModeNetworkAware TimeoutMode = "network_aware"
	TimeoutModeAdaptive     TimeoutMode = "adaptive"
)

type timeoutConfigSnapshot struct {
	clientDefaultSeconds   int
	upstreamBaseSeconds    int
	upstreamMinSeconds     int
	upstreamMaxSeconds     int
	contextThresholdTokens int
	contextBonusSeconds    int
	mode                   TimeoutMode
	maxRetryAttempts       int
	baseDelayMS            int
	maxDelayMS             int
	exponentialBackoff     bool
	keepaliveIntervalSecs  int
	lastNodeWaitSeconds    int
}

func (tc *TimeoutConfig) snapshotLocked() timeoutConfigSnapshot {
	return timeoutConfigSnapshot{
		clientDefaultSeconds:   tc.clientDefaultSeconds,
		upstreamBaseSeconds:    tc.upstreamBaseSeconds,
		upstreamMinSeconds:     tc.upstreamMinSeconds,
		upstreamMaxSeconds:     tc.upstreamMaxSeconds,
		contextThresholdTokens: tc.contextThresholdTokens,
		contextBonusSeconds:    tc.contextBonusSeconds,
		mode:                   tc.mode,
		maxRetryAttempts:       tc.maxRetryAttempts,
		baseDelayMS:            tc.baseDelayMS,
		maxDelayMS:             tc.maxDelayMS,
		exponentialBackoff:     tc.exponentialBackoff,
		keepaliveIntervalSecs:  tc.keepaliveIntervalSecs,
		lastNodeWaitSeconds:    tc.lastNodeWaitSeconds,
	}
}

func (tc *TimeoutConfig) applySnapshotLocked(s timeoutConfigSnapshot) {
	tc.clientDefaultSeconds = s.clientDefaultSeconds
	tc.upstreamBaseSeconds = s.upstreamBaseSeconds
	tc.upstreamMinSeconds = s.upstreamMinSeconds
	tc.upstreamMaxSeconds = s.upstreamMaxSeconds
	tc.contextThresholdTokens = s.contextThresholdTokens
	tc.contextBonusSeconds = s.contextBonusSeconds
	tc.mode = s.mode
	tc.maxRetryAttempts = s.maxRetryAttempts
	tc.baseDelayMS = s.baseDelayMS
	tc.maxDelayMS = s.maxDelayMS
	tc.exponentialBackoff = s.exponentialBackoff
	tc.keepaliveIntervalSecs = s.keepaliveIntervalSecs
	tc.lastNodeWaitSeconds = s.lastNodeWaitSeconds
}

func parsePositiveSetting(settings map[string]string, key string, target *int) (bool, error) {
	val, ok := settings[key]
	if !ok {
		return false, nil
	}
	v, err := strconv.Atoi(strings.Trim(val, "\""))
	if err != nil || v <= 0 {
		return false, fmt.Errorf("%s must be a positive integer", key)
	}
	*target = v
	return true, nil
}

func parseNonNegativeSetting(settings map[string]string, key string, target *int) (bool, error) {
	val, ok := settings[key]
	if !ok {
		return false, nil
	}
	v, err := strconv.Atoi(strings.Trim(val, "\""))
	if err != nil || v < 0 {
		return false, fmt.Errorf("%s must be a non-negative integer", key)
	}
	*target = v
	return true, nil
}

func parseBoolSetting(settings map[string]string, key string, target *bool) (bool, error) {
	val, ok := settings[key]
	if !ok {
		return false, nil
	}
	v, err := strconv.ParseBool(strings.Trim(val, "\""))
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	*target = v
	return true, nil
}

func validTimeoutMode(mode TimeoutMode) bool {
	switch mode {
	case TimeoutModeStatic, TimeoutModeContextAware, TimeoutModeNetworkAware, TimeoutModeAdaptive:
		return true
	default:
		return false
	}
}

func validateTimeoutSnapshot(s timeoutConfigSnapshot) error {
	if s.upstreamMinSeconds > s.upstreamBaseSeconds || s.upstreamBaseSeconds > s.upstreamMaxSeconds {
		return fmt.Errorf("timeout bounds must satisfy min <= base <= max")
	}
	if s.baseDelayMS > s.maxDelayMS {
		return fmt.Errorf("retry delays must satisfy base <= max")
	}
	if !validTimeoutMode(s.mode) {
		return fmt.Errorf("timeout.dynamic_mode %q is invalid", s.mode)
	}
	return nil
}

type TimeoutConfig struct {
	mu sync.RWMutex

	// Base configuration
	clientDefaultSeconds   int
	upstreamBaseSeconds    int
	upstreamMinSeconds     int
	upstreamMaxSeconds     int
	contextThresholdTokens int
	contextBonusSeconds    int
	mode                   TimeoutMode

	// Retry configuration
	maxRetryAttempts      int
	baseDelayMS           int
	maxDelayMS            int
	exponentialBackoff    bool
	keepaliveIntervalSecs int
	lastNodeWaitSeconds   int

	// Database connection for hot-reload
	db           DBQuerier
	reloadTicker *time.Ticker
	stopChan     chan struct{}
	logger       *slog.Logger
}

// TimeoutCalculationInput contains all inputs for dynamic timeout calculation
type TimeoutCalculationInput struct {
	ContextSizeTokens   int
	HistoricalLatencyMS int
	NetworkLatencyMS    int
	ModelName           string
	ProviderID          int
}

// TimeoutCalculationResult contains the calculated effective timeout
type TimeoutCalculationResult struct {
	EffectiveTimeoutSeconds int
	Mode                    TimeoutMode
	Reason                  string
}

// NewTimeoutConfig creates a new TimeoutConfig instance with *sql.DB
func NewTimeoutConfig(db *sql.DB, logger *slog.Logger) *TimeoutConfig {
	return newTimeoutConfigInternal(&sqlDBAdapter{db: db}, logger)
}

// NewTimeoutConfigWithPool creates a new TimeoutConfig instance with *pgxpool.Pool
func NewTimeoutConfigWithPool(pool *pgxpool.Pool, logger *slog.Logger) *TimeoutConfig {
	return newTimeoutConfigInternal(&pgxPoolAdapter{pool: pool}, logger)
}

// newTimeoutConfigInternal is the internal constructor
func newTimeoutConfigInternal(db DBQuerier, logger *slog.Logger) *TimeoutConfig {
	tc := &TimeoutConfig{
		db:       db,
		logger:   logger,
		stopChan: make(chan struct{}),

		// Default values (fallback if DB is unavailable)
		clientDefaultSeconds:   60,
		upstreamBaseSeconds:    90,
		upstreamMinSeconds:     20,
		upstreamMaxSeconds:     600,
		contextThresholdTokens: 20000,
		contextBonusSeconds:    45,
		mode:                   TimeoutModeAdaptive,

		maxRetryAttempts:      3,
		baseDelayMS:           1000,
		maxDelayMS:            10000,
		exponentialBackoff:    true,
		keepaliveIntervalSecs: 15,
		lastNodeWaitSeconds:   10,
	}

	// Initial load from database
	if err := tc.ReloadFromDB(context.Background()); err != nil {
		logger.Warn("failed to load initial timeout config from DB, using defaults",
			"error", err)
	}

	// Start background hot-reload
	tc.startAutoReload()

	return tc
}

// CalculateEffectiveTimeout computes the dynamic timeout based on multiple factors
func (tc *TimeoutConfig) CalculateEffectiveTimeout(input TimeoutCalculationInput) TimeoutCalculationResult {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	switch tc.mode {
	case TimeoutModeStatic:
		return tc.calculateStatic()
	case TimeoutModeContextAware:
		return tc.calculateContextAware(input)
	case TimeoutModeNetworkAware:
		return tc.calculateNetworkAware(input)
	case TimeoutModeAdaptive:
		return tc.calculateAdaptive(input)
	default:
		return tc.calculateStatic()
	}
}

// calculateStatic returns the base timeout (Phase 0 behavior)
func (tc *TimeoutConfig) calculateStatic() TimeoutCalculationResult {
	return TimeoutCalculationResult{
		EffectiveTimeoutSeconds: tc.upstreamBaseSeconds,
		Mode:                    TimeoutModeStatic,
		Reason:                  "static_base_timeout",
	}
}

// calculateContextAware adjusts timeout based on context size
func (tc *TimeoutConfig) calculateContextAware(input TimeoutCalculationInput) TimeoutCalculationResult {
	effective := tc.upstreamBaseSeconds

	// Add bonus if context is large
	if input.ContextSizeTokens > tc.contextThresholdTokens {
		effective += tc.contextBonusSeconds
		reason := fmt.Sprintf("context_size=%d_exceeds_threshold=%d_added=%ds",
			input.ContextSizeTokens, tc.contextThresholdTokens, tc.contextBonusSeconds)

		// Clamp to [min, max]
		effective = tc.clamp(effective)

		return TimeoutCalculationResult{
			EffectiveTimeoutSeconds: effective,
			Mode:                    TimeoutModeContextAware,
			Reason:                  reason,
		}
	}

	return TimeoutCalculationResult{
		EffectiveTimeoutSeconds: effective,
		Mode:                    TimeoutModeContextAware,
		Reason:                  fmt.Sprintf("context_size=%d_below_threshold", input.ContextSizeTokens),
	}
}

// calculateNetworkAware adjusts timeout based on historical latency
func (tc *TimeoutConfig) calculateNetworkAware(input TimeoutCalculationInput) TimeoutCalculationResult {
	effective := tc.upstreamBaseSeconds

	// Add 50% buffer based on historical latency
	if input.HistoricalLatencyMS > 0 {
		historicalSeconds := input.HistoricalLatencyMS / 1000
		buffer := historicalSeconds / 2 // 50% buffer
		effective += buffer

		reason := fmt.Sprintf("historical_latency=%dms_buffer=%ds",
			input.HistoricalLatencyMS, buffer)

		effective = tc.clamp(effective)

		return TimeoutCalculationResult{
			EffectiveTimeoutSeconds: effective,
			Mode:                    TimeoutModeNetworkAware,
			Reason:                  reason,
		}
	}

	return TimeoutCalculationResult{
		EffectiveTimeoutSeconds: effective,
		Mode:                    TimeoutModeNetworkAware,
		Reason:                  "no_historical_data",
	}
}

// calculateAdaptive combines context and network awareness
func (tc *TimeoutConfig) calculateAdaptive(input TimeoutCalculationInput) TimeoutCalculationResult {
	effective := tc.upstreamBaseSeconds
	reasons := []string{}

	// Factor 1: Context size
	if input.ContextSizeTokens > tc.contextThresholdTokens {
		effective += tc.contextBonusSeconds
		reasons = append(reasons, fmt.Sprintf("context=%d>%d(+%ds)",
			input.ContextSizeTokens, tc.contextThresholdTokens, tc.contextBonusSeconds))
	}

	// Factor 2: Historical latency (25% buffer in adaptive mode)
	if input.HistoricalLatencyMS > 0 {
		historicalSeconds := input.HistoricalLatencyMS / 1000
		buffer := historicalSeconds / 4 // 25% buffer in adaptive mode
		if buffer > 0 {
			effective += buffer
			reasons = append(reasons, fmt.Sprintf("hist_latency=%dms(+%ds)",
				input.HistoricalLatencyMS, buffer))
		}
	}

	// Factor 3: Network latency (if available)
	if input.NetworkLatencyMS > 500 {
		// High network latency, add small buffer
		effective += 5
		reasons = append(reasons, fmt.Sprintf("network_latency=%dms(+5s)",
			input.NetworkLatencyMS))
	}

	effective = tc.clamp(effective)

	reason := "adaptive"
	if len(reasons) > 0 {
		reason = fmt.Sprintf("adaptive:%s", reasons[0])
		if len(reasons) > 1 {
			for _, r := range reasons[1:] {
				reason += "," + r
			}
		}
	}

	return TimeoutCalculationResult{
		EffectiveTimeoutSeconds: effective,
		Mode:                    TimeoutModeAdaptive,
		Reason:                  reason,
	}
}

// clamp ensures the timeout is within [min, max] bounds
func (tc *TimeoutConfig) clamp(timeout int) int {
	if timeout < tc.upstreamMinSeconds {
		return tc.upstreamMinSeconds
	}
	if timeout > tc.upstreamMaxSeconds {
		return tc.upstreamMaxSeconds
	}
	return timeout
}

// ReloadFromDB loads configuration from system_settings table
func (tc *TimeoutConfig) ReloadFromDB(ctx context.Context) error {
	if tc.db == nil {
		return fmt.Errorf("database connection not available")
	}

	// Query all timeout-related settings
	query := `
		SELECT key, value #>> '{}' as value_text
		FROM system_settings
		WHERE category IN ('timeout', 'retry')
		  AND key IN (
		    'timeout.client_default_seconds',
		    'timeout.upstream_base_seconds',
		    'timeout.upstream_min_seconds',
		    'timeout.upstream_max_seconds',
		    'timeout.context_threshold_tokens',
		    'timeout.context_bonus_seconds',
		    'timeout.dynamic_mode',
		    'retry.max_attempts',
		    'retry.base_delay_ms',
		    'retry.max_delay_ms',
		    'retry.exponential_backoff',
		    'retry.keepalive_interval_seconds',
		    'retry.last_node_wait_seconds'
		  )
	`

	rows, err := tc.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("query system_settings: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			tc.logger.Warn("failed to close rows", "error", closeErr)
		}
	}()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("scan setting: %w", err)
		}
		settings[key] = value
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate settings: %w", err)
	}

	// Build and validate a complete candidate snapshot before taking the write
	// lock. Invalid DB rows must never partially replace a live configuration.
	tc.mu.RLock()
	candidate := tc.snapshotLocked()
	tc.mu.RUnlock()

	updated := 0
	for _, field := range []struct {
		key    string
		target *int
	}{
		{"timeout.client_default_seconds", &candidate.clientDefaultSeconds},
		{"timeout.upstream_base_seconds", &candidate.upstreamBaseSeconds},
		{"timeout.upstream_min_seconds", &candidate.upstreamMinSeconds},
		{"timeout.upstream_max_seconds", &candidate.upstreamMaxSeconds},
		{"timeout.context_threshold_tokens", &candidate.contextThresholdTokens},
		{"timeout.context_bonus_seconds", &candidate.contextBonusSeconds},
		{"retry.max_attempts", &candidate.maxRetryAttempts},
		{"retry.base_delay_ms", &candidate.baseDelayMS},
		{"retry.max_delay_ms", &candidate.maxDelayMS},
		{"retry.keepalive_interval_seconds", &candidate.keepaliveIntervalSecs},
		{"retry.last_node_wait_seconds", &candidate.lastNodeWaitSeconds},
	} {
		changed, err := parsePositiveSetting(settings, field.key, field.target)
		if field.key == "retry.max_attempts" {
			changed, err = parseNonNegativeSetting(settings, field.key, field.target)
		}
		if err != nil {
			return err
		}
		if changed {
			updated++
		}
	}
	if val, ok := settings["timeout.dynamic_mode"]; ok {
		candidate.mode = TimeoutMode(strings.Trim(val, "\""))
		updated++
	}
	if changed, err := parseBoolSetting(settings, "retry.exponential_backoff", &candidate.exponentialBackoff); err != nil {
		return err
	} else if changed {
		updated++
	}
	if err := validateTimeoutSnapshot(candidate); err != nil {
		return fmt.Errorf("validate timeout config: %w", err)
	}

	tc.mu.Lock()
	tc.applySnapshotLocked(candidate)
	tc.mu.Unlock()

	tc.logger.Info("timeout config reloaded from DB",
		"updated", updated,
		"mode", candidate.mode,
		"base_timeout", candidate.upstreamBaseSeconds,
		"context_threshold", candidate.contextThresholdTokens)

	return nil
}

// startAutoReload starts background config hot-reloading (every 30 seconds)
func (tc *TimeoutConfig) startAutoReload() {
	tc.reloadTicker = time.NewTicker(30 * time.Second)

	go func() {
		for {
			select {
			case <-tc.reloadTicker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := tc.ReloadFromDB(ctx); err != nil {
					tc.logger.Warn("failed to reload timeout config", "error", err)
				}
				cancel()
			case <-tc.stopChan:
				tc.reloadTicker.Stop()
				return
			}
		}
	}()

	tc.logger.Info("timeout config auto-reload started", "interval", "30s")
}

// Stop stops the background auto-reload
func (tc *TimeoutConfig) Stop() {
	close(tc.stopChan)
}

// GetRetryConfig returns current retry configuration
func (tc *TimeoutConfig) GetRetryConfig() (maxAttempts, baseDelayMS, maxDelayMS int, exponentialBackoff bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.maxRetryAttempts, tc.baseDelayMS, tc.maxDelayMS, tc.exponentialBackoff
}

// GetKeepaliveInterval returns keepalive interval in seconds
func (tc *TimeoutConfig) GetKeepaliveInterval() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.keepaliveIntervalSecs
}

// GetLastNodeWaitSeconds returns wait time after last node fails
func (tc *TimeoutConfig) GetLastNodeWaitSeconds() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.lastNodeWaitSeconds
}

// GetCurrentMode returns the current timeout mode
func (tc *TimeoutConfig) GetCurrentMode() TimeoutMode {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.mode
}

// GetBaseTimeout returns the base timeout in seconds
func (tc *TimeoutConfig) GetBaseTimeout() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return tc.upstreamBaseSeconds
}

// Calculate implements the executors.TimeoutCalculator interface
// This is an adapter method that bridges to CalculateEffectiveTimeout
func (tc *TimeoutConfig) Calculate(input interface{}) time.Duration {
	// Try to extract relevant fields from the input
	// The input could be AdaptiveTimeoutInput from executors package
	contextTokens := 0
	historicalLatency := 0

	// Use reflection to extract fields if needed
	// For now, use a simple default adaptive calculation
	result := tc.CalculateEffectiveTimeout(TimeoutCalculationInput{
		ContextSizeTokens:   contextTokens,
		HistoricalLatencyMS: historicalLatency,
	})

	return time.Duration(result.EffectiveTimeoutSeconds) * time.Second
}
