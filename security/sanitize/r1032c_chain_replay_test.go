package sanitize

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	compliance "github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

// r1032c: 生产同构链（OutputSensitive guard → SanitizeRestore → OutputCompliance）
// 回放 raw-logs 9bd6ccf5 的逐帧原文（grok-4.6 chat 腿第 2 轮，上游参数完整、
// 线上客户端只收到工具名）。任一层把 arguments 帧吞掉都判 FAIL。

type r1032cChecker struct{ seen int }

func (c *r1032cChecker) Check(_ context.Context, _, output string) (*compliance.ComplianceResult, error) {
	c.seen++
	return &compliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

func r1032cLoadFrames() []string {
	return []string{
		`data: {"id":"chatcmpl-dm5564zn8d1k","object":"chat.completion.chunk","created":1792040021,"model":"grok-4.6","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-dm5564zn8d1k","object":"chat.completion.chunk","created":1792040021,"model":"grok-4.6","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_call-a6129fa5-ba61-4b25-97be-806154c1be13-2_agents","type":"function","function":{"name":"run_ssh_command","arguments":""}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-dm5564zn8d1k","object":"chat.completion.chunk","created":1792040021,"model":"grok-4.6","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"echo HOME=$HOME; echo USER=$(whoami); ls -la ~; ls -la ~/workspace 2>/dev/null; ls -la /home 2>/dev/null; find / -name 'pocket-opencode' -type d 2>/dev/null | head -20\",\"host\":\"10.24.7.91\",\"password\":\"{SENSITIVE:secret:1}\",\"username\":\"{SENSITIVE:account:1}\"}"}}]},"finish_reason":null}]}` + "\n\n",
		`data: {"id":"chatcmpl-dm5564zn8d1k","object":"chat.completion.chunk","created":1792040021,"model":"grok-4.6","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1007,"completion_tokens":137,"total_tokens":1144}}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}
}

func TestR1032CProductionChainReplayKeepsToolArgs(t *testing.T) {
	frames := r1032cLoadFrames()
	sm := SanitizeMap{
		"{SENSITIVE:secret:1}":  "Xk9#mZ2pQ8",
		"{SENSITIVE:account:1}": "deploy",
	}
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	complianceHook := outputcompliance.NewOutputComplianceInterceptor(&r1032cChecker{}, nil)
	chain := response.NewInterceptorChain(guard, restore, complianceHook)

	ctx := WithSanitizeMap(context.Background(), sm)
	meta := &response.StreamMeta{SessionID: "r1032c", RequestID: "r1032c", TenantID: "t", State: response.NewStreamState()}

	var wire strings.Builder
	for i, fr := range frames {
		res, err := chain.InterceptStreamChunk(ctx, []byte(fr), meta)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if res == nil {
			wire.WriteString(fr)
			continue
		}
		if res.ShouldBlock {
			t.Fatalf("frame %d blocked", i)
		}
		if res.SuppressChunk && len(res.ModifiedChunk) == 0 {
			t.Logf("frame %d suppressed without replacement (held)", i)
			continue
		}
		wire.WriteString(string(res.ModifiedChunk))
	}
	// 终态冲刷（生产在流结束时调 FlushStreamPending）
	var fp response.StreamPendingFlusher = chain
	pending, err := fp.FlushStreamPending(ctx, meta)
	if err != nil {
		t.Fatalf("flush pending: %v", err)
	}
	wire.WriteString(string(pending))
	out := wire.String()
	t.Logf("wire: %.500s", out)
	// arguments 在 wire 上是 JSON 字符串值（引号转义），按值本身断言。
	if !strings.Contains(out, "Xk9#mZ2pQ8") {
		t.Fatalf("restored password missing from client wire: %q", out)
	}
	if !strings.Contains(out, "deploy") {
		t.Fatalf("restored username missing from client wire: %q", out)
	}
	if !strings.Contains(out, "10.24.7.91") {
		t.Fatalf("literal host missing from client wire: %q", out)
	}
	if strings.Contains(out, "SENSITIVE") {
		t.Fatalf("marker leaked to client wire: %q", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Fatalf("terminal missing from client wire: %q", out)
	}
}
