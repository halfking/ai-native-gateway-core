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
	key := SanitizeRedisKey(HashTenant("_unknown"), "session-test-1")
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
	vals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey(HashTenant("_unknown"), "session-multi")).Result()
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
	key := SanitizeRedisKey(HashTenant("_unknown"), "session-restore")
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

// ── 2026-08-07 回归：body passthrough 完整性 ────────────────────────────
//
// 事故背景：readBody 只读取不恢复 r.Body，导致所有"无需脱敏"的请求都以
// 空 body 抵达下游 chatHandler，被 json_parse_error 400 拒绝（生产全量宕机）。
// 以下测试锁定三条 passthrough 路径都必须把原始 body 完整交给下游。

// TestSanitizeInputMiddleware_NoSensitive_BodyIntact 无敏感信息时 body 必须原样透传。
func TestSanitizeInputMiddleware_NoSensitive_BodyIntact(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	original := `{"model":"claude-opus-5","messages":[{"role":"user","content":"帮我写一个快速排序"}]}`

	var got []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", "sess-no-sensitive")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.JSONEq(t, original, string(got),
		"无敏感信息时下游必须收到原始 body")

	// 下游必须能成功反序列化（这正是生产 json_parse_error 的判据）
	var parsed struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	assert.Equal(t, "claude-opus-5", parsed.Model)
}

// TestSanitizeInputMiddleware_NoMessagesField_BodyIntact 无 messages 字段时原样透传。
func TestSanitizeInputMiddleware_NoMessagesField_BodyIntact(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	original := `{"model":"claude-opus-5","prompt":"hello"}`

	var got []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/completions", strings.NewReader(original))
	req.Header.Set("X-Gw-Session-Id", "sess-no-messages")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.JSONEq(t, original, string(got), "无 messages 字段时必须原样透传")
}

// TestSanitizeInputMiddleware_MalformedJSON_BodyIntact 请求体非法 JSON 时，
// 中间件降级放行，但必须把原始字节交给下游，让下游给出准确的错误。
func TestSanitizeInputMiddleware_MalformedJSON_BodyIntact(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	original := `{"model":"claude-opus-5","messages":[{"role":"user",` // 截断

	var got []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("X-Gw-Session-Id", "sess-malformed")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, original, string(got),
		"非法 JSON 也必须原样透传，由下游判定错误")
}

// TestSanitizeInputMiddleware_LargeBody_Intact 大 body（生产事故是 1.64 MiB）
// 必须完整透传，不被截断。
func TestSanitizeInputMiddleware_LargeBody_Intact(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	filler := strings.Repeat("正常的代码上下文内容 no secrets here. ", 40000)
	original := `{"model":"claude-opus-5","messages":[{"role":"user","content":"` + filler + `"}]}`
	require.Greater(t, len(original), 1<<20, "构造的 body 应超过 1 MiB")

	var got []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("X-Gw-Session-Id", "sess-large")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, len(original), len(got), "大 body 必须完整透传")

	var parsed struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	assert.Equal(t, "claude-opus-5", parsed.Model)
}

// TestSanitizeInputMiddleware_Sanitized_ContentLengthSynced 脱敏改写 body 后，
// ContentLength 必须与新 body 一致。
func TestSanitizeInputMiddleware_Sanitized_ContentLengthSynced(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	original := `{"model":"claude-opus-5","messages":[{"role":"user","content":"我的手机号是13800138000"}]}`

	var got []byte
	var gotLen int64
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		gotLen = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("X-Gw-Session-Id", "sess-clen")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.NotContains(t, string(got), "13800138000", "敏感信息应已替换")
	assert.Equal(t, int64(len(got)), gotLen, "ContentLength 必须与改写后的 body 一致")
}

// ── 2026-08-07 回归：role 过滤 + 未知占位符 mask（Bug#2）───────────

