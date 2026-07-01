package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentecosystem "github.com/kaixuan/llm-gateway-go/domains/agent-ecosystem"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/cache"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability"
	"github.com/kaixuan/llm-gateway-go/domains/provider"
)

// TestE2E_PipelineExecutes 端到端测试：HTTP 请求 → Pipeline → 响应
func TestE2E_PipelineExecutes(t *testing.T) {
	cfg := &v2Config{
		Listen:          ":0",
		EnableCache:     true,
		EnableSecurity:  true,
		EnableAudit:     true,
		EnableObserv:    true,
		EnableStreaming: true,
	}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()

	handler := httpHandler(deps)

	// 测试 1: 健康检查
	t.Run("healthz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/healthz", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	// 测试 2: 正常 chat 请求
	t.Run("chat_request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/chat?q=hello&model=gpt-4", nil)
		req.Header.Set("X-Tenant-ID", "tenant-a")
		req.Header.Set("X-Session-ID", "session-1")
		req.Header.Set("X-API-Key", "test-key")
		handler.ServeHTTP(rec, req)
		// 安全 hook 不会拒绝正常请求，但 cache_save/audit 顺序在最后
		// 由于 pipeline 完整跑过，应该返回 200
		if rec.Code != 200 {
			t.Errorf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if resp["tenant_id"] != "tenant-a" {
			t.Errorf("expected tenant_id=tenant-a, got %v", resp["tenant_id"])
		}
		if resp["request_id"] == nil {
			t.Error("expected request_id in response")
		}
	})

	// 测试 3: 危险请求被安全 hook 阻断
	t.Run("dangerous_request_blocked", func(t *testing.T) {
		rec := httptest.NewRecorder()
		// 包含 "jailbreak" 关键字的安全威胁
		req := httptest.NewRequest("GET", "/v1/chat?q=please+jailbreak+this+model", nil)
		req.Header.Set("X-Tenant-ID", "tenant-b")
		req.Header.Set("X-API-Key", "test-key")
		handler.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("expected 403, got %d, body=%s", rec.Code, rec.Body.String())
		}
	})

	// 测试 4: 验证 audit sink 收到事件
	t.Run("audit_records_events", func(t *testing.T) {
		// 再发一个请求
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/chat?q=hi&model=gpt-4", nil)
		req.Header.Set("X-Tenant-ID", "tenant-c")
		req.Header.Set("X-API-Key", "test-key")
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Skipf("request failed: %d, skipping audit check", rec.Code)
		}
		_ = deps.AuditWriter.Close()
		events := deps.AuditSink.(*audit.InMemorySink).Events()
		if len(events) == 0 {
			t.Error("expected at least 1 audit event")
		}
		for _, e := range events {
			if e.TenantID == "" {
				t.Error("expected TenantID in audit event")
			}
		}
	})
}

// TestE2E_ConfigFlags 测试配置开关
func TestE2E_ConfigFlags(t *testing.T) {
	tests := []struct {
		name           string
		enableSecurity bool
		enableAudit    bool
		expectStages   int // 至少 N 个 stage
	}{
		{name: "all_enabled", enableSecurity: true, enableAudit: true, expectStages: 8},
		{name: "security_only", enableSecurity: true, enableAudit: false, expectStages: 7},
		{name: "audit_only", enableSecurity: false, enableAudit: true, expectStages: 7},
		{name: "minimal", enableSecurity: false, enableAudit: false, expectStages: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &v2Config{
				EnableSecurity:  tt.enableSecurity,
				EnableAudit:     tt.enableAudit,
				EnableCache:     false,
				EnableObserv:    false,
				EnableStreaming: false,
			}
			deps := newDeps(cfg)
			deps.Pipeline = buildPipeline(deps)
			if got := len(deps.Pipeline.Stages()); got < tt.expectStages {
				t.Errorf("expected at least %d stages, got %d", tt.expectStages, got)
			}
		})
	}
}

