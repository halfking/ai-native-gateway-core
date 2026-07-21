package main

import (
	"log/slog"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// wirePluginPollLoop constructs and starts a Plugin PollLoop if
// LLM_GATEWAY_PLUGIN_POLL_INTERVAL is set to a positive duration. Requires
// the maintain catalog client, the concrete installer, and the supervisor
// (for InstalledVersions). Returns the loop (for defer Stop) or nil if
// disabled. Caller is responsible for calling Stop on shutdown.
func wirePluginPollLoop(client *pluginruntime.MaintainCatalogClient, inst *pluginruntime.Installer, sup *pluginruntime.Supervisor, gatewayVersion string) *pluginruntime.PollLoop {
	raw := os.Getenv("LLM_GATEWAY_PLUGIN_POLL_INTERVAL")
	if raw == "" {
		slog.Info("plugin poll loop disabled (LLM_GATEWAY_PLUGIN_POLL_INTERVAL not set)")
		return nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		slog.Error("plugin poll loop: invalid LLM_GATEWAY_PLUGIN_POLL_INTERVAL, disabling", "raw", raw, "error", err)
		return nil
	}
	if interval <= 0 {
		slog.Info("plugin poll loop disabled (interval <= 0)", "raw", raw)
		return nil
	}
	if client == nil || inst == nil || sup == nil {
		slog.Warn("plugin poll loop: client/installer/supervisor nil, cannot start despite interval set", "client_set", client != nil, "installer_set", inst != nil, "sup_set", sup != nil)
		return nil
	}
	loop := pluginruntime.NewPollLoop(pluginruntime.PollLoopConfig{
		Interval:          interval,
		Client:            client,
		Installer:         inst,
		InstalledVersions: sup.InstalledVersions,
		GatewayVersion:    gatewayVersion,
	})
	loop.Start()
	slog.Info("plugin poll loop started", "interval", interval)
	return loop
}
