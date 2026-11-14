package main

import (
	"os"
	"strings"
	"testing"
)

// D-S4/D-S1 接线守卫（2026-11-14 审计轮）：
//
// ① dedicated 单段路由（POST /api/v1/plugins/{id}/activate|deactivate）按
//    ServeMux 优先级恒胜出 servePluginAPI 的 catch-all 分发。若 dedicated
//    路由改回 lifecycle 直调（Restart/Stop，不更新 disabled 覆盖层），
//    pluginapi 的编排与状态模型只在小节多段畸形 URL 可达、GET 清单恒报
//    Enabled=true、且 HealthLoop 会复活手动停用的插件——三笔 pluginapi
//    交付整体失效。故用源码钉把 dedicated 路由的委派对象钉死。
// ② HealthLoop 的 ShouldCheck 必须接到 pluginDisabledGate（→ IsDisabled）：
//    管理性停用必须豁免健康检查，否则空 socket 连续失败触发 Restarter 复活。
//
// 变异臂：把 dedicated 路由的 handler 换回 makePluginActivateHandler ⇒ 红。
func TestPluginLifecycleRoutesDelegateToPluginAPIHandler(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	for route, handler := range map[string]string{
		"activate":   "pluginapiHandler.ActivatePluginHandler",
		"deactivate": "pluginapiHandler.DeactivatePluginHandler",
	} {
		marker := `POST /api/v1/plugins/{id}/` + route
		idx := strings.Index(text, marker)
		if idx < 0 {
			t.Fatalf("main.go: dedicated %s route registration missing (refactored? re-evaluate guard)", route)
		}
		block := text[idx : idx+400]
		if !strings.Contains(block, handler) {
			t.Fatalf("D-S4 regression: dedicated %s route does not delegate to %s — lifecycle直调会让 disabled 覆盖层在规范 URL 上失效且 HealthLoop 复活手动停用（见审计 D-S1/D-S4）\nblock: %s", route, handler, block)
		}
		if strings.Contains(block, "makePluginActivateHandler") || strings.Contains(block, "makePluginDeactivateHandler") {
			t.Fatalf("D-S4 regression: dedicated %s route wired to lifecycle wrapper again", route)
		}
	}
}

func TestPluginHealthLoopRespectsAdminDisabledGate(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "pluginDisabledGate = pluginapiHandler.IsDisabled") {
		t.Fatal("D-S1 regression: pluginDisabledGate is not assigned from pluginapiHandler.IsDisabled — HealthLoop will revive admin-deactivated plugins")
	}
	if !strings.Contains(text, "ShouldCheck:      func(pluginID string) bool { return !pluginDisabledGate(pluginID) }") {
		t.Fatal("D-S1 regression: HealthLoop ShouldCheck is not wired to pluginDisabledGate")
	}
}
