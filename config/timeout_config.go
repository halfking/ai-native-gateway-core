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

// TimeoutConfig manages dynamic timeout calculation and hot-reloading
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

	// Apply settings with lock
	tc.mu.Lock()
	defer tc.mu.Unlock()

	updated := 0
	if val, ok := settings["timeout.client_default_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.clientDefaultSeconds = v
			updated++
		}
	}
	if val, ok := settings["timeout.upstream_base_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.upstreamBaseSeconds = v
			updated++
		}
	}
	if val, ok := settings["timeout.upstream_min_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.upstreamMinSeconds = v
			updated++
		}
	}
	if val, ok := settings["timeout.upstream_max_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.upstreamMaxSeconds = v
			updated++
		}
	}
	if val, ok := settings["timeout.context_threshold_tokens"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.contextThresholdTokens = v
			updated++
		}
	}
	if val, ok := settings["timeout.context_bonus_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.contextBonusSeconds = v
			updated++
		}
	}
	if val, ok := settings["timeout.dynamic_mode"]; ok {
		// Remove quotes if present
		val = strings.Trim(val, "\"")
		tc.mode = TimeoutMode(val)
		updated++
	}

	// Retry settings
	if val, ok := settings["retry.max_attempts"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.maxRetryAttempts = v
			updated++
		}
	}
	if val, ok := settings["retry.base_delay_ms"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.baseDelayMS = v
			updated++
		}
	}
	if val, ok := settings["retry.max_delay_ms"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.maxDelayMS = v
			updated++
		}
	}
	if val, ok := settings["retry.exponential_backoff"]; ok {
		tc.exponentialBackoff = (val == "true")
		updated++
	}
	if val, ok := settings["retry.keepalive_interval_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.keepaliveIntervalSecs = v
			updated++
		}
	}
	if val, ok := settings["retry.last_node_wait_seconds"]; ok {
		if v, err := strconv.Atoi(val); err == nil {
			tc.lastNodeWaitSeconds = v
			updated++
		}
	}

	tc.logger.Info("timeout config reloaded from DB",
		"updated", updated,
		"mode", tc.mode,
		"base_timeout", tc.upstreamBaseSeconds,
		"context_threshold", tc.contextThresholdTokens)

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