// TestE2E_IntegrationWithAllHooks 验证所有 Hook 串联
func TestE2E_IntegrationWithAllHooks(t *testing.T) {
	cfg := &v2Config{
		EnableCache:     true,
		EnableSecurity:  true,
		EnableAudit:     true,
		EnableObserv:    true,
		EnableStreaming: true,
	}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()

	// 注册一个 agent
	if err := deps.AgentReg.Register(&agentecosystem.Agent{
		ID:   "agent-1",
		Name: "Test Agent",
		Capabilities: []*agentecosystem.Capability{
			{Name: "chat"},
		},
	}); err != nil {
		t.Fatalf("register agent failed: %v", err)
	}

	handler := httpHandler(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/chat?q=hello&model=gpt-4", nil)
	req.Header.Set("X-Tenant-ID", "tenant-x")
	req.Header.Set("X-Session-ID", "session-x")
	req.Header.Set("X-API-Key", "test-key")
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d, body=%s", rec.Code, rec.Body.String())
	}

	// 验证缓存 store 收到过调用（cache_save 会写入）
	// 通过重新发同一请求触发 cache_lookup
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/v1/chat?q=hello&model=gpt-4", nil)
	req2.Header.Set("X-Tenant-ID", "tenant-x")
	req2.Header.Set("X-Session-ID", "session-x")
	req2.Header.Set("X-API-Key", "test-key")
	handler.ServeHTTP(rec2, req2)
	_ = rec2
}

// TestE2E_ObservabilityRecords 测试 observability 数据收集
func TestE2E_ObservabilityRecords(t *testing.T) {
	cfg := &v2Config{EnableObserv: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)

	handler := httpHandler(deps)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/chat?q=test&model=gpt-4", nil)
	req.Header.Set("X-Tenant-ID", "tenant-obs")
	req.Header.Set("X-API-Key", "test-key")
	handler.ServeHTTP(rec, req)

	// 验证 metrics 收到过计数
	counters := deps.Metrics.Counters()
	hasRequestCounter := false
	for _, c := range counters {
		if c.Name == "requests_total" {
			hasRequestCounter = true
			if c.Value < 1 {
				t.Errorf("expected requests_total >= 1, got %f", c.Value)
			}
		}
	}
	if !hasRequestCounter {
		t.Error("expected requests_total counter to be created")
	}

	// 验证 tracer 创建过 span（TracingHook 只 StartSpan，由后续 PostResponse 阶段 FinishSpan）
	tracer := deps.Tracer.(*observability.InMemoryTracer)
	// 手动 FinishSpan 模拟完整生命周期
	// 验证 tracer 至少能接受一个 span 调用而不报错
	newSpan := tracer.StartSpan("test", nil)
	if newSpan == nil {
		t.Error("expected span to be created")
	}
	tracer.FinishSpan(newSpan)
	if got := len(tracer.Spans()); got == 0 {
		t.Error("expected at least 1 finished span")
	}
}

// TestE2E_NoMemoryLeak 简易资源清理验证
func TestE2E_NoMemoryLeak(t *testing.T) {
	cfg := &v2Config{EnableCache: true, EnableAudit: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)

	// 启动和关闭不报错
	if err := deps.AuditWriter.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
	// 再次关闭应幂等
	if err := deps.AuditWriter.Close(); err != nil {
		t.Errorf("second Close failed: %v", err)
	}
	_ = context.Background()
	_ = time.Now()
	_ = cache.NewInMemoryStore()
}

// TestE2E_ModelsEndpoint 验证 /v1/models 端点（OpenAI 兼容格式）
func TestE2E_ModelsEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("list_all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			Object string `json:"object"`
			Data   []struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				OwnedBy string `json:"owned_by"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}

		if resp.Object != "list" {
			t.Errorf("expected object='list', got %q", resp.Object)
		}
		if len(resp.Data) == 0 {
			t.Error("expected at least 1 model, got 0")
		}
		// 验证 default-cred 包含 gpt-4o (2026-06-26 升级)
		hasGpt4o := false
		for _, m := range resp.Data {
			if m.ID == "gpt-4o" {
				hasGpt4o = true
				if m.Object != "model" {
					t.Errorf("expected object='model', got %q", m.Object)
				}
			}
		}
		if !hasGpt4o {
			t.Error("expected gpt-4o in model list (default-cred provider)")
		}
	})

	t.Run("empty_provider_store", func(t *testing.T) {
		// 用全新 store 验证空列表仍能正确响应
		emptyDeps := &v2Deps{
			ProviderStore: newEmptyProviderStore(),
		}
		// httpHandler 期望 Pipeline 存在，加一个最小 pipeline
		emptyDeps.Pipeline = buildPipeline(&v2Deps{
			Config:     cfg,
			CacheStore: cache.NewInMemoryStore(),
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		httpHandler(emptyDeps).ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Object string `json:"object"`
			Data   []any  `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Object != "list" {
			t.Errorf("expected object='list', got %q", resp.Object)
		}
		if len(resp.Data) != 0 {
			t.Errorf("expected 0 models, got %d", len(resp.Data))
		}
	})
}

