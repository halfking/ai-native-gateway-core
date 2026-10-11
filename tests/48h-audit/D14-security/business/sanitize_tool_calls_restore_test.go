// Package business - D14 业务测试：敏感信息在 tool_calls.arguments 上的端到端还原。
//
// B-01: OpenAI chat 完成式响应 + message.tool_calls[*].function.arguments 还原
// B-02: OpenAI chat 流式 + delta.tool_calls[*].function.arguments 还原
// B-03: 未知占位符 → [REDACTED:<type>] mask，覆盖 tool_calls.arguments 路径
//
// 跑测：
//
//	go test -race -timeout 60s ./tests/48h-audit/D14-security/business/...
package business

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRedis 启动内嵌 miniredis，作为 SanitizeMap 持久化介质。
func setupRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// seedMap 在 Redis 中预置 SanitizeMap（占位符 → 真实值）。
func seedMap(t *testing.T, rdb *redis.Client, sid string, kv map[string]string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid), kv).Err())
}

// sanitizeRestoreRequest 构造非流式 InterceptRequest。
func sanitizeRestoreRequest(sid string, body []byte) *response.InterceptRequest {
	return &response.InterceptRequest{SessionID: sid, ResponseBody: body}
}

// streamMeta 构造流式 StreamMeta。
func streamMeta(sid string) *response.StreamMeta {
	return &response.StreamMeta{SessionID: sid}
}

// TestBusiness_ToolCallsArgs_Restore_NonStream
// OpenAI chat 完成式响应：message.tool_calls[*].function.arguments
// （JSON 字符串）里的占位符必须被还原成真实敏感值。
func TestBusiness_ToolCallsArgs_Restore_NonStream(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-tool-call-nonstream"
	seedMap(t, rdb, sid, map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": "foo@bar.com",
	})

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	body := []byte(`{
		"id":"chatcmpl-x",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"已获取用户信息",
				"tool_calls":[
					{
						"id":"call_1",
						"type":"function",
						"function":{
							"name":"dial",
							"arguments":"{\"to\":\"{SENSITIVE:phone:1}\",\"note\":\"call {SENSITIVE:email:1}\"}"
						}
					},
					{
						"id":"call_2",
						"type":"function",
						"function":{
							"name":"noop",
							"arguments":"{}"
						}
					}
				]
			}
		}]
	}`)

	result, err := it.InterceptNonStream(context.Background(), sanitizeRestoreRequest(sid, body))
	require.NoError(t, err)
	require.NotNil(t, result, "含 tool_calls.arguments 的响应必须返回 ModifiedBody")

	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &resp))
	msg := resp.Choices[0].Message

	// content 也要还原占位符（这里 message.content 没占位符，透传即可）
	assert.Equal(t, "已获取用户信息", msg.Content)

	// tool_calls[0].function.arguments 必须还原
	var arg0 map[string]string
	require.NoError(t, json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &arg0))
	assert.Equal(t, "13800138000", arg0["to"], "phone 占位符必须还原")
	assert.Equal(t, "call foo@bar.com", arg0["note"], "email 占位符必须还原")

	// tool_calls[1] 不含占位符，arguments 透传
	assert.Equal(t, "{}", msg.ToolCalls[1].Function.Arguments)
}

// TestBusiness_ToolCallsArgs_Restore_Stream
// OpenAI chat 流式：delta.tool_calls[*].function.arguments 还原。
func TestBusiness_ToolCallsArgs_Restore_Stream(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-tool-call-stream"
	seedMap(t, rdb, sid, map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
	})

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 流式 chunk：一个 SSE frame 同时携带 content + tool_calls.arguments 增量
	chunk := []byte(`data: {"id":"chatcmpl-y","choices":[{"delta":{"content":"拨号","tool_calls":[{"index":0,"id":"call_1","function":{"name":"dial","arguments":"{\"to\":\"{SENSITIVE:phone:1}\"}"}}]}}]}` + "\n\n")

	result, err := it.InterceptStreamChunk(context.Background(), chunk, streamMeta(sid))
	require.NoError(t, err)
	require.NotNil(t, result, "流式 tool_calls.arguments 必须返回 ModifiedChunk")

	str := string(result.ModifiedChunk)
	assert.Contains(t, str, "拨号", "content 必须保留")
	assert.Contains(t, str, "13800138000", "流式 tool_calls.arguments 内占位符必须还原为真实值")
	assert.NotContains(t, str, "{SENSITIVE:phone:1}", "raw 占位符文本必须被替换")
}

// TestBusiness_ToolCallsArgs_UnknownPlaceholderMasked
// 未知占位符 → 带类型的遮蔽标记（[REDACTED:phone] / [REDACTED:email]），
// 覆盖 tool_calls.arguments 路径。
//
// 注：2026-09-23 初版断言的是裸 "[REDACTED]"；ce5e35ab3 起遮蔽标记带类型，
// 实际输出一直是 "[REDACTED:<type>]"，原断言因此恒红（fail-open 占位符透传
// 并未复发：raw 占位符从来没到过上游，见下方 NotContains 断言）。
func TestBusiness_ToolCallsArgs_UnknownPlaceholderMasked(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-tool-call-unknown"
	// sm 中只有 phone:1，phone:99 / email:99 都不存在
	seedMap(t, rdb, sid, map[string]string{
		"{SENSITIVE:phone:1}": "13800138000",
	})

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"arguments":"{\"to\":\"{SENSITIVE:phone:1}\",\"backup\":\"{SENSITIVE:phone:99}\",\"cc\":\"{SENSITIVE:email:99}\"}"
					}
				}]
			}
		}]
	}`)

	result, err := it.InterceptNonStream(context.Background(), sanitizeRestoreRequest(sid, body))
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &resp))
	arg := resp.Choices[0].Message.ToolCalls[0].Function.Arguments

	// 判据落在字段上（不是整串 Contains）：整串匹配会漏掉「占位符被还原成了
	// 另一个字段的真值」这类串位缺陷 —— 那样 13800138000 仍然出现在 arg 里，
	// 旧的 Contains 断言照样绿。
	var fields map[string]string
	require.NoError(t, json.Unmarshal([]byte(arg), &fields), "arguments 必须是合法 JSON 对象：%s", arg)
	assert.Equal(t, "13800138000", fields["to"], "已知占位符必须还原成自己的真值")

	// 未知占位符 → mask。标记带类型（ce5e35ab3「遮蔽标记带类型」）是当前契约，
	// 这里 pin 精确字面量而不是 `[REDACTED]` 前缀：前缀断言放得过宽，
	// 退回无类型标记也不会被发现。
	assert.Equal(t, "[REDACTED:phone]", fields["backup"],
		"未知 phone 占位符必须 mask 成带类型的遮蔽标记（不得回落成裸 [REDACTED]）")
	assert.Equal(t, "[REDACTED:email]", fields["cc"],
		"未知 email 占位符必须 mask 成带类型的遮蔽标记（不得回落成裸 [REDACTED]）")

	assert.NotContains(t, arg, "{SENSITIVE:phone:99}", "raw 占位符文本必须消失")
	assert.NotContains(t, arg, "{SENSITIVE:email:99}", "raw 占位符文本必须消失")
	assert.NotContains(t, arg, "{SENSITIVE:", "任何 raw 占位符文本都不得透传给上游")
}
