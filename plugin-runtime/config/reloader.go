package config

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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

// ReloadHandler applies configuration before the new snapshot is published.
// It applies newConfig. On failure, handlers already attempted are invoked in
// reverse with the arguments swapped to restore the old state. Handlers must
// therefore be idempotent and support applying either configuration.
type ReloadHandler func(ctx context.Context, oldConfig, newConfig *Config) error

type configReloader struct {
	holder     *ConfigHolder
	validator  Validator
	handlers   []ReloadHandler
	watcher    *fsnotify.Watcher
	configPath string

	mu       sync.RWMutex
	reloadMu sync.Mutex
	wg       sync.WaitGroup
	watching bool
	stopCh   chan struct{}
	stopped  bool
	lifetime context.Context
	cancel   context.CancelFunc
	stopOnce sync.Once
	stopErr  error
}

// NewConfigReloader creates a new configuration reloader.
func NewConfigReloader(initialConfig *Config, validator Validator) (ConfigReloader, error) {
	if initialConfig == nil || validator == nil {
		return nil, fmt.Errorf("initial config and validator are required")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	lifetime, cancel := context.WithCancel(context.Background())
	return &configReloader{
		holder:    NewConfigHolder(initialConfig),
		validator: validator,
		watcher:   watcher,
		stopCh:    make(chan struct{}),
		lifetime:  lifetime, cancel: cancel,
	}, nil
}

// Watch starts watching the configuration file.
func (r *configReloader) Watch(ctx context.Context, configPath string) error {
	path, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.stopped || r.watching {
		r.mu.Unlock()
		return fmt.Errorf("reloader is stopped or already watching")
	}
	// Watch the directory so atomic replacement retains the watch on Linux too.
	if err := r.watcher.Add(filepath.Dir(path)); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("watch config file: %w", err)
	}
	r.configPath = path
	r.watching = true
	r.wg.Add(1)
	r.mu.Unlock()

	log.Printf("INFO: Watching configuration file: %s", configPath)

	// Start watching goroutine
	go r.watch(ctx)

	return nil
}

// watch monitors file changes and triggers reloads.
func (r *configReloader) watch(ctx context.Context) {
	defer r.wg.Done()
	// Debounce timer to avoid multiple reloads on rapid file changes
	var timer *time.Timer
	var timerCh <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case event, ok := <-r.watcher.Events:
			if !ok {
				return
			}

			if filepath.Clean(event.Name) == r.configPath &&
				event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				log.Printf("INFO: Config file changed: %s (op: %s)", event.Name, event.Op)

				// Debounce: wait 100ms before reloading
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(100 * time.Millisecond)
				timerCh = timer.C
			}

		case <-timerCh:
			timerCh = nil
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

	if r.stopped || handler == nil {
		return fmt.Errorf("reloader stopped or nil handler")
	}
	r.handlers = append(r.handlers, handler)
	return nil
}

// Reload manually triggers a configuration reload.
func (r *configReloader) Reload(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(r.lifetime, cancel)
	defer func() { stopCancel(); cancel() }()
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.RLock()
	if r.stopped {
		r.mu.RUnlock()
		return fmt.Errorf("reloader is stopped")
	}
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

	// Publish only after every application succeeds. Include the failing handler
	// in rollback: it may have partially changed its own state before returning.
	for i, handler := range handlers {
		if err := handler(ctx, oldConfig, newConfig); err != nil {
			failures := []error{fmt.Errorf("reload handler %d: %w", i, err)}
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			for j := i; j >= 0; j-- {
				if rollbackErr := handlers[j](rollbackCtx, newConfig, oldConfig); rollbackErr != nil {
					failures = append(failures, fmt.Errorf("rollback handler %d: %w", j, rollbackErr))
				}
			}
			return errors.Join(failures...)
		}
	}
	r.holder.Set(newConfig)
	log.Printf("INFO: Configuration reloaded successfully")
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
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.cancel()
		close(r.stopCh)
		r.mu.Unlock()
		r.stopErr = r.watcher.Close()
		r.wg.Wait()
		r.reloadMu.Lock()
		r.reloadMu.Unlock()
		log.Println("INFO: Config reloader stopped")
	})
	return r.stopErr
}