// TestSanitizeInputMiddleware_NonUserNonSystemRolesSkipped
// assistant / tool / function 角色的消息不应被脱敏（属于上游生成）。
func TestSanitizeInputMiddleware_NonUserNonSystemRolesSkipped(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	original := `{"model":"gpt-4","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"我的手机号是13800138000"},
		{"role":"tool","content":"call result, my email is foo@bar.com"}
	]}`

	var got []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("X-Gw-Session-Id", "sess-role-skip")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	gotStr := string(got)
	assert.Contains(t, gotStr, `"role":"assistant","content":"我的手机号是13800138000"`,
		"assistant 消息不应被改动")
	assert.Contains(t, gotStr, `"role":"tool","content":"call result, my email is foo@bar.com"`,
		"tool 消息不应被改动")
	assert.NotContains(t, gotStr, "{SENSITIVE:", "不应产出占位符")
}

// TestRestoreResponseBody_NonAssistantRole_UnknownPlaceholderMasked
// 非 assistant role 的响应消息若含未知占位符（sm 找不到），
// 必须 mask 而非透传 — 防止上游注入的 raw placeholder 泄漏给客户端。
//
// 注：sm 中已知的占位符在非 assistant role 下也会还原（统一
// RestoreOutputOrMask），因为那代表真实值。
func TestRestoreResponseBody_NonAssistantRole_UnknownPlaceholderMasked(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// 准备映射表：phone:1 → 13800138000（email:99 不在 sm）
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-mask"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	// 响应体中含 role=tool 的消息，文本里同时有已知占位符与未知占位符
	body := []byte(`{
		"id":"chatcmpl-x",
		"choices":[{
			"message":{"role":"tool","content":"phone {SENSITIVE:phone:1} mail {SENSITIVE:email:99}"}
		}]
	}`)

	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID:    "sess-mask",
		ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result, "tool role 应被处理而非跳过")

	var resp struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &resp))

	msg := resp.Choices[0].Message
	assert.Equal(t, "tool", msg.Role)
	assert.Contains(t, msg.Content, "13800138000",
		"已知占位符应还原（无论 role）")
	assert.NotContains(t, msg.Content, "{SENSITIVE:email:99}",
		"未知占位符必须被 mask，不能泄漏给客户端")
	assert.Contains(t, msg.Content, "[REDACTED]",
		"未知占位符的 mask 文本")
}

// TestRestoreResponseBody_UnknownPlaceholderMasked
// 映射表中不存在的占位符，response 里出现时必须 mask，
// 不能让 {SENSITIVE:phone:99} 这种 raw 占位符泄漏到客户端。
func TestRestoreResponseBody_UnknownPlaceholderMasked(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	ctx := context.Background()
	// 只放 phone:1
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-unknown"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	body := []byte(`{
		"id":"chatcmpl-y",
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"你的手机号是 {SENSITIVE:phone:1}，邮箱 {SENSITIVE:email:99}"
			}
		}]
	}`)

	result, err := it.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID:    "sess-unknown",
		ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(result.ModifiedBody, &resp))
	content := resp.Choices[0].Message.Content
	assert.Contains(t, content, "13800138000", "已知占位符应还原")
	assert.NotContains(t, content, "{SENSITIVE:email:99}",
		"未知占位符必须被 mask，不能透传")
	assert.Contains(t, content, "[REDACTED]", "未知占位符的 mask 文本")
}

// ── 2026-08-07 回归：offset key 拆 key 修复（Bug#4）──────────────────

// TestSanitizeInputMiddleware_MultiRound_OffsetKeyAccumulation
// 跨多轮请求，每类的 offset 必须单调递增（避免占位符撞号 / 覆盖）。
// Bug#4 修复后：offset 存在独立 key session:{sid}:sanitize:offsets，
// 不再依赖扫整个 sanitize map 推导。
func TestSanitizeInputMiddleware_MultiRound_OffsetKeyAccumulation(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	var lastBody []byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))

	send := func(content string) {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"`+content+`"}]}`))
		req.Header.Set("X-Gw-Session-Id", "sess-offset-1")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	// 第 1 轮：1 个 phone
	send("我的手机号是13800138000")
	body1 := string(lastBody)
	assert.Contains(t, body1, "{SENSITIVE:phone:1}", "第 1 轮应是 phone:1")

	// 第 2 轮：再 1 个 phone（不同号码）→ 应是 phone:2
	send("另一个手机13900139000")
	body2 := string(lastBody)
	assert.Contains(t, body2, "{SENSITIVE:phone:2}", "第 2 轮应是 phone:2（不撞号）")

	// 第 3 轮：1 个 email → 应是 email:1（独立计数，phone 不递增）
	send("我的邮箱是 foo@bar.com")
	body3 := string(lastBody)
	assert.Contains(t, body3, "{SENSITIVE:email:1}", "第 3 轮 email 应是 email:1")
	assert.NotContains(t, body3, "{SENSITIVE:phone:",
		"第 3 轮没出现 phone，phone 编号不应增加")

	// 验证 offset key 单独存在，且字段正确
	ctx := context.Background()
	offsets, err := rdb.HGetAll(ctx, SanitizeOffsetRedisKey(HashTenant("_unknown"), "sess-offset-1")).Result()
	require.NoError(t, err)
	assert.Equal(t, "2", offsets["phone"], "phone offset 应累计到 2（第 3 轮无 phone）")
	assert.Equal(t, "1", offsets["email"], "email offset 应为 1")

	// 验证主 map 里有 2 个 phone + 1 个 email
	mapVals, err := rdb.HGetAll(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-offset-1")).Result()
	require.NoError(t, err)
	assert.Len(t, mapVals, 3, "主 map 应有 3 个占位符")
}

