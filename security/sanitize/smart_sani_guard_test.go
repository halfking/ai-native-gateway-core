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

// Replaying the same value retains one identity; a distinct value allocates
// the next token without overwriting the old mapping.
func TestSanitizeInputMiddleware_MultiRoundNoCollision(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	var bodies []string
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
	}))
	for _, phone := range []string{"13800138000", "13800138000", "13900139000"} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"`+phone+`"}]}`))
		req.Header.Set("X-Gw-Session-Id", "session-multi")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code)
	}
	require.Equal(t, bodies[0], bodies[1])
	require.Contains(t, bodies[2], "{SENSITIVE:phone:2}")
	values, err := rdb.HGetAll(context.Background(), SanitizeRedisKey("session-multi")).Result()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"{SENSITIVE:phone:1}": "13800138000", "{SENSITIVE:phone:2}": "13900139000"}, values)
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

// TestSanitizeInputMiddleware_MalformedJSONRejected 请求体非法 JSON 时，
// 中间件不能确认哪些文本需要脱敏，因此不得继续转发。
func TestSanitizeInputMiddleware_MalformedJSONRejected(t *testing.T) {
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
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, got, "非法 JSON 不得进入下游")
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

// TestSanitizeInputMiddleware_ReplayedAssistantAndToolRolesSanitized
// 已还原的 assistant/tool 历史必须在重放时重新脱敏。
func TestSanitizeInputMiddleware_ReplayedAssistantAndToolRolesSanitized(t *testing.T) {
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
	assert.NotContains(t, gotStr, "13800138000")
	assert.NotContains(t, gotStr, "foo@bar.com")
	assert.Contains(t, gotStr, "{SENSITIVE:phone:1}")
	assert.Contains(t, gotStr, "{SENSITIVE:email:1}")
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-mask"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-unknown"),
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

func TestRestoreResponseBody_UnknownPlaceholderMaskedWhenSessionMapIsEmpty(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"forged {SENSITIVE:phone:9}"}}]}`)

	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "sess-empty-map",
		ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result, "empty mapping must not bypass placeholder validation")
	assert.Contains(t, string(result.ModifiedBody), "[REDACTED]")
	assert.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:")
}

