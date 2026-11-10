// Package main —— mcp_gateway_test.go
//
// 主网关上的 MCP 网关接线验证：路由确实挂上了，且三道门的语义正确。
package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newMCPTestMux 起一个只挂了 MCP 网关的 mux（不牵动主流程的其它依赖）。
func newMCPTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerMCPGateway(mux, logger, nil)
	return mux
}

func TestRegisterMCPGatewayMountsRoutes(t *testing.T) {
	mux := newMCPTestMux(t)

	// 数据面：无凭证 ⇒ 401（不是 404，说明路由已挂载且鉴权生效）。
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without credential = %d, want 401", rec.Code)
	}

	// 运营看板：可匿名打开（数据要令牌才可见）。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp/admin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mcp/admin = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "MCP 网关运营看板") {
		t.Fatalf("dashboard body unexpected")
	}

	// 控制面：**未配置管理令牌** ⇒ 503（控制面不可用，而不是「无鉴权开放」）。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp/control/tools", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /mcp/control/tools without admin token configured = %d, want 503", rec.Code)
	}

	// 探针端点：公开可读（供 k8s 探针使用）。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp/control/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mcp/control/healthz = %d, want 200", rec.Code)
	}

	// RFC 9728 受保护资源元数据。
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /.well-known/oauth-protected-resource = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"resource\"") {
		t.Fatalf("protected resource metadata unexpected: %s", rec.Body.String())
	}
}

func TestRegisterMCPGatewayDisabledByEnv(t *testing.T) {
	t.Setenv("MCP_GATEWAY_ENABLED", "false")
	mux := http.NewServeMux()
	registerMCPGateway(mux, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// 关闭 ⇒ 路径不挂载 ⇒ 404（语义＝未部署该子系统）。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /mcp when disabled = %d, want 404", rec.Code)
	}
}

func TestRegisterMCPGatewayControlPlaneRejectsWrongToken(t *testing.T) {
	t.Setenv("MCP_GATEWAY_ADMIN_TOKEN", "right-token")
	mux := http.NewServeMux()
	registerMCPGateway(mux, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// 令牌不对 ⇒ 401。
	req := httptest.NewRequest(http.MethodGet, "/mcp/control/tools", nil)
	req.Header.Set("X-Admin-Token", "wrong-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /mcp/control/tools with wrong token = %d, want 401", rec.Code)
	}

	// 令牌正确 ⇒ 200。
	req = httptest.NewRequest(http.MethodGet, "/mcp/control/tools", nil)
	req.Header.Set("X-Admin-Token", "right-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /mcp/control/tools with right token = %d, want 200", rec.Code)
	}
}
