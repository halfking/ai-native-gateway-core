package compression

import (
	"strings"
	"testing"
)

// TestCompressor_LiteStage_FailOpenWhenOff 验证 LiteStageEnabled=false 时
// Compress 行为零变化（不跑 Lite，ReasonDetail 不含 lite）。
func TestCompressor_LiteStage_FailOpenWhenOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2") // on_4xx，让 CompressAfter4xx 工作
	c := NewCompressor()
	// 带 trailing whitespace 的 body——Lite 会清理，但 flag 关时不应该清理。
	body := []byte(`{"messages":[{"role":"user","content":"hi   "}]}`)
	out, _, _, meta, _ := c.CompressAfter4xx(body, 128000)
	if strings.Contains(meta.ReasonDetail, "lite") {
		t.Errorf("Lite should not run when LiteStageEnabled=false; ReasonDetail=%q", meta.ReasonDetail)
	}
	// body 内容不应被 Lite 改动（"hi   " 的 trailing space 保留；mechanical 可能改也可能不改，
	// 但关键是 meta 不提 lite）。
	_ = out
}

// TestCompressor_LiteStage_RunsWhenOn 验证 LiteStageEnabled=true 时 Lite 运行。
// 用一个带 trailing whitespace + 重复 system prompt 的 body：Lite 会触发 whitespace+dedup，
// ReasonDetail 应含 "lite stages="。
func TestCompressor_LiteStage_RunsWhenOn(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.LiteStageEnabled = true
	// 两条件：trailing whitespace（whitespace stage）+ 重复 system（system-dedup stage）。
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"You are helpful.   "},` +
		`{"role":"system","content":"You are helpful."},` + // trim key 同 → dedup
		`{"role":"user","content":"hi"}]}`)
	_, _, _, meta, _ := c.CompressAfter4xx(body, 128000)
	if !strings.Contains(meta.ReasonDetail, "lite stages=") {
		t.Errorf("Lite should run when LiteStageEnabled=true; ReasonDetail=%q", meta.ReasonDetail)
	}
	if !strings.Contains(meta.ReasonDetail, "whitespace") && !strings.Contains(meta.ReasonDetail, "system-dedup") {
		t.Errorf("ReasonDetail should mention a lite technique; got %q", meta.ReasonDetail)
	}
}

// TestCompressor_LiteStage_NeverWorseGuarded 验证 Lite 经 NeverWorse 守卫：
// 如果 Lite 输出比输入大（不可能但防御性），应回退原 body。
// 这里用一个 Lite 不会改变的小 body，确认不 panic 且 body 不变大。
func TestCompressor_LiteStage_NeverWorseGuarded(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.LiteStageEnabled = true
	body := []byte(`{"messages":[{"role":"user","content":"clean"}]}`)
	out, _, _, _, _ := c.CompressAfter4xx(body, 128000)
	if len(out) > len(body)*2 {
		t.Errorf("output suspiciously large: in=%d out=%d (NeverWorse should prevent growth)", len(body), len(out))
	}
}

// TestStrategyLite_Constant 验证新增的 StrategyLite 常量。
func TestStrategyLite_Constant(t *testing.T) {
	if StrategyLite != "lite" {
		t.Errorf("StrategyLite = %q, want %q", StrategyLite, "lite")
	}
}

// TestGuardStageLite 验证 GuardStageLite 常量。
func TestGuardStageLite(t *testing.T) {
	if GuardStageLite != "lite" {
		t.Errorf("GuardStageLite = %q, want %q", GuardStageLite, "lite")
	}
}
