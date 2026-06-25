package compression

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// ---------- LCSCompressor 测试 ----------

func TestLCSCompressor_EmptyMessages_NoError(t *testing.T) {
	c := NewLCSCompressor(4096)
	if err := c.Compress(&Context{Messages: nil}); err != nil {
		t.Fatalf("expected no error on empty messages, got %v", err)
	}
}

func TestLCSCompressor_LessThan10_RetainAll(t *testing.T) {
	c := NewLCSCompressor(4096)
	msgs := make([]Message, 5)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)}
	}
	ctx := Context{Messages: msgs}
	if err := c.Compress(&ctx); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(ctx.Messages) != 5 {
		t.Fatalf("expected 5 messages retained, got %d", len(ctx.Messages))
	}
}

func TestLCSCompressor_MoreThan10_Truncate(t *testing.T) {
	c := NewLCSCompressor(4096)
	msgs := make([]Message, 20)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)}
	}
	ctx := Context{Messages: msgs}
	if err := c.Compress(&ctx); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(ctx.Messages) != 10 {
		t.Fatalf("expected 10 messages after truncation, got %d", len(ctx.Messages))
	}
	// 验证保留的是最后 10 条
	if ctx.Messages[0].Content != "msg-10" {
		t.Fatalf("expected first retained to be msg-10, got %q", ctx.Messages[0].Content)
	}
	if ctx.Messages[9].Content != "msg-19" {
		t.Fatalf("expected last retained to be msg-19, got %q", ctx.Messages[9].Content)
	}
}

func TestLCSCompressor_Strategy(t *testing.T) {
	c := NewLCSCompressor(4096)
	if c.Strategy() != HookStrategyLCS {
		t.Fatalf("expected HookStrategyLCS, got %q", c.Strategy())
	}
	if c.Name() != "lcs" {
		t.Fatalf("expected name=lcs, got %q", c.Name())
	}
	if c.MaxTokens() != 4096 {
		t.Fatalf("expected MaxTokens=4096, got %d", c.MaxTokens())
	}
}

func TestLCSCompressor_DefaultMaxTokens(t *testing.T) {
	c := NewLCSCompressor(0)
	if c.MaxTokens() != 4096 {
		t.Fatalf("expected default 4096, got %d", c.MaxTokens())
	}
	c2 := NewLCSCompressor(-100)
	if c2.MaxTokens() != 4096 {
		t.Fatalf("expected default 4096 for negative, got %d", c2.MaxTokens())
	}
}

// ---------- NoopCompressor 测试 ----------

func TestNoopCompressor_PassThrough(t *testing.T) {
	n := NewNoopCompressor()
	if n.Name() != "noop" {
		t.Fatalf("expected name=noop, got %q", n.Name())
	}
	if n.Strategy() != HookStrategyNone {
		t.Fatalf("expected HookStrategyNone, got %q", n.Strategy())
	}
	msgs := []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	ctx := Context{Messages: msgs}
	if err := n.Compress(&ctx); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if len(ctx.Messages) != 2 {
		t.Fatalf("expected 2 messages preserved, got %d", len(ctx.Messages))
	}
}

// ---------- CompressionHook 测试 ----------

func TestCompressionHook_Disabled_WhenNeedsCompressionFalse(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("body"),
		Metadata:           map[string]any{MetaKeyNeedsCompression: false},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when needs_compression=false")
	}
}

func TestCompressionHook_Disabled_WhenMetadataMissing(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("body"),
		Metadata:           map[string]any{},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when needs_compression missing")
	}
}

func TestCompressionHook_Disabled_WhenEnvNil(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	if h.Enabled(context.Background(), nil) {
		t.Fatal("expected disabled on nil env")
	}
}

func TestCompressionHook_Disabled_WhenMetadataNil(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	env := &domain.PipelineRequest{TransformedRequest: []byte("b")}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when metadata is nil")
	}
}

func TestCompressionHook_Enabled_AndExecutes(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	msgs := make([]Message, 15)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("m-%d", i)}
	}
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("body"),
		Metadata: map[string]any{
			MetaKeyNeedsCompression: true,
			MetaKeyMessages:         msgs,
		},
	}
	if !h.Enabled(context.Background(), env) {
		t.Fatal("expected enabled")
	}
	if err := h.Execute(context.Background(), env); err != nil {
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

func TestCompressionHook_NoMessagesKey_EmptyCompress(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("b"),
		Metadata: map[string]any{
			MetaKeyNeedsCompression: true,
			// 没有 MetaKeyMessages
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := env.Metadata[MetaKeyCompressedMessages].([]Message)
	if len(got) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(got))
	}
}

func TestCompressionHook_WrongMessagesType_StillProceeds(t *testing.T) {
	// 当 MetaKeyMessages 类型错误时，Hook 不 panic，而是按空消息处理
	h := NewCompressionHook(NewLCSCompressor(4096))
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("b"),
		Metadata: map[string]any{
			MetaKeyNeedsCompression: true,
			MetaKeyMessages:         "not a slice", // 类型错误
		},
	}
	if err := h.Execute(context.Background(), env); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := env.Metadata[MetaKeyCompressedMessages].([]Message)
	if len(got) != 0 {
		t.Fatalf("expected 0 messages (typed as empty), got %d", len(got))
	}
}

// 错误注入 Compressor
type errCompressor struct{}

func (errCompressor) Name() string            { return "err" }
func (errCompressor) Strategy() HookStrategy  { return HookStrategy("err") }
func (errCompressor) Compress(*Context) error { return errors.New("boom") }

func TestCompressionHook_CompressorError_RecordedAndOnErrorSwallows(t *testing.T) {
	h := NewCompressionHook(errCompressor{})
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("b"),
		Metadata: map[string]any{
			MetaKeyNeedsCompression: true,
			MetaKeyMessages:         []Message{{Role: "user", Content: "hi"}},
		},
	}
	err := h.Execute(context.Background(), env)
	if err == nil {
		t.Fatal("expected Execute to return compressor error")
	}
	// OnError 吞掉
	if onErr := h.OnError(context.Background(), env, err); onErr != nil {
		t.Fatalf("OnError should swallow, got %v", onErr)
	}
	if _, ok := env.Metadata[MetaKeyCompressionError]; !ok {
		t.Fatal("expected compression_error in metadata")
	}
	// 降级：原始 messages 应被复制到 compressed_messages
	got, _ := env.Metadata[MetaKeyCompressedMessages].([]Message)
	if len(got) != 1 {
		t.Fatalf("expected fallback to 1 original message, got %d", len(got))
	}
}

func TestCompressionHook_NilCompressor_Disabled(t *testing.T) {
	h := &CompressionHook{compressor: nil}
	env := &domain.PipelineRequest{
		TransformedRequest: []byte("b"),
		Metadata:           map[string]any{MetaKeyNeedsCompression: true},
	}
	if h.Enabled(context.Background(), env) {
		t.Fatal("expected disabled when compressor is nil")
	}
}

func TestCompressionHook_CompressorAccessor(t *testing.T) {
	lc := NewLCSCompressor(4096)
	h := NewCompressionHook(lc)
	if h.Compressor() != lc {
		t.Fatal("Compressor() should return the same instance")
	}
}

func TestHookInterfaceCompliance(t *testing.T) {
	h := NewCompressionHook(NewLCSCompressor(4096))
	if h.Name() != "compression.apply" {
		t.Fatalf("name mismatch: %q", h.Name())
	}
	if h.Priority() != 100 {
		t.Fatalf("expected priority=100, got %d", h.Priority())
	}
}