// ── 2026-08-07 文档化：流式响应还原限制 ────────────────────────────

// TestSanitizeRestoreInterceptor_StreamChunk_TransparentPassThrough_EmptyMap
// 流式 chunk-level 拦截在无映射表（Redis 中无 placeholder 数据）时
// 必须透明透传 — 既保证不影响未脱敏会话，也避免无 placeholder 时的
// JSON 重序列化噪声。
func TestSanitizeRestoreInterceptor_StreamChunk_TransparentPassThrough_EmptyMap(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"id":"chatcmpl-x","choices":[{"delta":{"content":"hello world"}}]}`)

	result, err := it.InterceptStreamChunk(context.Background(), chunk, &response.StreamMeta{
		SessionID: "sess-stream-no-map",
	})
	require.NoError(t, err)
	assert.Nil(t, result, "无 placeholder 映射表时必须透传")
}

// TestSanitizeRestoreInterceptor_StreamEnd_RecoversAuditBody
// InterceptStreamEnd 对持久化/观测层的 body 做还原；
// 不返回 ModifiedBody（end-result 接口没有该字段），
// 但 action/metadata 用于审计可见性。
func TestSanitizeRestoreInterceptor_StreamEnd_RecoversAuditBody(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-stream-end"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	// stream-end 时 streamCapture 已重组为完整 body
	meta := &response.StreamMeta{
		SessionID:    "sess-stream-end",
		ResponseBody: []byte(`{"choices":[{"message":{"role":"assistant","content":"your phone is {SENSITIVE:phone:1}"}}]}`),
	}

	result, err := it.InterceptStreamEnd(ctx, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "sanitize_restore", result.Action)
	assert.NotNil(t, result.Metadata)
}

// ── 2026-08-07 回归：sessionID header 优先级（与 chatHandler 对齐）───

// TestSanitizeInputMiddleware_AlternativeSessionHeaders
// 客户端如果使用 X-Conversation-Id / X-Chat-Session-Id / X-Thread-Id
// 作为 sessionID header（这些是 chatHandler 默认支持的候选），
// 中间件也必须能识别，并写入对应的 Redis key。
// Bug：之前中间件只读 X-Gw-Session-Id / X-Session-Id 两个 header，
// 导致使用替代 header 的客户端脱敏后无法被还原。
func TestSanitizeInputMiddleware_AlternativeSessionHeaders(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	cases := []struct {
		name   string
		header string
		value  string
	}{
		{"X-Gw-Session-Id", "X-Gw-Session-Id", "session-A"},
		{"X-Session-Id", "X-Session-Id", "session-B"},
		{"X-Conversation-Id", "X-Conversation-Id", "session-C"},
		{"X-Chat-Session-Id", "X-Chat-Session-Id", "session-D"},
		{"X-Thread-Id", "X-Thread-Id", "session-E"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("POST", "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"我的手机号是13800138000"}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(tc.header, tc.value)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			// 验证 Redis key 用的是 header 里的 sessionID
			key := SanitizeRedisKey(HashTenant("_unknown"), tc.value)
			vals, err := rdb.HGetAll(context.Background(), key).Result()
			require.NoError(t, err)
			assert.Contains(t, vals, "{SENSITIVE:phone:1}",
				"header=%s 应被识别为 sessionID 并写入 Redis", tc.header)
			assert.Equal(t, "13800138000", vals["{SENSITIVE:phone:1}"])
		})
	}
}

// TestSanitizeInputMiddleware_SessionHeaderPriority
// 当多个 sessionID header 同时存在时，应按 X-Gw-Session-Id >
// X-Session-Id > X-Conversation-Id > X-Chat-Session-Id > X-Thread-Id
// 的顺序取最高优先级（与 chatHandler SessionHeadersPriority 一致）。
func TestSanitizeInputMiddleware_SessionHeaderPriority(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	mw, err := NewSanitizeInputMiddleware(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 同时设 5 个 header — 应优先用 X-Gw-Session-Id
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"我的手机号是13800138000"}]}`))
	req.Header.Set("X-Gw-Session-Id", "winner")
	req.Header.Set("X-Session-Id", "loser-1")
	req.Header.Set("X-Conversation-Id", "loser-2")
	req.Header.Set("X-Chat-Session-Id", "loser-3")
	req.Header.Set("X-Thread-Id", "loser-4")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	winnerVals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey(HashTenant("_unknown"), "winner")).Result()
	require.NoError(t, err)
	assert.Contains(t, winnerVals, "{SENSITIVE:phone:1}", "X-Gw-Session-Id 应胜出")

	for _, loser := range []string{"loser-1", "loser-2", "loser-3", "loser-4"} {
		loserVals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey(HashTenant("_unknown"), loser)).Result()
		require.NoError(t, err)
		assert.Empty(t, loserVals, "低优先级 header 不应被误用作 sessionID (loser=%s)", loser)
	}
}

