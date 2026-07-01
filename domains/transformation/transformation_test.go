package transformation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyRequestWhitelist_NoFilters 验证无 passthrough/strip 时 body 保持不变。
func TestApplyRequestWhitelist_NoFilters(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"custom":"value"}`)
	out := ApplyRequestWhitelist(body, nil, nil)
	assert.JSONEq(t, string(body), string(out))
}

// TestApplyRequestWhitelist_StripFields 验证 stripFields 中字段被删除。
func TestApplyRequestWhitelist_StripFields(t *testing.T) {
	body := []byte(`{"model":"gpt-4","secret":"x","messages":[]}`)
	out := ApplyRequestWhitelist(body, nil, []string{"secret"})
	assert.NotContains(t, string(out), "secret")
	assert.Contains(t, string(out), "model")
}

// TestApplyRequestWhitelist_PassthroughFields 验证未在 passthrough+allowlist 中的字段被删除。
func TestApplyRequestWhitelist_PassthroughFields(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[],"unknown_field":"x"}`)
	out := ApplyRequestWhitelist(body, []string{"model", "messages"}, nil)
	assert.Contains(t, string(out), "model")
	assert.Contains(t, string(out), "messages")
	assert.NotContains(t, string(out), "unknown_field")
}

// TestCompressMessagesIfNeeded_NoCompression 验证未超限时 body 不变。
func TestCompressMessagesIfNeeded_NoCompression(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	out := CompressMessagesIfNeeded(body, 100000)
	assert.Equal(t, string(body), string(out))
}

// TestCompressMessagesIfNeeded_ZeroContext 验证 contextWindow=0 时直接返回原 body。
func TestCompressMessagesIfNeeded_ZeroContext(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[]}`)
	out := CompressMessagesIfNeeded(body, 0)
	assert.Equal(t, string(body), string(out))
}

// TestTransformHook_ChainsTransformers 验证多个 transformer 串联执行。
func TestTransformHook_ChainsTransformers(t *testing.T) {
	t1 := &recordingTransformer{name: "t1"}
	t2 := &recordingTransformer{name: "t2"}
	hook := NewTransformHook(t1, t2)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	env.TransformedRequest = []byte(strings.Repeat("y", 100))

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, t1.called, "t1 should have been called")
	assert.True(t, t2.called, "t2 should have been called")
}

// TestTransformHook_SkipsWhenAlreadyTransformed 验证已转换时跳过。
func TestTransformHook_SkipsWhenAlreadyTransformed(t *testing.T) {
	hook := NewTransformHook(&recordingTransformer{})
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
	hook := NewTransformHook(&recordingTransformer{})
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
	t1 := &recordingTransformer{}
	hook := NewTransformHook(nil, t1, nil)

	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}
	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, t1.called, "non-nil transformer should still execute")
}

// failingTransformer 始终返回错误的 Transformer。
type failingTransformer struct {
	err error
}

func (f *failingTransformer) Name() string { return "failing" }
func (f *failingTransformer) Transform(ctx Context) error {
	return f.err
}

// recordingTransformer 记录被调用状态。
type recordingTransformer struct {
	name   string
	called bool
}

func (r *recordingTransformer) Name() string {
	if r.name == "" {
		return "recording"
	}
	return r.name
}

func (r *recordingTransformer) Transform(ctx Context) error {
	r.called = true
	return nil
}

// _ unused modifyTransformer removed: Context is passed by value, so a
// single transformer cannot mutate the hook's view of Request. Tests that
// rely on this behavior should set Request on env directly.
