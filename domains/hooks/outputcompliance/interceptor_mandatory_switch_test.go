package outputcompliance

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestMandatoryGuardRespectsGlobalSwitch 验证强制守卫也受 sanitize.output_check_enabled 控制
//
// 问题（2026-11-11）：
// - mandatory guard 的 it.mandatory=true 导致即使总开关关闭也会运行
// - 即使设置 sanitize.output_check_enabled=false，仍会拦截请求
//
// 修复：
// - isEnabled() 方法优先检查总开关
// - 总开关关闭时，所有守卫（包括强制守卫）都不运行
func TestMandatoryGuardRespectsGlobalSwitch(t *testing.T) {
	// 保存原始的 settings.Global
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	// 创建测试用的 settings registry 和 backend
	rows := make(map[string][]byte)
	r := settings.NewRegistry()
	for _, sp := range settings.ModuleSpecs() {
		r.MustRegisterSpec(sp)
	}
	r.RegisterBackend(settings.ScopePlatform, &testBackend{rows: rows})
	settings.Global = r

	// 创建一个会拦截所有内容的检查器（用于测试）
	checker := &alwaysBlockChecker{}

	// 创建强制守卫
	guard := NewMandatoryOutputComplianceInterceptor(checker, "test.pending")

	// 场景 1：总开关关闭时，强制守卫应该不运行
	t.Run("global_switch_off_disables_mandatory_guard", func(t *testing.T) {
		// 关闭总开关
		rows["sanitize.output_check_enabled"] = []byte("false")

		req := &response.InterceptRequest{
			ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"test"}}]}`),
			SessionID:    "test-session",
			TenantID:     "test-tenant",
		}

		// 调用拦截器
		result, err := guard.InterceptNonStream(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// 验证：总开关关闭时，即使检查器会拦截，守卫也不应该运行
		if result != nil && result.ShouldBlock {
			t.Errorf("mandatory guard should NOT block when sanitize.output_check_enabled=false, but got ShouldBlock=true")
		}
	})

	// 场景 2：总开关开启时，强制守卫应该正常运行
	t.Run("global_switch_on_enables_mandatory_guard", func(t *testing.T) {
		// 开启总开关
		rows["sanitize.output_check_enabled"] = []byte("true")

		req := &response.InterceptRequest{
			ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"test"}}]}`),
			SessionID:    "test-session",
			TenantID:     "test-tenant",
		}

		// 调用拦截器
		result, err := guard.InterceptNonStream(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// 验证：总开关开启时，检查器应该拦截
		if result == nil || !result.ShouldBlock {
			t.Errorf("mandatory guard should block when sanitize.output_check_enabled=true, but got ShouldBlock=false")
		}
	})
}

// alwaysBlockChecker 是一个测试用的检查器，总是返回拦截
type alwaysBlockChecker struct{}

func (c *alwaysBlockChecker) Check(ctx context.Context, tenant, text string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{
		Blocked: true,
		Issues: []outputcompliance.ComplianceIssue{
			{
				RuleID:  "test.always_block",
				Subtype: "test",
			},
		},
	}, nil
}

func (c *alwaysBlockChecker) CheckField(ctx context.Context, tenant, field, text string) (*outputcompliance.ComplianceResult, error) {
	return c.Check(ctx, tenant, text)
}

// testBackend 是一个简单的测试用 settings backend
type testBackend struct {
	rows map[string][]byte
}

func (b *testBackend) Get(_ settings.Scope, key string) ([]byte, error) {
	if v, ok := b.rows[key]; ok {
		return v, nil
	}
	return nil, nil
}

func (b *testBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) {
	return nil, nil
}

func (b *testBackend) GetTenant(_ string, key string) ([]byte, error) {
	if v, ok := b.rows[key]; ok {
		return v, nil
	}
	return nil, nil
}

func (b *testBackend) SetTenant(_ string, _ string, _ any) ([]byte, error) {
	return nil, nil
}
