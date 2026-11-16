// Package pluginruntime provides database-backed plugin registry persistence.
package pluginruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

// DBRegistry provides database-backed plugin registry with file fallback.
// It maintains plugin state (enabled/disabled) across restarts.
type DBRegistry struct {
	db *sql.DB
	
	// In-memory cache (protected by RWMutex)
	mu      sync.RWMutex
	plugins map[string]*PluginEntry
	
	// File-based fallback
	filePath string
}

// PluginEntry represents a plugin registration in the database.
type PluginEntry struct {
	PluginID     string                 `json:"plugin_id"`
	ManifestJSON map[string]interface{} `json:"manifest_json"`
	State        DBPluginState          `json:"state"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// DBPluginState represents the database state of a plugin (enabled/disabled).
type DBPluginState string

const (
	StateEnabled  DBPluginState = "enabled"
	StateDisabled DBPluginState = "disabled"
)

// NewDBRegistry creates a new database-backed plugin registry.
func NewDBRegistry(dbConnStr string, filePath string) (*DBRegistry, error) {
	// Connect to PostgreSQL
	db, err := sql.Open("postgres", dbConnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	registry := &DBRegistry{
		db:       db,
		plugins:  make(map[string]*PluginEntry),
		filePath: filePath,
	}

	// Initialize database schema
	if err := registry.initSchema(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Load existing plugins from database
	if err := registry.loadFromDB(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load plugins from database: %w", err)
	}

	fmt.Printf("INFO: DBRegistry initialized with %d plugins\n", len(registry.plugins))
	return registry, nil
}

// initSchema creates the plugin_registry table if it doesn't exist.
func (r *DBRegistry) initSchema(ctx context.Context) error {
	schema := `
		CREATE TABLE IF NOT EXISTS plugin_registry (
			plugin_id TEXT PRIMARY KEY,
			manifest_json JSONB NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('enabled', 'disabled')),
			updated_at TIMESTAMP NOT NULL DEFAULT NOW()
		);
		
		CREATE INDEX IF NOT EXISTS idx_plugin_registry_state ON plugin_registry(state);
		CREATE INDEX IF NOT EXISTS idx_plugin_registry_updated_at ON plugin_registry(updated_at);
	`

	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	fmt.Println("INFO: plugin_registry table initialized")
	return nil
}

// loadFromDB loads all plugin entries from the database into memory cache.
func (r *DBRegistry) loadFromDB(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `
		SELECT plugin_id, manifest_json, state, updated_at
		FROM plugin_registry
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query plugins: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var entry PluginEntry
		var manifestJSON []byte

		if err := rows.Scan(&entry.PluginID, &manifestJSON, &entry.State, &entry.UpdatedAt); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Unmarshal JSONB manifest
		if err := json.Unmarshal(manifestJSON, &entry.ManifestJSON); err != nil {
			return fmt.Errorf("failed to unmarshal manifest for %s: %w", entry.PluginID, err)
		}

		r.mu.Lock()
		r.plugins[entry.PluginID] = &entry
		r.mu.Unlock()

		count++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	fmt.Printf("INFO: loaded %d plugins from database\n", count)
	return nil
}

// Register registers a plugin in the database and memory cache.
func (r *DBRegistry) Register(ctx context.Context, pluginID string, manifest map[string]interface{}, state DBPluginState) error {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Dual write: DB first (source of truth), then memory cache
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO plugin_registry (plugin_id, manifest_json, state, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_id) 
		DO UPDATE SET 
			manifest_json = EXCLUDED.manifest_json,
			state = EXCLUDED.state,
			updated_at = EXCLUDED.updated_at
	`, pluginID, manifestJSON, state, time.Now())
	
	if err != nil {
		return fmt.Errorf("failed to insert/update plugin in database: %w", err)
	}

	// Update memory cache
	entry := &PluginEntry{
		PluginID:     pluginID,
		ManifestJSON: manifest,
		State:        state,
		UpdatedAt:    time.Now(),
	}

	r.mu.Lock()
	r.plugins[pluginID] = entry
	r.mu.Unlock()

	fmt.Printf("INFO: registered plugin %s with state %s\n", pluginID, state)
	return nil
}

// SetState updates the plugin state (enabled/disabled).
func (r *DBRegistry) SetState(ctx context.Context, pluginID string, state DBPluginState) error {
	// Update database first
	result, err := r.db.ExecContext(ctx, `
		UPDATE plugin_registry 
		SET state = $1, updated_at = $2
		WHERE plugin_id = $3
	`, state, time.Now(), pluginID)
	
	if err != nil {
		return fmt.Errorf("failed to update plugin state in database: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("plugin %s not found", pluginID)
	}

	// Update memory cache
	r.mu.Lock()
	if entry, exists := r.plugins[pluginID]; exists {
		entry.State = state
		entry.UpdatedAt = time.Now()
	}
	r.mu.Unlock()

	fmt.Printf("INFO: updated plugin %s state to %s\n", pluginID, state)
	return nil
}

// GetState retrieves the current state of a plugin.
func (r *DBRegistry) GetState(pluginID string) (DBPluginState, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.plugins[pluginID]
	if !exists {
		return "", fmt.Errorf("plugin %s not found", pluginID)
	}

	return entry.State, nil
}

// Get retrieves a plugin entry from the cache.
func (r *DBRegistry) Get(pluginID string) (*PluginEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.plugins[pluginID]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found", pluginID)
	}

	return entry, nil
}

// List returns all plugin entries.
func (r *DBRegistry) List() []*PluginEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]*PluginEntry, 0, len(r.plugins))
	for _, entry := range r.plugins {
		entries = append(entries, entry)
	}

	return entries
}

// ListByState returns all plugins with the specified state.
func (r *DBRegistry) ListByState(state DBPluginState) []*PluginEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]*PluginEntry, 0)
	for _, entry := range r.plugins {
		if entry.State == state {
			entries = append(entries, entry)
		}
	}

	return entries
}

// Unregister removes a plugin from the registry.
func (r *DBRegistry) Unregister(ctx context.Context, pluginID string) error {
	// Remove from database first
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM plugin_registry WHERE plugin_id = $1
	`, pluginID)
	
	if err != nil {
		return fmt.Errorf("failed to delete plugin from database: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("plugin %s not found", pluginID)
	}

	// Remove from memory cache
	r.mu.Lock()
	delete(r.plugins, pluginID)
	r.mu.Unlock()

	fmt.Printf("INFO: unregistered plugin %s\n", pluginID)
	return nil
}

// Close closes the database connection.
func (r *DBRegistry) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

// Sync forces a synchronization between memory cache and database.
// This is useful for recovery scenarios.
func (r *DBRegistry) Sync(ctx context.Context) error {
	return r.loadFromDB(ctx)
}
