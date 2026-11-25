// Package controller connects the JSON plugin runtime to configuration reloads,
// reconciliation, and authenticated cross-process events.
package controller

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"slices"
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
	// OptionalPlugins start in the background and reconcile independently.
	// Changes to the enabled list are applied as one configuration transaction.
	OptionalPlugins []string
	Ready           func(context.Context, string, *root.Manifest) error
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
	healthFailures map[string]int    // protected by opMu; observations are not desired config
	scope          func(string) bool // transaction scope protected by opMu
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
	if err := validateEnabledPluginIDs(cfg.Plugins.Enabled); err != nil {
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
	reconcilerCfg := reconcilerConfig(options)
	c.reconciler = reconciler.NewReconciler(&reconcilerCfg, adapter, adapter)
	c.reloader, err = config.NewConfigReloader(&cfg, config.NewValidator())
	if err == nil {
		err = c.reloader.OnReload(c.applyConfig)
	}
	required, optional := c.splitConfig()
	if err == nil {
		var desired map[string]*root.Manifest
		desired, err = scan(&required)
		if err == nil {
			err = c.transact(ctx, desired)
		}
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
		if len(optional) > 0 {
			c.reconcileBackground(lifeCtx)
		}
		for {
			select {
			case <-lifeCtx.Done():
				return
			case <-ticker.C:
				c.reconcileBackground(lifeCtx)
			}
		}
	}()
	context.AfterFunc(lifeCtx, func() { _ = c.Close() })
	return c, nil
}

func (c *Controller) isOptional(id string) bool {
	return slices.Contains(c.options.OptionalPlugins, id)
}

func (c *Controller) splitConfig() (config.Config, []string) {
	required := *c.cfg
	required.Plugins.Enabled = nil
	var optional []string
	for _, id := range c.cfg.Plugins.Enabled {
		if c.isOptional(id) {
			optional = append(optional, id)
		} else {
			required.Plugins.Enabled = append(required.Plugins.Enabled, id)
		}
	}
	return required, optional
}

func (c *Controller) inScope(id string) bool {
	return c.scope == nil || c.scope(id)
}

func (c *Controller) transactScoped(ctx context.Context, desired map[string]*root.Manifest, scope func(string) bool) error {
	c.scope = scope
	defer func() { c.scope = nil }()
	return c.transact(ctx, desired)
}

// ConfigRejection reports rejected bytes that still differ from accepted config.
func (c *Controller) ConfigRejection() (config.Rejection, bool) {
	return config.RejectionOf(c.reloader)
}

func (c *Controller) reconcileBackground(ctx context.Context) {
	if err := c.Reconcile(ctx); err != nil && ctx.Err() == nil {
		log.Printf("plugin runtime reconcile: %v", err)
	}
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
	required, optional := c.splitConfig()
	if len(optional) == 0 {
		desired, err := scan(c.cfg)
		if err == nil {
			return c.transact(ctx, desired)
		}
		if len(c.desired) == 0 {
			return err
		}
		return errors.Join(err, c.transact(ctx, cloneManifestMap(c.desired)))
	}

	// Keep the accepted desired manifest for other scopes. A failed proposal
	// must not withdraw their readiness, advance their crash counters or
	// roll back an unrelated upgrade completed in this cycle.
	desired, scanErr := scan(&required)
	if scanErr != nil {
		desired = cloneManifestMap(c.desired)
	} else {
		for id, old := range c.desired {
			if c.isOptional(id) {
				desired[id] = old
			}
		}
	}
	err := errors.Join(scanErr, c.transactScoped(ctx, desired, func(id string) bool { return !c.isOptional(id) }))
	for _, id := range optional {
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		cfg := *c.cfg
		cfg.Plugins.Enabled = []string{id}
		found, scanErr := scan(&cfg)
		if scanErr != nil && c.desired[id] == nil {
			err = errors.Join(err, scanErr)
			continue
		}
		desired := cloneManifestMap(c.desired)
		if scanErr == nil {
			desired[id] = found[id]
		}
		err = errors.Join(err, scanErr, c.transactScoped(ctx, desired, func(candidate string) bool { return candidate == id }))
	}
	return err
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
			if c.inScope(state.PluginID) && (desired[state.PluginID] == nil || c.disabled[state.PluginID]) {
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
		if desired := c.desired[id]; c.inScope(id) && desired != nil && desired.PluginVersion == actual.PluginVersion {
			c.desired[id] = actual
		}
	}
	c.mu.RUnlock()
	return nil
}

func (c *Controller) restore(ctx context.Context, previous map[string]*root.Manifest) error {
	var errs []error
	for _, state := range c.sup.ProcessSnapshot() {
		if !c.inScope(state.PluginID) {
			continue
		}
		c.mu.RLock()
		actual := c.actual[state.PluginID]
		c.mu.RUnlock()
		old := previous[state.PluginID]
		if actual != old || old == nil || state.Status == "crashed" {
			errs = append(errs, c.stop(state.PluginID))
		}
	}
	for id, old := range previous {
		if !c.inScope(id) {
			continue
		}
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
	if state, ok := c.readyProcess(id); ok {
		return "unix://" + state.SocketPath
	}
	return ""
}

// CapabilityEndpoint identifies one ready process and its accepted manifest version.
type CapabilityEndpoint struct {
	SocketPath string
	Version    string
	PID        int
}

// ReadyCapability resolves a gateway-originated v2 capability against one
// ready generation of the accepted manifest and private socket.
func (c *Controller) ReadyCapability(id, capability string) CapabilityEndpoint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := c.actual[id]
	if m == nil || m.SchemaVersion != 2 || !slices.Contains(m.Capabilities, capability) ||
		!slices.Contains(m.Permissions, capability) || !c.capabilities.Allows(id, capability) {
		return CapabilityEndpoint{}
	}
	if state, ok := c.readyProcess(id); ok {
		return CapabilityEndpoint{SocketPath: state.SocketPath, Version: m.PluginVersion, PID: state.Pid}
	}
	return CapabilityEndpoint{}
}

// Call with c.mu held so manifest readiness and the process are from the same generation.
func (c *Controller) readyProcess(id string) (root.PluginState, bool) {
	if c.actual[id] != nil && !c.unhealthy[id] && c.ctx.Err() == nil {
		for _, state := range c.sup.ProcessSnapshot() {
			if state.PluginID == id && state.Status != "crashed" && state.Status != "stopped" && state.SocketPath != "" {
				return state, true
			}
		}
	}
	return root.PluginState{}, false
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
