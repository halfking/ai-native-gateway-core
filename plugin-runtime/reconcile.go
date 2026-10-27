package pluginruntime

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
)

// LifecycleState is where a plugin is in Reconcile's life cycle.
type LifecycleState string

const (
	LifeOff      LifecycleState = "off"      // turned off in desired set
	LifePending  LifecycleState = "pending"  // on, waiting for a Required service
	LifeActive   LifecycleState = "active"   // applied
	LifeFailed   LifecycleState = "failed"   // Apply errored/panicked; isolated
	LifeDisposed LifecycleState = "disposed" // turned off at run time, parts gone
)

// CoreServices are host-provided services a plugin may Require without another
// plugin Providing them.
var CoreServices = []string{"gateway", "settings", "registry"}

// Driver applies/disposes one plugin. Process plugins wire Supervisor here;
// tests use a fake. Failures must not panic out of Apply — return an error.
type Driver interface {
	Apply(m *Manifest, config map[string]any) error
	Dispose(pluginID string) error
}

// LifecycleStatus is one plugin as Reconcile sees it now.
type LifecycleStatus struct {
	ID    string         `json:"id"`
	State LifecycleState `json:"state"`
	Why   string         `json:"why,omitempty"`
}

type runningPlugin struct {
	m        Manifest
	state    LifecycleState
	why      string
	wanted   bool
	config   map[string]any
	provided []string
}

// Reconciler brings desired on/off/config to the runtime: off first (reverse
// Dispose + cascade dependents), then on (pending until Requires satisfied).
// One failure is isolated — others keep running.
type Reconciler struct {
	mu       sync.Mutex
	plugins  map[string]*runningPlugin
	order    []string // applied order, for disposing newest-first
	services map[string]any
	driver   Driver
	// inUse probes: unknown/error counts as in use (never unused).
	inUse map[string]func() (int, error)
}

// NewReconciler builds a reconciler over the given manifests; none applied yet.
func NewReconciler(ms []*Manifest, driver Driver) *Reconciler {
	rt := &Reconciler{
		plugins:  map[string]*runningPlugin{},
		services: map[string]any{},
		driver:   driver,
		inUse:    map[string]func() (int, error){},
	}
	for _, s := range CoreServices {
		rt.services[s] = struct{}{}
	}
	for _, m := range ms {
		if m == nil || m.PluginID == "" {
			continue
		}
		cp := *m
		rt.plugins[m.PluginID] = &runningPlugin{m: cp, state: LifeOff}
	}
	return rt
}

// SetInUseProbe registers the runtime InUse counter for plugin id (JSON cannot carry funcs).
func (rt *Reconciler) SetInUseProbe(id string, fn func() (int, error)) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.inUse[id] = fn
}

// Reconcile brings the runtime to desired: each wanted-and-not-active plugin is
// applied once Requires are provided; each active one no longer wanted is
// disposed, with dependents that Require a service it Provided.
func (rt *Reconciler) Reconcile(on map[string]bool, config map[string]map[string]any) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for id, p := range rt.plugins {
		wanted := on[id]
		if !wanted {
			if fn := rt.inUse[id]; fn != nil {
				n, err := fn()
				if err != nil || n > 0 {
					wanted = true
				}
			}
		}
		p.wanted = wanted
		if cfg, ok := config[id]; ok {
			p.config = maps.Clone(cfg)
		}
	}
	// off first: reverse Dispose
	going := map[string]bool{}
	for {
		more := false
		for _, id := range rt.order {
			p := rt.plugins[id]
			if going[id] {
				continue
			}
			if !p.wanted || rt.needsGoing(p, going) {
				going[id], more = true, true
			}
		}
		if !more {
			break
		}
	}
	for i := len(rt.order) - 1; i >= 0; i-- {
		if id := rt.order[i]; going[id] {
			rt.disposeLocked(rt.plugins[id])
		}
	}
	// then on
	ids := make([]string, 0, len(rt.plugins))
	for id := range rt.plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for {
		applied := false
		for _, id := range ids {
			p := rt.plugins[id]
			if !p.wanted || p.state == LifeActive || p.state == LifeFailed {
				continue
			}
			if !Compatible(p.m.EffectiveAPI()) {
				p.state, p.why = LifeFailed, fmt.Sprintf("written for host API %s, this is %s", p.m.EffectiveAPI(), HostAPI)
				continue
			}
			if missing := rt.missing(p.m); len(missing) > 0 {
				p.state, p.why = LifePending, "waiting for "+strings.Join(missing, ", ")
				continue
			}
			rt.applyLocked(p)
			applied = applied || p.state == LifeActive
		}
		if !applied {
			break
		}
	}
	for _, p := range rt.plugins {
		if !p.wanted && p.state != LifeDisposed {
			p.state, p.why = LifeOff, ""
		}
		if p.state == LifePending {
			slog.Info("plugin pending", "plugin", p.m.PluginID, "why", p.why)
		}
	}
}

func (rt *Reconciler) needsGoing(p *runningPlugin, going map[string]bool) bool {
	for id := range going {
		q := rt.plugins[id]
		for _, s := range q.provided {
			if slices.Contains(p.m.Requires, s) {
				return true
			}
		}
	}
	return false
}

func (rt *Reconciler) missing(m Manifest) []string {
	var out []string
	for _, s := range m.Requires {
		if _, ok := rt.services[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

func (rt *Reconciler) applyLocked(p *runningPlugin) {
	cfg := p.config
	if cfg == nil {
		cfg = maps.Clone(p.m.Defaults)
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		if rt.driver == nil {
			return nil
		}
		return rt.driver.Apply(&p.m, cfg)
	}()
	if err != nil {
		p.state, p.why, p.provided = LifeFailed, err.Error(), nil
		slog.Warn("plugin apply failed", "plugin", p.m.PluginID, "error", err)
		return
	}
	provided := slices.Clone(p.m.Provides)
	for _, s := range provided {
		rt.services[s] = struct{}{}
	}
	p.state, p.why, p.provided = LifeActive, "", provided
	rt.order = append(rt.order, p.m.PluginID)
}

func (rt *Reconciler) disposeLocked(p *runningPlugin) {
	if rt.driver != nil && p.state == LifeActive {
		if err := rt.driver.Dispose(p.m.PluginID); err != nil {
			slog.Warn("plugin dispose failed", "plugin", p.m.PluginID, "error", err)
		}
	}
	for _, s := range p.provided {
		delete(rt.services, s)
	}
	p.provided, p.state, p.why = nil, LifeDisposed, ""
	rt.order = slices.DeleteFunc(rt.order, func(id string) bool { return id == p.m.PluginID })
}

// Statuses returns plugins sorted by id.
func (rt *Reconciler) Statuses() []LifecycleStatus {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]LifecycleStatus, 0, len(rt.plugins))
	for id, p := range rt.plugins {
		out = append(out, LifecycleStatus{ID: id, State: p.state, Why: p.why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Active reports whether plugin id is currently applied.
func (rt *Reconciler) Active(id string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	p := rt.plugins[id]
	return p != nil && p.state == LifeActive
}

// Stop disposes every active plugin, newest first.
func (rt *Reconciler) Stop() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for len(rt.order) > 0 {
		rt.disposeLocked(rt.plugins[rt.order[len(rt.order)-1]])
	}
}
