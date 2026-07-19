package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanPlugins 扫描 pluginsDir 下的 <plugin_id>/plugin-manifest.json，
// 校验后把 pages 写入 registry。P0 不启动进程；握手/进程由 P4 接入。
func ScanPlugins(pluginsDir string, reg *pluginruntime.Registry) error {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read plugins dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(pluginsDir, e.Name(), "plugin-manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}
		m, err := pluginruntime.LoadManifest(manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugin %s manifest invalid: %v\n", e.Name(), err)
			continue
		}
		reg.SetPlugin(&pluginruntime.PluginState{
			PluginID: m.PluginID, PluginVersion: m.PluginVersion, Status: "ready",
		})
		reg.SetNav(m.PluginID, m.PluginVersion, m.Pages)
	}
	return nil
}
