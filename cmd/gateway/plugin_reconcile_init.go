package main

import (
	"log/slog"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// reconcilePluginsFromYML applies plugins.yml desired state through Reconciler.
// When plugins.yml is absent, callers keep the legacy ScanAndStartPlugins path
// (all discovered manifests started). When present, Reconcile is the SSOT:
// reverse Dispose on off, pending on unmet Requires, failed isolation.
func reconcilePluginsFromYML(
	sup *pluginruntime.Supervisor,
	reg *pluginruntime.Registry,
	manifests []*pluginruntime.Manifest,
	pluginsDir string,
) *pluginruntime.Reconciler {
	ymlPath := pluginruntime.DefaultPluginsYMLPath(pluginsDir)
	// Also accept plugins.yml inside the plugins dir (operator convenience).
	alt := filepath.Join(pluginsDir, "plugins.yml")
	f, there, err := pluginruntime.LoadPluginsFile(ymlPath)
	if err != nil {
		slog.Warn("plugins.yml load failed; falling back to legacy start-all", "path", ymlPath, "error", err)
		return nil
	}
	if !there {
		f2, there2, err2 := pluginruntime.LoadPluginsFile(alt)
		if err2 != nil {
			slog.Warn("plugins.yml load failed; falling back to legacy start-all", "path", alt, "error", err2)
			return nil
		}
		if there2 {
			f, there, ymlPath = f2, true, alt
		}
	}
	if !there {
		return nil
	}
	driver := pluginruntime.SupervisorDriver{
		Sup:   sup,
		Ready: waitForPluginReady,
	}
	rt := pluginruntime.NewReconciler(manifests, driver)
	cs := pluginruntime.ComposeDesired(manifests, f)
	on, cfg := pluginruntime.DesiredMaps(cs)
	rt.Reconcile(on, cfg)
	for _, st := range rt.Statuses() {
		switch st.State {
		case pluginruntime.LifeActive:
			reg.SetPluginStatus(st.ID, "ready")
		case pluginruntime.LifeFailed:
			reg.SetPluginStatus(st.ID, "failed")
		case pluginruntime.LifePending:
			reg.SetPluginStatus(st.ID, "pending")
		case pluginruntime.LifeOff, pluginruntime.LifeDisposed:
			reg.SetPluginStatus(st.ID, "stopped")
		}
		if st.Why != "" {
			slog.Info("plugin lifecycle", "plugin", st.ID, "state", st.State, "why", st.Why)
		}
	}
	slog.Info("plugins reconciled from plugins.yml", "path", ymlPath, "count", len(cs))
	return rt
}
