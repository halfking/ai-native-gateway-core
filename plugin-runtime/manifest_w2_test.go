package pluginruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestSchemaVersion3 验证 schema_version 3 支持
func TestManifestSchemaVersion3(t *testing.T) {
	m := Manifest{
		SchemaVersion: 3,
		PluginID:      "test-plugin",
		PluginVersion: "1.0.0",
		GatewayCompatibility: GatewayCompatibility{
			APIContract: SupportedAPIContractV2,
		},
		Runtime: Runtime{
			Entrypoint:        "bin/plugin",
			Protocol:          "http-unix-socket",
			HealthPath:        "/health",
			HandshakePath:     "/handshake",
			ShutdownGraceSecs: 10,
		},
		API:       "^1.0",
		Requires:  []string{"postgresql", "redis"},
		Provides:  []string{"analytics.session"},
		Essential: true,
	}

	if err := m.validate(); err != nil {
		t.Fatalf("schema_version 3 validation failed: %v", err)
	}
}

// TestManifestSchemaVersion3WithSettingsSchema 验证 SettingsSchema 支持
func TestManifestSchemaVersion3WithSettingsSchema(t *testing.T) {
	m := Manifest{
		SchemaVersion: 3,
		PluginID:      "test-plugin",
		PluginVersion: "1.0.0",
		GatewayCompatibility: GatewayCompatibility{
			APIContract: SupportedAPIContractV2,
		},
		Runtime: Runtime{
			Entrypoint:        "bin/plugin",
			Protocol:          "http-unix-socket",
			HealthPath:        "/health",
			HandshakePath:     "/handshake",
			ShutdownGraceSecs: 10,
		},
		SettingsSchema: &SettingsSchema{
			Type: "object",
			Properties: map[string]*SettingsProperty{
				"enable_cache": {
					Type:    "boolean",
					Title:   "Enable Cache",
					Default: true,
				},
				"max_items": {
					Type:    "integer",
					Title:   "Max Items",
					Minimum: ptrFloat64(1),
					Maximum: ptrFloat64(1000),
					Default: int64(100),
				},
			},
			Order: []string{"enable_cache", "max_items"},
		},
	}

	if err := m.validate(); err != nil {
		t.Fatalf("SettingsSchema validation failed: %v", err)
	}

	// 验证 EffectiveSettingsSchema
	effective := m.EffectiveSettingsSchema()
	if effective == nil {
		t.Fatal("EffectiveSettingsSchema returned nil")
	}
	if len(effective.Properties) != 2 {
		t.Fatalf("expected 2 properties, got %d", len(effective.Properties))
	}
}

// TestManifestBackwardCompatibility 验证向后兼容性
func TestManifestBackwardCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		schemaVersion int
		apiContract   string
		shouldPass    bool
	}{
		{"v0 with v1 contract", 0, SupportedAPIContract, true},
		{"v1 with v1 contract", 1, SupportedAPIContract, true},
		{"v2 with v2 contract", 2, SupportedAPIContractV2, true},
		{"v3 with v2 contract", 3, SupportedAPIContractV2, true},
		{"v2 with v1 contract", 2, SupportedAPIContract, false},
		{"v3 with v1 contract", 3, SupportedAPIContract, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{
				SchemaVersion: tt.schemaVersion,
				PluginID:      "test-plugin",
				PluginVersion: "1.0.0",
				GatewayCompatibility: GatewayCompatibility{
					APIContract: tt.apiContract,
				},
				Runtime: Runtime{
					Entrypoint:        "bin/plugin",
					Protocol:          "http-unix-socket",
					HealthPath:        "/health",
					HandshakePath:     "/handshake",
					ShutdownGraceSecs: 10,
				},
			}

			err := m.validate()
			if tt.shouldPass && err != nil {
				t.Errorf("expected pass but got error: %v", err)
			}
			if !tt.shouldPass && err == nil {
				t.Errorf("expected error but validation passed")
			}
		})
	}
}

