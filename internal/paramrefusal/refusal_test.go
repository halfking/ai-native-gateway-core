package paramrefusal

// refusal_test.go — P0-1 记忆表判据：TTL 过期、键隔离、滑动续期、
// 并发安全（-race）、黑名单/白名单谓词、线体键派生。
// 同包白盒测试（直接设 nowFn 注入时钟，不暴露生产 API）。

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func newTestStore() *Store {
	s := NewStore()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return base }
	// NewStore samples wall time; sweep state must share the injected origin.
	// Otherwise advancing this fixed clock can still precede lastSweep.
	s.lastSweep = base
	return s
}

func TestRememberAndStrippableFields(t *testing.T) {
	s := newTestStore()
	s.Remember(36, "grok-4.6", "openai-responses", "metadata")
	got := s.StrippableFields(36, "grok-4.6", "openai-responses")
	if !reflect.DeepEqual(got, []string{"metadata"}) {
		t.Fatalf("StrippableFields = %v, want [metadata]", got)
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
}

func TestRememberDropsNonStrippableFields(t *testing.T) {
	s := newTestStore()
	// 黑名单与未登记字段不进记忆（上层兜底，正常不该送进来）。
	s.Remember(36, "grok-4.6", "openai-responses", "messages", "totally_unknown", "stream_options")
	got := s.StrippableFields(36, "grok-4.6", "openai-responses")
	if !reflect.DeepEqual(got, []string{"stream_options"}) {
		t.Fatalf("StrippableFields = %v, want [stream_options] only", got)
	}
}

func TestKeyIsolation(t *testing.T) {
	s := newTestStore()
	s.Remember(36, "grok-4.6", "openai-responses", "metadata")
	cases := []struct {
		provider int
		model    string
		wire     string
	}{
		{37, "grok-4.6", "openai-responses"},   // 不同 provider
		{36, "grok-4.7", "openai-responses"},   // 不同 model
		{36, "grok-4.6", "openai-completions"}, // 不同 wire
		{36, "GROK-4.6", "openai-responses"},   // 大小写不敏感 → 命中（同键归一）
	}
	for i, tc := range cases {
		got := s.StrippableFields(tc.provider, tc.model, tc.wire)
		if i == len(cases)-1 {
			// 大小写归一是刻意设计（模型名大小写漂移不该分裂记忆）。
			if len(got) != 1 {
				t.Fatalf("case %d: normalized key should hit, got %v", i, got)
			}
			continue
		}
		if len(got) != 0 {
			t.Fatalf("case %d (%+v): expected isolation, got %v", i, tc, got)
		}
	}
}

func TestTTLExpiry(t *testing.T) {
	s := newTestStore()
	base := s.nowFn()
	s.Remember(36, "grok-4.6", "openai-responses", "metadata", "service_tier")
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
	// TTL 内。
	s.nowFn = func() time.Time { return base.Add(fieldTTL - time.Minute) }
	if got := s.StrippableFields(36, "grok-4.6", "openai-responses"); len(got) != 2 {
		t.Fatalf("within TTL = %v, want both fields", got)
	}
	// TTL 外：读路径懒清理，键随最后一个字段一起消失。
	s.nowFn = func() time.Time { return base.Add(fieldTTL + time.Minute) }
	if got := s.StrippableFields(36, "grok-4.6", "openai-responses"); got != nil {
		t.Fatalf("after TTL = %v, want nil", got)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after expiry = %d, want 0", s.Len())
	}
}

func TestSlidingRenewal(t *testing.T) {
	s := newTestStore()
	base := s.nowFn()
	s.Remember(36, "grok-4.6", "openai-responses", "metadata")
	// 23h 后再次被拒 → 时间戳刷新。
	s.nowFn = func() time.Time { return base.Add(23 * time.Hour) }
	s.Remember(36, "grok-4.6", "openai-responses", "metadata")
	// 原到期点（首次 +24h）已过，但滑动续期让它活到 23h+24h。
	s.nowFn = func() time.Time { return base.Add(fieldTTL + time.Hour) }
	if got := s.StrippableFields(36, "grok-4.6", "openai-responses"); len(got) != 1 {
		t.Fatalf("sliding renewal failed: %v", got)
	}
}

func TestSweepClearsExpiredEntries(t *testing.T) {
	s := newTestStore()
	base := s.nowFn()
	s.Remember(1, "m1", "w1", "metadata")
	s.Remember(2, "m2", "w2", "service_tier")
	// 越过 TTL + sweepInterval（sweepLocked 由 Remember/Len 触发）。
	s.nowFn = func() time.Time { return base.Add(fieldTTL + 2*sweepInterval) }
	s.Remember(3, "m3", "w3", "logprobs")
	if s.Len() != 1 {
		t.Fatalf("Len after sweep = %d, want 1 (only the fresh key)", s.Len())
	}
	if got := s.StrippableFields(3, "m3", "w3"); len(got) != 1 {
		t.Fatalf("fresh entry lost by sweep: %v", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Remember(n, "model", "wire", "metadata", "logprobs")
				_ = s.StrippableFields(n, "model", "wire")
				_ = s.Len()
			}
		}(i)
	}
	wg.Wait()
	if s.Len() != 8 {
		t.Fatalf("Len = %d, want 8 distinct keys", s.Len())
	}
}

func TestStrippablePredicate(t *testing.T) {
	// 白名单家族成员。
	for _, f := range []string{"stream_options", "thinking", "reasoning_effort", "reasoning", "metadata", "parallel_tool_calls", "service_tier", "logprobs", "top_logprobs", "web_search_options", "prediction"} {
		if !Strippable(f) {
			t.Errorf("Strippable(%q) = false, want true", f)
		}
	}
	// 黑名单核心字段（大小写变体同样拒）。
	for _, f := range []string{"model", "messages", "tools", "tool_choice", "stream", "max_tokens", "temperature", "top_p", "stop", "n", "user", "input", "contents", "Messages", " TOOLS "} {
		if Strippable(f) {
			t.Errorf("Strippable(%q) = true, want false (core blacklist)", f)
		}
	}
	// 未登记 / 畸形 / 点号路径 / 超长。
	for _, f := range []string{"", "totally_unknown", "reasoning.effort", "stream_options.include_usage", "../../etc/passwd", "a b", repeat("a", 65)} {
		if Strippable(f) {
			t.Errorf("Strippable(%q) = true, want false", f)
		}
	}
}

func TestStripCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// vapeur 实测：点名嵌套路径 → 顶层键是首段；子键 include_usage
		// 不在可选家族（fail-closed，未登记不剔），只有 stream_options 候选。
		{name: "nested_path_vapeur", in: "stream_options.include_usage", want: []string{"stream_options"}},
		// pydantic loc 命名空间已由提取层剥掉；这里防御二次送达。
		{name: "pydantic_body_ns", in: "body.metadata", want: []string{"metadata"}},
		{name: "bare_field", in: "thinking", want: []string{"thinking"}},
		{name: "blacklist_never_candidate", in: "messages", want: nil},
		{name: "unknown_never_candidate", in: "kimi_future_param", want: nil},
		{name: "quoted_input", in: "'metadata'", want: []string{"metadata"}},
		{name: "empty", in: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripCandidates(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("StripCandidates(%q) = %v, want nil", tc.in, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("StripCandidates(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
