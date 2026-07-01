package cache

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// TestCachePipeline_PreAndPostStages 验证 Lookup/Save Hook 可拼装到 Pipeline 的
// PreRouting 与 PostResponse 阶段，且整条流程：第一次 miss→save，第二次 hit。
func TestCachePipeline_PreAndPostStages(t *testing.T) {
	store := NewInMemoryStore()
	ttl := time.Minute

	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_routing_cache_lookup",
		Phase: pipeline.PhasePreRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCacheLookupHook(store)},
	})
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_response_cache_save",
		Phase: pipeline.PhasePostResponse,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCacheSaveHook(store, ttl)},
	})

	// 第一次请求：miss → save
	body := []byte(`{"prompt":"hello"}`)
	env1 := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: body,
		UpstreamResponse:   []byte(`{"reply":"world"}`),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := p.Execute(context.Background(), env1); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if hit, _ := env1.Metadata[MetaKeyCacheHit].(bool); hit {
		t.Fatal("first request should be a miss")
	}
	if env1.UpstreamResponse == nil {
		t.Fatal("upstream response should remain set after miss")
	}

	// 第二次请求：同 (TenantID, Model, Hash) → hit
	env2 := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: body,
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := p.Execute(context.Background(), env2); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if hit, _ := env2.Metadata[MetaKeyCacheHit].(bool); !hit {
		t.Fatal("second request should be a hit")
	}
	if string(env2.UpstreamResponse) != `{"reply":"world"}` {
		t.Fatalf("expected cached response, got %q", env2.UpstreamResponse)
	}
}

// TestCachePipeline_DifferentTenants_NotShared 验证跨租户不会命中。
func TestCachePipeline_DifferentTenants_NotShared(t *testing.T) {
	store := NewInMemoryStore()
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "pre_routing_cache_lookup",
		Phase: pipeline.PhasePreRouting,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCacheLookupHook(store)},
	})
	p.AddStage(&pipeline.PipelineStage{
		Name:  "post_response_cache_save",
		Phase: pipeline.PhasePostResponse,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCacheSaveHook(store, time.Minute)},
	})

	body := []byte(`{"p":"x"}`)
	// 租户 t1 写入
	env1 := &domain.PipelineRequest{
		TenantID:           "t1",
		TransformedRequest: body,
		UpstreamResponse:   []byte(`{"r":"t1"}`),
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := p.Execute(context.Background(), env1); err != nil {
		t.Fatalf("t1 execute: %v", err)
	}
	// 租户 t2 查询同 body → 不应命中
	env2 := &domain.PipelineRequest{
		TenantID:           "t2",
		TransformedRequest: body,
		Metadata:           map[string]any{MetaKeyModel: "gpt-4"},
	}
	if err := p.Execute(context.Background(), env2); err != nil {
		t.Fatalf("t2 execute: %v", err)
	}
	if hit, _ := env2.Metadata[MetaKeyCacheHit].(bool); hit {
		t.Fatal("cross-tenant hit should NOT happen")
	}
}
