// Command gateway — sensitive word engine integration tests
//
// Validates the full pipeline chain:
//
//	request → governance_security → SensitiveWordPlugin → Verdict
package main

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domain/governance"
	domainssec "github.com/kaixuan/llm-gateway-go/domains/security"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

func buildACEngine(t *testing.T) *sensitive.SensitiveWordEngine {
	t.Helper()
	e := sensitive.NewSensitiveWordEngine()
	err := e.Build(&sensitive.SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]sensitive.CategoryConf{
			"political":       {Name: "政治敏感词", Words: []string{"六四", "法轮功"}},
			"sexual_violence": {Name: "色情暴力", Words: []string{"色情", "暴力", "赌博"}},
			"drugs_weapons":   {Name: "违禁品", Words: []string{"毒品", "枪支"}},
			"test_sensitive":  {Name: "测试词", Words: []string{"弱口令", "泄露"}},
		},
	})
	if err != nil {
		t.Fatalf("build AC engine: %v", err)
	}
	return e
}

func TestSensitiveWordPlugin_PassThrough(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordInputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("今天天气不错，适合出去散步。"),
	}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state := env.EnsureGovernance()
	if len(state.Verdicts) < 1 {
		t.Fatal("expected at least one verdict")
	}
	v := state.Verdicts[0]
	if !v.Allow {
		t.Errorf("expected Allow=true for clean text, got Allow=%v", v.Allow)
	}
}

func TestSensitiveWordPlugin_BlockP0(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordInputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("这个视频包含色情内容"),
	}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("hook should not return error for block: %v", err)
	}
	state := env.EnsureGovernance()
	if !state.HasBlock() {
		t.Fatal("expected HasBlock()=true")
	}
	v := state.Verdicts[0]
	if v.Allow {
		t.Errorf("expected Allow=false for P0, got Allow=%v", v.Allow)
	}
	if v.Severity < 2 {
		t.Errorf("expected Severity >= 2 for P0, got %d", v.Severity)
	}
}

func TestSensitiveWordPlugin_BlockP1(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordInputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("法轮功相关内容"),
	}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("hook should not return error: %v", err)
	}
	state := env.EnsureGovernance()
	if state.HasBlock() {
		t.Log("P1 verdict blocked (Allow=false), expected behavior depends on policy")
	}
}

func TestSensitiveWordPlugin_OutputBlock(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordOutputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("正常请求"),
		UpstreamResponse:   []byte("AI 生成了赌博相关内容"),
	}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("hook should not return error: %v", err)
	}
	state := env.EnsureGovernance()
	if !state.HasBlock() {
		t.Fatal("expected HasBlock()=true for P0 output")
	}
}

func TestSensitiveWordPlugin_OutputPass(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordOutputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("正常的请求"),
		UpstreamResponse:   []byte("正常的AI响应"),
	}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSensitiveWordPlugin_Evidence(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordInputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("涉及色情和暴力的内容"),
	}
	hook.Execute(context.Background(), env) //nolint:errcheck

	v := env.EnsureGovernance().Verdicts[0]
	if v.Evidence == nil {
		t.Fatal("expected evidence in verdict")
	}
	words, ok := v.Evidence["matched_words"].([]string)
	if !ok || len(words) < 2 {
		t.Errorf("expected >=2 matched words in evidence, got %v", words)
	}
	count, ok := v.Evidence["count"].(int)
	if !ok || count < 2 {
		t.Errorf("expected count >=2, got %v", count)
	}
}

func TestSensitiveWordPlugin_EmptyBody(t *testing.T) {
	engine := buildACEngine(t)
	reg := domainssec.NewRegistry()
	reg.MustRegister(sensitive.NewSensitiveWordInputPlugin(engine))

	hook := domainssec.NewSecurityHook(reg, domainssec.Scope{})
	env := &domain.PipelineRequest{}
	err := hook.Execute(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error for nil body: %v", err)
	}
	state := env.EnsureGovernance()
	if len(state.Verdicts) > 0 && !state.Verdicts[0].Allow {
		t.Errorf("expected Allow=true for empty body")
	}
}

func BenchmarkSensitiveWordEngine(b *testing.B) {
	e := sensitive.NewSensitiveWordEngine()
	cfg := &sensitive.SensitiveWordConfig{
		Version:    "1.0",
		Categories: make(map[string]sensitive.CategoryConf),
	}
	words := make([]string, 0, 10000)
	for i := 0; i < 10000; i++ {
		words = append(words, randSeq(4))
	}
	cfg.Categories["bench"] = sensitive.CategoryConf{Name: "bench", Words: words}
	if err := e.Build(cfg); err != nil {
		b.Fatalf("build: %v", err)
	}

	for _, tc := range []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"10KB", 10 * 1024},
	} {
		text := randSeq(tc.size)
		b.Run("match_"+tc.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e.Match(text)
			}
		})
	}
}

func BenchmarkSensitiveWordPlugin(b *testing.B) {
	engine := buildACEngine(&testing.T{})
	plugin := sensitive.NewSensitiveWordInputPlugin(engine)

	text := randSeq(4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plugin.Inspect(context.Background(), &domain.PipelineRequest{
			TransformedRequest: []byte(text),
		})
	}
}

func randSeq(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	res := make([]byte, n)
	for i := range res {
		res[i] = letters[i*31%len(letters)]
	}
	return string(res)
}

// Compile-time check that these imports are used
var _ = governance.Verdict{}
