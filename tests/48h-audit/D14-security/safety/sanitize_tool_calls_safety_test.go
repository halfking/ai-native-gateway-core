// Package safety - D14 安全测试：敏感信息还原的注入面 / 跨租户隔离。
//
// SF-01: 伪造占位符（{SENSITIVE:phone:99} 不在 sm）→ mask；
//         metrics.SanitizePlaceholderTamperingTotal 计数自增
// SF-02: 跨租户（X-Gw-Tenant-Id A vs B）→ A 的 sm 不能被 B 还原，
//         覆盖 tool_calls.arguments 路径
//
// 跑测：
//
//	go test -race -timeout 60s ./tests/48h-audit/D14-security/safety/...
package safety

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/metrics"
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

// readCounter 安全地读 Prometheus Counter 值。
func readCounter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// TestSafety_ToolCallsArgs_ForgedPlaceholderMasked
// LLM 在 tool_calls.arguments 里伪造 {SENSITIVE:phone:99}（sm 中没有），
// 必须 mask 为 [REDACTED] 并触发 SanitizePlaceholderTamperingTotal 计数。
func TestSafety_ToolCallsArgs_ForgedPlaceholderMasked(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-safety-forged"
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid),
		"{SENSITIVE:phone:1}", "13800138000",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 抓计数初始值（避免其它并行测试干扰）
	counter := metrics.SanitizePlaceholderTamperingTotal.WithLabelValues("llm_generated")
	before := readCounter(t, counter)

	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"arguments":"{\"known\":\"{SENSITIVE:phone:1}\",\"forged\":\"{SENSITIVE:phone:99}\"}"
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
	assert.Contains(t, arg, "13800138000", "已知占位符必须还原")
	assert.Contains(t, arg, "[REDACTED]", "伪造占位符必须 mask")
	assert.NotContains(t, arg, "{SENSITIVE:phone:99}", "raw 占位符文本必须消失")

	// Prometheus 计数：1 个伪造占位符 → +1
	after := readCounter(t, counter)
	assert.GreaterOrEqualf(t, after-before, 1.0,
		"伪造占位符计数必须 ≥1（before=%v after=%v）", before, after)
}

// TestSafety_ToolCallsArgs_CrossTenantIsolated
// 两个租户各自有 session；A 的 sm 不能被 B 读到。
// 覆盖 tool_calls.arguments 路径（防止插件在跨租户 call 时拿到对方 PII）。
func TestSafety_ToolCallsArgs_CrossTenantIsolated(t *testing.T) {
	rdb := setupRedis(t)
	ctx := context.Background()

	// 租户 A 仅有自己的 sm
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sanitize.HashTenant("tenantA"), "shared-sid"),
		"{SENSITIVE:phone:1}", "13800138000",
	).Err())
	// 租户 B 在自己的 session 上放真实映射
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sanitize.HashTenant("tenantB"), "shared-sid"),
		"{SENSITIVE:phone:1}", "99999999999",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"arguments":"{\"to\":\"{SENSITIVE:phone:1}\"}"
					}
				}]
			}
		}]
	}`)

	// 租户 A 还原 → 自己的 13800138000
	resA, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID: "shared-sid", TenantID: "tenantA", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, resA)
	var respA struct {
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
	require.NoError(t, json.Unmarshal(resA.ModifiedBody, &respA))
	assert.Contains(t, respA.Choices[0].Message.ToolCalls[0].Function.Arguments, "13800138000",
		"租户 A 必须拿到自己的真实值")

	// 租户 B 还原 → 自己的 99999999999，绝不能拿到 13800138000
	resB, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID: "shared-sid", TenantID: "tenantB", ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, resB)
	var respB struct {
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
	require.NoError(t, json.Unmarshal(resB.ModifiedBody, &respB))
	argB := respB.Choices[0].Message.ToolCalls[0].Function.Arguments
	assert.Contains(t, argB, "99999999999", "租户 B 必须拿到自己的真实值")
	assert.NotContains(t, argB, "13800138000", "租户 B 绝不能跨租户拿到 A 的真实值")
}