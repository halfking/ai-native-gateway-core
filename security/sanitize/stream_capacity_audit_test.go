package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func auditStreamFrame(t *testing.T, size int, terminal bool) []byte {
	t.Helper()
	var payload any = map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": ""}}}}
	if terminal {
		payload = map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": ""}}}}}}
	}
	empty := marshalAuditBody(t, payload)
	padding := bytes.Repeat([]byte("a"), size-len(empty)-len("data: \n\n"))
	// Both shapes contain exactly one empty string value.
	raw := bytes.Replace(empty, []byte(`""`), append(append([]byte{'"'}, padding...), '"'), 1)
	if !json.Valid(raw) {
		t.Fatal("invalid fixture")
	}
	return append(append([]byte("data: "), raw...), '\n', '\n')
}

// B/op is cumulative Go allocation, not live heap/RSS. The wire cap is not
// a promise that the parsed frames and regex working memory occupy 1 MiB.
func BenchmarkStorageAuditStreamCapacity(b *testing.B) {
	for _, frameSize := range []int{128, 8192, 1 << 20} {
		b.Run(fmt.Sprint(frameSize), func(b *testing.B) {
			prefix, suffix := `data: {"choices":[{"index":0,"delta":{"content":"`, "\"}}]}\n\n"
			frame := []byte(prefix + strings.Repeat("a", frameSize-len(prefix)-len(suffix)) + suffix)
			frames := ((1 << 20) - len("data: [DONE]\n\n")) / len(frame)
			if frames == 0 {
				frames = 1
			}
			s, _ := NewSanitizer(NewPatternDetector())
			guard := NewOutputSensitiveInterceptor(s, OutputMask)
			b.ReportAllocs()
			b.SetBytes(int64(frames * len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				meta := &response.StreamMeta{State: response.NewStreamState()}
				for j := 0; j < frames; j++ {
					result, err := guard.InterceptStreamChunk(context.Background(), frame, meta)
					if err != nil || result == nil || result.ShouldBlock || !result.SuppressChunk {
						b.Fatalf("pending failed: %+v %v", result, err)
					}
				}
				out, err := guard.FlushStreamPending(context.Background(), meta)
				if err != nil || len(out) != frames*len(frame) {
					b.Fatalf("flush len=%d err=%v", len(out), err)
				}
			}
			b.ReportMetric(float64(frames), "frames/op")
		})
	}
}

// ★★★ 存储审计的容量契约（2026-10-02 审计轮定下；★ 2026-10-29 误报轮 +
//
//	审计轮**改了契约的另一半** —— 这里必须同时钉住两半）★★★
//
// ① **缓冲有界**（存储侧的记忆体契约，★ 没变）：
//
//	pending + 当前完整帧 ≤ cap ⇒ 扣住；超过 ⇒ **缓冲必须被释放**
//	（否则记忆体就会越界）★★
//
// ② **超限不再杀请求**（★ 2026-10-29 误报轮的修复 ⇒ 契约**变了**）：
//
//	旧的「超过上限 ⇒ 阻断（fail-closed）」是 211 生产的一台**误报机器**
//	（★ 6 例/24h：★ 已经流出 3806~10529 个 chunk 的请求被整条作废，★ 且审核单
//	是空白的 ⇒ 管理员既看不出是哪条规则也无法批准）★★
//	⇒ ★★ 现在默认 **release**：★ 越界 ⇒ 把已扣下的帧**放行** + 后续帧逐帧放行
//	  + **大声记录**（WARN + 审核单 + 规则名）★★★
//	⇒ ★★ 「阻断」那条路仍留着（★ CAP_MODE=block），★ 但它从此**带元数据**
//	  （★ 审核单能说出「是缓冲越界，不是规则命中」）★★★
//
// ★★ 所以这条测试断言的是「**不阻断 + 降级放行 + 缓冲被释放**」★★
//
//	—— 而**不是**旧契约里的「超限即阻断」★★
func TestStorageAuditStreamTerminalCapacity(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name              string
		pending, terminal int
		degrade           bool
	}{
		{"terminal_at_limit", 0, limit, false},
		{"pending_plus_terminal_at_limit", 512, limit - 512, false},
		{"terminal_over_limit", 0, limit + 1, true},
		{"pending_plus_terminal_over_limit", 512, limit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewSanitizer(NewPatternDetector())
			guard := NewOutputSensitiveInterceptor(s, OutputMask)
			meta := &response.StreamMeta{State: response.NewStreamState()}
			if tc.pending > 0 {
				result, err := guard.InterceptStreamChunk(context.Background(), auditStreamFrame(t, tc.pending, false), meta)
				if err != nil || result == nil || !result.SuppressChunk || result.ShouldBlock {
					t.Fatalf("pending: %+v %v", result, err)
				}
			}
			result, err := guard.InterceptStreamChunk(context.Background(), auditStreamFrame(t, tc.terminal, true), meta)
			if err != nil {
				t.Fatal(err)
			}
			// ★★ 任何情况下都**不许阻断**（★ 越界不是内容违规）★★
			if result != nil && result.ShouldBlock {
				t.Fatalf("越界不许阻断请求（误报轮的修复点）: %+v", result)
			}
			if !tc.degrade {
				// ★ 未越界：★ 终态帧走**正常释放**路径（★ 待检缓冲里的内容在此
				//   一次性上屏，★ 这是终态的正常形状，★ 有时没有额外结果）★★
				//   ⇒ ★★ 判据不是「有没有 ModifiedChunk」，★ 而是「**有没有被
				//   降级**」★★
				if result != nil && result.Metadata != nil {
					if degraded, _ := result.Metadata["policy_degraded"].(bool); degraded {
						t.Fatalf("未越界不许标降级: %+v", result.Metadata)
					}
				}
				return
			}
			// ★ 越界：降级放行（★ 把此前扣下的帧还回去）★★
			if result == nil || !result.SuppressChunk || len(result.ModifiedChunk) == 0 {
				t.Fatalf("越界应当降级放行（把扣下的帧还回去）: %+v", result)
			}
			// ★ 降级也要留痕：规则名 + 原因 + 「这是降级不是拦截」★★
			meta2 := result.Metadata
			if meta2 == nil {
				t.Fatal("降级必须带元数据")
			}
			if rule, _ := meta2["policy_rule"].(string); rule == "" {
				t.Fatalf("降级必须带规则名: %+v", meta2)
			}
			if degraded, _ := meta2["policy_degraded"].(bool); !degraded {
				t.Fatalf("降级必须带 policy_degraded 标记: %+v", meta2)
			}
			// ★★ 缓冲必须**被释放**（存储侧的有界契约）：★ 上面的 ModifiedChunk
			//   就是「扣下的帧被还回去」的证据 ⇒ ★★ pending 没有继续累积 ⇒ ★★
			//   记忆体上界仍然成立（★ 这是存储审计那半条契约）★★
		})
	}
}

// ★★ fail-closed 模式仍然阻断，★ 但**必须带规则名与原因**（★ 不许再静默）★★
func TestStorageAuditStreamTerminalCapacityBlockModeCarriesRule(t *testing.T) {
	t.Setenv("LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE", "block")
	const limit = 1 << 20
	s, _ := NewSanitizer(NewPatternDetector())
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	meta := &response.StreamMeta{State: response.NewStreamState()}
	result, err := guard.InterceptStreamChunk(context.Background(), auditStreamFrame(t, limit+1, true), meta)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.ShouldBlock {
		t.Fatalf("block 模式应当阻断: %+v", result)
	}
	if rule, _ := result.Metadata["policy_rule"].(string); rule == "" {
		t.Fatalf("block 模式必须带规则名: %+v", result.Metadata)
	}
	if reason, _ := result.Metadata["policy_reason"].(string); reason == "" {
		t.Fatalf("block 模式必须带原因: %+v", result.Metadata)
	}
}
