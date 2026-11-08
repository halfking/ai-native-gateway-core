// Package main —— mcp_gateway.go
//
// 把 llm-gateway-go 内置的 MCP 服务网关挂到主网关上。
//
// 挂载点：
//
//	POST /mcp                     MCP 数据面（JSON-RPC over HTTP）
//	GET  /mcp/admin               运营看板（自包含 HTML）
//	/mcp/control/*                控制面（适配器/工具/智能体/技能/凭证/审批/审计）
//	GET  /.well-known/oauth-protected-resource   RFC 9728 受保护资源元数据
//
// 鉴权分层（与既有子系统的做法一致）：
//   - 全局 API key 门（middleware/auth_mw.go）把 /mcp 前缀列入 bypass：
//     MCP 客户端用自己的凭证（网关注发或外部账本签发），不再叠加网关主 key。
//   - 数据面自己校验 MCP 凭证；控制面自己校验管理令牌；两者互不复用。
//
// 开关：MCP_GATEWAY_ENABLED（默认 true）。关闭时整块不挂载 ⇒ 路径落 404，
// 语义与「未部署该子系统」一致。
package main

import (
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/mcp"
)

// registerMCPGateway 在主 mux 上挂载 MCP 网关。
//
// 失败时只记日志、不阻断主进程启动：MCP 网关是附加能力，不能因为它的
// 装配问题把模型网关拖下线。
func registerMCPGateway(mux *http.ServeMux, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	envCfg, err := mcp.LoadEnvConfig()
	if err != nil {
		logger.Error("mcp gateway: config invalid, gateway not mounted", "error", err)
		return
	}
	if !envCfg.Enabled {
		logger.Info("mcp gateway: disabled by MCP_GATEWAY_ENABLED=false")
		return
	}

	gateway, err := envCfg.Build()
	if err != nil {
		logger.Error("mcp gateway: init failed, gateway not mounted", "error", err)
		return
	}

	mount := mcp.NewMount(gateway)
	handler := mount.Handler()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.Handle("/.well-known/oauth-protected-resource", handler)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", handler)

	logger.Info("mcp gateway mounted",
		"data_plane", "/mcp",
		"control_plane", "/mcp/control",
		"dashboard", "/mcp/admin",
		"version", mcp.Version,
		"admin_token_configured", envCfg.AdminToken != "",
		"store_file", envCfg.StoreFile,
		"audit_file", envCfg.AuditFile,
		"external_ledger", envCfg.LedgerURL != "",
	)
}
