package compression

import (
	"fmt"
	"strings"
	"testing"
)

// toolFocusedFixture 生成一条命中 fileContent 策略的 tool 消息 body。
// 40 行代码 > keep(20)+tail(5)，压缩后含 "[15 lines elided]"。
func toolFocusedFixture() []byte {
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("const value%d = %d;", i, i))
	}
	var b strings.Builder
	b.WriteString(`{"messages":[{"role":"tool","tool_call_id":"call_1","content":"`)
	b.WriteString(strings.Join(lines, "\\n"))
	b.WriteString(`"}]}`)
	return []byte(b.String())
}

// TestCompressor_ToolFocusedStage_FailOpenWhenOff 验证 ToolFocusedStageEnabled=false
// 时 Compress 行为零变化（ReasonDetail 不含 toolfocused）。
func TestCompressor_ToolFocusedStage_FailOpenWhenOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2") // on_4xx，让 CompressAfter4xx 工作
	c := NewCompressor()
	_, _, _, meta, _ := c.CompressAfter4xx(toolFocusedFixture(), 128000)
	if strings.Contains(meta.ReasonDetail, "toolfocused") {
		t.Errorf("ToolFocused should not run when ToolFocusedStageEnabled=false; ReasonDetail=%q", meta.ReasonDetail)
	}
}

// TestCompressor_ToolFocusedStage_RunsWhenOn 验证 ToolFocusedStageEnabled=true 时
// Tool-Focused stage 运行（ReasonDetail 记录命中策略）。
func TestCompressor_ToolFocusedStage_RunsWhenOn(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.ToolFocusedStageEnabled = true
	_, _, _, meta, _ := c.CompressAfter4xx(toolFocusedFixture(), 128000)
	if !strings.Contains(meta.ReasonDetail, "toolfocused strategies=[fileContent]") {
		t.Errorf("ToolFocused should run when ToolFocusedStageEnabled=true; ReasonDetail=%q", meta.ReasonDetail)
	}
}

// TestCompressor_ToolFocusedStage_CompressesToolContent 端到端验证：
// stage 开启时输出 body 的 tool content 真的被压缩。
func TestCompressor_ToolFocusedStage_CompressesToolContent(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.ToolFocusedStageEnabled = true
	out, _, _, _, _ := c.CompressAfter4xx(toolFocusedFixture(), 128000)
	s := string(out)
	if !strings.Contains(s, "[15 lines elided]") {
		t.Errorf("tool content should contain elision marker; body=%q", s)
	}
	if len(out) >= len(toolFocusedFixture()) {
		// stage 输出经 NeverWorse 守卫后不应超过原始输入。
		t.Errorf("output (%d bytes) should be smaller than input (%d bytes)", len(out), len(toolFocusedFixture()))
	}
}

// TestCompressor_ToolFocusedStage_IdempotentMarker 验证幂等：
// 已压缩（[COMPRESSED: 前缀）的 tool content 不会再次命中 stage。
func TestCompressor_ToolFocusedStage_IdempotentMarker(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.ToolFocusedStageEnabled = true
	body := []byte(`{"messages":[{"role":"tool","content":"[COMPRESSED:summary] prior compression"}]}`)
	_, _, _, meta, _ := c.CompressAfter4xx(body, 128000)
	if strings.Contains(meta.ReasonDetail, "toolfocused") {
		t.Errorf("already-compressed content must be skipped; ReasonDetail=%q", meta.ReasonDetail)
	}
}

// TestStrategyToolFocused_Constant 验证新增常量。
func TestStrategyToolFocused_Constant(t *testing.T) {
	if StrategyToolFocused != "toolfocused" {
		t.Errorf("StrategyToolFocused = %q, want %q", StrategyToolFocused, "toolfocused")
	}
}

// TestGuardStageToolFocused 验证 guard 常量。
func TestGuardStageToolFocused(t *testing.T) {
	if GuardStageToolFocused != "toolfocused" {
		t.Errorf("GuardStageToolFocused = %q, want %q", GuardStageToolFocused, "toolfocused")
	}
}
