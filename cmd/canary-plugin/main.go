// canary-plugin 是灰度验证用的最小真实插件进程（http-unix-socket 合同）。
// 手握手、健康检查两个端点；顺带做一次 8MB 分配让 memory.current 有非零读数
// （cgroup 读回/导出链路的冒烟数据点）。
//
// 环境契约（supervisor.Start 注入）：
//   AI_SESSION_MANAGER_PLUGIN_SOCKET  unix socket 路径
//   AI_SESSION_MANAGER_MANIFEST       manifest 路径
package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
)

func main() {
	socket := os.Getenv("AI_SESSION_MANAGER_PLUGIN_SOCKET")
	if socket == "" {
		os.Exit(2)
	}
	pluginID := "canary"
	if m := os.Getenv("CANARY_PLUGIN_ID"); m != "" {
		pluginID = m
	}
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(3)
	}
	// 8MB 触碰一次（每页写一字节）——RSS 有真实读数，memory.current 非零。
	buf := make([]byte, 8<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/plugin/handshake", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"plugin_id": pluginID, "plugin_version": "0.1.0",
			"api_contract": "gateway-plugin-v1", "status": "ready",
		})
	})
	mux.HandleFunc("/plugin/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := http.Serve(listener, mux); err != nil {
		os.Exit(4)
	}
}
