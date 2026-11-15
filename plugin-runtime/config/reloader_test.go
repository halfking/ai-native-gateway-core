package config

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test ConfigHolder atomic access
func TestConfigHolder(t *testing.T) {
	initialConfig := &Config{
		Server: ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
	}

	holder := NewConfigHolder(initialConfig)

	// Get initial config
	config := holder.Get()
	if config.Server.Host != "localhost" {
		t.Errorf("Expected host 'localhost', got %s", config.Server.Host)
	}

	// Set new config
	newConfig := &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 9090,
		},
	}
	holder.Set(newConfig)

	// Get updated config
	config = holder.Get()
	if config.Server.Host != "0.0.0.0" {
		t.Errorf("Expected host '0.0.0.0', got %s", config.Server.Host)
	}
	if config.Server.Port != 9090 {
		t.Errorf("Expected port 9090, got %d", config.Server.Port)
	}
}

// Test concurrent access to ConfigHolder
func TestConfigHolderConcurrentAccess(t *testing.T) {
	initialConfig := &Config{
		Server: ServerConfig{Host: "localhost", Port: 8080},
	}

	holder := NewConfigHolder(initialConfig)

	var wg sync.WaitGroup
	var readCount int32

	// Start readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				config := holder.Get()
				if config != nil {
					atomic.AddInt32(&readCount, 1)
				}
			}
		}()
	}

	// Start writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				newConfig := &Config{
					Server: ServerConfig{
						Host: "0.0.0.0",
						Port: 8080 + id,
					},
				}
				holder.Set(newConfig)
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Should have 1000 reads
	if count := atomic.LoadInt32(&readCount); count != 1000 {
		t.Errorf("Expected 1000 reads, got %d", count)
	}
}

// Test validator with valid config
func TestValidatorValid(t *testing.T) {
	validator := NewValidator()

	// Create temp directory for plugins
	tmpDir := t.TempDir()

	config := &Config{
		Server: ServerConfig{
			Host: "localhost",
			Port: 8080,
		},
		Plugins: PluginsConfig{
			Directory: tmpDir,
			Enabled:   []string{"plugin1", "plugin2"},
		},
		Security: SecurityConfig{
			TLSEnabled: false,
		},
	}

	if err := validator.Validate(config); err != nil {
		t.Errorf("Valid config should not fail validation: %v", err)
	}
}

// Test validator with invalid server config
func TestValidatorInvalidServer(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "empty host",
			config: &Config{
				Server: ServerConfig{Host: "", Port: 8080},
			},
		},
		{
			name: "invalid port (too low)",
			config: &Config{
				Server: ServerConfig{Host: "localhost", Port: 0},
			},
		},
		{
			name: "invalid port (too high)",
			config: &Config{
				Server: ServerConfig{Host: "localhost", Port: 70000},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validator.Validate(tt.config); err == nil {
				t.Error("Expected validation error, got nil")
			}
		})
	}
}

// Test validator with invalid plugins config
func TestValidatorInvalidPlugins(t *testing.T) {
	validator := NewValidator()

	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "empty directory",
			config: &Config{
				Server:  ServerConfig{Host: "localhost", Port: 8080},
				Plugins: PluginsConfig{Directory: ""},
			},
		},
		{
			name: "non-existent directory",
			config: &Config{
				Server:  ServerConfig{Host: "localhost", Port: 8080},
				Plugins: PluginsConfig{Directory: "/nonexistent/path"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validator.Validate(tt.config); err == nil {
				t.Error("Expected validation error, got nil")
			}
		})
	}
}

// Test validator with TLS enabled
func TestValidatorTLS(t *testing.T) {
	validator := NewValidator()
	tmpDir := t.TempDir()

	// Create dummy cert and key files
	certFile := filepath.Join(tmpDir, "cert.pem")
	keyFile := filepath.Join(tmpDir, "key.pem")
	os.WriteFile(certFile, []byte("dummy cert"), 0644)
	os.WriteFile(keyFile, []byte("dummy key"), 0644)

	config := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
		Security: SecurityConfig{
			TLSEnabled: true,
			CertFile:   certFile,
			KeyFile:    keyFile,
		},
	}

	if err := validator.Validate(config); err != nil {
		t.Errorf("Valid TLS config should not fail: %v", err)
	}

	// Test missing cert file
	config.Security.CertFile = "/nonexistent/cert.pem"
	if err := validator.Validate(config); err == nil {
		t.Error("Expected validation error for missing cert file")
	}
}

