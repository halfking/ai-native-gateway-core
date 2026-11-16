package pluginruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewConfigWatcher(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial config
	initialConfig := `
plugins:
  directory: /tmp/plugins
  auto_discover: true
  essentials:
    - core-plugin
gateway:
  port: 8080
  host: localhost
  read_timeout: 30s
  write_timeout: 30s
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create config watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Verify initial config was loaded
	config := watcher.GetConfig()
	if config == nil {
		t.Fatal("GetConfig returned nil")
	}

	if config.Gateway.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", config.Gateway.Port)
	}

	if config.Gateway.Host != "localhost" {
		t.Errorf("Expected host 'localhost', got %s", config.Gateway.Host)
	}

	if config.Plugins.Directory != "/tmp/plugins" {
		t.Errorf("Expected plugins directory '/tmp/plugins', got %s", config.Plugins.Directory)
	}

	if !config.Plugins.AutoDiscover {
		t.Error("Expected auto_discover to be true")
	}
}

func TestConfigWatcher_HotReload(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	
	// Create plugins directory
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("Failed to create plugins directory: %v", err)
	}

	// Write initial config
	initialConfig := fmt.Sprintf(`
plugins:
  directory: %s
  auto_discover: true
gateway:
  port: 8080
  host: localhost
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create config watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Start watching
	watcher.Start()

	// Register callback to detect config changes
	changeDetected := make(chan bool, 1)
	watcher.RegisterCallback(func(oldConfig, newConfig *GatewayConfig) error {
		t.Logf("Config changed: port %d -> %d", oldConfig.Gateway.Port, newConfig.Gateway.Port)
		changeDetected <- true
		return nil
	})

	// Wait a bit for watcher to initialize
	time.Sleep(100 * time.Millisecond)

	// Modify config file
	newConfig := fmt.Sprintf(`
plugins:
  directory: %s
  auto_discover: false
gateway:
  port: 9090
  host: 0.0.0.0
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(newConfig), 0644); err != nil {
		t.Fatalf("Failed to write new config: %v", err)
	}

	// Wait for change detection (with timeout)
	select {
	case <-changeDetected:
		t.Log("Config change detected successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("Config change was not detected within timeout")
	}

	// Verify new config was loaded
	config := watcher.GetConfig()
	if config.Gateway.Port != 9090 {
		t.Errorf("Expected port 9090 after reload, got %d", config.Gateway.Port)
	}

	if config.Gateway.Host != "0.0.0.0" {
		t.Errorf("Expected host '0.0.0.0' after reload, got %s", config.Gateway.Host)
	}

	if config.Plugins.AutoDiscover {
		t.Error("Expected auto_discover to be false after reload")
	}
}

func TestConfigWatcher_InvalidConfigRollback(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")

	// Write initial valid config
	initialConfig := `
plugins:
  directory: /tmp/plugins
gateway:
  port: 8080
  host: localhost
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create config watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Start watching
	watcher.Start()

	// Wait for watcher to initialize
	time.Sleep(100 * time.Millisecond)

	// Write invalid config (invalid port)
	invalidConfig := `
plugins:
  directory: /tmp/plugins
gateway:
  port: 99999
  host: localhost
`
	if err := os.WriteFile(configPath, []byte(invalidConfig), 0644); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Wait for reload attempt
	time.Sleep(1 * time.Second)

	// Verify old config is still in use
	config := watcher.GetConfig()
	if config.Gateway.Port != 8080 {
		t.Errorf("Expected old port 8080 (rollback), got %d", config.Gateway.Port)
	}
}

func TestConfigWatcher_CallbackFailureRollback(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	
	// Create plugins directory
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("Failed to create plugins directory: %v", err)
	}

	// Write initial config
	initialConfig := fmt.Sprintf(`
plugins:
  directory: %s
gateway:
  port: 8080
  host: localhost
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create config watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Start watching
	watcher.Start()

	// Register callback that fails
	watcher.RegisterCallback(func(oldConfig, newConfig *GatewayConfig) error {
		return os.ErrPermission // Simulate callback failure
	})

	// Wait for watcher to initialize
	time.Sleep(100 * time.Millisecond)

	// Write new config
	newConfig := fmt.Sprintf(`
plugins:
  directory: %s
gateway:
  port: 9090
  host: localhost
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(newConfig), 0644); err != nil {
		t.Fatalf("Failed to write new config: %v", err)
	}

	// Wait for reload attempt
	time.Sleep(1 * time.Second)

	// Verify old config is still in use (rollback due to callback failure)
	config := watcher.GetConfig()
	if config.Gateway.Port != 8080 {
		t.Errorf("Expected old port 8080 (rollback), got %d", config.Gateway.Port)
	}
}

