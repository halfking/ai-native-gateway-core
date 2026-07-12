package sessionforensics_test

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/sessionforensics"
)

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func boolPtrOrNil(b bool) *bool { return &b }

func TestMakeRequestLogHook_NilHook_NoOp(t *testing.T) {
	cb := sessionforensics.MakeRequestLogHook(nil, "default")
	entry := &telemetry.RequestLogEntry{}
	cb(entry) // 不应 panic
}

func TestMakeRequestLogHook_FiltersAutoRequest(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	cb := sessionforensics.MakeRequestLogHook(hook, "default")

	// IsAutoRequest=true 应该被跳过（防止内部调用浪费 quota）
	entry := &telemetry.RequestLogEntry{
		Success:       true,
		GwSessionID:   strPtrOrNil("gw_xx_filter_001"),
		IsAutoRequest: boolPtrOrNil(true),
	}
	cb(entry)
}

func TestMakeRequestLogHook_FiltersEmptySessionID(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	cb := sessionforensics.MakeRequestLogHook(hook, "default")

	entry := &telemetry.RequestLogEntry{
		Success:     true,
		GwSessionID: strPtrOrNil(""), // 空 → 跳过
	}
	cb(entry)
}

func TestMakeRequestLogHook_FiltersNonGwPrefix(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	cb := sessionforensics.MakeRequestLogHook(hook, "default")

	entry := &telemetry.RequestLogEntry{
		Success:     true,
		GwSessionID: strPtrOrNil("xx_legacy_session"), // 没 gw_ 前缀 → 跳过
	}
	cb(entry)
}

func TestMakeRequestLogHook_FiltersFailedRequest(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook.Start(ctx)
	defer hook.Stop()

	cb := sessionforensics.MakeRequestLogHook(hook, "default")

	// 失败的请求（Success=false）不应触发摘要
	entry := &telemetry.RequestLogEntry{
		Success:        false,
		GwSessionID:    strPtrOrNil("gw_xx_failed_001"),
		RequestPreview: strPtrOrNil("hi"),
	}
	cb(entry)
}

func TestMakeRequestLogHook_AcceptsValid(t *testing.T) {
	svc := sessionforensics.NewService(sessionforensics.ServiceConfig{})
	hook := sessionforensics.NewAutoSummaryHook(svc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook.Start(ctx)
	defer hook.Stop()

	cb := sessionforensics.MakeRequestLogHook(hook, "default")

	entry := &telemetry.RequestLogEntry{
		Success:        true,
		GwSessionID:    strPtrOrNil("gw_xx_valid_001"),
		RequestPreview: strPtrOrNil("请帮我写一个 Python hello world"),
	}
	cb(entry)
	// 这次应该入队了
}