// TestManifestRequiresProvides 验证 Requires/Provides 服务依赖
func TestManifestRequiresProvides(t *testing.T) {
	tests := []struct {
		name       string
		requires   []string
		provides   []string
		shouldPass bool
	}{
		{"valid services", []string{"postgresql", "redis"}, []string{"analytics.session"}, true},
		{"empty requires", []string{}, []string{"analytics.session"}, true},
		{"empty provides", []string{"postgresql"}, []string{}, true},
		{"invalid service name", []string{"PostgreSQL"}, []string{}, false}, // 大写不允许
		{"duplicate requires", []string{"redis", "redis"}, []string{}, false},
		{"duplicate provides", []string{}, []string{"analytics", "analytics"}, false},
		{"service name too long", []string{"a123456789012345678901234567890123456789012345678901234567890123456789"}, []string{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{
				SchemaVersion: 3,
				PluginID:      "test-plugin",
				PluginVersion: "1.0.0",
				GatewayCompatibility: GatewayCompatibility{
					APIContract: SupportedAPIContractV2,
				},
				Runtime: Runtime{
					Entrypoint:        "bin/plugin",
					Protocol:          "http-unix-socket",
					HealthPath:        "/health",
					HandshakePath:     "/handshake",
					ShutdownGraceSecs: 10,
				},
				Requires: tt.requires,
				Provides: tt.provides,
			}

			err := m.validate()
			if tt.shouldPass && err != nil {
				t.Errorf("expected pass but got error: %v", err)
			}
			if !tt.shouldPass && err == nil {
				t.Errorf("expected error but validation passed")
			}
		})
	}
}

// TestManifestAPI 验证 API 字段（caret range）
func TestManifestAPI(t *testing.T) {
	tests := []struct {
		name       string
		api        string
		shouldPass bool
	}{
		{"empty API defaults to ^1", "", true},
		{"^1 format", "^1", true},
		{"^1.0 format", "^1.0", true},
		{"^1.2.3 format", "^1.2.3", true},
		{"^2 format", "^2", true},
		{"invalid without caret", "1.0", false},
		{"invalid non-numeric", "^abc", false},
		{"spaces trimmed", " ^1 ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{
				SchemaVersion: 3,
				PluginID:      "test-plugin",
				PluginVersion: "1.0.0",
				GatewayCompatibility: GatewayCompatibility{
					APIContract: SupportedAPIContractV2,
				},
				Runtime: Runtime{
					Entrypoint:        "bin/plugin",
					Protocol:          "http-unix-socket",
					HealthPath:        "/health",
					HandshakePath:     "/handshake",
					ShutdownGraceSecs: 10,
				},
				API: tt.api,
			}

			err := m.validate()
			if tt.shouldPass && err != nil {
				t.Errorf("expected pass but got error: %v", err)
			}
			if !tt.shouldPass && err == nil {
				t.Errorf("expected error but validation passed")
			}
		})
	}
}

