package telemetry

// v1_write_gate_fixture_test.go —— 本包测试的**环境前提**（2026-10-20，审计 D0）。
//
// 背景：`storage.request_logs_write_enabled` 的**代码默认**由 true 改为 false
// （settings/spec_storage.go）。本包绝大多数用例测的是 **v1 写路径本身**
// （pgxmock 对 `UPDATE _request_logs_hot` / `INSERT INTO _request_logs_bodies_hot`
// 的期望序列），它们此前隐式依赖「未配置 ⇒ 继续双写」这个环境默认。默认翻转
// 之后，这些用例的 pgxmock 期望不再被满足 —— **不是断言变弱，而是前提从隐式
// 变成了显式**：门控的取值现在是断言的一部分，不能再由环境默认兜底。
//
// 因此本文件把「v1 写门 = 开」装成**本包的环境前提**，而不是让每个用例各自去
// 猜全局默认。测停写门控的用例（request_logs_stop_write_gate_test.go 的
// withStopWriteGateSettings）自己显式钉住门的取值，不受此前提影响。
//
// ⚠ 这不是「为了让测试变绿而放宽」：没有任何一条断言被改动、被跳过或被降级；
// 改的是「这条断言成立需要什么环境」，而它以前是隐式的、现在写在了明面上。

import (
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestMain installs the package-wide fixture: the v1 write gate is ON, i.e.
// the same ambient state this package had before the D0 default flip.
func TestMain(m *testing.M) {
	prev := settings.Global
	settings.Global = newV1WritesOnRegistry()
	code := m.Run()
	settings.Global = prev
	os.Exit(code)
}

// newV1WritesOnRegistry builds a registry whose only platform override is
// "v1 writes are allowed". Everything else falls through to the real spec
// defaults, so the fixture cannot mask a default change elsewhere.
func newV1WritesOnRegistry() *settings.Registry {
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: map[string][]byte{
		settings.KeyRequestLogsWriteEnabled: []byte("true"),
	}})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range settings.StorageSpecs() {
		registry.MustRegisterSpec(spec)
	}
	return registry
}
