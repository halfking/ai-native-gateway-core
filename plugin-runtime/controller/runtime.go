package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	root "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/discovery"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/eventbus"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/reconciler"
	dto "github.com/kaixuan/llm-gateway-go/plugin-runtime/supervisor"
)

// runtimeAdapter uses the new reconciliation DTOs, but every process operation
// is delegated to the established root supervisor.
type runtimeAdapter struct{ c *Controller }

func (a *runtimeAdapter) List(_ context.Context, filter reconciler.RegistryFilter) ([]*reconciler.RegistryPlugin, error) {
	var result []*reconciler.RegistryPlugin
	for id, m := range a.c.desired {
		enabled := !a.c.disabled[id]
		if filter.EnabledOnly && !enabled {
			continue
		}
		result = append(result, &reconciler.RegistryPlugin{ID: id, Name: id, Version: m.PluginVersion, Enabled: enabled})
	}
	return result, nil
}
func (a *runtimeAdapter) UpdateVersion(_ context.Context, id, version string) error {
	m := a.c.desired[id]
	if m == nil || m.PluginVersion != version {
		return fmt.Errorf("desired manifest %s@%s missing", id, version)
	}
	return nil
}
func (a *runtimeAdapter) UpdateStatus(_ context.Context, id string, status reconciler.PluginStatus) error {
	a.c.disabled[id] = status == reconciler.StatusDisabled
	return nil
}
func (a *runtimeAdapter) GetManifest(_ context.Context, id string) (*discovery.Manifest, error) {
	m := a.c.desired[id]
	if m == nil {
		return nil, fmt.Errorf("desired manifest %s missing", id)
	}
	return &discovery.Manifest{Metadata: discovery.Metadata{Name: id, Version: m.PluginVersion}}, nil
}
func (a *runtimeAdapter) Start(ctx context.Context, m *discovery.Manifest) error {
	desired := a.c.desired[m.Metadata.Name]
	if desired == nil || desired.PluginVersion != m.Metadata.Version {
		return errors.New("desired manifest changed")
	}
	return a.c.start(ctx, desired)
}
func (a *runtimeAdapter) Stop(_ context.Context, id string) error { return a.c.stop(id) }
func (a *runtimeAdapter) Restart(ctx context.Context, id string) error {
	a.c.mu.RLock()
	unhealthy := a.c.unhealthy[id]
	a.c.mu.RUnlock()
	kind := eventbus.EventTypePluginCrashed
	if unhealthy {
		kind = "plugin.degraded"
	}
	a.c.lifecycle(kind, id, nil)
	if err := a.c.stop(id); err != nil {
		return err
	}
	return a.c.start(ctx, a.c.desired[id])
}
func (a *runtimeAdapter) Upgrade(ctx context.Context, m *discovery.Manifest) error {
	c := a.c
	c.mu.RLock()
	old := c.actual[m.Metadata.Name]
	c.mu.RUnlock()
	if err := c.stop(m.Metadata.Name); err != nil {
		return err
	}
	if err := a.Start(ctx, m); err != nil {
		rollbackCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		defer cancel()
		if old != nil {
			return errors.Join(err, c.start(rollbackCtx, old))
		}
		return err
	}
	c.lifecycle(eventbus.EventTypePluginUpgraded, m.Metadata.Name, map[string]string{"version": m.Metadata.Version})
	return nil
}
func (a *runtimeAdapter) ListAll() []*dto.ProcessState {
	states := a.c.sup.ProcessSnapshot()
	out := make([]*dto.ProcessState, 0, len(states))
	a.c.mu.RLock()
	defer a.c.mu.RUnlock()
	for _, state := range states {
		status := dto.StatusStopped
		switch {
		case state.Status == "crashed" || a.c.unhealthy[state.PluginID]:
			status = dto.StatusCrashed
		case a.c.actual[state.PluginID] != nil && state.SocketPath != "":
			status = dto.StatusRunning
		case state.Status == "starting":
			status = dto.StatusStarting
		}
		out = append(out, &dto.ProcessState{PluginID: state.PluginID, Version: state.PluginVersion, PID: state.Pid, Status: status})
	}
	return out
}
func (a *runtimeAdapter) StateOf(id string) (*dto.ProcessState, error) {
	for _, state := range a.ListAll() {
		if state.PluginID == id {
			return state, nil
		}
	}
	return nil, fmt.Errorf("plugin %s not found", id)
}

