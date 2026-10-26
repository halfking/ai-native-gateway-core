package streaming

// r1013_write_time_restored_values_test.go — 2026-10-10 write-time 脱敏豁免。
//
// BuildRedactBodyFn 是**最后一米**：它在 w.Write 之前改客户端可见字节，跑在
// 占位符还原**之后**。它与输出合规拦截器「互补且幂等」——但幂等不等于等价：
// 拦截器已经放行的调用方原值，若 write-time 这条不认，就会在离客户端最近的
// 地方被重新遮蔽，且没有任何东西会在后面把它换回来。
//
// 旧实现用 context.Background() 调 Checker.Check（注释里写「write-time 无请求
// 上下文」），豁免集合根本没有通道能送进去。本文件钉住它现在有。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func r1013Body(t *testing.T, text string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": text},
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func r1013AssistantText(t *testing.T, body []byte) string {
	t.Helper()
	output, err := extractAssistantContent(body)
	if err != nil {
		t.Fatalf("extractAssistantContent: %v", err)
	}
	return output
}

// r1013RequireComplianceOn 强制打开可选输出合规，否则 write-time 路径整体短路，
// 下面关于豁免的断言会假绿。产品默认已改为关闭，本 helper 只服务本文件。
func r1013RequireComplianceOn(t *testing.T) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	rows := map[string][]byte{"output_compliance.enabled": []byte(`true`)}
	r := settings.NewRegistry()
	r.RegisterBackend(settings.ScopePlatform, &r1013FakeBackend{store: rows})
	for _, sp := range settings.ModuleSpecs() {
		if sp.Key != "output_compliance.enabled" {
			continue
		}
		if err := r.RegisterSpec(sp); err != nil {
			t.Fatal(err)
		}
	}
	settings.Global = r
	if !outputComplianceEnabled() {
		t.Fatal("failed to force output_compliance.enabled=true for write-time exemption tests")
	}
}

type r1013FakeBackend struct{ store map[string][]byte }

func (b *r1013FakeBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	return b.store[key], nil
}
func (b *r1013FakeBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}
func (b *r1013FakeBackend) GetTenant(_, key string) ([]byte, error) {
	return b.store[key], nil
}
func (b *r1013FakeBackend) SetTenant(_, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func TestBuildRedactBodyFn_ProtectsRestoredCallerValues(t *testing.T) {
	r1013RequireComplianceOn(t)

	const callerEmail = "alice@example.com"
	checker := outputcompliance.NewBuiltinChecker()
	ownerFn := func(string, string) (string, string) { return "caller1", "owner1" }
	restored := func(context.Context, string, string) ([]string, error) {
		return []string{callerEmail}, nil
	}

	withExemption := BuildRedactBodyFn(checker, ownerFn, restored)
	withoutExemption := BuildRedactBodyFn(checker, ownerFn, nil)
	if withExemption == nil || withoutExemption == nil {
		t.Fatal("BuildRedactBodyFn returned nil")
	}
	body := r1013Body(t, "回执发到 "+callerEmail+" 了")

	got := r1013AssistantText(t, withExemption(body, "sess-1", "tenant-a"))

	// 对照组：没有豁免来源时必须照遮。缺了它，上面那条可能因为「压根没检出」
	// 而是个假绿。
	control := r1013AssistantText(t, withoutExemption(body, "sess-1", "tenant-a"))
	if !strings.Contains(control, outputcompliance.RedactionMarker("email")) {
		t.Fatalf("对照组：没有豁免来源时邮箱未被遮蔽，本文件将退化为假绿。got=%q", control)
	}

	if !strings.Contains(got, callerEmail) {
		t.Fatalf("write-time 路径把调用方原值销毁了（离客户端最近的一道）: %q", got)
	}
	if strings.Contains(got, "[REDACTED") {
		t.Fatalf("已还原值仍被遮蔽: %q", got)
	}
}

// TestBuildRedactBodyFn_RestoredValueLookupFailureStillMasks 解析失败退回旧
// 行为（照遮），不因豁免取不到就把内容放行。
func TestBuildRedactBodyFn_RestoredValueLookupFailureStillMasks(t *testing.T) {
	r1013RequireComplianceOn(t)

	const callerEmail = "alice@example.com"
	checker := outputcompliance.NewBuiltinChecker()
	ownerFn := func(string, string) (string, string) { return "caller1", "owner1" }
	failing := func(context.Context, string, string) ([]string, error) {
		return nil, context.DeadlineExceeded
	}

	fn := BuildRedactBodyFn(checker, ownerFn, failing)
	got := r1013AssistantText(t, fn(r1013Body(t, "回执发到 "+callerEmail+" 了"), "sess-1", "tenant-a"))
	if strings.Contains(got, callerEmail) {
		t.Fatalf("豁免来源失败时退化成放行，等于静默关掉输出合规: %q", got)
	}
}
