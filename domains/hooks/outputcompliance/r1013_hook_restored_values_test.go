package outputcompliance

// r1013_hook_restored_values_test.go — 2026-10-10 拦截器层接线回归。
//
// domains/outputcompliance 侧的测试证明的是「拿到已还原值集合时会豁免」。
// 这个文件证明的是**集合真的会被送到检查器手里**：豁免来源由 cmd/gateway
// 在装配期注入（wireRestoredValueSource），中间隔着输出合规拦截器与三条响应
// 入口（非流式 / 流式 chunk / 流结束）。任何一环漏掉，症状都会原样复现，
// 而且不会有任何报错。
//
// 用邮箱而不是 IP 做端到端：NewBuiltinChecker 的内置规则里邮箱是正则命中
// 整个值的那一类（IP 同理），豁免判定「整段相等」才真正咬合；凭据类规则的
// 命中区间含 `password=` 这类标签前缀，整段相等天然不成立，用它断言会得到
// 一个什么都没测到的假绿。IP 形态本身由
// domains/outputcompliance/r1013_restored_values_test.go 覆盖。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

const r1013CallerEmail = "alice@example.com"

func r1013ChatBody(t *testing.T, text string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": text},
		}},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return body
}

// TestHookRestoredValueSourceProtectsCallerValues 端到端：调用方自己的邮箱
// 被占位符还原器换回原文后，不应再被输出合规压掉。
func TestHookRestoredValueSourceProtectsCallerValues(t *testing.T) {
	hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil)
	hook.SetRestoredValueSource(func(_ context.Context, sessionID, _ string) ([]string, error) {
		if sessionID == "sess-1" {
			return []string{r1013CallerEmail}, nil
		}
		return nil, nil
	})

	body := r1013ChatBody(t, "回执发到 "+r1013CallerEmail+" 了")
	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "sess-1",
		TenantID:     "tenant-a",
		CallerOwner:  "owner-a",
		ResponseBody: body,
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	got := string(result.ModifiedBody)
	if got == "" {
		got = string(body)
	}
	if !strings.Contains(got, r1013CallerEmail) {
		t.Fatalf("调用方原值在拦截器层被销毁: %s", got)
	}
	if strings.Contains(got, "[REDACTED") {
		t.Fatalf("已还原值仍被遮蔽: %s", got)
	}
}

// TestHookWithoutRestoredSourceStillMasks 是同一段代码的对照组：没有豁免来源
// 时遮蔽必须照旧发生。没有这一条，上一条可能是因为「压根没检出」而绿的。
func TestHookWithoutRestoredSourceStillMasks(t *testing.T) {
	hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil)
	body := r1013ChatBody(t, "回执发到 "+r1013CallerEmail+" 了")

	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "sess-1",
		TenantID:     "tenant-a",
		CallerOwner:  "owner-a",
		ResponseBody: body,
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	got := string(body)
	if result != nil && len(result.ModifiedBody) > 0 {
		got = string(result.ModifiedBody)
	}
	if strings.Contains(got, r1013CallerEmail) {
		t.Fatalf("无豁免来源时邮箱未被遮蔽，合规形同虚设: %s", got)
	}
	if !strings.Contains(got, outputcompliance.RedactionMarker("email")) {
		t.Fatalf("遮蔽物不是带类型标记: %s", got)
	}
}

// ctxSpyChecker 只记录「检查时 ctx 上有没有已还原值集合」，不做任何检测。
type ctxSpyChecker struct {
	saw []string
}

func (c *ctxSpyChecker) Check(ctx context.Context, _, text string) (*outputcompliance.ComplianceResult, error) {
	for value := range outputcompliance.RestoredValuesFromContext(ctx) {
		c.saw = append(c.saw, value)
	}
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
}

