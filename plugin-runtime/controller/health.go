package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

const healthProbeTimeout = 500 * time.Millisecond

// probeHealth runs only inside the serialized apply/reconcile transaction. Two
// consecutive failures withdraw readiness and let the reconciler use the same
// bounded recovery and handshake path as a process crash. Requests only read
// this readiness state; they never perform a health probe themselves.
func (c *Controller) probeHealth(ctx context.Context) error {
	for _, state := range c.sup.ProcessSnapshot() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if state.Status == "crashed" || state.Status == "stopped" || c.desired[state.PluginID] == nil || c.disabled[state.PluginID] {
			continue
		}
		c.mu.RLock()
		m := c.actual[state.PluginID]
		c.mu.RUnlock()
		if m == nil {
			continue
		}
		err := probeHTTPHealth(ctx, state.SocketPath, m.Runtime.HealthPath)
		// Caller cancellation is not evidence that the plugin is unhealthy.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			delete(c.healthFailures, state.PluginID)
			c.mu.Lock()
			delete(c.unhealthy, state.PluginID)
			c.mu.Unlock()
			c.registry.SetPluginStatus(state.PluginID, "ready")
			continue
		}
		c.healthFailures[state.PluginID]++
		if c.healthFailures[state.PluginID] >= 2 {
			c.mu.Lock()
			c.unhealthy[state.PluginID] = true
			c.mu.Unlock()
			c.registry.SetPluginStatus(state.PluginID, "degraded")
		}
	}
	return nil
}

func probeHTTPHealth(ctx context.Context, socket, path string) error {
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("plugin health status %d", response.StatusCode)
	}
	return nil
}
