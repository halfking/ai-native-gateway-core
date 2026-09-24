package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/auth"
	"github.com/kaixuan/llm-gateway-go/internal/mockprobe"
	"github.com/kaixuan/llm-gateway-go/internal/providers/mock"
)

// buildTestGateway 构建完整装配（pipeline + httpHandler + authChain），
// 返回 handler 与 deps（供收尾）。apiKey 非空时启用 X-API-Key 鉴权门。
func buildTestGateway(t *testing.T, cfg *v2Config) (http.Handler, *v2Deps) {
	t.Helper()
	deps := newDeps(cfg)
	deps.Pipeline = buildPipeline(deps)
	t.Cleanup(func() { _ = deps.AuditWriter.Close() })
	return authChain(deps, cfg), deps
}

func post(t *testing.T, h http.Handler, path, bearer, apiKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestE2E_MockProbeDisabled_MockEndpointsAbsent：子系统关闭时 4 个 mock
// 端点全部 404（未注册），带不带鉴权门均如此（验收第 1 条前半）。
func TestE2E_MockProbeDisabled_MockEndpointsAbsent(t *testing.T) {
	cfg := &v2Config{MockProbeEnabled: false}
	h, _ := buildTestGateway(t, cfg)
	paths := []string{
		"/mock/v1/chat/completions/fast",
		"/mock/v1/chat/completions/slow",
		"/mock/v1/messages/fast",
		"/mock/v1/messages/slow",
	}
	for _, p := range paths {
		rec := post(t, h, p, auth.MockProbeClientToken, "", `{"messages":[{"role":"user","content":"x"}]}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s should 404 when disabled, got %d", p, rec.Code)
		}
	}

	// API-Key 门启用时：无 X-API-Key 的请求先被 401（旁路随子系统关闭），
	// 持有效 key 的请求穿门后仍 404 —— 端点确实不存在。
	cfg2 := &v2Config{MockProbeEnabled: false, APIKey: "secret"}
	h2, _ := buildTestGateway(t, cfg2)
	if rec := post(t, h2, "/mock/v1/chat/completions/fast", auth.MockProbeClientToken, "", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled bypass should leave API gate active, got %d", rec.Code)
	}
	if rec := post(t, h2, "/mock/v1/chat/completions/fast", "", "secret", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("authed request should reach mux 404, got %d", rec.Code)
	}
}

// TestE2E_MockProbeEnabled_GuardedEndpoints：4 端点在守卫后存活；
// 正确 token → 200、错误 token → 401、GET → 405（验收第 1 条后半 +
// 设计 §三 Step 3/4）。
func TestE2E_MockProbeEnabled_GuardedEndpoints(t *testing.T) {
	cfg := &v2Config{MockProbeEnabled: true}
	h, _ := buildTestGateway(t, cfg)

	for _, p := range []string{
		"/mock/v1/chat/completions/fast",
		"/mock/v1/chat/completions/slow",
	} {
		rec := post(t, h, p, auth.MockProbeClientToken, "", `{"messages":[{"role":"user","content":"x"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s should 200, got %d body=%s", p, rec.Code, rec.Body.String())
		}
		var body struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil ||
			len(body.Choices) == 0 || body.Choices[0].Message.Content == "" {
			t.Fatalf("%s body invalid: %s", p, rec.Body.String())
		}
	}
	for _, p := range []string{"/mock/v1/messages/fast", "/mock/v1/messages/slow"} {
		rec := post(t, h, p, auth.MockProbeClientToken, "", `{"messages":[{"role":"user","content":"x"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s should 200, got %d", p, rec.Code)
		}
	}

	// 守卫：错误 token 401；无 token 401；GET 405。
	if rec := post(t, h, "/mock/v1/chat/completions/fast", "sk-wrong", "", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer should 401, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/mock/v1/chat/completions/fast", nil)
	req.Header.Set("Authorization", "Bearer "+auth.MockProbeClientToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET should 405, got %d", rec.Code)
	}
}

// TestE2E_MockProbeBypassThroughAuthChain：API-Key 门启用时，mock
// 探测请求（Bearer mock-probe-client、无 X-API-Key）经旁路穿全链路到
// mux；非 mock 路径与无凭证请求仍被门拦（设计 §二"鉴权旁路"）。
func TestE2E_MockProbeBypassThroughAuthChain(t *testing.T) {
	cfg := &v2Config{MockProbeEnabled: true, APIKey: "secret"}
	h, _ := buildTestGateway(t, cfg)

	// 旁路命中：仅 Bearer，无 X-API-Key。
	rec := post(t, h, "/mock/v1/chat/completions/fast", auth.MockProbeClientToken, "", `{"messages":[{"role":"user","content":"x"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mock bearer should bypass API gate, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 非 mock 路径不旁路：同样的 Bearer 打 /healthz 仍被 401。
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer "+auth.MockProbeClientToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-mock path should hit API gate, got %d", rec.Code)
	}

	// 持有效 X-API-Key 但无 mock Bearer 的请求：穿门后仍被端点守卫 401。
	rec = post(t, h, "/mock/v1/chat/completions/fast", "", "secret", `{"messages":[{"role":"user","content":"x"}]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("endpoint guard should require mock bearer, got %d", rec.Code)
	}
}

// TestE2E_MockProbeRunnerThroughGateway：完整链路（CORS→recovery→
// requestid→旁路→mux→守卫→mock handler）上的 runner 编排：2x2 探测
// 周期执行，/metrics 暴露 scope="mock_probe" 序列（验收第 2、3 条的
// 进程内版本；真库版见 mockprobe realdb 测试）。
func TestE2E_MockProbeRunnerThroughGateway(t *testing.T) {
	cfg := &v2Config{MockProbeEnabled: true, MockProbeInterval: 100 * time.Millisecond, MockProbeFailureThreshold: 3}
	h, _ := buildTestGateway(t, cfg)
	srv := httptest.NewServer(h)
	defer srv.Close()

	runner := mockprobe.NewRunner(mockprobe.NewClient(srv.URL), 100*time.Millisecond, 3, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !runner.Start(ctx) {
		t.Fatal("runner failed to start")
	}
	defer runner.Stop(context.Background())

	deadline := time.Now().Add(8 * time.Second)
	// 文本暴露层的标签按字母序：scope,status,stream,supplier。一轮 2x2
	// 完整跑过后 4 个通道序列才齐备（slow 通道需 ~1.6s）。
	needles := make([]string, 0, 4)
	for _, sup := range []string{mock.CodeFast, mock.CodeSlow} {
		for _, st := range []string{"false", "true"} {
			needles = append(needles,
				`mock_probe_request_total{scope="mock_probe",status="ok",stream="`+st+`",supplier="`+sup+`"}`)
		}
	}
	sawScope := false
	for time.Now().Before(deadline) && !sawScope {
		resp, err := http.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		_ = resp.Body.Close()
		text := sb.String()
		sawScope = strings.Contains(text, "mock_probe_latency_seconds_bucket")
		for _, n := range needles {
			if !strings.Contains(text, n) {
				sawScope = false
				break
			}
		}
		if !sawScope {
			time.Sleep(150 * time.Millisecond)
		}
	}
	if !sawScope {
		t.Fatal("/metrics never exposed all 4 scope=\"mock_probe\" ok-series within budget")
	}
	if runner.FailureCount() != 0 {
		t.Fatalf("healthy gateway should have zero probe failures, got %d", runner.FailureCount())
	}
}