// ── 2026-08-07 P2 修复：流式 chunk 还原测试 ──────────────────────────

// TestSanitizeRestoreInterceptor_StreamChunk_OpenAIDeltaRestore
// OpenAI chat completion delta chunk 含占位符时，必须在写入客户端前还原。
func TestSanitizeRestoreInterceptor_StreamChunk_OpenAIDeltaRestore(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-oai"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"id":"chatcmpl-x","choices":[{"delta":{"content":"您的手机号{SENSITIVE:phone:1}"}}]}` + "\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-oai"})
	require.NoError(t, err)
	require.NotNil(t, result, "含占位符的 chunk 必须返回 ModifiedChunk")
	assert.Contains(t, string(result.ModifiedChunk), "13800138000", "占位符必须还原为真实值")
	assert.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:phone:1}", "占位符文本必须被替换")
	// SSE 帧结构保留（data: 前缀 + 末尾 \n\n）
	assert.True(t, bytes.HasPrefix(result.ModifiedChunk, []byte("data: ")))
	assert.True(t, bytes.HasSuffix(result.ModifiedChunk, []byte("\n\n")))
}

// TestSanitizeRestoreInterceptor_StreamChunk_AnthropicDeltaRestore
// Anthropic content_block_delta 类型 chunk 的 delta.text 还原。
func TestSanitizeRestoreInterceptor_StreamChunk_AnthropicDeltaRestore(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-ant"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"phone: {SENSITIVE:phone:1}"}}` + "\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-ant"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, string(result.ModifiedChunk), "13800138000")
}

// TestSanitizeRestoreInterceptor_StreamChunk_AnthropicNonDelta_NoOp
// Anthropic 流式 chunk 不是 content_block_delta 时（如 message_start、ping），
// 必须原样透传，不得误改其他类型字段。
func TestSanitizeRestoreInterceptor_StreamChunk_AnthropicNonDelta_NoOp(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-ant-ctrl"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"type":"message_start","message":{"id":"msg_x"}}` + "\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-ant-ctrl"})
	require.NoError(t, err)
	assert.Nil(t, result, "非 content_block_delta 类型必须透传")
}

