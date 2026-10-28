package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

type stubHook struct {
	name     string
	kind     pipeline.DispatchKind
	spec     pipeline.TaskSpec
	hasSpec  bool
	priority int
}

func (h *stubHook) Name() string  { return h.name }
func (h *stubHook) Priority() int { return h.priority }
func (h *stubHook) Enabled(context.Context, *domain.PipelineRequest) bool {
	return true
}
func (h *stubHook) Execute(context.Context, *domain.PipelineRequest) error { return nil }
func (h *stubHook) OnError(context.Context, *domain.PipelineRequest, error) error {
	return nil
}
func (h *stubHook) DispatchKind() pipeline.DispatchKind { return h.kind }
func (h *stubHook) TaskSpec() pipeline.TaskSpec {
	if h.hasSpec {
		return h.spec
	}
	return pipeline.TaskSpec{}
}

type lookupMap map[string]bool

func (m lookupMap) HasHandler(taskType string) bool { return m[taskType] }

func TestValidateStage_V1_RejectsTerminatePhase(t *testing.T) {
	h := &stubHook{
		name:    "bad.auth",
		kind:    pipeline.DispatchTaskBus,
		hasSpec: true,
		spec: pipeline.TaskSpec{
			TaskType:      "x.v1",
			SchemaVersion: 1,
			IdempotencyOf: func(*domain.PipelineRequest) string { return "k" },
		},
	}
	err := pipeline.ValidateStage(&pipeline.PipelineStage{
		Name: "authentication", Phase: pipeline.PhaseAuthentication,
		Hooks: []pipeline.Hook{h},
	}, lookupMap{"x.v1": true})
	if err == nil || !strings.Contains(err.Error(), "V1") {
		t.Fatalf("want V1 error, got %v", err)
	}
}

func TestValidateStage_V2_RequiresHandler(t *testing.T) {
	h := &stubHook{
		name:    "cache.save",
		kind:    pipeline.DispatchTaskBus,
		hasSpec: true,
		spec: pipeline.TaskSpec{
			TaskType:      "cache.save.v1",
			SchemaVersion: 1,
			IdempotencyOf: func(*domain.PipelineRequest) string { return "k" },
		},
	}
	err := pipeline.ValidateStage(&pipeline.PipelineStage{
		Name: "cache_save", Phase: pipeline.PhasePostResponse,
		Hooks: []pipeline.Hook{h},
	}, lookupMap{})
	if err == nil || !strings.Contains(err.Error(), "V2") {
		t.Fatalf("want V2 error, got %v", err)
	}
}

func TestValidateStage_V5_RejectsMix(t *testing.T) {
	inline := &stubHook{name: "metrics", kind: pipeline.DispatchInline}
	bus := &stubHook{
		name: "cache.save", kind: pipeline.DispatchTaskBus, hasSpec: true,
		spec: pipeline.TaskSpec{
			TaskType: "cache.save.v1", SchemaVersion: 1,
			IdempotencyOf: func(*domain.PipelineRequest) string { return "k" },
		},
	}
	err := pipeline.ValidateStage(&pipeline.PipelineStage{
		Name: "mixed", Phase: pipeline.PhasePostResponse,
		Hooks: []pipeline.Hook{inline, bus},
	}, lookupMap{"cache.save.v1": true})
	if err == nil || !strings.Contains(err.Error(), "V5") {
		t.Fatalf("want V5 error, got %v", err)
	}
}

func TestValidateStage_OK_TaskBusOnPostResponse(t *testing.T) {
	h := &stubHook{
		name: "cache.save", kind: pipeline.DispatchTaskBus, hasSpec: true,
		spec: pipeline.TaskSpec{
			TaskType: "cache.save.v1", SchemaVersion: 1,
			IdempotencyOf: func(*domain.PipelineRequest) string { return "k" },
		},
	}
	err := pipeline.ValidateStage(&pipeline.PipelineStage{
		Name: "cache_save", Phase: pipeline.PhasePostResponse,
		Hooks: []pipeline.Hook{h},
	}, lookupMap{"cache.save.v1": true})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveDispatchKind_DefaultInline(t *testing.T) {
	var h pipeline.Hook = &inlineOnlyHook{}
	if got := pipeline.ResolveDispatchKind(h); got != pipeline.DispatchInline {
		t.Fatalf("got %q want inline", got)
	}
}

type inlineOnlyHook struct{}

func (*inlineOnlyHook) Name() string                                           { return "inline-only" }
func (*inlineOnlyHook) Priority() int                                          { return 1 }
func (*inlineOnlyHook) Enabled(context.Context, *domain.PipelineRequest) bool  { return true }
func (*inlineOnlyHook) Execute(context.Context, *domain.PipelineRequest) error { return nil }
func (*inlineOnlyHook) OnError(context.Context, *domain.PipelineRequest, error) error {
	return nil
}
