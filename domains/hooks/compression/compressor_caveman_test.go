package compression

import (
	"strings"
	"testing"
)

// TestCompressor_CavemanStage_FailOpenWhenOff 验证 CavemanStageEnabled=false 时
// Compress 行为零变化（ReasonDetail 不含 caveman）。
func TestCompressor_CavemanStage_FailOpenWhenOff(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2") // on_4xx，让 CompressAfter4xx 工作
	c := NewCompressor()
	body := []byte(`{"messages":[{"role":"user","content":"please could you help me debug this very long request that should be compressed but caveman is off"}]}`)
	_, _, _, meta, _ := c.CompressAfter4xx(body, 128000)
	if strings.Contains(meta.ReasonDetail, "caveman") {
		t.Errorf("Caveman should not run when CavemanStageEnabled=false; ReasonDetail=%q", meta.ReasonDetail)
	}
}

// TestCompressor_CavemanStage_RunsWhenOn 验证 CavemanStageEnabled=true 时 Caveman 运行。
func TestCompressor_CavemanStage_RunsWhenOn(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	c.CavemanStageEnabled = true
	// 长英文请求，含 pleasantries + polite_framing。
	body := []byte(`{"messages":[{"role":"user","content":"Please could you kindly help me with this problem. I would be very happy if you could explain how it works. Thank you so much for your help understanding this tricky bug today."}]}`)
	_, _, _, meta, _ := c.CompressAfter4xx(body, 128000)
	if !strings.Contains(meta.ReasonDetail, "caveman rules=") {
		t.Errorf("Caveman should run when CavemanStageEnabled=true; ReasonDetail=%q", meta.ReasonDetail)
	}
}

// TestStrategyCaveman_Constant 验证新增常量。
func TestStrategyCaveman_Constant(t *testing.T) {
	if StrategyCaveman != "caveman" {
		t.Errorf("StrategyCaveman = %q, want %q", StrategyCaveman, "caveman")
	}
}

// TestGuardStageCaveman 验证 guard 常量。
func TestGuardStageCaveman(t *testing.T) {
	if GuardStageCaveman != "caveman" {
		t.Errorf("GuardStageCaveman = %q, want %q", GuardStageCaveman, "caveman")
	}
}
