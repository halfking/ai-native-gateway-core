package config

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// ConfigReloader watches and reloads configuration files.
type ConfigReloader interface {
	// Watch starts watching the configuration file for changes.
	Watch(ctx context.Context, configPath string) error

	// OnReload registers a handler to be called when config is reloaded.
	OnReload(handler ReloadHandler) error

	// Reload manually triggers a configuration reload.
	Reload(ctx context.Context) error

	// GetConfig returns the current configuration.
	GetConfig() *Config

	// Stop stops watching for configuration changes.
	Stop() error
}

// ReloadHandler is called when configuration is successfully reloaded.
// It receives both the old and new configuration for comparison.
type ReloadHandler func(ctx context.Context, oldConfig, newConfig *Config) error

type configReloader struct {
	holder     *ConfigHolder
	validator  Validator
	handlers   []ReloadHandler
	watcher    *fsnotify.Watcher
	configPath string

	mu      sync.RWMutex
	stopCh  chan struct{}
	stopped bool
}

// NewConfigReloader creates a new configuration reloader.
func NewConfigReloader(initialConfig *Config, validator Validator) (ConfigReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	return &configReloader{
		holder:    NewConfigHolder(initialConfig),
		validator: validator,
		watcher:   watcher,
		stopCh:    make(chan struct{}),
	}, nil
}

// Watch starts watching the configuration file.
func (r *configReloader) Watch(ctx context.Context, configPath string) error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return fmt.Errorf("reloader is stopped")
	}
	r.configPath = configPath
	r.mu.Unlock()

	// Add file to watcher
	if err := r.watcher.Add(configPath); err != nil {
		return fmt.Errorf("watch config file: %w", err)
	}

	log.Printf("INFO: Watching configuration file: %s", configPath)

	// Start watching goroutine
	go r.watch(ctx)

	return nil
}

// watch monitors file changes and triggers reloads.
func (r *configReloader) watch(ctx context.Context) {
	// Debounce timer to avoid multiple reloads on rapid file changes
	var timer *time.Timer
	var timerCh <-chan time.Time

	for {
		select {
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}

			// Only handle Write and Create events
			if event.Op&fsnotify.Write == fsnotify.Write ||
				event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("INFO: Config file changed: %s (op: %s)", event.Name, event.Op)

				// Debounce: wait 100ms before reloading
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(100 * time.Millisecond)
				timerCh = timer.C
			}

		case <-timerCh:
			// Trigger reload after debounce
			if err := r.Reload(ctx); err != nil {
				log.Printf("ERROR: Config reload failed: %v", err)
			}

		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("ERROR: Watcher error: %v", err)

		case <-r.stopCh:
			return

		case <-ctx.Done():
			return
		}
	}
}

// OnReload registers a reload handler.
func (r *configReloader) OnReload(handler ReloadHandler) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers = append(r.handlers, handler)
	return nil
}

// Reload manually triggers a configuration reload.
func (r *configReloader) Reload(ctx context.Context) error {
	r.mu.RLock()
	configPath := r.configPath
	handlers := make([]ReloadHandler, len(r.handlers))
	copy(handlers, r.handlers)
	r.mu.RUnlock()

	if configPath == "" {
		return fmt.Errorf("no config path set")
	}

	// Load old config
	oldConfig := r.holder.Get()

	// Read and parse new config
	newConfig, err := r.loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Validate new config
	if err := r.validator.Validate(newConfig); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}

	// Atomically replace config
	r.holder.Set(newConfig)

	log.Printf("INFO: Configuration reloaded successfully")

	// Call reload handlers
	for i, handler := range handlers {
		if err := handler(ctx, oldConfig, newConfig); err != nil {
			log.Printf("ERROR: Reload handler %d failed: %v", i, err)
			// Continue calling other handlers
		}
	}

	return nil
}

// loadConfig loads and parses the configuration file.
func (r *configReloader) loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	return &config, nil
}

// GetConfig returns the current configuration.
func (r *configReloader) GetConfig() *Config {
	return r.holder.Get()
}

// Stop stops watching for configuration changes.
func (r *configReloader) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stopped {
		return nil
	}

	r.stopped = true
	close(r.stopCh)

	if err := r.watcher.Close(); err != nil {
		return fmt.Errorf("close watcher: %w", err)
	}

	log.Println("INFO: Config reloader stopped")
	return nil
}
