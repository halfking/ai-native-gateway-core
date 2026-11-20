package settings

import (
	"encoding/json"
	"testing"
)

// system_config_test.go — 基础设置轮（2026-11-20）三键的注册面契约钉测。
//
// ★ 前车之鉴（AGENTS.md §143.44 同族）：lifecycle.session_turns_probe_hot_ttl_days
// 曾经「只存在于读方的 fallback、从未 Register 成 spec」⇒ 写 settings_kv 完全无效。
// ⇒ 凡新增系统级键，必须钉测它在 PlatformRegistry 里——否则写通道是空文。

func findSystemConfigSpec(t *testing.T, key string) *Spec {
	t.Helper()
	for _, sp := range SystemConfigSpecs() {
		if sp.Key == key {
			return sp
		}
	}
	t.Fatalf("SystemConfigSpecs() missing key %q", key)
	return nil
}

func TestSystemConfigSpecs_RegisteredInPlatformSpecs(t *testing.T) {
	want := map[string]bool{
		GatewayDisplayNameKey:      false,
		SystemSkinKey:              false,
		AttachmentPublicBaseURLKey: false,
	}
	for _, sp := range PlatformSpecs() {
		if _, ok := want[sp.Key]; ok {
			want[sp.Key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("key %q registered by SystemConfigSpecs but absent from PlatformSpecs()", key)
		}
	}
}

func TestSystemSkinSpec_Contract(t *testing.T) {
	sp := findSystemConfigSpec(t, SystemSkinKey)
	if sp.Type != TypeEnum {
		t.Errorf("system.skin type = %v, want enum", sp.Type)
	}
	if len(sp.Options) != 3 {
		t.Fatalf("system.skin options = %v, want [auto light dark]", sp.Options)
	}
	for _, opt := range []string{"auto", "light", "dark"} {
		if err := sp.Validate(opt); err != nil {
			t.Errorf("system.skin.Validate(%q) = %v, want nil", opt, err)
		}
	}
	if err := sp.Validate("neon"); err == nil {
		t.Error("system.skin.Validate(\"neon\") = nil, want error")
	}
	if !sp.HotReload {
		t.Error("system.skin must be HotReload")
	}
	if sp.DangerLevel != Safe {
		t.Errorf("system.skin danger = %v, want Safe (纯展示层)", sp.DangerLevel)
	}
}

func TestGatewayDisplayNameSpec_Contract(t *testing.T) {
	sp := findSystemConfigSpec(t, GatewayDisplayNameKey)
	if sp.Type != TypeString {
		t.Errorf("gateway.display_name type = %v, want string", sp.Type)
	}
	if err := sp.Validate("问学灵果网关"); err != nil {
		t.Errorf("display name Validate = %v, want nil", err)
	}
	if !sp.HotReload || sp.DangerLevel != Safe {
		t.Errorf("display name must be HotReload+Safe, got hot=%v danger=%v", sp.HotReload, sp.DangerLevel)
	}
}

func TestAttachmentPublicBaseURLSpec_Contract(t *testing.T) {
	sp := findSystemConfigSpec(t, AttachmentPublicBaseURLKey)
	if sp.Type != TypeString {
		t.Errorf("attachment base url type = %v, want string", sp.Type)
	}
	if sp.DangerLevel == Safe {
		t.Errorf("attachment base url should be at least Warning (影响出站 URL 形状), got %v", sp.DangerLevel)
	}
}

func TestAttachmentPublicBaseURL_ResolutionChain(t *testing.T) {
	// 无指针、无 env → 默认。
	if got := AttachmentPublicBaseURL(); got != DefaultAttachmentPublicBaseURL {
		t.Errorf("default resolution = %q, want %q", got, DefaultAttachmentPublicBaseURL)
	}

	// env → env 值（无尾斜杠）。
	t.Setenv("LLM_GATEWAY_ATTACHMENT_PUBLIC_URL", "https://files.example.com/attachments/")
	if got := AttachmentPublicBaseURL(); got != "https://files.example.com/attachments" {
		t.Errorf("env resolution = %q, want trimmed host prefix", got)
	}

	// 设置值 → 覆盖 env（Publish 之后指针优先）。
	t.Setenv("LLM_GATEWAY_ATTACHMENT_PUBLIC_URL", "https://env-should-lose.example.com")
	PublishAttachmentPublicBaseURL("https://db-should-win.example.com/prefix")
	if got := AttachmentPublicBaseURL(); got != "https://db-should-win.example.com/prefix" {
		t.Errorf("pointer resolution = %q, want published value", got)
	}

	// 显式空串 = 取消发布 → 落回 env 链。
	PublishAttachmentPublicBaseURL("  ")
	if got := AttachmentPublicBaseURL(); got != "https://env-should-lose.example.com" {
		t.Errorf("after explicit-empty publish, expect env chain, got %q", got)
	}

	// 指针恢复空（还原测试态）。
	attachmentURLBase.Store(nil)
}

func TestSystemSkin_ValueValidation(t *testing.T) {
	// SystemSkin() 的白名单回退：非法值回 auto。
	if got := SystemSkin(); got != DefaultSystemSkin && got != "auto" && got != "light" && got != "dark" {
		t.Errorf("SystemSkin returned out-of-whitelist value %q", got)
	}
}

// SystemConfigSpecs 三键的 Default 都必须能通过自己的 Validate——
// 「默认值本身非法」是最难看的配置事故。
func TestSystemConfigSpecs_DefaultsValidate(t *testing.T) {
	for _, sp := range SystemConfigSpecs() {
		if sp.Default == nil {
			continue // display_name 默认空串是合法的「未设置」
		}
		raw, err := json.Marshal(sp.Default)
		if err != nil {
			t.Errorf("%s: default not JSON-marshalable: %v", sp.Key, err)
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Errorf("%s: default not JSON-roundtrippable: %v", sp.Key, err)
			continue
		}
		// TypeEnum 的 Default 必须在 Options 里；其余类型走 Validate。
		if sp.Type == TypeEnum {
			valid := false
			for _, opt := range sp.Options {
				if opt == v.(string) {
					valid = true
					break
				}
			}
			if !valid {
				t.Errorf("%s: default %v not in options %v", sp.Key, v, sp.Options)
			}
			continue
		}
		if err := sp.Validate(v); err != nil {
			t.Errorf("%s: default %v fails own Validate: %v", sp.Key, v, err)
		}
	}
}
