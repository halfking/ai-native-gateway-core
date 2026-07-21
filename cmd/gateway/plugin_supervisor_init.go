package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanAndStartPlugins 启动所有已扫描到的插件进程，并返回 pluginID -> "unix://<socketPath>"
// 的映射，供 plugin api proxy 反向代理使用。manifest 中相对的 entrypoint（如
// bin/ai-session-manager）会被解析为相对 manifest 文件所在目录（由
// pluginruntime.LoadManifest 写入的 ManifestPath 决定）。
//
// 这样两种布局都能正确解析：
//   - 版化安装：<id>/current/bin/<entrypoint>（current 指向 <id>/<version>）
//   - 扁平布局：<id>/bin/<entrypoint>
//
// 单个插件启动失败仅记录告警，不会阻断 gateway 启动（P5 再补充重启/降级处理）。
func ScanAndStartPlugins(sup *pluginruntime.Supervisor, pluginsDir string, manifests []*pluginruntime.Manifest) map[string]string {
	bases := map[string]string{}
	for _, m := range manifests {
		m2 := *m
		// P12: entrypoint 基准 = manifest 所在目录。LoadManifest 把 ManifestPath
		// 设为绝对路径，因此 filepath.Dir 在版化和扁平两种布局下都正确。
		baseDir := filepath.Dir(m2.ManifestPath)
		m2.Runtime.Entrypoint = filepath.Join(baseDir, m.Runtime.Entrypoint)
		st, err := sup.Start(context.Background(), &m2)
		if err != nil {
			slog.Warn("plugin start failed", "plugin", m.PluginID, "error", err)
			continue
		}
		slog.Info("plugin started", "plugin", m.PluginID, "pid", st.Pid, "socket", st.SocketPath)
		bases[m.PluginID] = "unix://" + st.SocketPath
	}
	return bases
}