func (c *Controller) start(ctx context.Context, m *root.Manifest) error {
	if m == nil {
		return errors.New("plugin manifest missing")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pinned, err := c.snapshot(m)
	if err != nil {
		return err
	}
	if err := c.events.grant(m.PluginID); err != nil {
		return err
	}
	state, err := c.sup.Start(c.ctx, pinned)
	if err != nil {
		c.events.revoke(m.PluginID)
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ready := c.options.Ready
	if ready == nil {
		ready = waitReady
	}
	if err := ready(readyCtx, state.SocketPath, pinned); err != nil {
		return errors.Join(fmt.Errorf("plugin %s readiness: %w", m.PluginID, err), c.stop(m.PluginID))
	}
	if err := readyCtx.Err(); err != nil {
		return errors.Join(err, c.stop(m.PluginID))
	}
	if err := os.Chmod(state.SocketPath, 0600); err != nil {
		return errors.Join(err, c.stop(m.PluginID))
	}
	if err := c.registry.SetBindings(m.PluginID, pinned.Bindings, root.BindingValidationOptions{}); err != nil {
		return errors.Join(err, c.stop(m.PluginID))
	}
	if err := c.capabilities.RegisterManifest(pinned); err != nil {
		return errors.Join(err, c.stop(m.PluginID))
	}
	readyState := *state
	readyState.Status = "ready"
	readyState.StartedAt = time.Now()
	c.registry.SetPlugin(&readyState)
	c.registry.SetNav(m.PluginID, m.PluginVersion, pinned.Pages)
	c.mu.Lock()
	c.actual[m.PluginID] = pinned
	delete(c.unhealthy, m.PluginID)
	c.mu.Unlock()
	delete(c.healthFailures, m.PluginID)
	c.lifecycle(eventbus.EventTypePluginStarted, m.PluginID, map[string]any{"version": m.PluginVersion, "pid": state.Pid})
	return nil
}

func (c *Controller) stop(id string) error {
	c.mu.Lock()
	old := c.actual[id]
	delete(c.actual, id)
	delete(c.unhealthy, id)
	c.mu.Unlock()
	delete(c.healthFailures, id)
	c.registry.RemovePlugin(id)
	_ = c.registry.SetBindings(id, nil, root.BindingValidationOptions{})
	_ = c.capabilities.RegisterManifest(&root.Manifest{PluginID: id})
	c.events.revoke(id)
	var err error
	if c.sup.SocketPathOf(id) != "" {
		err = c.sup.Stop(id)
	}
	if old != nil {
		c.lifecycle(eventbus.EventTypePluginStopped, id, nil)
	}
	return err
}

func (c *Controller) lifecycle(kind, id string, payload any) {
	event, err := eventbus.NewPluginEvent(kind, id, payload)
	if err == nil {
		_ = c.events.bus.Publish(c.ctx, event)
	}
}

func waitReady(ctx context.Context, socket string, m *root.Manifest) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		hs, err := root.HandshakeContext(ctx, client, "http://unix", m.Runtime.HandshakePath, m)
		if err == nil && (hs.Status != "ready" || hs.PluginVersion != m.PluginVersion) {
			err = errors.New("handshake not ready or version mismatch")
		}
		if err == nil {
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+m.Runtime.HealthPath, nil)
			if requestErr != nil {
				return requestErr
			}
			resp, healthErr := client.Do(req)
			err = healthErr
			if resp != nil {
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("health status %d", resp.StatusCode)
				}
			}
			if err == nil {
				return nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), last)
		case <-ticker.C:
		}
	}
}