// TestManifestEffectiveAPI 验证 EffectiveAPI() 方法
func TestManifestEffectiveAPI(t *testing.T) {
	tests := []struct {
		name     string
		api      string
		expected string
	}{
		{"empty defaults to ^1", "", "^1"},
		{"^1 returns ^1", "^1", "^1"},
		{"^1.5 returns ^1.5", "^1.5", "^1.5"},
		{"spaces trimmed", "  ^2  ", "^2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{API: tt.api}
			got := m.EffectiveAPI()
			if got != tt.expected {
				t.Errorf("EffectiveAPI() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestManifestClientAssets 验证 ClientAssets 路径安全性
func TestManifestClientAssets(t *testing.T) {
	tests := []struct {
		name       string
		assets     []string
		shouldPass bool
	}{
		{"valid relative path", []string{"web/index.html", "web/style.css"}, true},
		{"empty assets", []string{}, true},
		{"absolute path rejected", []string{"/etc/passwd"}, false},
		{"parent escape rejected", []string{"../../../etc/passwd"}, false},
		{"dot-dot in middle rejected", []string{"web/../../etc/passwd"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Manifest{
				SchemaVersion: 3,
				PluginID:      "test-plugin",
				PluginVersion: "1.0.0",
				GatewayCompatibility: GatewayCompatibility{
					APIContract: SupportedAPIContractV2,
				},
				Runtime: Runtime{
					Entrypoint:        "bin/plugin",
					Protocol:          "http-unix-socket",
					HealthPath:        "/health",
					HandshakePath:     "/handshake",
					ShutdownGraceSecs: 10,
				},
				ClientAssets: tt.assets,
			}

			err := m.validate()
			if tt.shouldPass && err != nil {
				t.Errorf("expected pass but got error: %v", err)
			}
			if !tt.shouldPass && err == nil {
				t.Errorf("expected error but validation passed")
			}
		})
	}
}

// TestManifestLoadFromFile 验证从文件加载 schema_version 3 manifest
func TestManifestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "plugin-manifest.json")

	manifestJSON := `{
  "schema_version": 3,
  "plugin_id": "test-plugin-v3",
  "display_name": "Test Plugin V3",
  "plugin_version": "1.0.0",
  "gateway_compatibility": {
    "api_contract": "gateway-plugin-v2"
  },
  "runtime": {
    "entrypoint": "bin/plugin",
    "protocol": "http-unix-socket",
    "health_path": "/health",
    "handshake_path": "/handshake",
    "shutdown_grace_seconds": 10
  },
  "api": "^1.0",
  "requires": ["postgresql"],
  "provides": ["analytics.session"],
  "essential": true,
  "defaults": {
    "enable_cache": true,
    "cache_ttl": 300
  },
  "settings_schema": {
    "type": "object",
    "properties": {
      "enable_cache": {
        "type": "boolean",
        "title": "Enable Cache",
        "default": true
      }
    }
  },
  "client_assets": ["web/index.html"],
  "capabilities": [],
  "pages": [],
  "web": {
    "mount": "/test",
    "base_path": "/",
    "entry": "index.html"
  },
  "activation": {}
}`

	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	// 验证字段
	if m.SchemaVersion != 3 {
		t.Errorf("SchemaVersion = %d, want 3", m.SchemaVersion)
	}
	if m.PluginID != "test-plugin-v3" {
		t.Errorf("PluginID = %q, want %q", m.PluginID, "test-plugin-v3")
	}
	if m.EffectiveAPI() != "^1.0" {
		t.Errorf("EffectiveAPI() = %q, want %q", m.EffectiveAPI(), "^1.0")
	}
	if len(m.Requires) != 1 || m.Requires[0] != "postgresql" {
		t.Errorf("Requires = %v, want [postgresql]", m.Requires)
	}
	if len(m.Provides) != 1 || m.Provides[0] != "analytics.session" {
		t.Errorf("Provides = %v, want [analytics.session]", m.Provides)
	}
	if !m.Essential {
		t.Error("Essential should be true")
	}
	if m.Defaults["enable_cache"] != true {
		t.Errorf("Defaults[enable_cache] = %v, want true", m.Defaults["enable_cache"])
	}
	if m.SettingsSchema == nil {
		t.Fatal("SettingsSchema should not be nil")
	}
	if len(m.ClientAssets) != 1 || m.ClientAssets[0] != "web/index.html" {
		t.Errorf("ClientAssets = %v, want [web/index.html]", m.ClientAssets)
	}
}

// TestManifestSettingsSchemaValidation 验证 SettingsSchema 过大检查
func TestManifestSettingsSchemaValidation(t *testing.T) {
	// 测试：超过64个属性应该失败
	props := make(map[string]*SettingsProperty)
	for i := 0; i < 65; i++ {
		props[string(rune('a'+i%26))+string(rune('0'+i))] = &SettingsProperty{
			Type:  "string",
			Title: "Test",
		}
	}

	m := Manifest{
		SchemaVersion: 3,
		PluginID:      "test-plugin",
		PluginVersion: "1.0.0",
		GatewayCompatibility: GatewayCompatibility{
			APIContract: SupportedAPIContractV2,
		},
		Runtime: Runtime{
			Entrypoint:        "bin/plugin",
			Protocol:          "http-unix-socket",
			HealthPath:        "/health",
			HandshakePath:     "/handshake",
			ShutdownGraceSecs: 10,
		},
		SettingsSchema: &SettingsSchema{
			Type:       "object",
			Properties: props,
		},
	}

	err := m.validate()
	if err == nil {
		t.Error("expected error for too many properties, got nil")
	}
}

// TestManifestJSONRoundtrip 验证 JSON 序列化往返
func TestManifestJSONRoundtrip(t *testing.T) {
	original := Manifest{
		SchemaVersion: 3,
		PluginID:      "test-plugin",
		DisplayName:   "Test Plugin",
		PluginVersion: "1.0.0",
		GatewayCompatibility: GatewayCompatibility{
			APIContract: SupportedAPIContractV2,
		},
		Runtime: Runtime{
			Entrypoint:        "bin/plugin",
			Protocol:          "http-unix-socket",
			HealthPath:        "/health",
			HandshakePath:     "/handshake",
			ShutdownGraceSecs: 10,
		},
		API:       "^1.0",
		Requires:  []string{"postgresql"},
		Provides:  []string{"analytics"},
		Essential: true,
		Defaults: map[string]any{
			"enabled": true,
			"ttl":     300,
		},
	}

	// 序列化
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// 反序列化
	var decoded Manifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// 验证关键字段
	if decoded.SchemaVersion != original.SchemaVersion {
		t.Errorf("SchemaVersion mismatch: %d != %d", decoded.SchemaVersion, original.SchemaVersion)
	}
	if decoded.API != original.API {
		t.Errorf("API mismatch: %q != %q", decoded.API, original.API)
	}
	if decoded.Essential != original.Essential {
		t.Errorf("Essential mismatch: %v != %v", decoded.Essential, original.Essential)
	}
}

// 辅助函数
func ptrFloat64(f float64) *float64 {
	return &f
}

// TestManifestV3ExampleFixtureIsLoadable —— 让 testdata/manifest-v3-example.json
// 从**死文件**变成**活的夹具**。
//
// ★ 2026-11-25 12h 修订审计轮 P2-3 的修正：
//
//	该文件随 W2 一起落盘，但**全仓零引用**（没有任何测试读它）⇒
//	它既不校验任何东西，又会在字段漂移时静默过期 ⇒ 是典型的「摆在那儿的文档」。
//	⇒ 现在让它成为夹具：schema_version 3 的 manifest **必须能真的加载并通过校验**。
//
// ★ 这道门顺带钉住「严格解码不误伤」：v3 走 DisallowUnknownFields
//
//	⇒ 若夹具里出现运行时不认识的字段，这道门立刻红 ⇒ 夹具与结构体不会各说各话。
func TestManifestV3ExampleFixtureIsLoadable(t *testing.T) {
	m, err := LoadManifest(filepath.Join("testdata", "manifest-v3-example.json"))
	if err != nil {
		t.Fatalf("加载 testdata/manifest-v3-example.json 失败：%v", err)
	}
	if m.SchemaVersion != 3 {
		t.Errorf("夹具的 schema_version = %d，期望 3", m.SchemaVersion)
	}
	if m.PluginID != "example-analytics-plugin" {
		t.Errorf("夹具的 plugin_id = %q，期望 %q", m.PluginID, "example-analytics-plugin")
	}
	if m.EffectiveAPI() != "^1.0" {
		t.Errorf("夹具的 api 解析为 %q，期望 %q", m.EffectiveAPI(), "^1.0")
	}
	if len(m.Provides) != 2 {
		t.Errorf("夹具的 provides = %v，期望 2 项", m.Provides)
	}
	if m.SettingsSchema == nil || len(m.SettingsSchema.Properties) != 5 {
		t.Fatalf("夹具的 settings_schema 未被解析出来（properties 数不对）")
	}
	// x-order 是 SettingsSchema.Order 的实际 tag ⇒ 顺序必须真的落进来。
	if len(m.SettingsSchema.Order) != 5 || m.SettingsSchema.Order[0] != "enable_cache" {
		t.Errorf("夹具的 settings_schema.x-order 未被解析：%v", m.SettingsSchema.Order)
	}
}

// TestManifestV3RejectsUnknownFields —— v3 的「未知字段报错」是 v3 的**唯一**语义差异。
//
// ★ 背景（审计轮 P1-2）：encoding/json 默认**忽略**结构体里没有的字段
//
//	⇒ 一份声明了运行时不认识的字段的 manifest 会被「加载成功」而该字段
//	**无声丢失**（插件以为自己配了的东西根本没生效）。
//	⇒ v3 起用严格解码：未知字段当场报错，并**在报错里带上字段名**。
func TestManifestV3RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin-manifest.json")
	body := `{
  "schema_version": 3,
  "plugin_id": "p-unknown-field",
  "plugin_version": "1.0.0",
  "gateway_compatibility": {"api_contract": "gateway-plugin-v2"},
  "runtime": {
    "entrypoint": "bin/plugin",
    "protocol": "http-unix-socket",
    "health_path": "/health",
    "handshake_path": "/handshake",
    "shutdown_grace_seconds": 10
  },
  "totally_unknown_field": "should-not-be-silently-dropped"
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 manifest 失败：%v", err)
	}
	_, err := LoadManifest(path)
	if err == nil {
		t.Fatal("v3 manifest 含未知字段时应当报错（静默丢弃 = 插件配了却没生效）")
	}
	if !strings.Contains(err.Error(), "totally_unknown_field") {
		t.Errorf("报错里应当带上未知字段名，实际：%v", err)
	}
}

// TestManifestV2StillAcceptsUnknownFields —— v1/v2 **不得**被这次收紧波及。
//
// ★ 为什么必须钉住这一条：严格解码是**随版本递进**的收紧
//
//	（仓内既有 v1/v2 manifest 可能带运行时不关心的附加字段）
//	⇒ 一旦误伤 v1/v2，存量环境会**整批加载失败** ⇒ 这是一道防「收紧过头」的钉测。
func TestManifestV2StillAcceptsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin-manifest.json")
	body := `{
  "schema_version": 2,
  "plugin_id": "p-v2-extra-field",
  "plugin_version": "1.0.0",
  "gateway_compatibility": {"api_contract": "gateway-plugin-v2"},
  "runtime": {
    "entrypoint": "bin/plugin",
    "protocol": "http-unix-socket",
    "health_path": "/health",
    "handshake_path": "/handshake",
    "shutdown_grace_seconds": 10
  },
  "legacy_extra_field": "v2-keeps-ignoring-it"
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 manifest 失败：%v", err)
	}
	if _, err := LoadManifest(path); err != nil {
		t.Fatalf("v2 manifest 不应因未知字段被拒（收紧只针对 v3+）：%v", err)
	}
}
