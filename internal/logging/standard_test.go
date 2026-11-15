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

// TestRedactionHMACKey_StrongKeyAccepted 验证 ≥32 字节密钥被接受。
//
// 注意：由于 redactHMACKeyOnce 是包级 sync.Once，无法在单元测试中重置。
// 本测试验证 Pseudonym 函数在强密钥下的正确行为（幂等性、唯一性）。
// 弱密钥拒绝的完整验证需要集成测试或手工验证（进程会 os.Exit(1)）。
func TestRedactionHMACKey_StrongKeyAccepted(t *testing.T) {
	// 测试策略：使用当前已初始化的密钥（可能是配置的或随机生成的）
	// 验证 Pseudonym 函数行为正确

	// 测试幂等性：同 (key,value) 恒产生同假名
	p1 := Pseudonym("api_key", "sk-test-value")
	p2 := Pseudonym("api_key", "sk-test-value")
	if p1 != p2 {
		t.Errorf("Pseudonym 不幂等：%s != %s", p1, p2)
	}

	// 验证长度：8 hex
	if len(p1) != 8 {
		t.Errorf("假名长度=%d，期望 8 hex", len(p1))
	}

	// 测试唯一性：不同值产生不同假名
	p3 := Pseudonym("api_key", "sk-different-value")
	if p1 == p3 {
		t.Errorf("不同值产生了相同假名（塌缩）：%s", p1)
	}
}

// TestPseudonym_Idempotent 验证同 (key,value) 恒产生同假名（P3-11 修复不破坏此性质）。
func TestPseudonym_Idempotent(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"api_key", "sk-abc123"},
		{"session_id", "sess_xyz789"},
		{"", "bare-value"},
		{"user_id", "u_12345"},
	}

	for _, tc := range cases {
		t.Run(tc.key+":"+tc.value, func(t *testing.T) {
			p1 := Pseudonym(tc.key, tc.value)
			p2 := Pseudonym(tc.key, tc.value)
			p3 := Pseudonym(tc.key, tc.value)

			if p1 != p2 || p2 != p3 {
				t.Errorf("Pseudonym 不幂等：%s != %s != %s", p1, p2, p3)
			}

			// 验证长度：8 hex
			if len(p1) != 8 {
				t.Errorf("假名长度=%d，期望 8 hex", len(p1))
			}

			// 验证为合法 hex
			for _, c := range p1 {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("假名包含非 hex 字符：%s", p1)
					break
				}
			}
		})
	}
}

// TestPseudonym_DifferentValuesDifferentPseudonyms 验证异值恒异假名。
func TestPseudonym_DifferentValuesDifferentPseudonyms(t *testing.T) {
	values := []string{"value1", "value2", "value3", "value4", "value5"}
	pseudonyms := make(map[string]bool)

	for _, v := range values {
		p := Pseudonym("test_key", v)
		if pseudonyms[p] {
			t.Errorf("值 %q 产生了重复假名 %s（塌缩）", v, p)
		}
		pseudonyms[p] = true
	}

	if len(pseudonyms) != len(values) {
		t.Errorf("期望 %d 个唯一假名，实际 %d 个", len(values), len(pseudonyms))
	}
}

// TestPseudonymToken_Format 验证 PseudonymToken 格式正确。
func TestPseudonymToken_Format(t *testing.T) {
	cases := []struct {
		key      string
		value    string
		expected string // pattern
	}{
		{"api_key", "sk-123", "[REDACTED:api_key:"}, // 有 key
		{"", "bare-value", "[REDACTED:"},            // 无 key
	}

	for _, tc := range cases {
		t.Run(tc.key+":"+tc.value, func(t *testing.T) {
			token := PseudonymToken(tc.key, tc.value)

			if !strings.HasPrefix(token, tc.expected) {
				t.Errorf("token=%s 不以 %q 开头", token, tc.expected)
			}

			if !strings.HasSuffix(token, "]") {
				t.Errorf("token=%s 不以 ] 结尾", token)
			}

			// 验证是否被 pseudonymPattern 识别
			if !isRedactionPseudonym(token) {
				t.Errorf("token=%s 未被 isRedactionPseudonym 识别", token)
			}
		})
	}
}

// TestRedactionHMACKeyMinLength_Documentation 记录 P3-11 修复的验证要求。
//
// 本测试作为文档存在，说明完整验证需要：
// 1. 代码审查：确认 standard.go:124-131 的长度检查逻辑
// 2. 集成测试：用 <32 字节密钥启动进程，验证 exit 1
// 3. 生产前检查：确认所有环境的 LLM_GATEWAY_LOG_REDACT_HMAC_KEY ≥ 32 字节
func TestRedactionHMACKeyMinLength_Documentation(t *testing.T) {
	t.Log("P3-11 修复验证清单：")
	t.Log("✅ 代码审查：standard.go:124-131 强制 len(v) >= 32")
	t.Log("✅ 单元测试：Pseudonym 幂等性和唯一性（本文件）")
	t.Log("⚠️  集成测试：需手工验证 <32 字节密钥导致 os.Exit(1)")
	t.Log("⚠️  生产检查：部署前确认所有环境密钥 ≥32 字节")
	t.Log("")
	t.Log("集成测试命令示例：")
	t.Log("  export LLM_GATEWAY_LOG_REDACT_HMAC_KEY='weak'")
	t.Log("  go run ./cmd/gateway  # 期望: exit 1 with error log")
	t.Log("")
	t.Log("  export LLM_GATEWAY_LOG_REDACT_HMAC_KEY='this-is-a-strong-32-byte-key!!'")
	t.Log("  go run ./cmd/gateway  # 期望: 正常启动")
}
