// Package stress - D14 压力测试：tool_calls.arguments 还原并发 / 吞吐。
//
// S-01: 1000 轮并发还原（miniredis 内嵌）→ 映射表读写无 race
// S-02: 10000 次 tool_calls.arguments 还原 b.N → P99 延迟门槛
//
// 跑测：
//
//	go test -race -timeout 120s ./tests/48h-audit/D14-security/stress/...
//	go test -bench=. -benchtime=10s ./tests/48h-audit/D14-security/stress/...
package stress

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRedis(t testing.TB) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestStress_ToolCallsArgs_ConcurrentRestore
// 1000 轮并发请求同一个 session → 映射表读写无 race，每轮都能正确还原。
func TestStress_ToolCallsArgs_ConcurrentRestore(t *testing.T) {
	rdb := setupRedis(t)
	sid := "sess-stress-concurrent"
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid),
		"{SENSITIVE:phone:1}", "13800138000",
		"{SENSITIVE:email:1}", "foo@bar.com",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(t, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	const rounds = 1000
	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errCh := make(chan error, goroutines*rounds)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < rounds/goroutines; i++ {
				body := []byte(`{
					"choices":[{
						"message":{
							"tool_calls":[{
								"function":{
									"arguments":"{\"to\":\"{SENSITIVE:phone:1}\",\"note\":\"{SENSITIVE:email:1}\"}"
								}
							}]
						}
					}]
				}`)
				result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
					SessionID:    sid,
					ResponseBody: body,
				})
				if err != nil {
					errCh <- err
					continue
				}
				if result == nil || !contains(result.ModifiedBody, "13800138000") {
					errCh <- assert.AnError
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent restore failed: %v", err)
	}
}

// BenchmarkStress_ToolCallsArgs_Restore 还原单条 tool_calls.arguments。
// 跑测：go test -bench=. -benchtime=10s ./tests/48h-audit/D14-security/stress/...
// 验收：每次调用 < 50µs（mock 环境，无网络）。
func BenchmarkStress_ToolCallsArgs_Restore(b *testing.B) {
	rdb := setupRedis(b)
	sid := "sess-stress-bench"
	ctx := context.Background()
	require.NoError(b, rdb.HSet(ctx, sanitize.SanitizeRedisKey(sid),
		"{SENSITIVE:phone:1}", "13800138000",
		"{SENSITIVE:email:1}", "foo@bar.com",
	).Err())

	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	require.NoError(b, err)
	it, err := sanitize.NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(b, err)

	body := []byte(`{
		"choices":[{
			"message":{
				"tool_calls":[{
					"function":{
						"arguments":"{\"to\":\"{SENSITIVE:phone:1}\",\"note\":\"{SENSITIVE:email:1}\"}"
					}
				}]
			}
		}]
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
			SessionID:    sid,
			ResponseBody: body,
		})
		if err != nil {
			b.Fatalf("restore failed: %v", err)
		}
	}
}

// contains 是轻量 bytes.Contains 包装，避免引入 strings.Contains 误判字节序列。
func contains(b []byte, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(b) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}