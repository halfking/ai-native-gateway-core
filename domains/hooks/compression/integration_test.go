package compression

import (
	"context"
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// TestCompressionPipeline_TransformStage 验证 CompressionHook 可拼装到 Pipeline
// 的 Transform 阶段；当 env.Metadata[MetaKeyNeedsCompression]=true 时执行。
func TestCompressionPipeline_TransformStage(t *testing.T) {
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "compression_transform",
		Phase: pipeline.PhaseTransform,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCompressionHook(NewLCSCompressor(4096))},
	})

	// 构造 15 条消息 → 期望压缩到 10 条
	msgs := make([]Message, 15)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("m-%d", i)}
	}
	env := &domain.PipelineRequest{
		TransformedRequest: []byte(`{"x":1}`),
		Metadata: map[string]any{
			MetaKeyNeedsCompression: true,
			MetaKeyMessages:         msgs,
		},
	}
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, ok := env.Metadata[MetaKeyCompressedMessages].([]Message)
	if !ok {
		t.Fatalf("expected compressed_messages in metadata, got %T", env.Metadata[MetaKeyCompressedMessages])
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 messages after compression, got %d", len(got))
	}
	if str, _ := env.Metadata[MetaKeyCompressionStrategy].(string); str != "lcs" {
		t.Fatalf("expected strategy=lcs, got %q", str)
	}
}

// TestCompressionPipeline_NotEnabled_Skipped 验证未设置 needs_compression 时
// Hook 在 Pipeline 中被跳过（不写任何 metadata）。
func TestCompressionPipeline_NotEnabled_Skipped(t *testing.T) {
	p := pipeline.NewRequestPipeline()
	p.AddStage(&pipeline.PipelineStage{
		Name:  "compression_transform",
		Phase: pipeline.PhaseTransform,
		Mode:  pipeline.ModeSequential,
		Hooks: []pipeline.Hook{NewCompressionHook(NewLCSCompressor(4096))},
	})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte(`{"x":1}`),
		Metadata:           map[string]any{},
	}
	if err := p.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, exists := env.Metadata[MetaKeyCompressedMessages]; exists {
		t.Fatal("expected compressed_messages to be absent when needs_compression=false")
	}
}
