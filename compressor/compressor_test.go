package compressor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMode_String(t *testing.T) {
	cases := []struct {
		m    Mode
		want string
	}{
		{ModeOff, "off"},
		{ModeAutoThreshold, "auto_threshold"},
		{ModeOn4xx, "on_4xx"},
		{Mode(99), "unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("Mode(%d).String(): want %q, got %q", int(tc.m), tc.want, got)
		}
	}
}

func TestEnvMode_Defaults(t *testing.T) {
	// Unset → ModeOn4xx (v7 §2 default per user Q1).
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "")
	if got := envMode(); got != ModeOn4xx {
		t.Errorf("envMode unset: want ModeOn4xx, got %v", got)
	}
}

func TestEnvMode_ExplicitValues(t *testing.T) {
	cases := []struct {
		raw  string
		want Mode
	}{
		{"0", ModeOff},
		{"1", ModeAutoThreshold},
		{"2", ModeOn4xx},
		// Bad values fall back to ModeOn4xx (defensive default).
		{"", ModeOn4xx},
		{"3", ModeOn4xx},
		{"abc", ModeOn4xx},
		{"  ", ModeOn4xx},
	}
	for _, tc := range cases {
		t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", tc.raw)
		got := envMode()
		if got != tc.want {
			t.Errorf("envMode(%q): want %v, got %v", tc.raw, tc.want, got)
		}
	}
}

func TestNewCompressor_ReadsEnvMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "1")
	c := NewCompressor()
	if c.Mode() != ModeAutoThreshold {
		t.Errorf("NewCompressor with mode=1: want ModeAutoThreshold, got %v", c.Mode())
	}
	if c.Estimator() == nil {
		t.Error("Estimator should not be nil")
	}
}

func TestCompressor_NilSafety(t *testing.T) {
	var c *Compressor
	if c.Mode() != ModeOff {
		t.Error("nil Mode() should return ModeOff")
	}
	if c.Estimator() != nil {
		t.Error("nil Estimator() should return nil")
	}
	if c.ShouldCompressPreRequest([]byte("hello"), 128000) {
		t.Error("nil ShouldCompressPreRequest should return false")
	}
}

func TestCompress_ModeOff_NoOp(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "0")
	c := NewCompressor()
	body := make([]byte, 1_000_000)
	newBody, reason, strategy, meta, didCompress := c.Compress(body, 128000)
	if didCompress {
		t.Error("mode=off must not compress")
	}
	if reason != ReasonNone || strategy != StrategyNoop {
		t.Errorf("expected ReasonNone/StrategyNoop, got %v/%v", reason, strategy)
	}
	if string(newBody) != string(body) {
		t.Error("body must be returned unchanged")
	}
	if meta.BytesBefore != len(body) {
		t.Errorf("BytesBefore: want %d, got %d", len(body), meta.BytesBefore)
	}
}

func TestCompress_ModeOn4xx_RejectsPreRequest(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	body := make([]byte, 500_000)
	newBody, reason, strategy, _, didCompress := c.Compress(body, 128000)
	if didCompress {
		t.Error("mode=on_4xx must not pre-request-compress (caller must use CompressAfter4xx)")
	}
	if reason != ReasonNone || strategy != StrategyNoop {
		t.Errorf("expected ReasonNone/StrategyNoop, got %v/%v", reason, strategy)
	}
	if string(newBody) != string(body) {
		t.Error("body must be returned unchanged")
	}
}

func TestCompress_ModeAutoThreshold_NoOpForSmallBody(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "1")
	c := NewCompressor()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_, reason, strategy, meta, didCompress := c.Compress(body, 128000)
	if didCompress {
		t.Error("small body must not trigger compression")
	}
	if reason != ReasonNone || strategy != StrategyNoop {
		t.Errorf("expected ReasonNone/StrategyNoop, got %v/%v", reason, strategy)
	}
	if !strings.Contains(meta.ReasonDetail, "under threshold") {
		t.Errorf("ReasonDetail should mention under threshold, got %q", meta.ReasonDetail)
	}
	if meta.ThresholdBytes == 0 {
		t.Error("ThresholdBytes should be populated even on noop (for metrics)")
	}
	if meta.ContextWindowUsed == nil || *meta.ContextWindowUsed != 128000 {
		t.Error("ContextWindowUsed should be 128000")
	}
}

