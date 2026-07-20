package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

// ScanAndStartPlugins 启动所有已扫描到的插件进程。
// manifest 中相对的 entrypoint（如 bin/ai-session-manager）会被解析为
// <pluginsDir>/<pluginId>/<entrypoint>。单个插件启动失败仅记录告警，
// 不会阻断 gateway 启动（P5 再补充重启/降级处理）。
func ScanAndStartPlugins(sup *pluginruntime.Supervisor, pluginsDir string, manifests []*pluginruntime.Manifest) {
	for _, m := range manifests {
		m2 := *m
		m2.Runtime.Entrypoint = filepath.Join(pluginsDir, m.PluginID, m.Runtime.Entrypoint)
		st, err := sup.Start(context.Background(), &m2)
		if err != nil {
			slog.Warn("plugin start failed", "plugin", m.PluginID, "error", err)
			continue
		}
		slog.Info("plugin started", "plugin", m.PluginID, "pid", st.Pid, "socket", st.SocketPath)
	}
}
