// Package sanitize - smart_sani_guard_test.go
//
// 端到端测试 SmartSaniGuard 主链路：
//   - SanitizeInputMiddleware 在 chatHandler 之前替换敏感信息
//   - SanitizeRestoreInterceptor 在 OutputCompliance 之后还原占位符
//   - Redis 跨轮次持久化映射表
//   - 跨轮次占位符编号不冲突
package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRedis 启动内嵌的 miniredis
func setupSaniGuardRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestSanitizeInputMiddleware_BasicSanitize 验证输入侧脱敏：手机号→占位符
func TestSanitizeInputMiddleware_BasicSanitize(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	capturedBody := bytes.Buffer{}
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody.Write(body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{
			"model": "gpt-4",
			"messages": [
				{"role":"user","content":"我的手机号是13800138000"}
			]
		}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", "session-test-1")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// handler 收到的请求体应包含占位符而非真实手机号
	assert.NotContains(t, capturedBody.String(), "13800138000", "端handler不应看到真实手机号")
	assert.Contains(t, capturedBody.String(), "{SENSITIVE:phone:1}", "应有占位符")

	// Redis 中应存有映射表
	key := SanitizeRedisKey("session-test-1")
	vals, err := rdb.HGetAll(context.Background(), key).Result()
	require.NoError(t, err)
	assert.Equal(t, "13800138000", vals["{SENSITIVE:phone:1}"])
}

// TestSanitizeInputMiddleware_MultiRoundNoCollision 验证多轮会话占位符编号不冲突
func TestSanitizeInputMiddleware_MultiRoundNoCollision(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	capturedBodies := sync.Map{}
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBodies.Store(r.Header.Get("X-Gw-Session-Id"), body)
		w.WriteHeader(http.StatusOK)
	}))

	// 同一会话两次请求，每次都说"我的手机号是13800138000"
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			bytes.NewReader([]byte(`{
				"model":"gpt-4",
				"messages":[{"role":"user","content":"我的手机号是13800138000"}]
			}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gw-Session-Id", "session-multi")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	// Redis 中应有两个不同编号的占位符
	vals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey("session-multi")).Result()
	require.NoError(t, err)
	assert.Contains(t, vals, "{SENSITIVE:phone:1}", "第1轮占位符")
	assert.Contains(t, vals, "{SENSITIVE:phone:2}", "第2轮占位符（与第1轮不撞号）")
	assert.Equal(t, "13800138000", vals["{SENSITIVE:phone:1}"])
	assert.Equal(t, "13800138000", vals["{SENSITIVE:phone:2}"])

	// 两轮请求应分别使用不同占位符
	raw1, _ := capturedBodies.Load("session-multi")
	body1 := raw1.([]byte)
	assert.Contains(t, string(body1), "{SENSITIVE:phone:", "应有占位符")

	// 再次抓一次第2轮请求
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{
			"model":"gpt-4",
			"messages":[{"role":"user","content":"我的手机号是13800138000"}]
		}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", "session-multi")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	raw2, _ := capturedBodies.Load("session-multi")
	body2 := raw2.([]byte)
	// 两次入站请求体应使用不同占位符编号
	if bytes.Contains(body1, []byte("{SENSITIVE:phone:1}")) {
		assert.Contains(t, string(body2), "{SENSITIVE:phone:2}",
			"第2轮应使用 phone:2（避免占位符轮次冲突）")
	}
}

// TestSanitizeRestoreInterceptor_RestoresPlaceholders 验证响应侧还原
func TestSanitizeRestoreInterceptor_RestoresPlaceholders(t *testing.T) {
	rdb := setupSaniGuardRedis(t)

	// 预先把映射表写入 Redis（模拟输入侧已写入）
	key := SanitizeRedisKey("session-restore")
	rdb.HSet(context.Background(), key, map[string]any{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": "test@example.com",
	})

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	interceptor, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 模拟 LLM 响应（含占位符）
	respBody := []byte(`{
		"id": "chatcmpl-xxx",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"您的手机号{SENSITIVE:phone:1}和邮箱{SENSITIVE:email:1}已验证"
			}
		}]
	}`)

	result, err := interceptor.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "session-restore",
		ResponseBody: respBody,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.ModifiedBody)

	var restored struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	err = json.Unmarshal(result.ModifiedBody, &restored)
	require.NoError(t, err)
	assert.Contains(t, restored.Choices[0].Message.Content, "13800138000")
	assert.Contains(t, restored.Choices[0].Message.Content, "test@example.com")
	assert.NotContains(t, restored.Choices[0].Message.Content, "{SENSITIVE:")
}

// TestSanitizeRestoreInterceptor_EmptySession 验证无映射表时无副作用
func TestSanitizeRestoreInterceptor_EmptySession(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	interceptor, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	respBody := []byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
	result, err := interceptor.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "nonexistent-session",
		ResponseBody: respBody,
	})
	require.NoError(t, err)
	assert.Nil(t, result, "无映射表时应返回 nil（不修改响应）")
}

// TestSmartSaniGuard_End2End 输入脱敏 + 输出还原的完整链路
func TestSmartSaniGuard_End2End(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)

	// 1. 构造输入脱敏中间件
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 2. 构造"fake chatHandler" — 把中间件处理后的请求体转发到响应侧
	interceptor, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	var receivedByUpstream string
	var restoredDownstream string

	// 模拟上游 LLM：把收到的请求体里的占位符字符串当作响应内容
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedByUpstream = string(body)
		w.Header().Set("Content-Type", "application/json")
		// 模拟 LLM 在响应中引用占位符
		w.Write([]byte(`{
			"id":"chatcmpl-xxx",
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"` + extractPlaceholder(receivedByUpstream) + ` 已验证"
				}
			}]
		}`))
	})

	// 模拟客户端：接收响应后还原
	client := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 收集上游响应
		// 简单做法：直接由中间件上游生成响应后再走还原拦截器
		// 此处演示时直接输出已还原的结果
		w.Write([]byte(restoredDownstream))
	})

	// 客户端请求
	sessID := "e2e-session-1"
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟 chatHandler：先调用真实 upstream，再用拦截器还原响应
		upstreamRec := httptest.NewRecorder()
		upstream.ServeHTTP(upstreamRec, r)
		// 让内层处理能读到 r.Body（httptest 需重新构造）
		// 用收到的占位符作为响应内容
		// 真实 chatHandler 在 exec 阶段会调用 upstream 并写回响应
		// 这里简化为直接生成响应体
		respBody := []byte(`{
			"id":"chatcmpl-xxx",
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"` + extractPlaceholder(receivedByUpstream) + ` 已验证"
				}
			}]
		}`)

		ir, err := interceptor.InterceptNonStream(r.Context(), &response.InterceptRequest{
			SessionID:    sessID,
			ResponseBody: respBody,
		})
		_ = ir
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		if ir != nil && len(ir.ModifiedBody) > 0 {
			w.Write(ir.ModifiedBody)
		} else {
			w.Write(respBody)
		}
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{
			"model":"gpt-4",
			"messages":[{"role":"user","content":"我的手机号是13800138000"}]
		}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", sessID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// 验证：上游看到的请求体是占位符（不暴露真实手机号）
	assert.NotContains(t, receivedByUpstream, "13800138000", "上游不应看到真实手机号")
	assert.Contains(t, receivedByUpstream, "{SENSITIVE:phone:", "上游应看到占位符")

	// 验证：客户端收到的响应应已被还原回真实敏感值
	downstream := rec.Body.String()
	assert.Contains(t, downstream, "13800138000", "客户端应收到还原后的真实手机号")
	_ = client
}

// extractPlaceholder 从请求体里提取第一个 {SENSITIVE:...} 字符串
func extractPlaceholder(body string) string {
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return ""
	}
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "{SENSITIVE:") {
			return m.Content
		}
	}
	return ""
}