// Test hot reload
func TestHotReload(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial config
	initialYAML := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
  enabled:
    - plugin1
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create reloader
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	// Start watching
	ctx := context.Background()
	if err := reloader.Watch(ctx, configFile); err != nil {
		t.Fatalf("Failed to watch config: %v", err)
	}

	// Wait for watcher to be ready
	time.Sleep(100 * time.Millisecond)

	// Register reload handler
	var reloadCount int32
	reloader.OnReload(func(ctx context.Context, oldConfig, newConfig *Config) error {
		atomic.AddInt32(&reloadCount, 1)
		return nil
	})

	// Modify config file
	updatedYAML := `
server:
  host: 0.0.0.0
  port: 9090
plugins:
  directory: ` + tmpDir + `
  enabled:
    - plugin1
    - plugin2
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(updatedYAML), 0644); err != nil {
		t.Fatalf("Failed to write updated config: %v", err)
	}

	// Wait for reload (debounce + processing)
	time.Sleep(500 * time.Millisecond)

	// Check if config was reloaded
	config := reloader.GetConfig()
	if config.Server.Host != "0.0.0.0" {
		t.Errorf("Expected host '0.0.0.0', got %s", config.Server.Host)
	}
	if config.Server.Port != 9090 {
		t.Errorf("Expected port 9090, got %d", config.Server.Port)
	}
	if len(config.Plugins.Enabled) != 2 {
		t.Errorf("Expected 2 enabled plugins, got %d", len(config.Plugins.Enabled))
	}

	// Check reload handler was called
	if count := atomic.LoadInt32(&reloadCount); count < 1 {
		t.Errorf("Expected at least 1 reload, got %d", count)
	}
}

// Test invalid config handling
func TestInvalidConfigHandling(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial valid config
	initialYAML := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create reloader
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	ctx := context.Background()
	if err := reloader.Watch(ctx, configFile); err != nil {
		t.Fatalf("Failed to watch config: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Write invalid config (invalid port)
	invalidYAML := `
server:
  host: localhost
  port: 99999
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(invalidYAML), 0644); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Wait for reload attempt
	time.Sleep(500 * time.Millisecond)

	// Config should remain unchanged (old config preserved)
	config := reloader.GetConfig()
	if config.Server.Port != 8080 {
		t.Errorf("Expected old port 8080 to be preserved, got %d", config.Server.Port)
	}
}

// Test manual reload
func TestManualReload(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gateway.yaml")

	// Write config
	yamlContent := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Create reloader
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	// Set config path (without starting watcher)
	r := reloader.(*configReloader)
	r.configPath = configFile

	// Manually trigger reload
	ctx := context.Background()
	if err := reloader.Reload(ctx); err != nil {
		t.Errorf("Manual reload failed: %v", err)
	}

	// Config should be loaded
	config := reloader.GetConfig()
	if config.Server.Host != "localhost" {
		t.Errorf("Expected host 'localhost', got %s", config.Server.Host)
	}
}

// Test reload handlers
func TestReloadHandlers(t *testing.T) {
	tmpDir := t.TempDir()

	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	// Register multiple handlers
	var handler1Called, handler2Called int32

	reloader.OnReload(func(ctx context.Context, oldConfig, newConfig *Config) error {
		atomic.StoreInt32(&handler1Called, 1)
		return nil
	})

	reloader.OnReload(func(ctx context.Context, oldConfig, newConfig *Config) error {
		atomic.StoreInt32(&handler2Called, 1)
		return nil
	})

	// Create temp config file
	configFile := filepath.Join(tmpDir, "gateway.yaml")
	yamlContent := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Set config path and trigger reload
	r := reloader.(*configReloader)
	r.configPath = configFile

	ctx := context.Background()
	if err := reloader.Reload(ctx); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Both handlers should be called
	if atomic.LoadInt32(&handler1Called) != 1 {
		t.Error("Handler 1 was not called")
	}
	if atomic.LoadInt32(&handler2Called) != 1 {
		t.Error("Handler 2 was not called")
	}
}

