// Package data - D14 数据测试：tool_calls.arguments JSON 序列化形态钉桩。
//
// D-01: restoreJSONRecursive 浅遍历深度（字符串 → map → 数组 → 嵌套 map）
// D-02: arguments 不是合法 JSON 时退化为字符串路径占位符替换
//
// 跑测：
//
//	go test -race -timeout 60s ./tests/48h-audit/D14-security/data/...
package data

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

func setupRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestData_ToolCallsArgs_NestedJSONRestored
// tool_calls.arguments 是含嵌套 map + 数组的合法 JSON 字符串：
// 所有 string 字段里的占位符必须还原；非字符串字段不受影响。
func TestData_ToolCallsArgs_NestedJSONRestored(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-data-nested"
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid),
		"{SENSITIVE:phone:1}", "13800138000",
		"{SENSITIVE:email:1}", "foo@bar.com",
		"{SENSITIVE:id_card:1}", "110101199001011234",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 嵌套 JSON：顶层对象 + contacts 数组（数组里再嵌 map）
	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"name":"send_invites",
						"arguments":"{\"primary\":\"{SENSITIVE:phone:1}\",\"owner\":{\"email\":\"{SENSITIVE:email:1}\",\"verified\":true},\"contacts\":[{\"id_card\":\"{SENSITIVE:id_card:1}\",\"score\":42}]}"
					}
				}]
			}
		}]
	}`)

	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{SessionID: sid, ResponseBody: body})
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

	var args struct {
		Primary  string `json:"primary"`
		Owner    struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
		} `json:"owner"`
		Contacts []struct {
			IDCard string `json:"id_card"`
			Score  int    `json:"score"`
		} `json:"contacts"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Choices[0].Message.ToolCalls[0].Function.Arguments), &args))

	assert.Equal(t, "13800138000", args.Primary, "顶层字符串占位符还原")
	assert.Equal(t, "foo@bar.com", args.Owner.Email, "嵌套 map 字符串占位符还原")
	assert.True(t, args.Owner.Verified, "非字符串字段不变")
	assert.Equal(t, "110101199001011234", args.Contacts[0].IDCard, "数组内 map 字符串占位符还原")
	assert.Equal(t, 42, args.Contacts[0].Score, "数组内非字符串字段不变")
}

// TestData_ToolCallsArgs_InvalidJSONFallbackToStringReplace
// arguments 不是合法 JSON（罕见但存在：上游把某些 token 序列化为半截 JSON）
// → 退化为字符串路径占位符替换，不让 raw 占位符泄漏。
func TestData_ToolCallsArgs_InvalidJSONFallbackToStringReplace(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-data-invalid"
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid),
		"{SENSITIVE:phone:1}", "13800138000",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 半截 JSON：含占位符但不能完整 unmarshal
	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"name":"dial",
						"arguments":"prefix {SENSITIVE:phone:1} suffix"
					}
				}]
			}
		}]
	}`)

	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{SessionID: sid, ResponseBody: body})
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
	assert.Contains(t, arg, "prefix 13800138000 suffix", "非 JSON 路径退化为字符串占位符替换")
	assert.NotContains(t, arg, "{SENSITIVE:phone:1}", "raw 占位符文本必须消失")
}