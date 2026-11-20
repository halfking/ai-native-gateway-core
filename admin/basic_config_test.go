package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// basic_config_test.go — 基础设置聚合端点（2026-11-20 基础设置轮）契约钉测。

// withBasicConfigRegistry 构造与生产 main.go 同构的注册表（全量 PlatformSpecs）。
// 测试各自替换 settings.Global（仓内既有模式），Cleanup 还原。
func withBasicConfigRegistry(t *testing.T) {
	t.Helper()
	old := settings.Global
	t.Cleanup(func() { settings.Global = old })
	settings.Global = settings.NewRegistry()
	for _, sp := range settings.PlatformSpecs() {
		_ = settings.Global.RegisterSpec(sp)
	}
}

// TestBasicConfigGet_GroupShape 验证：分组端点输出 identity/storage/schedules
// 三组，所有可编辑键都是 platform scope 的已注册键。
func TestBasicConfigGet_GroupShape(t *testing.T) {
	withBasicConfigRegistry(t)
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/basic-config", nil)
	w := httptest.NewRecorder()
	h.basicConfigGet(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", w.Code, w.Body.String())
	}
	var out struct {
		Groups []struct {
			ID    string `json:"id"`
			Items []struct {
				Key     string `json:"key"`
				Group   string `json:"group"`
				Ediable bool   `json:"editable"`
				Value   any    `json:"value"`
				Source  string `json:"source"`
			} `json:"items"`
		} `json:"groups"`
		Infra map[string]any `json:"infra"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	seen := map[string]bool{}
	groupIDs := map[string]bool{}
	for _, g := range out.Groups {
		groupIDs[g.ID] = true
		for _, it := range g.Items {
			seen[it.Key] = true
			if it.Group != g.ID {
				t.Errorf("key %s claims group %q but lives in %q", it.Key, it.Group, g.ID)
			}
			sp := settings.Global.Spec(it.Key)
			if sp == nil {
				t.Errorf("key %s listed in basic-config but not registered", it.Key)
				continue
			}
			if sp.Scope != settings.ScopePlatform {
				t.Errorf("key %s must be platform scope, got %v", it.Key, sp.Scope)
			}
		}
	}
	for _, gid := range []string{"identity", "storage", "schedules"} {
		if !groupIDs[gid] {
			t.Errorf("missing group %q", gid)
		}
	}
	for _, key := range []string{
		settings.GatewayDisplayNameKey,
		settings.SystemSkinKey,
		settings.SystemTimezoneKey,
		"gateway.mode",
		settings.AttachmentPublicBaseURLKey,
		"storage.attachment_dir_override",
		"reports.daily_rollup.hour",
	} {
		if !seen[key] {
			t.Errorf("basic-config missing expected key %q", key)
		}
	}

	// infra 必须有 instance_id 与 reload_hint（只读区语义标记）。
	if out.Infra == nil {
		t.Fatal("missing infra block")
	}
	if _, ok := out.Infra["instance_id"]; !ok {
		t.Error("infra missing instance_id")
	}
	if hint, _ := out.Infra["reload_hint"].(string); !strings.Contains(hint, "restart") {
		t.Errorf("infra reload_hint = %v, want restart-required marker", hint)
	}
}

// TestApplyRuntimeSettingAttachmentBaseURL 附件 URL 键经 applyRuntimeSetting
// 即时发布进程指针（热加载，不依赖消费方 TTL）。
func TestApplyRuntimeSettingAttachmentBaseURL(t *testing.T) {
	withBasicConfigRegistry(t)
	// 非法值（非 JSON）也必须安全：不 panic、指针链回落。
	applyRuntimeSetting(settings.AttachmentPublicBaseURLKey, json.RawMessage(`not-json`))
	// 合法 JSON 字符串值。
	applyRuntimeSetting(settings.AttachmentPublicBaseURLKey, json.RawMessage(`"https://cdn.example.com/attachments"`))
	got := settings.AttachmentPublicBaseURL()
	if got != "https://cdn.example.com/attachments" {
		t.Errorf("after applyRuntimeSetting, base = %q, want cdn host prefix", got)
	}
	// 还原测试态。
	applyRuntimeSetting(settings.AttachmentPublicBaseURLKey, json.RawMessage(`""`))
}