func TestRestoreResponseBody_UnknownPlaceholderMaskedWhenRedisIsUnavailable(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	mr.Close()

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"forged {SENSITIVE:phone:9}"}}]}`)
	result, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{
		SessionID:    "sess-redis-unavailable",
		ResponseBody: body,
	})
	require.NoError(t, err)
	require.NotNil(t, result, "Redis failure must not bypass placeholder validation")
	assert.Contains(t, string(result.ModifiedBody), "[REDACTED]")
	assert.NotContains(t, string(result.ModifiedBody), "{SENSITIVE:")
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
	offsets, err := rdb.HGetAll(ctx, SanitizeOffsetRedisKey("sess-offset-1")).Result()
	require.NoError(t, err)
	assert.Equal(t, "2", offsets["phone"], "phone offset 应累计到 2（第 3 轮无 phone）")
	assert.Equal(t, "1", offsets["email"], "email offset 应为 1")

	// 验证主 map 里有 2 个 phone + 1 个 email
	mapVals, err := rdb.HGetAll(ctx, SanitizeRedisKey("sess-offset-1")).Result()
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-stream-end"),
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
			key := SanitizeRedisKey(tc.value)
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

	winnerVals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey("winner")).Result()
	require.NoError(t, err)
	assert.Contains(t, winnerVals, "{SENSITIVE:phone:1}", "X-Gw-Session-Id 应胜出")

	for _, loser := range []string{"loser-1", "loser-2", "loser-3", "loser-4"} {
		loserVals, err := rdb.HGetAll(context.Background(), SanitizeRedisKey(loser)).Result()
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-oai"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-ant"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-ant-ctrl"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-resp"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-done"),
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
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-unknown"),
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

// TestSanitizeRestoreInterceptor_PlaceholderSplitAcrossCompleteFrames
// SSE handler 已负责把同一事件内拆开的多次 Write 缓冲成完整帧；本测试
// 覆盖不同完整事件之间的文本增量边界。LLM 可能把一个占位符拆成多个
// delta.content 事件；interceptor 必须暂存有效尾片，再还原后续完整事件，
// 并且不能把尾片泄漏到客户端。生产 writer 集成另有独立回归测试。
func TestSanitizeRestoreInterceptor_PlaceholderSplitAcrossCompleteFrames(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-split"),
		"{SENSITIVE:phone:1}", "13800138000").Err())

	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)

	meta := &response.StreamMeta{SessionID: "sess-split", RequestID: "req-split", State: response.NewStreamState()}
	chunk1 := []byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"phone {SENSITIVE:phone:\"}}]}\n\n")
	result1, err := it.InterceptStreamChunk(ctx, chunk1, meta)
	require.NoError(t, err)
	require.NotNil(t, result1, "不完整占位符尾片应从当前事件中暂存")
	assert.Contains(t, string(result1.ModifiedChunk), `"content":"phone "`)
	assert.NotContains(t, string(result1.ModifiedChunk), "{SENSITIVE:")

	chunk2 := []byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"1} suffix\"}}]}\n\n")
	result2, err := it.InterceptStreamChunk(ctx, chunk2, meta)
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Contains(t, string(result2.ModifiedChunk), "13800138000 suffix")
	assert.NotContains(t, string(result2.ModifiedChunk), "{SENSITIVE:")
}

func TestSanitizeRestoreInterceptor_SplitUnknownPlaceholderMasksWithoutMap(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-unknown-split", RequestID: "req-unknown-split", State: response.NewStreamState()}

	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"before {SENS\"}}]}\n\n")
	gotFirst, err := it.InterceptStreamChunk(context.Background(), first, meta)
	require.NoError(t, err)
	require.NotNil(t, gotFirst)
	assert.Contains(t, string(gotFirst.ModifiedChunk), `"content":"before "`)
	assert.NotContains(t, string(gotFirst.ModifiedChunk), "{SENS")

	second := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ITIVE:forged:7} after\"}}]}\n\n")
	gotSecond, err := it.InterceptStreamChunk(context.Background(), second, meta)
	require.NoError(t, err)
	require.NotNil(t, gotSecond)
	assert.Contains(t, string(gotSecond.ModifiedChunk), "[REDACTED] after")
	assert.NotContains(t, string(gotSecond.ModifiedChunk), "{SENSITIVE:")
}

func TestSanitizeRestoreInterceptor_StreamTailIsRequestAndFieldScoped(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-lanes"),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-lanes", RequestID: "req-lanes", State: response.NewStreamState()}

	contentPrefix := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x {SENSITIVE:phone:\"}}]}\n\n")
	first, err := it.InterceptStreamChunk(ctx, contentPrefix, meta)
	require.NoError(t, err)
	require.NotNil(t, first)
	toolData, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": `{"to":"safe"}`}}}}}}})
	require.NoError(t, err)
	toolDelta := append(append([]byte("data: "), toolData...), []byte("\n\n")...)
	tool, err := it.InterceptStreamChunk(ctx, toolDelta, meta)
	require.NoError(t, err)
	assert.Nil(t, tool, "different output lane must not consume the content tail")

	contentSuffix := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"1} y\"}}]}\n\n")
	last, err := it.InterceptStreamChunk(ctx, contentSuffix, meta)
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Contains(t, string(last.ModifiedChunk), "13800138000 y")
	assert.NotContains(t, string(last.ModifiedChunk), "{SENSITIVE:")

	otherRequest := &response.StreamMeta{SessionID: "sess-lanes", RequestID: "req-other", State: response.NewStreamState()}
	otherChunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"1} remains separate\"}}]}\n\n")
	other, err := it.InterceptStreamChunk(ctx, otherChunk, otherRequest)
	require.NoError(t, err)
	assert.Nil(t, other, "a separate stream must not inherit another request's tail")
}

func TestSanitizeRestoreInterceptor_IncompletePlaceholderTailIsDropped(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-truncated", RequestID: "req-truncated", State: response.NewStreamState()}
	chunk := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"safe {SENSITIVE:phone:\"}}]}\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), chunk, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, string(result.ModifiedChunk), `"content":"safe "`)
	assert.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	// If the stream ends here, the uncommitted tail is discarded with its
	// request-local StreamState; no process-global map retains it.
}

func TestSanitizeRestoreInterceptor_SplitPlaceholderAcrossProtocolDeltaFields(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	ctx := context.Background()
	require.NoError(t, rdb.HSet(ctx, SanitizeRedisKey("sess-protocol-split"),
		"{SENSITIVE:phone:1}", "13800138000").Err())
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	encodeFrame := func(payload map[string]any) []byte {
		data, marshalErr := json.Marshal(payload)
		require.NoError(t, marshalErr)
		return append(append([]byte("data: "), data...), []byte("\n\n")...)
	}
	content := func(value string) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": value}}}}
	}
	openAIToolArgs := func(value string) map[string]any {
		tool := map[string]any{"index": 4, "function": map[string]any{"arguments": value}}
		delta := map[string]any{"tool_calls": []any{tool}}
		return map[string]any{"choices": []any{map[string]any{"delta": delta}}}
	}
	openAIFunctionCallArgs := func(value string) map[string]any {
		return map[string]any{"choices": []any{map[string]any{"index": 2, "delta": map[string]any{"function_call": map[string]any{"arguments": value}}}}}
	}
	tests := []struct {
		name  string
		first map[string]any
		last  map[string]any
	}{
		{
			name:  "responses output_text delta",
			first: map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "msg_1", "delta": "say {SENSITIVE:phone:"},
			last:  map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "msg_1", "delta": "1} now"},
		},
		{
			name:  "responses function_call_arguments delta",
			first: map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc_1", "delta": `{"phone":"{SENSITIVE:phone:`},
			last:  map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc_1", "delta": `1}"}`},
		},
		{
			name:  "responses refusal delta",
			first: map[string]any{"type": "response.refusal.delta", "item_id": "msg_refusal", "delta": "noted {SENSITIVE:phone:"},
			last:  map[string]any{"type": "response.refusal.delta", "item_id": "msg_refusal", "delta": "1} safely"},
		},
		{
			name:  "responses audio transcript delta",
			first: map[string]any{"type": "response.audio_transcript.delta", "item_id": "msg_audio", "delta": "spoken {SENSITIVE:phone:"},
			last:  map[string]any{"type": "response.audio_transcript.delta", "item_id": "msg_audio", "delta": "1} safely"},
		},
		{
			name:  "anthropic text delta",
			first: map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "text_delta", "text": "say {SENSITIVE:phone:"}},
			last:  map[string]any{"type": "content_block_delta", "index": 2, "delta": map[string]any{"type": "text_delta", "text": "1} now"}},
		},
		{
			name:  "anthropic partial_json tool delta",
			first: map[string]any{"type": "content_block_delta", "index": 3, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"to\":\"{SENSITIVE:phone:"}},
			last:  map[string]any{"type": "content_block_delta", "index": 3, "delta": map[string]any{"type": "input_json_delta", "partial_json": "1}\"}"}},
		},
		{
			name:  "openai tool arguments",
			first: openAIToolArgs("{\"to\":\"{SENSITIVE:phone:"),
			last:  openAIToolArgs("1}\"}"),
		},
		{
			name:  "openai legacy function_call arguments",
			first: openAIFunctionCallArgs("{\"to\":\"{SENSITIVE:phone:"),
			last:  openAIFunctionCallArgs("1}\"}"),
		},
		{
			name:  "openai refusal delta",
			first: map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"refusal": "refusal {SENSITIVE:phone:"}}}},
			last:  map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"refusal": "1} safe"}}}},
		},
		{
			name:  "openai content",
			first: content("say {SENSITIVE:phone:"),
			last:  content("1} now"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := &response.StreamMeta{SessionID: "sess-protocol-split", RequestID: tt.name, State: response.NewStreamState()}
			first, err := it.InterceptStreamChunk(ctx, encodeFrame(tt.first), meta)
			require.NoError(t, err)
			require.NotNil(t, first)
			assert.NotContains(t, string(first.ModifiedChunk), "{SENSITIVE:")
			last, err := it.InterceptStreamChunk(ctx, encodeFrame(tt.last), meta)
			require.NoError(t, err)
			require.NotNil(t, last)
			assert.Contains(t, string(last.ModifiedChunk), "13800138000")
			assert.NotContains(t, string(last.ModifiedChunk), "{SENSITIVE:")
		})
	}
}

func TestSanitizeRestoreInterceptor_BlocksWhenStreamLaneBoundIsExceeded(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	choices := make([]any, 0, maxStreamRestoreLanes+1)
	for i := 0; i < maxStreamRestoreLanes+1; i++ {
		choices = append(choices, map[string]any{"delta": map[string]any{"content": "{SENSITIVE:phone:"}})
	}
	payload, err := json.Marshal(map[string]any{"choices": choices})
	require.NoError(t, err)
	frame := append(append([]byte("data: "), payload...), []byte("\n\n")...)
	meta := &response.StreamMeta{SessionID: "sess-lane-limit", RequestID: "req-lane-limit", State: response.NewStreamState()}
	result, err := it.InterceptStreamChunk(context.Background(), frame, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldBlock, "exhausting the per-stream lane bound must fail closed")
	state, ok := meta.State.GetOrCreate(streamRestoreStateKey, func() any { return &streamRestoreState{} }).(*streamRestoreState)
	require.True(t, ok)
	assert.LessOrEqual(t, len(state.tails), maxStreamRestoreLanes)
}

func TestSanitizeRestoreInterceptor_BoundsUntrustedStreamLaneIdentifiers(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-lane-id-bound", RequestID: "req-lane-id-bound", State: response.NewStreamState()}
	longID := strings.Repeat("x", 128*1024)
	frame := []byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":" + `"` + longID + `"` + ",\"delta\":\"{SENSITIVE:phone:\"}\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), frame, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	state, ok := meta.State.GetOrCreate(streamRestoreStateKey, func() any { return &streamRestoreState{} }).(*streamRestoreState)
	require.True(t, ok)
	require.Len(t, state.tails, 1)
	for lane := range state.tails {
		assert.Less(t, len(lane), 160, "opaque provider ID must not inflate the state-map key")
	}
}

func TestSanitizeRestoreInterceptor_MasksInvalidPlaceholderContinuationAndKeepsStreamOpen(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-invalid-continuation", RequestID: "req-invalid-continuation", State: response.NewStreamState()}
	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:\"}}]}\n\n")
	_, err = it.InterceptStreamChunk(context.Background(), first, meta)
	require.NoError(t, err)
	invalid := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"13800138000x tail\"}}]}\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), invalid, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.ShouldBlock, "a malformed placeholder must not terminate the client stream")
	assert.Contains(t, string(result.ModifiedChunk), "[REDACTED]")
	assert.NotContains(t, string(result.ModifiedChunk), "{SENSITIVE:")
	assert.NotContains(t, string(result.ModifiedChunk), "13800138000")
	continued, err := it.InterceptStreamChunk(context.Background(), contentFrame("following"), meta)
	require.NoError(t, err)
	if continued != nil {
		assert.False(t, continued.ShouldBlock, "later response content must continue after redaction")
	}
}

func TestSanitizeRestoreInterceptor_BracePrefixContinuationPreservesStream(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-json-prefix", RequestID: "req-json-prefix", State: response.NewStreamState()}

	first, err := it.InterceptStreamChunk(context.Background(), contentFrame("{"), meta)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Contains(t, string(first.ModifiedChunk), `"content":""`)

	second, err := it.InterceptStreamChunk(context.Background(), contentFrame(`"key":1}`), meta)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.ShouldBlock, "ordinary JSON content must not abort the client stream")
	assert.Contains(t, string(second.ModifiedChunk), `"content":"{\"key\":1}"`)
}

func TestSanitizeRestoreInterceptor_BlocksOpaqueFrameWhilePlaceholderTailIsPending(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, err := NewSanitizer(NewPatternDetector())
	require.NoError(t, err)
	it, err := NewSanitizeRestoreInterceptor(s, rdb, 30*time.Minute)
	require.NoError(t, err)
	meta := &response.StreamMeta{SessionID: "sess-opaque-continuation", RequestID: "req-opaque-continuation", State: response.NewStreamState()}
	first := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"{SENSITIVE:phone:\"}}]}\n\n")
	_, err = it.InterceptStreamChunk(context.Background(), first, meta)
	require.NoError(t, err)

	malformed := []byte("data: \"opaque continuation 1}\"\n\n")
	result, err := it.InterceptStreamChunk(context.Background(), malformed, meta)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.ShouldBlock, "opaque payload cannot safely consume a withheld protocol-lane prefix")
}

func contentFrame(value string) []byte {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": value}}}})
	return append(append([]byte("data: "), data...), []byte("\n\n")...)
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
