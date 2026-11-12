package outputcompliance

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// ──────────────────────────────────────────────────────────────────────────
// r1045（2026-11-13 owner 裁决）：「对于脱敏的信息在客户端一级一定要还原，
// 不然执行工具或显示内容都会出错。」
//
// 语义拆分与默认值：
//   - 输出**检查**（检测/台账/审核单/硬拦清单）照常（sanitize.output_check_enabled）；
//   - 占位符**还原**照常（还原链，与本拦截器无关）；
//   - **客户端可见遮蔽改写**（把输出文本重写成 [REDACTED:*]）默认关闭
//     （sanitize.output_mask_enabled=false），显式开启才恢复旧行为。
//
// 本文件钉住三件事：
//  1. 默认（mask off）：命中文本原样下发客户端——字节不变、无 [REDACTED:*]，
//     命中仍进 observe 台账（issue_count 元数据）；
//  2. 显式开启（mask on）：恢复旧遮蔽行为（改写字节）；
//  3. 硬底不变：检查器判 Blocked（硬拦清单/外传守卫）时无论 mask 开关如何
//     都终止——mask 开关只拆「改写」，不拆「拦截」。
// ──────────────────────────────────────────────────────────────────────────

// maskCheckerStub 是可编排的检查器：对含 secretMark 的文本产出遮蔽结果，
// 对 blockedMark 返回硬拦。
type maskCheckerStub struct{}

const (
	stubSecret  = "FreshSecret987!"
	stubMasked  = "masked [REDACTED:secret] form"
	stubBlocked = "HARD_BLOCK_SHAPE"
)

func (c *maskCheckerStub) Check(_ context.Context, _ string, text string) (*outputcompliance.ComplianceResult, error) {
	if strings.Contains(text, stubBlocked) {
		return &outputcompliance.ComplianceResult{Blocked: true, Issues: []outputcompliance.ComplianceIssue{{
			Type: "sensitive", Subtype: "secret", Severity: 8, RuleID: "test.hard_block",
		}}}, nil
	}
	if !strings.Contains(text, stubSecret) {
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	return &outputcompliance.ComplianceResult{
		Compliant: false, RedactedOutput: strings.ReplaceAll(text, stubSecret, "[REDACTED:secret]"),
		Issues: []outputcompliance.ComplianceIssue{{
			Type: "sensitive", Subtype: "secret", Severity: 8, Redacted: true, RuleID: "test.mask_rule",
		}},
	}, nil
}

// r1045SettingsRegistry 挂上含新开关的设置注册表；rows 允许用例改值。
func r1045SettingsRegistry(t *testing.T) map[string][]byte {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	rows := make(map[string][]byte)
	r := settings.NewRegistry()
	for _, sp := range settings.ModuleSpecs() {
		r.MustRegisterSpec(sp)
	}
	r.RegisterBackend(settings.ScopePlatform, &testBackend{rows: rows})
	settings.Global = r
	return rows
}

func TestR1045_DefaultNoClientVisibleMasking_NonStream(t *testing.T) {
	rows := r1045SettingsRegistry(t)
	// 默认：sanitize.output_mask_enabled 未写 ⇒ false（裁决默认值）。
	if _, exists := rows["sanitize.output_mask_enabled"]; exists {
		t.Fatal("test premise: mask switch must default to absent")
	}
	guard := NewMandatoryOutputComplianceInterceptor(&maskCheckerStub{}, "r1045.pending")
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"leak ` + stubSecret + ` here"}}]}`)

	result, err := guard.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: body, SessionID: "s-r1045", TenantID: "t",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if result == nil {
		t.Fatal("nil result: guard must still run (detection/ledger stay on)")
	}
	if result.ShouldBlock {
		t.Fatalf("masked-shape must not block when mask off: %+v", result)
	}
	if result.ModifiedBody != nil {
		t.Fatalf("client bytes rewritten under default-off masking: %s", result.ModifiedBody)
	}
	// 命中仍要进台账（observe 元数据）——检测面没关。
	if result.Metadata == nil || result.Metadata["issue_count"] == nil {
		t.Fatalf("hit not ledgered: %+v", result.Metadata)
	}

	// 显式开启：恢复旧遮蔽行为。
	rows["sanitize.output_mask_enabled"] = []byte("true")
	result, err = guard.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: body, SessionID: "s-r1045", TenantID: "t",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if result == nil || result.ModifiedBody == nil || !strings.Contains(string(result.ModifiedBody), "[REDACTED:secret]") {
		t.Fatalf("mask-on did not rewrite: %+v", result)
	}
}

func TestR1045_DefaultNoClientVisibleMasking_Stream(t *testing.T) {
	rows := r1045SettingsRegistry(t)
	guard := NewMandatoryOutputComplianceInterceptor(&maskCheckerStub{}, "r1045.pending")

	meta := &response.StreamMeta{State: response.NewStreamState(), SessionID: "s-r1045", TenantID: "t"}
	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"leak " + stubSecret + " now\"},\"finish_reason\":null}]}\n\n")
	first, err := guard.InterceptStreamChunk(context.Background(), frame, meta)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if first != nil && first.ShouldBlock {
		t.Fatalf("masked-shape must not kill stream when mask off: %+v", first)
	}
	// 释放缓冲（终态/flush），断言客户端字节原样、无遮蔽标记。
	released, err := guard.FlushStreamPending(context.Background(), meta)
	if err != nil {
		t.Fatalf("flush err=%v", err)
	}
	if released == nil || strings.Contains(string(released), "[REDACTED") {
		t.Fatalf("client stream bytes masked under default-off: %q", released)
	}
	if !strings.Contains(string(released), stubSecret) {
		t.Fatalf("original text lost from stream release: %q", released)
	}

	// mask on：同帧改写。
	rows["sanitize.output_mask_enabled"] = []byte("true")
	meta2 := &response.StreamMeta{State: response.NewStreamState(), SessionID: "s-r1045b", TenantID: "t"}
	first2, err := guard.InterceptStreamChunk(context.Background(), frame, meta2)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	_ = first2
	released2, err := guard.FlushStreamPending(context.Background(), meta2)
	if err != nil {
		t.Fatalf("flush err=%v", err)
	}
	if released2 == nil || !strings.Contains(string(released2), "[REDACTED:secret]") {
		t.Fatalf("mask-on stream did not rewrite: %q", released2)
	}
}

func TestR1045_HardBlockFloorIndependentOfMaskSwitch(t *testing.T) {
	r1045SettingsRegistry(t)
	guard := NewMandatoryOutputComplianceInterceptor(&maskCheckerStub{}, "r1045.pending")
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"` + stubBlocked + `"}}]}`)

	result, err := guard.InterceptNonStream(context.Background(), &response.InterceptRequest{
		ResponseBody: body, SessionID: "s-r1045", TenantID: "t",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if result == nil || !result.ShouldBlock {
		t.Fatalf("hard block floor lost under mask-off: %+v", result)
	}

	// 流式同判。
	meta := &response.StreamMeta{State: response.NewStreamState(), SessionID: "s-r1045c", TenantID: "t"}
	frame := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + stubBlocked + "\"}}]}\n\n")
	streamResult, err := guard.InterceptStreamChunk(context.Background(), frame, meta)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if streamResult == nil || !streamResult.ShouldBlock {
		// 某些形态在 flush 才判——补一次 flush 再断言。
		if _, ferr := guard.FlushStreamPending(context.Background(), meta); ferr != nil {
			t.Fatalf("flush err=%v", ferr)
		}
		t.Fatalf("hard block floor lost on stream: %+v", streamResult)
	}
}
