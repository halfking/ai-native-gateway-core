package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// captureStandard 用内存 buffer 捕获规范 handler 的输出。
func captureStandard(t *testing.T, opts StandardOptions) func() map[string]any {
	t.Helper()
	if opts.Service == "" {
		opts.Service = "llm-gateway"
	}
	if opts.Version == "" {
		opts.Version = "0.0.1"
	}
	if opts.Env == "" {
		opts.Env = "test"
	}

	var buf bytes.Buffer
	handler, err := NewStandardHandler(&buf, opts)
	if err != nil {
		t.Fatalf("NewStandardHandler 失败: %v", err)
	}
	slog.SetDefault(slog.New(handler))

	return func() map[string]any {
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) == 0 || lines[len(lines)-1] == "" {
			t.Fatalf("无日志输出")
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &obj); err != nil {
			t.Fatalf("日志不是合法 JSON: %v (line=%s)", err, lines[len(lines)-1])
		}
		return obj
	}
}

// TestStandardRequiredFields 验证 8 个必填字段全部注入。
func TestStandardRequiredFields(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("user profile loaded", "user_id", "u_a3f9")

	obj := last()
	for _, key := range []string{
		"timestamp", "severity_text", "severity_number",
		"msg", "service", "version", "env", "instance_id",
	} {
		if _, ok := obj[key]; !ok {
			t.Errorf("缺必填字段 %q，实际字段=%v", key, obj)
		}
	}
}

// TestStandardFlat 验证输出为扁平单层。
func TestStandardFlat(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("flat check", "request_id", "req_1")

	for key, val := range last() {
		switch val.(type) {
		case map[string]any:
			t.Errorf("字段 %q 是嵌套对象，违反扁平约束", key)
		case []any:
			t.Errorf("字段 %q 是数组，违反扁平约束", key)
		}
	}
}

// TestStandardSeverityAlias 验证 severity_text 与 level 别名同时存在且一致。
//
// 这一条专门守护一个历史坑：slog.LevelKey 的字面值就是 "level"，
// 若 ReplaceAttr 不区分值的类型，别名会被一起丢掉。
func TestStandardSeverityAlias(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("alias check")

	obj := last()
	if obj["severity_text"] != "INFO" {
		t.Errorf("severity_text=%v，期望 INFO", obj["severity_text"])
	}
	// level 是小写别名（ECS log.level 惯例），与 severity_text 大小写不同。
	level, ok := obj["level"].(string)
	if !ok {
		t.Fatalf("level 别名缺失")
	}
	if level != strings.ToLower(obj["severity_text"].(string)) {
		t.Errorf("level=%v，期望 severity_text 的小写形态", level)
	}
}

// TestStandardSeverityNumber 验证 severity_number 符合 OTel 数值表。
func TestStandardSeverityNumber(t *testing.T) {
	cases := []struct {
		level slog.Level
		want  float64
	}{
		{slog.LevelDebug, 5},
		{slog.LevelInfo, 9},
		{slog.LevelWarn, 13},
		{slog.LevelError, 17},
	}
	for _, tc := range cases {
		last := captureStandard(t, StandardOptions{Level: tc.level})
		slog.Log(context.Background(), tc.level, "level check")
		got, ok := last()["severity_number"].(float64)
		if !ok {
			t.Fatalf("severity_number 缺失")
		}
		if got != tc.want {
			t.Errorf("level %v → severity_number=%v，期望 %v", tc.level, got, tc.want)
		}
	}
}

// TestStandardRedactsSensitiveKeys 验证敏感键被脱敏。
// 2026-11-15 假名化改判：脱敏值不再是常量 "[REDACTED]"，而是确定性假名
// "[REDACTED:<key>:<8hex>]"（不同值不同假名——脱敏不可塌缩，同值同假名可
// 关联/可还原对拍）。此处钉：原值不出现 + 假名形状（前缀+键段+8hex）。
func TestStandardRedactsSensitiveKeys(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("secret check", "password", "hunter2", "api_key", "sk-abcdefghijklmnop")

	obj := last()
	if got, _ := obj["password"].(string); !isRedactionPseudonym(got) || strings.Contains(got, "hunter2") {
		t.Errorf("password=%v，期望假名形状且不含原值", obj["password"])
	}
	if got, _ := obj["api_key"].(string); !isRedactionPseudonym(got) || strings.Contains(got, "sk-abcdefghijklmnop") {
		t.Errorf("api_key=%v，期望假名形状且不含原值", obj["api_key"])
	}
	var pw, ak string
	pw, _ = obj["password"].(string)
	ak, _ = obj["api_key"].(string)
	if pw != "[REDACTED:password:"+Pseudonym("password", "hunter2")+"]" {
		t.Errorf("password=%v，期望确定性假名（键段=password）", pw)
	}
	if ak != "[REDACTED:api_key:"+Pseudonym("api_key", "sk-abcdefghijklmnop")+"]" {
		t.Errorf("api_key=%v，期望确定性假名（键段=api_key）", ak)
	}
}

