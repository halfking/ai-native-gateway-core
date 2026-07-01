package integration

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func TestBuildFullPipeline(t *testing.T) {
	p := BuildFullPipeline(MinimalDeps{})
	if p == nil {
		t.Fatal("BuildFullPipeline returned nil")
	}
	stages := p.Stages()
	if len(stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(stages))
	}
}

func TestBuildFullPipeline_CustomCompressMax(t *testing.T) {
	p := BuildFullPipeline(MinimalDeps{CompressMaxTokens: 2048})
	if p == nil {
		t.Fatal("BuildFullPipeline returned nil")
	}
}

func TestBuildFullPipeline_Execute(t *testing.T) {
	p := BuildFullPipeline(MinimalDeps{})
	env := domain.NewRequestEnvelope(context.Background(), nil)
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
}

func TestBuildRoutingOnlyPipeline(t *testing.T) {
	p := BuildRoutingOnlyPipeline()
	if p == nil {
		t.Fatal("BuildRoutingOnlyPipeline returned nil")
	}
	stages := p.Stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].Name != "routing" {
		t.Errorf("stage name = %q, want routing", stages[0].Name)
	}
}

func TestBuildTransformOnlyPipeline_Default(t *testing.T) {
	p := BuildTransformOnlyPipeline(0) // <=0 → default
	if p == nil {
		t.Fatal("BuildTransformOnlyPipeline returned nil")
	}
	stages := p.Stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
}

func TestBuildTransformOnlyPipeline_Custom(t *testing.T) {
	p := BuildTransformOnlyPipeline(2048)
	if p == nil {
		t.Fatal("BuildTransformOnlyPipeline returned nil")
	}
}

func TestBuildStreamingOnlyPipeline(t *testing.T) {
	p := BuildStreamingOnlyPipeline()
	if p == nil {
		t.Fatal("BuildStreamingOnlyPipeline returned nil")
	}
	stages := p.Stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].Name != "post_upstream" {
		t.Errorf("stage name = %q, want post_upstream", stages[0].Name)
	}
}