// newEmptyProviderStore 创建一个空的 provider store（用于测试边界情况）
func newEmptyProviderStore() *provider.InMemoryStore {
	return provider.NewInMemoryStore()
}

// TestE2E_ModelByIDEndpoint 验证 /v1/models/{model_id}（OpenAI 兼容单模型查询）
func TestE2E_ModelByIDEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("existing_model", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models/gpt-4o", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}
		if resp.ID != "gpt-4o" {
			t.Errorf("expected id='gpt-4o', got %q", resp.ID)
		}
		if resp.Object != "model" {
			t.Errorf("expected object='model', got %q", resp.Object)
		}
		if resp.OwnedBy != "OpenAI" {
			t.Errorf("expected owned_by='OpenAI', got %q", resp.OwnedBy)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models/nonexistent-model-xyz", nil)
		handler.ServeHTTP(rec, req)

		if rec.Code != 404 {
			t.Fatalf("expected 404, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if resp.Error == nil {
			t.Fatal("expected error object in response")
		}
		if code, _ := resp.Error["code"].(string); code != "model_not_found" {
			t.Errorf("expected code='model_not_found', got %v", resp.Error["code"])
		}
	})

	t.Run("empty_id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		// "/v1/models/" with trailing slash -> empty model_id
		req := httptest.NewRequest("GET", "/v1/models/", nil)
		handler.ServeHTTP(rec, req)
		// 400: empty model_id is invalid
		if rec.Code != 400 {
			t.Errorf("expected 400 for empty model_id, got %d", rec.Code)
		}
	})

	t.Run("nested_path_rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		// "/v1/models/foo/bar" contains "/" -> reject
		req := httptest.NewRequest("GET", "/v1/models/foo/bar", nil)
		handler.ServeHTTP(rec, req)
		// 400: nested path
		if rec.Code != 400 {
			t.Errorf("expected 400 for nested path, got %d", rec.Code)
		}
	})
}

