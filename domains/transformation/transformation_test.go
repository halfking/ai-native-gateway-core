package transformation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizer_Transform 验证 Sanitizer.Transform 不报错且写入元数据。
func TestSanitizer_Transform(t *testing.T) {
	s := NewSanitizer()
	ctx := Context{
		Request:  []byte(`{"password": "abc"}`),
		Metadata: map[string]any{},
	}
	err := s.Transform(ctx)
	require.NoError(t, err)
	assert.Equal(t, len(defaultSanitizeRules()), ctx.Metadata["sanitize_rules_count"])
	assert.Equal(t, "sanitizer", s.Name())
}

// TestSanitizer_NilRequest 验证 nil Request 不 panic。
func TestSanitizer_NilRequest(t *testing.T) {
	s := NewSanitizer()
	ctx := Context{Request: nil, Metadata: map[string]any{}}
	err := s.Transform(ctx)
	require.NoError(t, err)
}

// TestSanitizer_NilReceiver 验证 nil 接收者安全。
func TestSanitizer_NilReceiver(t *testing.T) {
	var s *Sanitizer
	ctx := Context{Request: []byte("x"), Metadata: map[string]any{}}
	err := s.Transform(ctx)
	require.NoError(t, err)
}

// TestCompressor_LongRequest 验证超长请求被标记。
func TestCompressor_LongRequest(t *testing.T) {
	c := NewCompressor(100) // 阈值 400 字节
	long := strings.Repeat("x", 500)
	ctx := Context{Request: []byte(long), Metadata: map[string]any{}}
	err := c.Transform(ctx)
	require.NoError(t, err)
	assert.Equal(t, true, ctx.Metadata["needs_compression"])
	assert.Equal(t, 100, ctx.Metadata["compress_max_tokens"])
}

// TestCompressor_ShortRequest 验证正常请求不标记。
func TestCompressor_ShortRequest(t *testing.T) {
	c := NewCompressor(4096) // 阈值 16384 字节
	short := []byte(`{"model":"gpt-4"}`)
	ctx := Context{Request: short, Metadata: map[string]any{}}
	err := c.Transform(ctx)
	require.NoError(t, err)
	assert.Equal(t, false, ctx.Metadata["needs_compression"])
}

// TestCompressor_DefaultMaxTokens 验证 0 maxTokens 使用默认 4096。
func TestCompressor_DefaultMaxTokens(t *testing.T) {
	c := NewCompressor(0)
	assert.Equal(t, 4096, c.maxTokens)

	c2 := NewCompressor(-1)
	assert.Equal(t, 4096, c2.maxTokens)
}

// TestTransformHook_ChainsTransformers 验证多个 transformer 串联执行。
func TestTransformHook_ChainsTransformers(t *testing.T) {
	s := NewSanitizer()
	c := NewCompressor(100)
	hook := NewTransformHook(s, c)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	env.TransformedRequest = []byte(strings.Repeat("y", 500))

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, true, env.Metadata["needs_compression"], "compressor should mark long request")
	assert.NotNil(t, env.Metadata["sanitize_rules_count"], "sanitizer should have run")
}

// TestTransformHook_SkipsWhenAlreadyTransformed 验证已转换时跳过。
func TestTransformHook_SkipsWhenAlreadyTransformed(t *testing.T) {
	hook := NewTransformHook(NewSanitizer())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.TransformedRequest = []byte("already-transformed")
	enabled := hook.Enabled(context.Background(), env)
	assert.False(t, enabled, "should be disabled when TransformedRequest is set")
}

// TestTransformHook_PropagatesTransformerError 验证 transformer 错误被透传。
func TestTransformHook_PropagatesTransformerError(t *testing.T) {
	boom := errors.New("transformer boom")
	failingT := &failingTransformer{err: boom}
	hook := NewTransformHook(failingT)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	err := hook.Execute(context.Background(), env)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transformer boom")

	propagated := hook.OnError(context.Background(), env, err)
	assert.Error(t, propagated)
}

// TestTransformHook_NilEnv 验证 nil envelope 不 panic。
func TestTransformHook_NilEnv(t *testing.T) {
	hook := NewTransformHook(NewSanitizer())
	err := hook.Execute(context.Background(), nil)
	require.NoError(t, err)
}

// TestTransformHook_EmptyChain 验证空 transformer 链合法。
func TestTransformHook_EmptyChain(t *testing.T) {
	hook := NewTransformHook()
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
}

// TestTransformHook_NilTransformerSkipped 验证 nil transformer 跳过。
func TestTransformHook_NilTransformerSkipped(t *testing.T) {
	called := false
	t1 := &countingTransformer{called: &called}
	hook := NewTransformHook(nil, t1, nil)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, called, "non-nil transformer should still execute")
}

// failingTransformer 始终返回错误的 Transformer。
type failingTransformer struct {
	err error
}

func (f *failingTransformer) Name() string { return "failing" }
func (f *failingTransformer) Transform(ctx Context) error {
	return f.err
}

// countingTransformer 记录是否被调用。
type countingTransformer struct {
	called *bool
}

func (c *countingTransformer) Name() string { return "counting" }
func (c *countingTransformer) Transform(ctx Context) error {
	*c.called = true
	return nil
}
