package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanAndStartPlugins keeps the legacy call shape for tests and callers that
// only need process startup. Production uses ScanAndStartPluginsWithRegistry.
func ScanAndStartPlugins(sup *pluginruntime.Supervisor, pluginsDir string, manifests []*pluginruntime.Manifest) map[string]string {
	return ScanAndStartPluginsWithRegistry(sup, pluginruntime.NewRegistry(), manifests)
}

// ScanAndStartPluginsWithRegistry starts processes and exposes only plugins that
// pass signature, handshake, health, and binding readiness gates.
func ScanAndStartPluginsWithRegistry(sup *pluginruntime.Supervisor, reg *pluginruntime.Registry, manifests []*pluginruntime.Manifest) map[string]string {
	bases := map[string]string{}
	for _, m := range manifests {
		m2 := *m
		baseDir := filepath.Dir(m2.ManifestPath)
		m2.Runtime.Entrypoint = filepath.Join(baseDir, m.Runtime.Entrypoint)
		st, err := sup.Start(context.Background(), &m2)
		if err != nil {
			slog.Warn("plugin start failed", "plugin", m.PluginID, "error", err)
			reg.SetPluginStatus(m.PluginID, "failed")
			continue
		}
		if err := waitForPluginReady(context.Background(), st.SocketPath, &m2); err != nil {
			slog.Warn("plugin readiness failed", "plugin", m.PluginID, "error", err)
			_ = sup.Stop(m.PluginID)
			reg.SetPluginStatus(m.PluginID, "failed")
			continue
		}
		reg.SetPluginStatus(m.PluginID, "ready")
		slog.Info("plugin ready", "plugin", m.PluginID, "pid", st.Pid, "socket", st.SocketPath)
		bases[m.PluginID] = "unix://" + st.SocketPath
	}
	return bases
}

func waitForPluginReady(ctx context.Context, socketPath string, manifest *pluginruntime.Manifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest required")
	}
	const timeout = 5 * time.Second
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	client := &http.Client{Transport: transport, Timeout: timeout}
	if _, err := pluginruntime.HandshakeContext(deadlineCtx, client, "http://unix", manifest.Runtime.HandshakePath, manifest); err != nil {
		return err
	}
	if err := pluginruntime.PreciseHealthCheck(socketPath, manifest.Runtime.HealthPath, timeout)(); err != nil {
		return err
	}
	return nil
}