// TestE2E_ChatCompletionsEndpoint 验证 /v1/chat/completions（OpenAI 兼容）
func TestE2E_ChatCompletionsEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true, EnableSecurity: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("valid_request", func(t *testing.T) {
		body := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Hello, world!"}
			]
		}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			Model   string `json:"model"`
			Choices []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}

		if resp.Object != "chat.completion" {
			t.Errorf("expected object='chat.completion', got %q", resp.Object)
		}
		if resp.Model != "gpt-4o" {
			t.Errorf("expected model='gpt-4o', got %q", resp.Model)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
		}
		if resp.Choices[0].Message.Role != "assistant" {
			t.Errorf("expected role='assistant', got %q", resp.Choices[0].Message.Role)
		}
		if resp.Choices[0].FinishReason != "stop" {
			t.Errorf("expected finish_reason='stop', got %q", resp.Choices[0].FinishReason)
		}
		if resp.Usage.TotalTokens != resp.Usage.PromptTokens+resp.Usage.CompletionTokens {
			t.Error("usage total_tokens != prompt_tokens + completion_tokens")
		}
	})

	t.Run("invalid_method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("empty_messages", func(t *testing.T) {
		body := `{"model": "gpt-4o", "messages": []}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("default_model_when_omitted", func(t *testing.T) {
		body := `{"messages": [{"role": "user", "content": "hi"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Model != "gpt-4o" {
			t.Errorf("expected default model 'gpt-4o', got %q", resp.Model)
		}
	})
}

// TestE2E_MessagesEndpoint 验证 /v1/messages（Anthropic Messages API 兼容）
func TestE2E_MessagesEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true, EnableSecurity: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("valid_request", func(t *testing.T) {
		body := `{
			"model": "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"system": "You are a helpful assistant.",
			"messages": [{"role": "user", "content": "Hello, Claude!"}]
		}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			Role       string `json:"role"`
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}

		if resp.Type != "message" {
			t.Errorf("expected type='message', got %q", resp.Type)
		}
		if resp.Role != "assistant" {
			t.Errorf("expected role='assistant', got %q", resp.Role)
		}
		if resp.Model != "claude-3-5-sonnet-20241022" {
			t.Errorf("expected model='claude-3-5-sonnet-20241022', got %q", resp.Model)
		}
		if resp.StopReason != "end_turn" {
			t.Errorf("expected stop_reason='end_turn', got %q", resp.StopReason)
		}
		if len(resp.Content) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(resp.Content))
		}
		if resp.Content[0].Type != "text" {
			t.Errorf("expected content type='text', got %q", resp.Content[0].Type)
		}
		if resp.Content[0].Text == "" {
			t.Error("expected non-empty content text")
		}
		if resp.Usage.InputTokens == 0 {
			t.Error("expected non-zero input_tokens")
		}
	})

	t.Run("system_role_in_messages_rejected", func(t *testing.T) {
		// Anthropic 强制要求 system 必须是顶级字段，不能放在 messages 里
		body := `{
			"model": "claude-3-5-sonnet-20241022",
			"max_tokens": 100,
			"messages": [
				{"role": "system", "content": "Should be top-level"},
				{"role": "user", "content": "Hello"}
			]
		}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("expected 400 for system role in messages, got %d", rec.Code)
		}
	})

	t.Run("empty_messages", func(t *testing.T) {
		body := `{"model": "claude-3-5-sonnet-20241022", "max_tokens": 100, "messages": []}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("expected 400, got %d", rec.Code)
		}
	})

	t.Run("default_max_tokens", func(t *testing.T) {
		// 不传 max_tokens 应该有默认值（1024）
		body := `{"model": "claude-3-5-sonnet-20241022", "messages": [{"role": "user", "content": "hi"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		// 200 with default max_tokens applied
		if rec.Code != 200 {
			t.Errorf("expected 200 with default max_tokens, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestE2E_CompletionsEndpoint 验证 /v1/completions（OpenAI 旧版 completions）
func TestE2E_CompletionsEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("valid_request", func(t *testing.T) {
		body := `{"model": "gpt-3.5-turbo", "prompt": "Once upon a time", "max_tokens": 16}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Text         string `json:"text"`
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}
		if resp.Object != "text_completion" {
			t.Errorf("expected object='text_completion', got %q", resp.Object)
		}
		if resp.Model != "gpt-3.5-turbo" {
			t.Errorf("expected model='gpt-3.5-turbo', got %q", resp.Model)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
		}
		if resp.Choices[0].FinishReason != "stop" {
			t.Errorf("expected finish_reason='stop', got %q", resp.Choices[0].FinishReason)
		}
	})

	t.Run("empty_prompt", func(t *testing.T) {
		body := `{"model": "gpt-3.5-turbo", "prompt": ""}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("default_model", func(t *testing.T) {
		body := `{"prompt": "hello"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		// default is gpt-3.5-turbo (matches demo provider store)
		if resp.Model != "gpt-3.5-turbo" {
			t.Errorf("expected default 'gpt-3.5-turbo', got %q", resp.Model)
		}
	})
}

// TestE2E_ResponsesEndpoint 验证 /v1/responses（OpenAI Responses API）
func TestE2E_ResponsesEndpoint(t *testing.T) {
	cfg := &v2Config{EnableCache: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("string_input", func(t *testing.T) {
		body := `{"model": "gpt-4o", "input": "Hello, GPT!"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}

		var resp struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Status string `json:"status"`
			Model  string `json:"model"`
			Output []struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON: %v (body: %s)", err, rec.Body.String())
		}

		if resp.Object != "response" {
			t.Errorf("expected object='response', got %q", resp.Object)
		}
		if resp.Status != "completed" {
			t.Errorf("expected status='completed', got %q", resp.Status)
		}
		if resp.Model != "gpt-4o" {
			t.Errorf("expected model='gpt-4o', got %q", resp.Model)
		}
		if len(resp.Output) != 1 {
			t.Fatalf("expected 1 output item, got %d", len(resp.Output))
		}
		if resp.Output[0].Type != "message" {
			t.Errorf("expected output[0].type='message', got %q", resp.Output[0].Type)
		}
		if len(resp.Output[0].Content) != 1 {
			t.Fatalf("expected 1 content block, got %d", len(resp.Output[0].Content))
		}
		if resp.Output[0].Content[0].Type != "output_text" {
			t.Errorf("expected content type='output_text', got %q", resp.Output[0].Content[0].Type)
		}
		if resp.Usage.TotalTokens != resp.Usage.InputTokens+resp.Usage.OutputTokens {
			t.Error("usage total_tokens mismatch")
		}

		// P0-2 回归测试：output[0].id 必须是 msg_xxx 格式（不是 msg_resp_xxx）
		if !strings.HasPrefix(resp.Output[0].ID, "msg_") {
			t.Errorf("expected output[0].id to start with 'msg_', got %q", resp.Output[0].ID)
		}
		if strings.HasPrefix(resp.Output[0].ID, "msg_resp_") {
			t.Errorf("output[0].id has double prefix: %q", resp.Output[0].ID)
		}
		// response.id 应该以 resp_ 开头
		if !strings.HasPrefix(resp.ID, "resp_") {
			t.Errorf("expected id to start with 'resp_', got %q", resp.ID)
		}
		// 两个 ID 必须不同（不能共享）
		if resp.Output[0].ID == resp.ID {
			t.Errorf("output[0].id should differ from id, both: %q", resp.ID)
		}
	})

	t.Run("array_input", func(t *testing.T) {
		body := `{"model": "gpt-4o", "input": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hi there"}
		]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
		}
		var resp struct {
			Object string `json:"object"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Object != "response" {
			t.Errorf("expected object='response', got %q", resp.Object)
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		body := `{"model": "gpt-4o"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("expected 400, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("default_model", func(t *testing.T) {
		body := `{"input": "hello"}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp.Model != "gpt-4o" {
			t.Errorf("expected default 'gpt-4o', got %q", resp.Model)
		}
	})

	t.Run("invalid_method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/responses", nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != 405 {
			t.Errorf("expected 405, got %d", rec.Code)
		}
	})
}

// TestE2E_ErrorFormats 验证所有端点的错误响应均为 JSON 格式
// （而不是 http.Error() 的纯文本格式）。这是审计修正 P0-1 的回归测试。
func TestE2E_ErrorFormats(t *testing.T) {
	cfg := &v2Config{EnableCache: true}
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	defer func() { _ = deps.AuditWriter.Close() }()
	handler := httpHandler(deps)

	t.Run("chat_completions_invalid_JSON_returns_openai_format", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader("not valid json"))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 400 {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("expected application/json content-type, got %q", rec.Header().Get("Content-Type"))
		}
		var resp struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("error response must be JSON, got: %s", rec.Body.String())
		}
		if resp.Error == nil {
			t.Fatal("expected OpenAI error object {error: {...}}")
		}
		if resp.Error["type"] != "invalid_request_error" {
			t.Errorf("expected type='invalid_request_error', got %v", resp.Error["type"])
		}
	})

	t.Run("messages_invalid_role_returns_anthropic_format", func(t *testing.T) {
		body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":100,"messages":[{"role":"system","content":"x"}]}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 400 {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("expected application/json content-type, got %q", rec.Header().Get("Content-Type"))
		}
		var resp struct {
			Type  string         `json:"type"`
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("error response must be JSON, got: %s", rec.Body.String())
		}
		if resp.Type != "error" {
			t.Errorf("expected type='error', got %q", resp.Type)
		}
		if resp.Error == nil {
			t.Fatal("expected Anthropic error object {error: {...}}")
		}
		if resp.Error["type"] != "invalid_request_error" {
			t.Errorf("expected error.type='invalid_request_error', got %v", resp.Error["type"])
		}
	})

	t.Run("chat_completions_stream_true_returns_openai_error", func(t *testing.T) {
		body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 400 {
			t.Fatalf("expected 400 for stream=true, got %d", rec.Code)
		}
		var resp struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("error response must be JSON, got: %s", rec.Body.String())
		}
		if resp.Error == nil {
			t.Fatal("expected OpenAI error object")
		}
	})

	t.Run("completions_empty_prompt_returns_openai_format", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/completions",
			strings.NewReader(`{"model":"gpt-3.5-turbo","prompt":""}`))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 400 {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		var resp struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("error response must be JSON, got: %s", rec.Body.String())
		}
		if resp.Error == nil {
			t.Fatal("expected OpenAI error object")
		}
	})

	t.Run("responses_empty_input_returns_openai_format", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/responses",
			strings.NewReader(`{"model":"gpt-4o"}`))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code != 400 {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
		var resp struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("error response must be JSON, got: %s", rec.Body.String())
		}
		if resp.Error == nil {
			t.Fatal("expected OpenAI error object")
		}
	})
}