// TestStandardRedactsSensitiveValues 验证敏感值模式被脱敏。
// 2026-11-15 假名化改判：模式命中 → "[REDACTED:<8hex>]"（无键段）。
func TestStandardRedactsSensitiveValues(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("value check", "note", "token is Bearer abcdef123456")

	got, _ := last()["note"].(string)
	if !isRedactionPseudonym(got) || strings.Contains(got, "Bearer") {
		t.Errorf("note=%v，期望整体脱敏为假名", got)
	}
}

// TestStandardRedactionPseudonymInjective 钉脱敏假名的注入性（2026-11-15）：
// 不同原值 → 不同假名（不可塌缩）；同值同键 → 同假名（可关联/可还原）；
// 异键同值 → 异假名（键段参与哈希，命名空间隔离）。
func TestStandardRedactionPseudonymInjective(t *testing.T) {
	last := captureStandard(t, StandardOptions{})

	// 同键异值 → 异假名（脱敏不可塌缩——本测试是本轮的核心钉）。
	slog.Info("injective check", "session_id", "gw_aaa")
	sidA, _ := last()["session_id"].(string)
	slog.Info("injective check", "session_id", "gw_bbb")
	sidB, _ := last()["session_id"].(string)
	if sidA == "" || sidA == sidB {
		t.Errorf("同键异值必须得到异假名：aaa=%v bbb=%v", sidA, sidB)
	}

	// 异键同值 → 异假名（键段参与哈希）。
	slog.Info("injective check", "credential_id", "gw_aaa")
	cidA, _ := last()["credential_id"].(string)
	if cidA == sidA {
		t.Errorf("异键同值必须得到异假名：session_id=%v credential_id=%v", sidA, cidA)
	}

	// 同值同键跨两次调用恒同假名（关联性/可还原对拍的前提）。
	slog.Info("injective check again", "session_id", "gw_aaa")
	sidA2, _ := last()["session_id"].(string)
	if sidA2 != sidA {
		t.Errorf("假名不稳定：%v → %v", sidA, sidA2)
	}
	if sidA != "[REDACTED:session_id:"+Pseudonym("session_id", "gw_aaa")+"]" {
		t.Errorf("假名与 Pseudonym 对拍不符：%v", sidA)
	}
}

// TestStandardRedactionIdempotent 钉假名幂等（2026-11-15）：已经是假名的值
// 二次脱敏必须恒等——redactRecord 与内层 ReplaceAttr 双路径叠加安全。
func TestStandardRedactionIdempotent(t *testing.T) {
	pseudo := PseudonymToken("session_id", "gw_xyz")
	if got := redactKeyValue("session_id", pseudo); got != pseudo {
		t.Errorf("假名二次按键脱敏被改写：%v → %v", pseudo, got)
	}
	if got := redactString(pseudo); got != pseudo {
		t.Errorf("假名二次按模式脱敏被改写：%v → %v", pseudo, got)
	}
	bare := RedactedPlaceholder
	if got := redactString(bare); got != bare {
		t.Errorf("裸占位符被改写：%v → %v", bare, got)
	}
}

// TestStandardRejectsInvalidOptions 验证非法参数 fail-loud。
func TestStandardRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		name string
		opts StandardOptions
	}{
		{"缺 Service", StandardOptions{Version: "1.0", Env: "test"}},
		{"缺 Version", StandardOptions{Service: "svc", Env: "test"}},
		{"缺 Env", StandardOptions{Service: "svc", Version: "1.0"}},
		{"Env 非法", StandardOptions{Service: "svc", Version: "1.0", Env: "production"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewStandardHandler(&bytes.Buffer{}, tc.opts); err == nil {
				t.Errorf("期望返回错误，实际 nil")
			}
		})
	}
}

