package streaming

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSEStreamer_ThreeChunks 验证 3 个 chunk 全部发出。
func TestSSEStreamer_ThreeChunks(t *testing.T) {
	s := NewSSEStreamer()
	src := make(chan []byte, 3)
	src <- []byte("chunk-1")
	src <- []byte("chunk-2")
	src <- []byte("chunk-3")
	close(src)

	out := make(chan *StreamChunk, 10)
	err := s.Stream(StreamContext{ResponseChan: src}, out)
	require.NoError(t, err)

	var got []*StreamChunk
	for c := range out {
		got = append(got, c)
	}
	assert.Equal(t, 4, len(got), "3 content + 1 done")
	assert.Equal(t, ChunkTypeContent, got[0].Type)
	assert.Equal(t, []byte("chunk-1"), got[0].Data)
	assert.Equal(t, 1, got[0].Index)
	assert.Equal(t, ChunkTypeContent, got[1].Type)
	assert.Equal(t, []byte("chunk-2"), got[1].Data)
	assert.Equal(t, 2, got[1].Index)
	assert.Equal(t, ChunkTypeContent, got[2].Type)
	assert.Equal(t, 3, got[2].Index)
	assert.Equal(t, ChunkTypeDone, got[3].Type, "last chunk should be done")
}

// TestSSEStreamer_ExitsOnUpstreamClose 验证关闭上游 ch 后 streamer 退出。
func TestSSEStreamer_ExitsOnUpstreamClose(t *testing.T) {
	s := NewSSEStreamer()
	src := make(chan []byte, 1)
	src <- []byte("only")
	close(src)

	out := make(chan *StreamChunk, 10)
	done := make(chan error, 1)
	go func() {
		done <- s.Stream(StreamContext{ResponseChan: src}, out)
	}()

	var seenDone bool
	for c := range out {
		if c.Type == ChunkTypeDone {
			seenDone = true
		}
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Streamer did not return in time")
	}
	assert.True(t, seenDone, "should emit a done chunk")
}

// TestSSEStreamer_IndexIncrements 验证 Index 正确递增。
func TestSSEStreamer_IndexIncrements(t *testing.T) {
	s := NewSSEStreamer()
	src := make(chan []byte, 5)
	for i := 0; i < 5; i++ {
		src <- []byte{byte('a' + i)}
	}
	close(src)

	out := make(chan *StreamChunk, 10)
	_ = s.Stream(StreamContext{ResponseChan: src}, out)

	idx := 0
	for c := range out {
		if c.Type == ChunkTypeContent {
			assert.Equal(t, idx+1, c.Index, "index should be 1-based and increment")
			idx++
		}
	}
	assert.Equal(t, 5, idx, "should have 5 content chunks")
}

// TestSSEStreamer_NilChannel 验证 nil 输出 channel 报错（防御性）。
func TestSSEStreamer_NilChannel(t *testing.T) {
	s := NewSSEStreamer()
	err := s.Stream(StreamContext{ResponseChan: nil}, nil)
	require.Error(t, err)
}

// TestSSEStreamer_EmitsTimestamps 验证 EmitAt 被填充。
func TestSSEStreamer_EmitsTimestamps(t *testing.T) {
	s := NewSSEStreamer()
	src := make(chan []byte, 1)
	src <- []byte("hi")
	close(src)
	out := make(chan *StreamChunk, 5)
	_ = s.Stream(StreamContext{ResponseChan: src}, out)

	for c := range out {
		assert.False(t, c.EmitAt.IsZero(), "EmitAt should be set")
	}
}

// TestStreamHook_SkipsWhenUpstreamNil 验证 UpstreamResponse 为 nil 时跳过。
func TestStreamHook_SkipsWhenUpstreamNil(t *testing.T) {
	hook := NewStreamHook(NewSSEStreamer())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	// env.UpstreamResponse 为 nil
	enabled := hook.Enabled(context.Background(), env)
	assert.False(t, enabled)
}

// TestStreamHook_EnabledWhenUpstreamSet 验证 UpstreamResponse 非 nil 时启用。
func TestStreamHook_EnabledWhenUpstreamSet(t *testing.T) {
	hook := NewStreamHook(NewSSEStreamer())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.UpstreamResponse = []byte("first chunk")
	enabled := hook.Enabled(context.Background(), env)
	assert.True(t, enabled)
}

// TestStreamHook_ExecuteFillsMetadata 验证 Execute 写入 metadata。
func TestStreamHook_ExecuteFillsMetadata(t *testing.T) {
	hook := NewStreamHook(NewSSEStreamer())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.UpstreamResponse = []byte("ok")
	env.TransformedRequest = []byte(`{"model":"gpt-4"}`)
	env.Metadata = map[string]any{}

	err := hook.Execute(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "sse", env.Metadata["streamer_invoked"])
	_, hasTs := env.Metadata["streamer_invoked_at"]
	assert.True(t, hasTs, "timestamp should be recorded")
	assert.Equal(t, len(env.TransformedRequest), env.Metadata["stream_request_bytes"])
}

// TestStreamHook_NilEnv 验证 nil envelope 不 panic。
func TestStreamHook_NilEnv(t *testing.T) {
	hook := NewStreamHook(NewSSEStreamer())
	err := hook.Execute(context.Background(), nil)
	require.NoError(t, err)
}

// TestStreamHook_OnErrorSwallows 验证 OnError 降级（返回 nil）。
func TestStreamHook_OnErrorSwallows(t *testing.T) {
	hook := NewStreamHook(NewSSEStreamer())
	env := domain.NewRequestEnvelope(context.Background(), nil)
	env.Metadata = map[string]any{}

	err := hook.OnError(context.Background(), env, assert.AnError)
	require.NoError(t, err, "OnError should swallow (return nil)")
	assert.Contains(t, env.Metadata["streamer_error"].(string), "assert")
}
