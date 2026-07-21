package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanPlugins 扫描 pluginsDir 下的插件 manifest，校验后把 pages 写入 registry，
// 并返回所有已扫描到的 manifest。进程的启动由 ScanAndStartPlugins 在 P4 接入。
//
// 支持两种布局（P12）：
//  1. 版化安装（Installer 产物）：<id>/current/plugin-manifest.json，其中 current
//     是指向 <id>/<version> 的符号链接。
//  2. 旧版扁平布局（P0–P8 手动安装）：<id>/plugin-manifest.json。
//
// 优先读取 current/ 版本；若不存在则回退扁平布局，保证已有部署不会被破坏。
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
		pluginDir := filepath.Join(pluginsDir, e.Name())
		// P12: 优先 <id>/current/plugin-manifest.json（版化安装），
		// 回退 <id>/plugin-manifest.json（P0–P8 扁平布局）。
		manifestPath := filepath.Join(pluginDir, "current", "plugin-manifest.json")
		if _, err := os.Stat(manifestPath); err != nil {
			manifestPath = filepath.Join(pluginDir, "plugin-manifest.json")
			if _, err := os.Stat(manifestPath); err != nil {
				continue
			}
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
