package admin

// topology_plugins_test.go — 实时拓扑「在用插件」端点判据。
//
// 三条契约各有判据（变异验证过）：
//  1. 装配后 builtin/external 如实投影（含 failed/degraded —— 运维最需要
//     看到的恰恰是非 ready 状态，绝不能像 NavEntries 那样过滤掉）；
//  2. 未装配（SetTopologyPlugins 没被调用，如精简启动/单测）必须是
//     200 + degraded 信封，**不是**「没有插件」的空清单 —— 空清单会让
//     用户以为治理层是空的；
//  3. 非 GET 拒绝。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeTopologyPlugins struct {
	builtin  []TopologyBuiltinPlugin
	external []TopologyExternalPlugin
}

func (f fakeTopologyPlugins) BuiltinPlugins() []TopologyBuiltinPlugin   { return f.builtin }
func (f fakeTopologyPlugins) ExternalPlugins() []TopologyExternalPlugin { return f.external }

func callTopologyPlugins(t *testing.T, h *Handler, method string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, "/api/admin/topology/plugins", nil)
	w := httptest.NewRecorder()
	h.handleTopologyPlugins(w, req)
	body := w.Body.Bytes()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out), "body=%s", body)
	return out
}

func TestTopologyPluginsProjectsWithFailedExternal(t *testing.T) {
	h := &Handler{}
	h.SetTopologyPlugins(fakeTopologyPlugins{
		builtin: []TopologyBuiltinPlugin{
			{Name: "prompt_injection", Direction: "input"},
			{Name: "sensitive_word_output", Direction: "output"},
		},
		external: []TopologyExternalPlugin{
			{PluginID: "ai-session-manager", PluginVersion: "1.2.0", Status: "ready"},
			{PluginID: "broken-one", PluginVersion: "0.3.0", Status: "failed"},
		},
	})

	out := callTopologyPlugins(t, h, http.MethodGet)
	require.Equal(t, false, out["degraded"])

	builtin := out["builtin"].([]any)
	require.Len(t, builtin, 2)
	require.Equal(t, "prompt_injection", builtin[0].(map[string]any)["name"])
	require.Equal(t, "input", builtin[0].(map[string]any)["direction"])

	external := out["external"].([]any)
	require.Len(t, external, 2)
	statuses := map[string]string{}
	for _, row := range external {
		m := row.(map[string]any)
		statuses[m["plugin_id"].(string)] = m["status"].(string)
	}
	// failed 必须保留：NavEntries 会藏掉非 ready，运维视图不能。
	require.Equal(t, "ready", statuses["ai-session-manager"])
	require.Equal(t, "failed", statuses["broken-one"])
}

func TestTopologyPluginsUnwiredIsDegradedNotEmpty(t *testing.T) {
	h := &Handler{} // SetTopologyPlugins 未调用

	out := callTopologyPlugins(t, h, http.MethodGet)
	require.Equal(t, true, out["degraded"])
	require.Equal(t, "TOPOLOGY_PLUGINS_UNAVAILABLE", out["error_code"])
	// degraded 信封仍带空数组（形状稳定），前端按 degraded 分支渲染。
	require.NotNil(t, out["builtin"])
	require.NotNil(t, out["external"])
}

func TestTopologyPluginsRejectsNonGet(t *testing.T) {
	h := &Handler{}
	h.SetTopologyPlugins(fakeTopologyPlugins{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/topology/plugins", nil)
	w := httptest.NewRecorder()
	h.handleTopologyPlugins(w, req)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