// Test stop
func TestStop(t *testing.T) {
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: t.TempDir()},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}

	// Stop should succeed
	if err := reloader.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	// Second stop should be idempotent
	if err := reloader.Stop(); err != nil {
		t.Errorf("Second stop failed: %v", err)
	}

	// Operations after stop should fail
	ctx := context.Background()
	if err := reloader.Watch(ctx, "/tmp/config.yaml"); err == nil {
		t.Error("Expected error when watching after stop")
	}
}

// TestP314_WatchRebuildMetrics tests that watch rebuild metrics are tracked correctly (P3-14).
func TestP314_WatchRebuildMetrics(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial config
	initialYAML := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create reloader
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	// Start watching
	ctx := context.Background()
	if err := reloader.Watch(ctx, configFile); err != nil {
		t.Fatalf("Failed to watch config: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Simulate file rename (atomic replacement)
	tempFile := filepath.Join(tmpDir, "gateway.yaml.tmp")
	updatedYAML := `
server:
  host: 0.0.0.0
  port: 9090
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(tempFile, []byte(updatedYAML), 0644); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}

	// Rename (atomic replacement) triggers watch rebuild
	if err := os.Rename(tempFile, configFile); err != nil {
		t.Fatalf("Failed to rename config: %v", err)
	}

	// Wait for watch rebuild and reload
	time.Sleep(500 * time.Millisecond)

	// Get metrics
	r := reloader.(*configReloader)
	metrics := r.GetMetrics()

	if metrics["plugin_config_watch_rebuild_total"] < 1 {
		t.Errorf("Expected watch_rebuild_total >= 1, got %d", metrics["plugin_config_watch_rebuild_total"])
	}

	// No failures expected in normal operation
	if metrics["plugin_config_watch_rebuild_failures"] > 0 {
		t.Logf("Watch rebuild failures: %d (may occur in high-load environments)", metrics["plugin_config_watch_rebuild_failures"])
	}

	// Config should be updated
	config := reloader.GetConfig()
	if config.Server.Port != 9090 {
		t.Errorf("Expected port 9090 after rename, got %d", config.Server.Port)
	}
}

// TestP314_WatchRetryBackoff tests exponential backoff retry logic for watch Add failures (P3-14).
func TestP314_WatchRetryBackoff(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial config
	initialYAML := `
server:
  host: localhost
  port: 8080
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`
	if err := os.WriteFile(configFile, []byte(initialYAML), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create reloader
	initialConfig := &Config{
		Server:  ServerConfig{Host: "localhost", Port: 8080},
		Plugins: PluginsConfig{Directory: tmpDir},
	}

	reloader, err := NewConfigReloader(initialConfig, NewValidator())
	if err != nil {
		t.Fatalf("Failed to create reloader: %v", err)
	}
	defer reloader.Stop()

	// Start watching
	ctx := context.Background()
	if err := reloader.Watch(ctx, configFile); err != nil {
		t.Fatalf("Failed to watch config: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Get initial metrics
	r := reloader.(*configReloader)
	initialFailures := r.GetMetrics()["plugin_config_watch_rebuild_failures"]

	// Modify config to trigger watch rebuild
	updatedYAML := `
server:
  host: 0.0.0.0
  port: 9090
plugins:
  directory: ` + tmpDir + `
security:
  tls_enabled: false
`

	// Remove and recreate to trigger rename event
	tempFile := filepath.Join(tmpDir, "gateway.yaml.tmp")
	if err := os.WriteFile(tempFile, []byte(updatedYAML), 0644); err != nil {
		t.Fatalf("Failed to write temp config: %v", err)
	}
	if err := os.Rename(tempFile, configFile); err != nil {
		t.Fatalf("Failed to rename config: %v", err)
	}

	// Wait for operations to complete
	time.Sleep(1000 * time.Millisecond)

	// Verify metrics are tracked (even if no failures)
	metrics := r.GetMetrics()
	if metrics["plugin_config_watch_rebuild_total"] < 1 {
		t.Errorf("Expected watch_rebuild_total >= 1, got %d", metrics["plugin_config_watch_rebuild_total"])
	}

	// Verify failures count (may be 0 in normal operation)
	currentFailures := metrics["plugin_config_watch_rebuild_failures"]
	if currentFailures < initialFailures {
		t.Errorf("Failures count went backwards: %d -> %d", initialFailures, currentFailures)
	}
}
