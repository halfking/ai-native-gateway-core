// Package controller connects the JSON plugin runtime to configuration reloads,
// reconciliation, and authenticated cross-process events.
package controller

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	root "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/config"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/reconciler"
	"gopkg.in/yaml.v3"
)

// Options explicitly opts the gateway into configuration-managed plugins.
type Options struct {
	ConfigPath    string
	Interval      time.Duration
	Registry      *root.Registry
	Capabilities  *root.CapabilityRegistry
	SigningPubkey string
	Ready         func(context.Context, string, *root.Manifest) error
}

// Controller owns every process, watcher, and transport it starts. A controller
// cannot be restarted after Close. Upgrades stop the old process before starting
// the replacement because the existing plugin protocol has one socket per ID.
type Controller struct {
	ctx            context.Context
	cancel         context.CancelFunc
	opMu           sync.Mutex   // serializes configuration transactions and reconciliation
	mu             sync.RWMutex // protects the public view of actual ready manifests
	actual         map[string]*root.Manifest
	unhealthy      map[string]bool
	healthFailures map[string]int // protected by opMu; observations are not desired config
	desired        map[string]*root.Manifest
	disabled       map[string]bool
	cfg            *config.Config
	options        Options
	sup            *root.Supervisor
	registry       *root.Registry
	capabilities   *root.CapabilityRegistry
	reloader       config.ConfigReloader
	reconciler     reconciler.Reconciler
	events         *eventTransport
	runtimeDir     string
	wg             sync.WaitGroup
	closeOnce      sync.Once
	closeErr       error
}

// Start validates and applies the initial configuration before exposing a ready
// controller. Child processes always use the controller's lifetime context.
func Start(ctx context.Context, options Options) (*Controller, error) {
	if options.ConfigPath == "" {
		return nil, errors.New("plugin runtime config path required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := config.NewValidator().Validate(&cfg); err != nil {
		return nil, err
	}
	// Unix socket paths have a small platform limit; macOS's default temporary
	// directory is often too long even before a plugin ID is appended.
	runtimeDir, err := os.MkdirTemp("/tmp", "gateway-runtime-")
	if err != nil {
		return nil, err
	}
	lifeCtx, cancel := context.WithCancel(ctx)
	c := &Controller{ctx: lifeCtx, cancel: cancel, options: options, cfg: &cfg, runtimeDir: runtimeDir,
		actual: make(map[string]*root.Manifest), desired: make(map[string]*root.Manifest), disabled: make(map[string]bool),
		unhealthy: make(map[string]bool), healthFailures: make(map[string]int),
		registry: options.Registry, capabilities: options.Capabilities}
	if c.registry == nil {
		c.registry = root.NewRegistry()
	}
	if c.capabilities == nil {
		c.capabilities = root.NewCapabilityRegistry()
	}
	c.events, err = newEventTransport(lifeCtx, runtimeDir, eventbus.NewMemoryEventBus(eventQueueSize))
	if err != nil {
		cancel()
		_ = os.RemoveAll(runtimeDir)
		return nil, err
	}
	c.sup = root.NewSupervisor(root.SupervisorConfig{SocketDir: runtimeDir, SigningPubkey: options.SigningPubkey, PluginEnv: c.events.environment})
	adapter := &runtimeAdapter{c: c}
	c.reconciler = reconciler.NewReconciler(&reconciler.Config{Interval: options.Interval}, adapter, adapter)
	c.reloader, err = config.NewConfigReloader(&cfg, config.NewValidator())
	if err == nil {
		err = c.reloader.OnReload(c.applyConfig)
	}
	if err == nil {
		err = c.Reconcile(ctx)
	}
	if err == nil {
		err = c.reloader.Watch(lifeCtx, options.ConfigPath)
	}
	if err != nil {
		return nil, errors.Join(err, c.Close())
	}
	interval := options.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-lifeCtx.Done():
				return
			case <-ticker.C:
				if err := c.Reconcile(lifeCtx); err != nil && lifeCtx.Err() == nil {
					log.Printf("plugin runtime reconcile: %v", err)
				}
			}
		}
	}()
	context.AfterFunc(lifeCtx, func() { _ = c.Close() })
	return c, nil
}

// Reload commits configuration only after its runtime transaction succeeds.
func (c *Controller) Reload(ctx context.Context) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	return c.reloader.Reload(ctx)
}

func (c *Controller) applyConfig(ctx context.Context, _, next *config.Config) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	// A failed transaction already restored its old desired and actual state.
	// The reloader's compensating callback must not rescan a rejected bundle.
	if reflect.DeepEqual(c.cfg, next) {
		return nil
	}
	if c.cfg.Server != next.Server || c.cfg.Security != next.Security || c.cfg.Plugins.Directory != next.Plugins.Directory {
		return errors.New("server, security, and plugin directory changes require restart")
	}
	desired, err := scan(next)
	if err != nil {
		return err
	}
	oldDisabled := c.disabled
	c.disabled = make(map[string]bool)
	if err := c.transact(ctx, desired); err != nil {
		c.disabled = oldDisabled
		return err
	}
	c.cfg = next
	return nil
}

