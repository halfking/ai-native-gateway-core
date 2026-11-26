// Package main initialization - legacy auth registry shell.
//
// ★ 2026-11-27 L135 轮（断点③修复）：本文件此前是一条**独立的** registry
//
//	装配链（只读 env、只注册 auth/user/tenant 三接口、无 ApprovalProvider、
//	casdoor 配置单独走 loadCasdoorConfig）⇒ 与 plugin_init.go 的
//	initializePluginRegistry 形成两条平行链：adminHandler 挂旧链实例，
//	plugin-config API 挂新链实例 —— PUT 切换的 provider 对登录面永远不可见。
//
//	现在两者合并：initAuthRegistry 只是 initializePluginRegistry 的薄壳，
//	jwtSecret 参数作为 native 签名链的回退键传入（链：
//	LLM_GATEWAY_JWT_SECRET → jwtSecretFallback），与 admin 面同一把签名密钥。
//	main.go 的调用点（adminHandler 装配处）已改为直接调
//	initializePluginRegistry —— 本壳仅为保留函数名与既有测试兼容。
package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// initAuthRegistry creates the authentication plugin registry via the
// unified assembly point (initializePluginRegistry). Kept as a thin shell
// for naming compatibility; production wiring goes through
// initializePluginRegistry directly.
func initAuthRegistry(db *pgxpool.Pool, jwtSecret string) (*authplugin.Registry, error) {
	return initializePluginRegistry(db, nil, jwtSecret)
}