// TestSanitizeRestoreInterceptor_StreamChunk_ResponsesDeltaRestore
// OpenAI Responses API response.output_text.delta chunk 还原。
func TestSanitizeRestoreInterceptor_StreamChunk_ResponsesDeltaRestore(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-resp"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"type":"response.output_text.delta","item_id":"msg_x","output_index":0,"content_index":0,"delta":"phone {SENSITIVE:phone:1}"}` + "\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-resp"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, string(result.ModifiedChunk), "13800138000")
}

// TestSanitizeRestoreInterceptor_StreamChunk_NoDataLine_Passthrough
// chunk 不含 data: 行（如控制帧 [DONE]）时必须原样透传。
func TestSanitizeRestoreInterceptor_StreamChunk_NoDataLine_Passthrough(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-done"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	// [DONE] 终止帧
	chunk := []byte("data: [DONE]\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-done"})
	require.NoError(t, err)
	assert.Nil(t, result, "[DONE] 帧必须透传")

	// 空行（心跳注释）
	chunk2 := []byte("\n")
	result2, err := it.InterceptStreamChunk(ctx, chunk2, &response.StreamMeta{SessionID: "sess-done"})
	require.NoError(t, err)
	assert.Nil(t, result2, "空 chunk 必须透传")
}

// TestSanitizeRestoreInterceptor_StreamChunk_UnknownPlaceholderMasked
// chunk 中含映射表里没有的占位符 → 该占位符替换为 [REDACTED]，防止
// 上游注入的 raw 占位符文本泄漏到客户端。
func TestSanitizeRestoreInterceptor_StreamChunk_UnknownPlaceholderMasked(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	// 只放 phone:1 的映射，phone:99 不存在
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-unknown"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk := []byte(`data: {"id":"x","choices":[{"delta":{"content":"已知{SENSITIVE:phone:1} 未知{SENSITIVE:phone:99}"}}]}` + "\n\n")
	result, err := it.InterceptStreamChunk(ctx, chunk, &response.StreamMeta{SessionID: "sess-unknown"})
	require.NoError(t, err)
	require.NotNil(t, result)
	str := string(result.ModifiedChunk)
	assert.Contains(t, str, "13800138000", "已知占位符必须还原")
	assert.Contains(t, str, "[REDACTED]", "未知占位符必须 mask")
	assert.NotContains(t, str, "{SENSITIVE:phone:99}", "raw 占位符文本必须消失")
}

// TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks
// 2026-08-08 audit: SSE framing 在 chunk 边界上是经典坑位. 上游可能把
// 同一 SSE 帧 (data: <json>\n\n) 拆到两次 Write 调用里:
//
//	chunk1 = "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"phone "
//	chunk2 = "{SENSITIVE:phone:1}\"}}]}\n\n"
//
// 当前实现是按 chunk 独立做 SSE framing + JSON 解析; chunk1 没有完整
// JSON, 命中 lineEnd == -1 路径 → 返回 (nil, false, nil) 透传;
// chunk2 开头没有 data: 前缀, 也走不出 data: 行 → 同样透传.
// 净效果: 这一帧不会被还原 (占位符文本泄漏到客户端).
//
// 本测试钉住当前行为, 把"已知缺陷"显式化为回归 — 防止后续开发者
// "修复" 跨 chunk 路径时不慎引入更糟的 bug (例如把第二个 chunk
// 错当成完整 data 行解析). 修复需要 streaming handler.go 层加
// cross-chunk 帧缓冲 (interceptingStreamWriter 的 follow-up 范围),
// 而不是改 restoreStreamChunk 单 chunk 内部行为.
func TestSanitizeRestoreInterceptor_StreamChunk_FrameSplitAcrossChunks(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey(HashTenant("_unknown"), "sess-split"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	chunk1 := []byte(`data: {"id":"x","choices":[{"delta":{"content":"phone `)
	result1, err := it.InterceptStreamChunk(ctx, chunk1, &response.StreamMeta{SessionID: "sess-split"})
	require.NoError(t, err)
	// 当前实现: chunk1 没有完整 data: 行 → 返回 nil 透传, 不修改原 chunk.
	assert.Nil(t, result1, "chunk1 (无完整 data 行) 必须透传")

	chunk2 := []byte(`{SENSITIVE:phone:1}\"}}]}` + "\n\n")
	result2, err := it.InterceptStreamChunk(ctx, chunk2, &response.StreamMeta{SessionID: "sess-split"})
	require.NoError(t, err)
	// chunk2 也没有 data: 前缀, 但它自身能解析成一个"完整 JSON 帧":
	// 行扫描找不到 data: 行 → jsonPayload 空 → 返回 nil 透传.
	assert.Nil(t, result2, "chunk2 (无 data: 前缀) 必须透传")

	// 已知限制: 跨 chunk 拆分场景下占位符不会被还原, raw 占位符
	// 文本会进入客户端. 这个限制需要在 streaming handler 层加 SSE
	// 帧缓冲解决 (interceptingStreamWriter 的 follow-up #1).
}

// ── 2026-08-07 回归：response chain append 行为（修复#1 依赖）────────

// TestResponseChain_ListAndAppend_SafeCopy
// 验证 InterceptorChain.ListInterceptors 返回副本（修改不影响原 chain），
// 且可以基于返回的副本 + 还原拦截器重建一个等价的 chain。
// 这是 installSmartSaniGuard 的核心依赖：append 到现有 chain 末尾。
func TestResponseChain_ListAndAppend_SafeCopy(t *testing.T) {
	outputCompliance := newFakeInterceptor("output_compliance")
	restoreHook := newFakeInterceptor("sanitize_restore")

	original := response.NewInterceptorChain(outputCompliance)
	list := original.ListInterceptors()
	require.Len(t, list, 1)
	assert.Equal(t, "output_compliance", namedAs(list[0]),
		"原 chain 应含 1 个 output_compliance")

	// 修改 list 不应影响 original
	list[0] = newFakeInterceptor("tampered")
	list2 := original.ListInterceptors()
	assert.Equal(t, "output_compliance", namedAs(list2[0]),
		"ListInterceptors 必须返回独立副本（防止外部 mutate 内部状态）")

	// 重建 chain：append 还原拦截器
	expanded := response.NewInterceptorChain(append(original.ListInterceptors(), restoreHook)...)
	expandedList := expanded.ListInterceptors()
	require.Len(t, expandedList, 2, "append 后应有 2 个拦截器")
	assert.Equal(t, "output_compliance", namedAs(expandedList[0]),
		"原拦截器必须在最前面（output_compliance → sanitize_restore 顺序）")
	assert.Equal(t, "sanitize_restore", namedAs(expandedList[1]),
		"sanitize_restore 必须在 output_compliance 之后（用户要求'先安全检查再还原'）")
}

// namedAs 提取拦截器名（response.ResponseInterceptor 接口本身没有 Name()，
// 需要类型断言；fakeInterceptor 实现 Name()）。
func namedAs(it response.ResponseInterceptor) string {
	if n, ok := it.(interface{ Name() string }); ok {
		return n.Name()
	}
	return ""
}

// fakeInterceptor 测试用 ResponseInterceptor stub。
type fakeInterceptor struct {
	name string
}

func newFakeInterceptor(name string) response.ResponseInterceptor {
	return &fakeInterceptor{name: name}
}

func (f *fakeInterceptor) Name() string { return f.name }

func (f *fakeInterceptor) InterceptNonStream(_ context.Context, _ *response.InterceptRequest) (*response.InterceptResult, error) {
	return nil, nil
}

func (f *fakeInterceptor) InterceptStreamChunk(_ context.Context, chunk []byte, _ *response.StreamMeta) (*response.ChunkResult, error) {
	return nil, nil
}

func (f *fakeInterceptor) InterceptStreamEnd(_ context.Context, _ *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}
