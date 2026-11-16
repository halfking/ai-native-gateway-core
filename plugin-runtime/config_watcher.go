// Package pluginruntime provides configuration hot reload capability.
package pluginruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// ConfigWatcher monitors configuration files and triggers reload callbacks.
type ConfigWatcher struct {
	configPath string
	watcher    *fsnotify.Watcher
	
	// Current configuration (protected by RWMutex)
	mu     sync.RWMutex
	config *GatewayConfig
	
	// Callback registry
	callbacksMu sync.RWMutex
	callbacks   []ConfigChangeCallback
	
	// Control channels
	stopCh  chan struct{}
	doneCh  chan struct{}
	started bool
	startMu sync.Mutex
}

// GatewayConfig represents the gateway configuration structure.
// This is a simplified version - extend as needed for actual config structure.
type GatewayConfig struct {
	Plugins PluginsConfig `yaml:"plugins"`
	Gateway GatewayServer `yaml:"gateway"`
	// Add other config sections as needed
}

// PluginsConfig holds plugin-related configuration.
type PluginsConfig struct {
	Directory    string            `yaml:"directory"`
	AutoDiscover bool              `yaml:"auto_discover"`
	Essentials   []string          `yaml:"essentials"`
	Settings     map[string]any    `yaml:"settings"`
}

// GatewayServer holds gateway server configuration.
type GatewayServer struct {
	Port         int    `yaml:"port"`
	Host         string `yaml:"host"`
	ReadTimeout  string `yaml:"read_timeout"`
	WriteTimeout string `yaml:"write_timeout"`
}

// ConfigChangeCallback is called when configuration changes.
type ConfigChangeCallback func(oldConfig, newConfig *GatewayConfig) error

// NewConfigWatcher creates a new configuration watcher.
func NewConfigWatcher(configPath string) (*ConfigWatcher, error) {
	// Resolve to absolute path
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path: %w", err)
	}

	// Load initial configuration
	config, err := loadConfig(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load initial config: %w", err)
	}

	// Create fsnotify watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	// Watch the config file's directory (not the file itself, to handle atomic writes)
	configDir := filepath.Dir(absPath)
	if err := watcher.Add(configDir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("failed to watch config directory: %w", err)
	}

	cw := &ConfigWatcher{
		configPath: absPath,
		watcher:    watcher,
		config:     config,
		callbacks:  make([]ConfigChangeCallback, 0),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}

	fmt.Printf("INFO: config watcher initialized for %s\n", absPath)
	return cw, nil
}

// Start begins watching for configuration changes.
func (cw *ConfigWatcher) Start() {
	cw.startMu.Lock()
	defer cw.startMu.Unlock()
	
	if cw.started {
		return
	}
	
	cw.started = true
	go cw.watchLoop()
}

// Stop stops the configuration watcher.
func (cw *ConfigWatcher) Stop() {
	cw.startMu.Lock()
	started := cw.started
	cw.startMu.Unlock()
	
	if !started {
		// Never started, just close watcher
		cw.watcher.Close()
		return
	}
	
	close(cw.stopCh)
	<-cw.doneCh
	cw.watcher.Close()
	fmt.Println("INFO: config watcher stopped")
}

// RegisterCallback registers a callback to be called when configuration changes.
func (cw *ConfigWatcher) RegisterCallback(cb ConfigChangeCallback) {
	cw.callbacksMu.Lock()
	defer cw.callbacksMu.Unlock()
	cw.callbacks = append(cw.callbacks, cb)
}

// GetConfig returns a copy of the current configuration (thread-safe).
func (cw *ConfigWatcher) GetConfig() *GatewayConfig {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	
	// Return a deep copy to prevent external mutations
	return copyConfig(cw.config)
}

// watchLoop is the main event loop for file system events.
func (cw *ConfigWatcher) watchLoop() {
	defer close(cw.doneCh)

	// Debounce timer to avoid multiple reloads for rapid file changes
	var debounceTimer *time.Timer
	const debounceDelay = 500 * time.Millisecond

	for {
		select {
		case event, ok := <-cw.watcher.Events:
			if !ok {
				return
			}

			// Only react to Write or Create events on the config file
			if event.Name != cw.configPath {
				continue
			}

			if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
				// Debounce: reset timer if already running
				if debounceTimer != nil {
					debounceTimer.Stop()
				}

				debounceTimer = time.AfterFunc(debounceDelay, func() {
					cw.reloadConfig()
				})
			}

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("WARN: config watcher error: %v\n", err)

		case <-cw.stopCh:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		}
	}
}

// reloadConfig attempts to reload the configuration file.
func (cw *ConfigWatcher) reloadConfig() {
	fmt.Printf("INFO: detected config change, reloading %s\n", cw.configPath)

	// Load new configuration
	newConfig, err := loadConfig(cw.configPath)
	if err != nil {
		fmt.Printf("ERROR: failed to load new config (keeping old config): %v\n", err)
		return
	}

	// Validate new configuration
	if err := validateConfig(newConfig); err != nil {
		fmt.Printf("ERROR: invalid new config (keeping old config): %v\n", err)
		return
	}

	// Get old config for callbacks
	cw.mu.RLock()
	oldConfig := cw.config
	cw.mu.RUnlock()

	// Notify callbacks BEFORE swapping config
	cw.callbacksMu.RLock()
	callbacks := make([]ConfigChangeCallback, len(cw.callbacks))
	copy(callbacks, cw.callbacks)
	cw.callbacksMu.RUnlock()

	for _, cb := range callbacks {
		if err := cb(oldConfig, newConfig); err != nil {
			fmt.Printf("ERROR: config change callback failed (rolling back): %v\n", err)
			// Don't update config if any callback fails
			return
		}
	}

	// Atomically swap configuration
	cw.mu.Lock()
	cw.config = newConfig
	cw.mu.Unlock()

	fmt.Printf("INFO: config reloaded successfully\n")
}

// loadConfig loads configuration from a YAML file.
func loadConfig(path string) (*GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config GatewayConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	return &config, nil
}

// validateConfig validates configuration structure and values.
func validateConfig(config *GatewayConfig) error {
	if config == nil {
		return fmt.Errorf("config is nil")
	}

	// Validate gateway server config
	if config.Gateway.Port <= 0 || config.Gateway.Port > 65535 {
		return fmt.Errorf("invalid gateway port: %d", config.Gateway.Port)
	}

	// Validate plugins directory exists (if specified)
	if config.Plugins.Directory != "" {
		if _, err := os.Stat(config.Plugins.Directory); err != nil {
			return fmt.Errorf("plugins directory does not exist: %s", config.Plugins.Directory)
		}
	}

	return nil
}

// copyConfig creates a deep copy of the configuration.
func copyConfig(config *GatewayConfig) *GatewayConfig {
	if config == nil {
		return nil
	}

	// For simplicity, use YAML marshal/unmarshal for deep copy
	// In production, consider using a proper deep copy library
	data, err := yaml.Marshal(config)
	if err != nil {
		// Fallback: return original (not ideal but prevents nil)
		return config
	}

	var copy GatewayConfig
	if err := yaml.Unmarshal(data, &copy); err != nil {
		return config
	}

	return &copy
}