// TestStandardResourceConstant 验证资源字段跨日志保持常量。
func TestStandardResourceConstant(t *testing.T) {
	last := captureStandard(t, StandardOptions{})
	slog.Info("first")
	first := last()
	slog.Info("second")
	second := last()

	for _, key := range []string{"service", "version", "env", "instance_id"} {
		if first[key] != second[key] {
			t.Errorf("资源字段 %q 不稳定: %v → %v", key, first[key], second[key])
		}
	}
}

// TestServiceEnvNormalization 验证环境归一：
// 本仓历史上用 "production"，规范要求 "prod"——不归一会让 AI 按 env
// 分组时出现两个桶。
func TestServiceEnvNormalization(t *testing.T) {
	cases := map[string]string{
		"production": "prod",
		"prod":       "prod",
		"staging":    "staging",
		"dev":        "dev",
		"test":       "test",
		"local":      "local",
		"":           "local",
		"garbage":    "local",
	}
	for raw, want := range cases {
		t.Setenv("LLM_GATEWAY_ENV", raw)
		if got := ServiceEnv(); got != want {
			t.Errorf("LLM_GATEWAY_ENV=%q → ServiceEnv()=%q，期望 %q", raw, got, want)
		}
	}
}

// TestServiceNameDefault 验证服务标识默认为 llm-gateway 且可覆盖。
func TestServiceNameDefault(t *testing.T) {
	os.Unsetenv("LLM_GATEWAY_SERVICE")
	if got := ServiceName(); got != DefaultServiceName {
		t.Errorf("ServiceName()=%q，期望 %q", got, DefaultServiceName)
	}
	t.Setenv("LLM_GATEWAY_SERVICE", "custom-svc")
	if got := ServiceName(); got != "custom-svc" {
		t.Errorf("ServiceName()=%q，期望 custom-svc", got)
	}
}

// TestServiceVersionNonEmpty 验证版本永远非空（门禁要求）。
func TestServiceVersionNonEmpty(t *testing.T) {
	t.Setenv("LLM_GATEWAY_VERSION", "9.9.9")
	if got := ServiceVersion(); got != "9.9.9" {
		t.Errorf("ServiceVersion()=%q，期望 9.9.9", got)
	}
	os.Unsetenv("LLM_GATEWAY_VERSION")
	if got := ServiceVersion(); got == "" {
		t.Errorf("ServiceVersion() 为空——门禁要求 version 必填")
	}
}

// TestStandardReservedNamesWin 验证保留名优先：
// 业务字段不得覆盖 4 个资源字段，否则必填契约会被击穿
// （实测：mcp-gateway 的业务 version 曾覆盖服务版本，导致日志缺必填字段）。
func TestStandardReservedNamesWin(t *testing.T) {
	last := captureStandard(t, StandardOptions{
		Service: "llm-gateway",
		Version: "2.4.1",
		Env:     "prod",
	})
	slog.Info("reserved conflict",
		"version", "business-version",
		"service", "business-service",
		"env", "business-env",
	)

	obj := last()
	if obj["version"] != "2.4.1" {
		t.Errorf("version=%v，期望服务版本 2.4.1（保留名优先）", obj["version"])
	}
	if obj["service"] != "llm-gateway" {
		t.Errorf("service=%v，期望 llm-gateway（保留名优先）", obj["service"])
	}
	if obj["env"] != "prod" {
		t.Errorf("env=%v，期望 prod（保留名优先）", obj["env"])
	}
}

// TestInstallStandardAppliesToAllPaths 验证所有 handler 安装路径都带规范字段。
//
// 回归守护：此前 Init / ReInit 各自直接 slog.SetDefault，
// 运行期切换日志配置（/api/admin/logs/config PUT）后格式会退回旧版——
// 缺 service/version/env/instance_id，AI 侧会突然「认不出」这个服务。
func TestInstallStandardAppliesToAllPaths(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: replaceAttr,
	})
	installStandard(inner, slog.LevelInfo)

	slog.Info("after install", "probe", "value")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("无日志输出")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &obj); err != nil {
		t.Fatalf("日志不是合法 JSON: %v", err)
	}
	for _, key := range []string{
		"timestamp", "severity_text", "severity_number",
		"msg", "service", "version", "env", "instance_id",
	} {
		if _, ok := obj[key]; !ok {
			t.Errorf("installStandard 后缺必填字段 %q，实际=%v", key, obj)
		}
	}
}