// TestMandatoryGuardRejectsRestoredValueSource 强制守卫排在还原**之前**，
// 它的职责是筛查模型自造值。给它挂豁免来源等于给模型开后门：模型写一个与
// 会话里某个调用方值相同的串就能绕过。因此 SetRestoredValueSource 必须对
// mandatory 实例无效——用 ctx 间谍直接观测，而不是靠「没被遮蔽」间接推断。
func TestMandatoryGuardRejectsRestoredValueSource(t *testing.T) {
	mandatorySpy := &ctxSpyChecker{}
	mandatory := NewMandatoryOutputComplianceInterceptor(mandatorySpy, "r1013.mandatory")
	mandatory.SetRestoredValueSource(func(context.Context, string, string) ([]string, error) {
		return []string{r1013CallerEmail}, nil
	})
	if _, err := mandatory.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "sess-1", TenantID: "tenant-a", ResponseBody: r1013ChatBody(t, "x"),
	}); err != nil {
		t.Fatalf("mandatory InterceptNonStream: %v", err)
	}
	if len(mandatorySpy.saw) != 0 {
		t.Fatalf("强制守卫收到了豁免集合 %v：模型自造值可借此绕过", mandatorySpy.saw)
	}

	optionalSpy := &ctxSpyChecker{}
	optional := NewOutputComplianceInterceptor(optionalSpy, nil)
	optional.SetRestoredValueSource(func(context.Context, string, string) ([]string, error) {
		return []string{r1013CallerEmail}, nil
	})
	if _, err := optional.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "sess-1", TenantID: "tenant-a", CallerOwner: "owner-a",
		ResponseBody: r1013ChatBody(t, "x"),
	}); err != nil {
		t.Fatalf("optional InterceptNonStream: %v", err)
	}
	if len(optionalSpy.saw) != 1 || optionalSpy.saw[0] != r1013CallerEmail {
		t.Fatalf("可选合规拦截器没有收到豁免集合: %v", optionalSpy.saw)
	}
}

// TestRestoredValueSourceIsResolvedOncePerStream 流式路径按 chunk 调用检查，
// 每帧解析一次就等于每帧一次 Redis 读。用计数钉住：必须每响应一次。
func TestRestoredValueSourceIsResolvedOncePerStream(t *testing.T) {
	var calls int
	hook := NewOutputComplianceInterceptor(&ctxSpyChecker{}, nil)
	hook.SetRestoredValueSource(func(context.Context, string, string) ([]string, error) {
		calls++
		return []string{r1013CallerEmail}, nil
	})

	meta := &response.StreamMeta{SessionID: "sess-1", TenantID: "tenant-a", State: response.NewStreamState()}
	for _, frame := range []string{
		`data: {"choices":[{"delta":{"content":"hello "}}]}`,
		`data: {"choices":[{"delta":{"content":"mail ` + r1013CallerEmail + ` ok"}}]}`,
		`data: {"choices":[{"delta":{"content":""}}]}`,
	} {
		if _, err := hook.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
			t.Fatalf("InterceptStreamChunk(%q): %v", frame, err)
		}
	}
	if calls == 0 {
		t.Fatal("流式路径没有解析豁免来源：已还原值在流式响应里会被遮蔽")
	}
	if calls > 1 {
		t.Fatalf("豁免来源被解析 %d 次（应每响应一次），流式路径退化成每帧一次 Redis 读", calls)
	}
}

// TestRestoredValueSourceFailureDegradesToMasking 解析失败**不能**阻断响应：
// 豁免是保护措施不是闸门，拿不到集合时退回旧行为（照遮），而不是把流量打挂。
func TestRestoredValueSourceFailureDegradesToMasking(t *testing.T) {
	hook := NewOutputComplianceInterceptor(outputcompliance.NewBuiltinChecker(), nil)
	hook.SetRestoredValueSource(func(context.Context, string, string) ([]string, error) {
		return nil, context.DeadlineExceeded
	})
	body := r1013ChatBody(t, "回执发到 "+r1013CallerEmail+" 了")
	result, err := hook.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID: "sess-1", TenantID: "tenant-a", CallerOwner: "owner-a", ResponseBody: body,
	})
	if err != nil {
		t.Fatalf("豁免来源失败不应中断响应: %v", err)
	}
	got := string(body)
	if result != nil && len(result.ModifiedBody) > 0 {
		got = string(result.ModifiedBody)
	}
	if strings.Contains(got, r1013CallerEmail) {
		t.Fatalf("解析失败时退化成放行，等于静默关掉输出合规: %s", got)
	}
}