func TestConfigWatcher_MultipleCallbacks(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	
	// Create plugins directory
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("Failed to create plugins directory: %v", err)
	}

	// Write initial config
	initialConfig := fmt.Sprintf(`
plugins:
  directory: %s
gateway:
  port: 8080
  host: localhost
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create config watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}
	defer watcher.Stop()

	// Start watching
	watcher.Start()

	// Register multiple callbacks
	callback1Called := make(chan bool, 1)
	callback2Called := make(chan bool, 1)

	watcher.RegisterCallback(func(oldConfig, newConfig *GatewayConfig) error {
		t.Log("Callback 1 called")
		callback1Called <- true
		return nil
	})

	watcher.RegisterCallback(func(oldConfig, newConfig *GatewayConfig) error {
		t.Log("Callback 2 called")
		callback2Called <- true
		return nil
	})

	// Wait for watcher to initialize
	time.Sleep(100 * time.Millisecond)

	// Write new config
	newConfig := fmt.Sprintf(`
plugins:
  directory: %s
gateway:
  port: 9090
  host: localhost
`, pluginsDir)
	if err := os.WriteFile(configPath, []byte(newConfig), 0644); err != nil {
		t.Fatalf("Failed to write new config: %v", err)
	}

	// Wait for both callbacks
	select {
	case <-callback1Called:
		t.Log("Callback 1 detected change")
	case <-time.After(2 * time.Second):
		t.Fatal("Callback 1 was not called within timeout")
	}

	select {
	case <-callback2Called:
		t.Log("Callback 2 detected change")
	case <-time.After(2 * time.Second):
		t.Fatal("Callback 2 was not called within timeout")
	}
}

func TestConfigWatcher_Stop(t *testing.T) {
	// Create temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gateway.yaml")

	initialConfig := `
plugins:
  directory: /tmp/plugins
gateway:
  port: 8080
  host: localhost
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Create and start watcher
	watcher, err := NewConfigWatcher(configPath)
	if err != nil {
		t.Fatalf("NewConfigWatcher failed: %v", err)
	}

	watcher.Start()
	time.Sleep(100 * time.Millisecond)

	// Stop should complete quickly
	done := make(chan bool)
	go func() {
		watcher.Stop()
		done <- true
	}()

	select {
	case <-done:
		t.Log("Watcher stopped successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("Watcher did not stop within timeout")
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *GatewayConfig
		wantError bool
	}{
		{
			name: "valid config",
			config: &GatewayConfig{
				Gateway: GatewayServer{
					Port: 8080,
					Host: "localhost",
				},
				Plugins: PluginsConfig{
					Directory: os.TempDir(),
				},
			},
			wantError: false,
		},
		{
			name:      "nil config",
			config:    nil,
			wantError: true,
		},
		{
			name: "invalid port (too low)",
			config: &GatewayConfig{
				Gateway: GatewayServer{
					Port: 0,
					Host: "localhost",
				},
			},
			wantError: true,
		},
		{
			name: "invalid port (too high)",
			config: &GatewayConfig{
				Gateway: GatewayServer{
					Port: 70000,
					Host: "localhost",
				},
			},
			wantError: true,
		},
		{
			name: "nonexistent plugins directory",
			config: &GatewayConfig{
				Gateway: GatewayServer{
					Port: 8080,
					Host: "localhost",
				},
				Plugins: PluginsConfig{
					Directory: "/nonexistent/directory/that/does/not/exist",
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("validateConfig() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
