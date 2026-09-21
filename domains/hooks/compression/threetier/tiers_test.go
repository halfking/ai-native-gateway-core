// Package threetier tests (2026-09-01)
//
// 覆盖 Tier 枚举、Snapshot、BuildAlignments、DetectMisalignment 的核心不变量。
package threetier

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

func TestTierName(t *testing.T) {
	cases := []struct {
		tier Tier
		want string
	}{
		{TierRaw, "raw"},
		{TierCompressed, "compressed"},
		{TierSanitized, "sanitized"},
	}
	for _, c := range cases {
		if got := c.tier.Name(); got != c.want {
			t.Errorf("Tier(%d).Name() = %q, want %q", c.tier, got, c.want)
		}
		if got := c.tier.String(); got != c.want {
			t.Errorf("Tier(%d).String() = %q, want %q", c.tier, got, c.want)
		}
	}
	if got := (Tier(99)).Name(); got != "unknown" {
		t.Errorf("unknown tier should return 'unknown', got %q", got)
	}
}

func TestSnapshot_NilSafe(t *testing.T) {
	s := Snapshot(nil)
	for i, snap := range s {
		if snap.Tokens != 0 || snap.Messages != 0 || snap.HasPrefix || snap.HasMap {
			t.Errorf("nil state: snap[%d] = %+v, want zero", i, snap)
		}
	}
}

func TestSnapshot_RawAndCompressed(t *testing.T) {
	state := &compression.SessionState{
		RawTokenEstimate:     1000,
		RawMsgCount:          50,
		CompressedTokens:     600,
		CompressedMsgs:       30,
		CompressedPrefixHash: "abcdef1234567890"[:16],
	}
	s := Snapshot(state)
	if s[0].Tokens != 1000 || s[0].Messages != 50 {
		t.Errorf("raw snap = %+v", s[0])
	}
	if s[1].Tokens != 600 || s[1].Messages != 30 || !s[1].HasPrefix || s[1].PrefixSHA != "abcdef1234567890" {
		t.Errorf("compressed snap = %+v", s[1])
	}
	if s[2].HasMap {
		t.Errorf("sanitized.HasMap should be false when SanitizeMapRef empty")
	}
}

func TestSnapshot_Sanitized(t *testing.T) {
	state := &compression.SessionState{
		SanitizeMapRef: "session:sc:tenant:abc:v1",
	}
	s := Snapshot(state)
	if !s[2].HasMap {
		t.Errorf("sanitized.HasMap should be true, got %+v", s[2])
	}
}

func TestBuildAlignments_NilSafe(t *testing.T) {
	if got := BuildAlignments(nil); got != nil {
		t.Errorf("nil state: got %v want nil", got)
	}
	state := &compression.SessionState{} // empty map
	if got := BuildAlignments(state); got != nil {
		t.Errorf("empty map: got %v want nil", got)
	}
}

func TestBuildAlignments_ConvertsAlignmentMap(t *testing.T) {
	state := &compression.SessionState{
		AlignmentMap: []compression.AlignmentInfo{
			{IsCompressed: true, CompressedIndex: 0},
			{IsCompressed: false, CompressedIndex: 1},
			{IsCompressed: true, CompressedIndex: 2},
			{IsCompressed: true, CompressedIndex: -1}, // 跳过负索引
		},
	}
	got := BuildAlignments(state)
	if len(got) != 3 {
		t.Fatalf("got %d ranges, want 3", len(got))
	}
	for i, r := range got {
		if r.Tier != TierCompressed {
			t.Errorf("range[%d].Tier = %v want compressed", i, r.Tier)
		}
	}
	if got[0].Layer != "compressed" || got[0].Start != 0 || got[0].End != 1 {
		t.Errorf("range[0] = %+v", got[0])
	}
}

func TestDetectMisalignment_NoState(t *testing.T) {
	if got := DetectMisalignment(nil); got != nil {
		t.Errorf("nil state: got %v want nil", got)
	}
}

func TestDetectMisalignment_Healthy(t *testing.T) {
	state := &compression.SessionState{
		RawTokenEstimate: 1000,
		RawMsgCount:      50,
		CompressedTokens: 600,
		CompressedMsgs:   30,
	}
	if got := DetectMisalignment(state); len(got) != 0 {
		t.Errorf("healthy state should report no misalignment, got %v", got)
	}
}

func TestDetectMisalignment_CompressedExceedsRaw(t *testing.T) {
	state := &compression.SessionState{
		RawTokenEstimate: 1000,
		RawMsgCount:      50,
		CompressedTokens: 1500, // regression: 压缩后反而变多
		CompressedMsgs:   60,   // regression
	}
	got := DetectMisalignment(state)
	if len(got) != 2 {
		t.Fatalf("expected 2 misalignments (tokens + msgs), got %d: %+v", len(got), got)
	}
}

func TestDetectMisalignment_SanitizeOrphan(t *testing.T) {
	state := &compression.SessionState{
		SanitizeMapRef: "session:sc:tenant:abc:v1",
		// RawTokenEstimate / CompressedTokens 都未设置
	}
	got := DetectMisalignment(state)
	if len(got) != 1 || got[0].Tier != TierSanitized {
		t.Fatalf("sanitize orphan: got %+v, want 1 entry tier=Sanitized", got)
	}
}

func TestShortHash_Stable(t *testing.T) {
	h1 := ShortHash([]byte("hello"))
	h2 := ShortHash([]byte("hello"))
	if h1 != h2 || len(h1) != 16 {
		t.Errorf("ShortHash unstable or wrong length: %q vs %q", h1, h2)
	}
	if ShortHash([]byte("hello")) == ShortHash([]byte("world")) {
		t.Error("different inputs must produce different hashes")
	}
}