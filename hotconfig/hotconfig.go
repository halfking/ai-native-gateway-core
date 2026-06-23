// Package hotconfig provides hot-reloadable configuration for llm-gateway-go.
//
// All runtime parameters (TTLs, limits, thresholds) are stored in the
// settings_kv table and polled every 30 seconds. Changes take effect
// immediately without restart.
//
// Configuration hierarchy:
//   - Platform-level: settings_kv (all tenants share)
//   - Tenant-level: tenant_settings_kv (per-tenant overrides)
//
// Usage:
//
//	cfg := config.New(db)
//	cfg.Start(ctx)
//	defer cfg.Stop()
//
//	ttl := cfg.GetInt("slot_ttl_seconds", 86400)
package hotconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config is a hot-reloadable configuration store backed by settings_kv.
type Config struct {
	pool   *pgxpool.Pool
	mu     sync.RWMutex
	values map[string]interface{}
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// New creates a configuration store. Call Start to begin polling.
func New(pool *pgxpool.Pool) *Config {
	return &Config{
		pool:   pool,
		values: make(map[string]interface{}),
		stopCh: make(chan struct{}),
	}
}

// Start begins polling settings_kv every 30 seconds.
func (c *Config) Start(ctx context.Context) error {
	// Initial load
	if err := c.reload(ctx); err != nil {
		return fmt.Errorf("config: initial load failed: %w", err)
	}

	// Background poller
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := c.reload(ctx); err != nil {
					slog.Error("config: reload failed", "error", err)
				}
			case <-c.stopCh:
				return
			}
		}
	}()

	return nil
}

// Stop halts the background poller.
func (c *Config) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

// reload fetches all settings from settings_kv and atomically replaces
// the in-memory map.
func (c *Config) reload(ctx context.Context) error {
	rows, err := c.pool.Query(ctx, `
		SELECT key, value
		FROM settings_kv
		WHERE key LIKE 'llmgw_%'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	newValues := make(map[string]interface{})
	for rows.Next() {
		var key string
		var valueJSON []byte
		if err := rows.Scan(&key, &valueJSON); err != nil {
			slog.Warn("config: scan failed", "key", key, "error", err)
			continue
		}

		var val interface{}
		if err := json.Unmarshal(valueJSON, &val); err != nil {
			slog.Warn("config: unmarshal failed", "key", key, "error", err)
			continue
		}
		newValues[key] = val
	}

	c.mu.Lock()
	c.values = newValues
	c.mu.Unlock()

	slog.Debug("config: reloaded", "count", len(newValues))
	return nil
}

// GetInt reads an integer setting. Returns defaultValue if not found.
func (c *Config) GetInt(key string, defaultValue int) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	val, ok := c.values[key]
	if !ok {
		return defaultValue
	}

	// JSONB numbers are float64 in Go
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		slog.Warn("config: type mismatch", "key", key, "expected", "int", "got", fmt.Sprintf("%T", v))
		return defaultValue
	}
}

// GetString reads a string setting.
func (c *Config) GetString(key string, defaultValue string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	val, ok := c.values[key]
	if !ok {
		return defaultValue
	}

	if s, ok := val.(string); ok {
		return s
	}
	slog.Warn("config: type mismatch", "key", key, "expected", "string", "got", fmt.Sprintf("%T", val))
	return defaultValue
}

// GetBool reads a boolean setting.
func (c *Config) GetBool(key string, defaultValue bool) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	val, ok := c.values[key]
	if !ok {
		return defaultValue
	}

	if b, ok := val.(bool); ok {
		return b
	}
	slog.Warn("config: type mismatch", "key", key, "expected", "bool", "got", fmt.Sprintf("%T", val))
	return defaultValue
}

// GetFloat reads a float64 setting.
func (c *Config) GetFloat(key string, defaultValue float64) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	val, ok := c.values[key]
	if !ok {
		return defaultValue
	}

	if f, ok := val.(float64); ok {
		return f
	}
	slog.Warn("config: type mismatch", "key", key, "expected", "float64", "got", fmt.Sprintf("%T", val))
	return defaultValue
}

// GetDuration reads a duration (in seconds) and converts to time.Duration.
func (c *Config) GetDuration(key string, defaultValue time.Duration) time.Duration {
	seconds := c.GetInt(key, int(defaultValue.Seconds()))
	return time.Duration(seconds) * time.Second
}

// Set writes a setting to settings_kv. Used by admin API.
func (c *Config) Set(ctx context.Context, key string, value interface{}) error {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("config: marshal failed: %w", err)
	}

	_, err = c.pool.Exec(ctx, `
		INSERT INTO settings_kv (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET
			value = EXCLUDED.value,
			updated_at = now()
	`, key, valueJSON)
	if err != nil {
		return err
	}

	// Immediate reload so the change takes effect without waiting 30s
	return c.reload(ctx)
}

// Delete removes a setting.
func (c *Config) Delete(ctx context.Context, key string) error {
	_, err := c.pool.Exec(ctx, `DELETE FROM settings_kv WHERE key = $1`, key)
	if err != nil {
		return err
	}
	return c.reload(ctx)
}

// ListAll returns all settings (for admin UI).
func (c *Config) ListAll() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a copy to prevent race conditions
	result := make(map[string]interface{}, len(c.values))
	for k, v := range c.values {
		result[k] = v
	}
	return result
}
