package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/stretchr/testify/require"
)

func newSSEBodyRestorer(t *testing.T) *SanitizeRestoreInterceptor {
	t.Helper()
	rdb := setupSaniGuardRedis(t)
	require.NoError(t, rdb.HSet(context.Background(), SanitizeRedisKey("sse-restore"),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	return it
}

// SSE permits data: with no space, and each data field contributes a line to
// the event payload. Both forms must reach the JSON delta restorer.
func TestSSEStreamRestoreLegalDataFraming(t *testing.T) {
	it := newSSEBodyRestorer(t)
	tests := []struct {
		name  string
		frame string
		front string
		end   string
	}{
		{
			name:  "no space after colon",
			frame: "event: message\ndata:{\"choices\":[{\"delta\":{\"content\":\"call {SENSITIVE:phone:1}\"}}]}\n\n",
			front: "event: message\ndata:",
			end:   "\n\n",
		},
		{
			name:  "multiline data with CRLF and comment",
			frame: "event: content_block_delta\r\n: keep this comment\r\ndata: {\"type\":\"content_block_delta\",\r\ndata: \"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"call {SENSITIVE:phone:1}\"}}\r\n\r\n",
			front: "event: content_block_delta\r\n: keep this comment\r\ndata: ",
			end:   "\r\n\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := it.InterceptStreamChunk(context.Background(), []byte(tt.frame),
				&response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.ShouldBlock)
			require.True(t, bytes.HasPrefix(result.ModifiedChunk, []byte(tt.front)))
			require.True(t, bytes.HasSuffix(result.ModifiedChunk, []byte(tt.end)))
			require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
			payload := testSSEPayload(t, result.ModifiedChunk)
			require.Contains(t, string(payload), "13800138000")
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(payload, &decoded))
		})
	}
}

func TestSSEStreamRestoreMultipleEventsInOneChunk(t *testing.T) {
	it := newSSEBodyRestorer(t)
	frame := ": heartbeat\n\n" +
		"event: message\ndata:{\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n" +
		"event: message\ndata:{\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:1}\"}}]}\n\n"
	result, err := it.InterceptStreamChunk(context.Background(), []byte(frame),
		&response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ShouldBlock)
	require.True(t, bytes.HasPrefix(result.ModifiedChunk, []byte(": heartbeat\n\n")))
	require.Contains(t, string(result.ModifiedChunk), `"content":"first"`)
	require.Contains(t, string(result.ModifiedChunk), "13800138000")
	require.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
}

func TestSSEStreamRestoreUnsafeParseBlocksSubsequentFrames(t *testing.T) {
	it := newSSEBodyRestorer(t)
	for _, frame := range []string{
		"data:{\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:1}\"\n\n",
		"data: {\"type\":\"unknown\",\"metadata\":\"{SENSITIVE:phone:1}\"}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}],\"metadata\":\"{SENSITIVE:phone:1}\"}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:1}\"}}],\"metadata\":\"{SENSITIVE:phone:1}\"}\n\n",
		"data: {\"metadata\":\"{SENSITIVE:phone:1}\",\"metadata\":\"safe\"}\n\n",
	} {
		meta := &response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()}
		result, err := it.InterceptStreamChunk(context.Background(), []byte(frame), meta)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.ShouldBlock, "unparsed or unhandled marker must not reach the client")
		result, err = it.InterceptStreamChunk(context.Background(), contentFrame("later"), meta)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.ShouldBlock, "blocked stream cannot resume with later content")
	}
}

func TestSSEStreamRestoreEscapedUnknownFieldMarkerBlocks(t *testing.T) {
	it := newSSEBodyRestorer(t)
	frame := `data:{"type":"unknown","metadata":"\u007bSENSITIVE:phone:1}"}` + "\n\n"
	result, err := it.InterceptStreamChunk(context.Background(), []byte(frame),
		&response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ShouldBlock)
}

func TestSSEStreamRestoreMalformedEscapedMarkerBlocksButOpaqueControlPasses(t *testing.T) {
	it := newSSEBodyRestorer(t)
	for _, frame := range []string{
		`data:{"choices":[{"delta":{"content":"\u007bSENSITIVE:phone:1}` + "\n\n",
		`data:{"choices":[{"delta":{"content":"\u007b\u0053\u0045\u004e\u0053\u0049\u0054\u0049\u0056\u0045\u003aphone:1}` + "\n\n",
	} {
		result, err := it.InterceptStreamChunk(context.Background(), []byte(frame),
			&response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.ShouldBlock)
	}
	result, err := it.InterceptStreamChunk(context.Background(), []byte(`data: invalid \u4f60`+"\n\n"),
		&response.StreamMeta{SessionID: "sse-restore", State: response.NewStreamState()})
	require.NoError(t, err)
	require.Nil(t, result, "opaque control data without a marker keeps its original framing")
}

func TestSSEStreamRestoreMissingSessionBlocksReservedMarker(t *testing.T) {
	it := newSSEBodyRestorer(t)
	frame := []byte(`data:{"choices":[{"delta":{"content":"{SENSITIVE:phone:1}"}}]}` + "\n\n")
	for _, meta := range []*response.StreamMeta{nil, {State: response.NewStreamState()}} {
		result, err := it.InterceptStreamChunk(context.Background(), frame, meta)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.True(t, result.ShouldBlock)
	}
	control, err := it.InterceptStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), nil)
	require.NoError(t, err)
	require.Nil(t, control)
}

// Decode the event the way an SSE client does: remove one optional ASCII
// space after data:, then join all data fields with a newline.
func testSSEPayload(t *testing.T, frame []byte) []byte {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(string(frame), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		part := strings.TrimPrefix(line, "data:")
		part = strings.TrimPrefix(part, " ")
		lines = append(lines, part)
	}
	require.NotEmpty(t, lines)
	return []byte(strings.Join(lines, "\n"))
}
