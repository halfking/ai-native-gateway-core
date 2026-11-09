// Command mcp-gateway 是 llm-gateway-go 内置 MCP 网关的独立运行入口。
//
// 用途：
//   - 企业**边缘/独立部署**：只跑工具面网关，不牵动模型网关与数据库；
//   - **联调与演示**：--demo 会起一个内置的示例上游 MCP 服务并自动注册，
//     便于本地走通「注册 → 签发凭证 → 调用 → 审计」闭环；
//   - **Maestro 验证**：--demo 提供确定性的数据面，便于浏览器 flow 断言。
//
// 环境变量见 mcp/env.go（Env* 常量）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/kaixuan/llm-gateway-go/mcp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	var (
		listen     = flag.String("listen", envOr("MCP_GATEWAY_LISTEN", ":8089"), "监听地址")
		adminToken = flag.String("admin-token", os.Getenv(mcp.EnvAdminToken), "控制面管理令牌（默认取环境变量）")
		demo       = flag.Bool("demo", false, "启动内置示例上游并预置数据（演示/验证用）")
		demoAddr   = flag.String("demo-upstream", "127.0.0.1:0", "示例上游监听地址")
	)
	flag.Parse()

	// rule 71：结构化 JSONL + 规范字段。此前用 Text handler，
	// 与同仓其他入口（cmd/gateway 用 JSON）格式不一致，
	// AI 消费同一服务需要两套解析分支。
	logger := logging.NewServiceLogger(slog.LevelInfo)

	envCfg, err := mcp.LoadEnvConfig()
	if err != nil {
		logger.Error("mcp gateway config invalid", "error", err)
		os.Exit(1)
	}
	if *adminToken != "" {
		envCfg.AdminToken = *adminToken
	}

	gateway, err := envCfg.Build()
	if err != nil {
		logger.Error("mcp gateway init failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = gateway.Audit().Close() }()

	if *demo {
		if err := seedDemo(gateway, *demoAddr, logger); err != nil {
			logger.Error("demo seed failed", "error", err)
			os.Exit(1)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(gateway.HandleMCP))
	mux.Handle("/mcp/", http.HandlerFunc(gateway.HandleMCP))
	mux.Handle("/mcp/control/", http.StripPrefix("/mcp/control", mcp.NewControlPlane(gateway).Handler()))
	mux.HandleFunc("GET /mcp/admin", func(w http.ResponseWriter, r *http.Request) {
		mux2 := mcp.NewMount(gateway)
		mux2.Handler().ServeHTTP(w, r)
	})
	// ★ 抓取前先刷新「抓取时刻求值」的指标（mcp_gateway_uptime_seconds）：
	//   Prometheus 的抓取是外部触发的，刷新必须挂在抓取路径上；
	//   不挂 ⇒ 该指标在 /metrics 上恒为 ABSENT（不是 0），面板无数据、
	//   rate() 告警永不触发。
	mux.Handle("/metrics", gateway.ScrapeHandler(promhttp.Handler()))
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		mcp.NewMount(gateway).Handler().ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("mcp gateway listening",
			"addr", *listen,
			// rule 71：version 是保留名（服务版本），mcp 协议版本改用 mcp_version。
			"mcp_version", mcp.Version,
			"demo", *demo,
			"admin_token_configured", envCfg.AdminToken != "",
			"store_file", envCfg.StoreFile,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("mcp gateway server stopped", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("mcp gateway shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// listenLoopback 在回环地址上随机端口监听。
func listenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// seedDemo 起一个内置示例上游 MCP 服务，注册为适配器并签发一把演示凭证。
//
// 返回的凭证明文写到 stdout（演示用途；生产不会走这条路径）。
func seedDemo(gateway *mcp.Gateway, addr string, logger *slog.Logger) error {
	upstream := startDemoUpstream(logger)
	adapter := mcp.Adapter{
		Name:            "demo",
		DisplayName:     "内置示例 MCP 服务",
		Description:     "用于联调与 Maestro 验证的示例上游",
		Endpoint:        "http://" + upstream.addr,
		Enabled:         true,
		Capability:      "memory",
		Sensitivity:     mcp.SensitivityRead,
		ProtocolVersion: mcp.ProtocolVersion,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := gateway.Store().PutAdapter(context.Background(), adapter); err != nil {
		return err
	}
	secret, err := seedDemoKey(gateway)
	if err != nil {
		return err
	}
	fmt.Printf("DEMO_API_KEY=%s\n", secret)
	logger.Info("demo seeded", "adapter", adapter.Name, "upstream", adapter.Endpoint)
	return nil
}

func seedDemoKey(gateway *mcp.Gateway) (string, error) {
	_, secret, err := gateway.IssueKey(context.Background(), mcp.KeySpec{
		TenantID:        "default",
		Name:            "demo-agent",
		UserID:          "demo-user",
		Scopes:          []string{mcp.ScopeRead, mcp.ScopeExecute},
		AllowedAdapters: []string{"demo"},
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// startDemoUpstream 起一个最小 MCP 上游（JSON-RPC over HTTP）。
func startDemoUpstream(logger *slog.Logger) *demoUpstream {
	up := &demoUpstream{logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", up.handle)
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := listenLoopback()
	if err != nil {
		logger.Error("demo upstream listen failed", "error", err)
		return up
	}
	up.srv = srv
	up.addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	return up
}

type demoUpstream struct {
	logger *slog.Logger
	srv    *http.Server
	addr   string
}

func (d *demoUpstream) handle(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	method, _ := req["method"].(string)
	resp := map[string]any{"jsonrpc": "2.0", "id": req["id"]}
	switch method {
	case "tools/list":
		resp["result"] = map[string]any{"tools": []any{
			map[string]any{
				"name":        "search_memory",
				"description": "在内置示例库里检索记忆",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"q": map[string]any{"type": "string"}},
					"required":   []any{"q"},
				},
			},
			map[string]any{
				"name":        "add_memory",
				"description": "向内置示例库写入一条记忆",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []any{"text"},
				},
			},
		}}
	case "tools/call":
		name, _ := req["params"].(map[string]any)["name"].(string)
		resp["result"] = map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "demo upstream handled " + name}},
			"isError": false,
			// ★ 回显本上游**实际收到**的能力路由头：让「头格式是否逐字一致」
			//   成为**运行时可观测**的证据，而不只靠读代码断言。
			//   - capability：请求级 ?capability= 是否覆盖了适配器默认能力域；
			//   - capabilityTools：X-MCP-Capability-Tools 的**原样字符串**
			//     （既有实现是 JSON.stringify([...]) ⇒ 必须是 JSON 数组字符串）。
			"capability":      r.Header.Get("X-MCP-Capability"),
			"capabilityTools": r.Header.Get("X-MCP-Capability-Tools"),
			"protocolVersion": r.Header.Get("MCP-Protocol-Version"),
		}
	default:
		resp["result"] = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
