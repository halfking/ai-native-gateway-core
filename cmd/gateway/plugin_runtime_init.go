package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanPlugins 扫描 pluginsDir 下的 <plugin_id>/plugin-manifest.json，
// 校验后把 pages 写入 registry，并返回所有已扫描到的 manifest。
// 进程的启动由 ScanAndStartPlugins 在 P4 接入。
func ScanPlugins(pluginsDir string, reg *pluginruntime.Registry) ([]*pluginruntime.Manifest, error) {
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugins dir: %w", err)
	}
	var manifests []*pluginruntime.Manifest
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
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// wirePluginAuthExtractor bridges admin.AuthContext → plugin-runtime AuthExtractor.
// Super = super_admin or admin_key (legacy). PlatformOps = Super AND default tenant.
// TenantPortal = non-default tenant. Called once at startup after admin middleware is ready.
func wirePluginAuthExtractor() {
	pluginruntime.SetAuthExtractor(func(r *http.Request) (pluginruntime.AuthInfo, bool) {
		auth := admin.GetAuthContext(r)
		if auth == nil {
			return pluginruntime.AuthInfo{}, false
		}
		super := auth.Role == "super_admin" || auth.Role == "admin_key"
		return pluginruntime.AuthInfo{
			Super:        super,
			PlatformOps:  super && auth.TenantID == "default",
			TenantPortal: auth.TenantID != "default",
		}, true
	})
}