func TestCompress_ModeAutoThreshold_TriggersOnBigBody(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "1")
	c := NewCompressor()
	// Build a body large enough to exceed 358K threshold (window=128K).
	// Need 20+ rounds of user/assistant so the mechanical trimmer has
	// something to drop (single-message bodies can't be trimmed per v7 §6).
	rounds := []string{}
	for i := 0; i < 20; i++ {
		chunk := strings.Repeat("a", 20_000)
		rounds = append(rounds,
			`{"role":"user","content":"`+chunk+`"}`,
			`{"role":"assistant","content":"`+chunk+`"}`,
		)
	}
	bodyStr := `{"model":"m","messages":[{"role":"system","content":"S"}`
	for _, m := range rounds {
		bodyStr += "," + m
	}
	bodyStr += "]}"
	body := []byte(bodyStr)
	_, reason, strategy, meta, didCompress := c.Compress(body, 128000)
	if !didCompress {
		t.Fatal("big body must trigger compression")
	}
	if reason != ReasonAutoThreshold {
		t.Errorf("reason: want ReasonAutoThreshold, got %v", reason)
	}
	if strategy != StrategyMechanicalTrim {
		t.Errorf("strategy: want StrategyMechanicalTrim, got %v", strategy)
	}
	if meta.BytesBefore != len(body) {
		t.Errorf("BytesBefore: want %d, got %d", len(body), meta.BytesBefore)
	}
	if meta.BytesAfter == 0 || meta.BytesAfter >= meta.BytesBefore {
		t.Error("BytesAfter should be smaller than BytesBefore after trim")
	}
	if meta.TokensBefore == nil || *meta.TokensBefore <= 0 {
		t.Error("TokensBefore should be populated")
	}
	if meta.DroppedMessages == nil || *meta.DroppedMessages <= 0 {
		t.Error("DroppedMessages should be positive")
	}
}

func TestCompress_ModeAutoThreshold_UnknownWindowNoCompress(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "1")
	c := NewCompressor()
	body := make([]byte, 1_000_000) // huge
	_, _, strategy, _, didCompress := c.Compress(body, 0) // unknown window
	if didCompress {
		t.Error("unknown context window must not trigger compression")
	}
	if strategy != StrategyNoop {
		t.Errorf("strategy: want StrategyNoop, got %v", strategy)
	}
}

func TestCompressAfter4xx_Triggers(t *testing.T) {
	t.Setenv("LLM_GATEWAY_COMPRESSION_MODE", "2")
	c := NewCompressor()
	rounds := []string{}
	for i := 0; i < 20; i++ {
		chunk := strings.Repeat("a", 20_000)
		rounds = append(rounds,
			`{"role":"user","content":"`+chunk+`"}`,
			`{"role":"assistant","content":"`+chunk+`"}`,
		)
	}
	bodyStr := `{"model":"m","messages":[`
	for i, m := range rounds {
		if i == 0 {
			bodyStr += m
		} else {
			bodyStr += "," + m
		}
	}
	bodyStr += "]}"
	body := []byte(bodyStr)
	_, reason, strategy, meta, didCompress := c.CompressAfter4xx(body, 128000)
	if !didCompress {
		t.Fatal("CompressAfter4xx must succeed on big body + known window")
	}
	if reason != ReasonOn4xx {
		t.Errorf("reason: want ReasonOn4xx, got %v", reason)
	}
	if strategy != StrategyMechanicalTrim {
		t.Errorf("strategy: want StrategyMechanicalTrim, got %v", strategy)
	}
	if meta.ReasonDetail == "" {
		t.Error("ReasonDetail should be populated")
	}
}

func TestCompressAfter4xx_UnknownWindow(t *testing.T) {
	c := NewCompressor()
	body := make([]byte, 100_000)
	_, reason, strategy, _, didCompress := c.CompressAfter4xx(body, 0)
	if didCompress {
		t.Error("unknown window must not compress")
	}
	if reason != ReasonOn4xx {
		t.Errorf("reason: want ReasonOn4xx, got %v", reason)
	}
	if strategy != StrategyNoop {
		t.Errorf("strategy: want StrategyNoop, got %v", strategy)
	}
}

func TestMeta_Marshal(t *testing.T) {
	m := Meta{
		BytesBefore:       358400,
		BytesAfter:        105000,
		SystemRetained:    true,
		FirstUserRetained: false,
	}
	out := m.Marshal()
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["bytes_before"].(float64) != 358400 {
		t.Errorf("bytes_before wrong: %v", got["bytes_before"])
	}
	if got["system_retained"] != true {
		t.Errorf("system_retained wrong: %v", got["system_retained"])
	}
	if got["first_user_retained"] != false {
		t.Errorf("first_user_retained wrong: %v", got["first_user_retained"])
	}
}

func TestCountDroppedMessages(t *testing.T) {
	before := []byte(`{"messages":[
		{"role":"user","content":"1"},
		{"role":"assistant","content":"2"},
		{"role":"user","content":"3"},
		{"role":"assistant","content":"4"}
	]}`)
	after := []byte(`{"messages":[
		{"role":"user","content":"3"},
		{"role":"assistant","content":"4"}
	]}`)
	if got := countDroppedMessages(before, after); got != 2 {
		t.Errorf("countDroppedMessages: want 2, got %d", got)
	}
	// after >= before → return 0
	if got := countDroppedMessages(before, before); got != 0 {
		t.Errorf("same body: want 0, got %d", got)
	}
	// no messages in either → return 0
	if got := countDroppedMessages([]byte(`{}`), []byte(`{}`)); got != 0 {
		t.Errorf("empty bodies: want 0, got %d", got)
	}
}