// Reconcile scans JSON manifests and compares them with versions actually
// launched by the supervisor, rather than reporting disk versions as actual.
func (c *Controller) Reconcile(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	desired, err := scan(c.cfg)
	if err != nil {
		// A rejected deployment must not prevent recovery of already accepted
		// processes. Keep the scan error visible while reconciling their pinned
		// bundles; on initial startup there is no accepted state to fall back to.
		if len(c.desired) == 0 {
			return err
		}
		return errors.Join(err, c.transact(ctx, cloneManifestMap(c.desired)))
	}
	return c.transact(ctx, desired)
}

func (c *Controller) transact(ctx context.Context, desired map[string]*root.Manifest) error {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	defer c.pruneSnapshots()
	previous := c.desired
	oldDisabled := make(map[string]bool, len(c.disabled))
	for id, disabled := range c.disabled {
		oldDisabled[id] = disabled
	}
	c.mu.RLock()
	running := cloneManifestMap(c.actual)
	c.mu.RUnlock()
	c.desired = desired
	err := c.probeHealth(ctx)
	if err == nil {
		err = c.reconciler.Reconcile(ctx)
	}
	if err == nil {
		// Failed and stopped processes also need identity and index cleanup
		// when they disappear from the desired set.
		for _, state := range c.sup.ProcessSnapshot() {
			if desired[state.PluginID] == nil || c.disabled[state.PluginID] {
				err = errors.Join(err, c.stop(state.PluginID))
			}
		}
	}
	if err != nil {
		c.desired = previous
		c.disabled = oldDisabled
		rollbackCtx, stop := context.WithTimeout(c.ctx, 10*time.Second)
		defer stop()
		return errors.Join(err, c.restore(rollbackCtx, running))
	}
	// Accepted desired state must remain executable even if a later deployment
	// deletes or corrupts the original files. Keep each running version pinned.
	c.mu.RLock()
	for id, actual := range c.actual {
		if desired := c.desired[id]; desired != nil && desired.PluginVersion == actual.PluginVersion {
			c.desired[id] = actual
		}
	}
	c.mu.RUnlock()
	return nil
}

func (c *Controller) restore(ctx context.Context, previous map[string]*root.Manifest) error {
	var errs []error
	for _, state := range c.sup.ProcessSnapshot() {
		c.mu.RLock()
		actual := c.actual[state.PluginID]
		c.mu.RUnlock()
		old := previous[state.PluginID]
		if actual != old || old == nil || state.Status == "crashed" {
			errs = append(errs, c.stop(state.PluginID))
		}
	}
	for id, old := range previous {
		c.mu.RLock()
		actual := c.actual[id]
		c.mu.RUnlock()
		if actual != old {
			errs = append(errs, c.start(ctx, old))
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return merged, func() { stop(); cancel() }
}

func cloneManifestMap(source map[string]*root.Manifest) map[string]*root.Manifest {
	out := make(map[string]*root.Manifest, len(source))
	for id, m := range source {
		out[id] = m
	}
	return out
}

// BaseURL returns a route only for a ready, still-running process.
func (c *Controller) BaseURL(id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Keep readiness and the process snapshot in the same lifecycle generation.
	ready := c.actual[id] != nil && !c.unhealthy[id]
	if !ready || c.ctx.Err() != nil {
		return ""
	}
	for _, state := range c.sup.ProcessSnapshot() {
		if state.PluginID == id && state.Status != "crashed" && state.Status != "stopped" && state.SocketPath != "" {
			return "unix://" + state.SocketPath
		}
	}
	return ""
}

// Manifest returns an independent copy of the manifest actually made ready.
func (c *Controller) Manifest(id string) *root.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyManifest(c.actual[id])
}

func (c *Controller) Supervisor() *root.Supervisor { return c.sup }

// Close cancels in-flight waits, joins loops/watchers and IPC handlers, stops all
// managed children, revokes their identities, and removes private runtime files.
func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		var errs []error
		if c.reloader != nil {
			errs = append(errs, c.reloader.Stop())
		}
		c.wg.Wait()
		c.opMu.Lock()
		if c.sup != nil {
			for _, state := range c.sup.ProcessSnapshot() {
				errs = append(errs, c.stop(state.PluginID))
			}
		}
		c.opMu.Unlock()
		if c.events != nil {
			errs = append(errs, c.events.close())
		}
		errs = append(errs, os.RemoveAll(c.runtimeDir))
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
