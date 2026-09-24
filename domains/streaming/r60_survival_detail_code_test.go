package streaming

// r60_survival_detail_code_test.go — R60 S7-1 回归测试：survival 终态
// 结构化 detail code 在三协议面的收口。
//
// 背景（R59 S7-1 修复，无测试背书，本文件补上）：survival 协调器到达终态
// 后 runSurvivalCoordinator 返回 *survivalTerminalError，chat 面
// （handler.go）、messages 面（messages.go）、responses 面（responses.go）
// 的错误分支都必须把 failure_detail_code 记为 ste.detailCode()
// （gateway_survival_<action>）而不是泛化的 provider_error，并原样返回
// （协调器已在串行化写手上渲染过协议终态帧，错误面不得再叠第二帧）。
//
// R59 只接了 handler.go（chat）一处？不——R59 同时补了 messages.go /
// responses.go 两处分支，但三处都没有错误面回归测试。本测试用与
// survival_no_nodes_e2e_test.go 相同的 harness（零候选 → 重试预算耗尽 →
// fail_closed 终态）驱动 /v1/messages 与 /v1/responses 流式请求，断言：
//
//  1. wire 上恰好一个协议终态帧，code 为 gateway_survival_fail_closed
//     （= survivalTerminalError.detailCode()，不是 provider_error）；
//  2. X-Gateway-Last-Kind 头被置为最后一次尝试的 kind 串
//     （只有 ste 分支会置，泛化 fallthrough 不会）——错误面确实走进了
//     ste 分支的直接证据；
//  3. 泛化第二帧（"Upstream request failed" / overloaded_error /
//     provider_unavailable）不得出现在 wire 上。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// driveSurvivalTerminalThroughFace 用零候选 harness 驱动一个协议面的流式
// 请求直至 survival 终态，返回 (recorder, wire)。
func driveSurvivalTerminalThroughFace(t *testing.T, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h, _ := newNoNodesHandler(t, 60*time.Millisecond, 3)

	var handler http.Handler
	switch path {
	case "/v1/messages":
		handler = NewMessagesHandler(h)
	case "/v1/responses":
		handler = NewResponsesHandler(h)
	default:
		t.Fatalf("unsupported face path %q", path)
	}

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("handler did not return within 30s (path=%s)", path)
	}
	return rec, rec.Body.String()
}

func TestR60SurvivalTerminalDetailCodeOnMessagesFace(t *testing.T) {
	// Anthropic /v1/messages 流式请求体。
	body := `{"model":"glm-5.2","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	rec, wire := driveSurvivalTerminalThroughFace(t, "/v1/messages", body)

	// 断言 1：恰好一个 anthropic 协议终态帧，code = detailCode()。
	if n := strings.Count(wire, "event: error"); n != 1 {
		t.Fatalf("event: error frame count = %d, want exactly 1\nwire=%q", n, wire)
	}
	const wantCode = `"code":"gateway_survival_fail_closed"`
	if !strings.Contains(wire, wantCode) {
		t.Fatalf("survival detail code %s missing from /v1/messages wire\nwire=%q", wantCode, wire)
	}
	// 断言 2：泛化 provider_error fallthrough 的第二帧不得出现。
	for _, banned := range []string{"Upstream request failed", `"overloaded_error"`} {
		if strings.Contains(wire, banned) {
			t.Fatalf("generic second error frame %q leaked onto /v1/messages wire\nwire tail=%q", banned, tailBytes(wire, 500))
		}
	}
	// 断言 3：X-Gateway-Last-Kind 只有 ste 分支会设置（kinds 非空时）。
	if got := rec.Header().Get("X-Gateway-Last-Kind"); !strings.Contains(got, "no_available_channel") {
		t.Fatalf("X-Gateway-Last-Kind = %q, want to contain no_available_channel (ste branch must have fired)", got)
	}
}

func TestR60SurvivalTerminalDetailCodeOnResponsesFace(t *testing.T) {
	// OpenAI /v1/responses 流式请求体。
	body := `{"model":"glm-5.2","stream":true,"input":"hi"}`
	rec, wire := driveSurvivalTerminalThroughFace(t, "/v1/responses", body)

	// 断言 1：恰好一个 responses 协议终态帧，code = detailCode()。
	if n := strings.Count(wire, `"type":"response.failed"`); n != 1 {
		t.Fatalf("response.failed frame count = %d, want exactly 1\nwire=%q", n, wire)
	}
	const wantCode = `"code":"gateway_survival_fail_closed"`
	if !strings.Contains(wire, wantCode) {
		t.Fatalf("survival detail code %s missing from /v1/responses wire\nwire=%q", wantCode, wire)
	}
	// 断言 2：泛化 fallthrough 帧（upstream_error / provider_unavailable）不得出现。
	for _, banned := range []string{"Upstream request failed", `"upstream_error"`, `"provider_unavailable"`} {
		if strings.Contains(wire, banned) {
			t.Fatalf("generic second error frame %q leaked onto /v1/responses wire\nwire tail=%q", banned, tailBytes(wire, 500))
		}
	}
	// 断言 3：X-Gateway-Last-Kind 只有 ste 分支会设置（kinds 非空时）。
	if got := rec.Header().Get("X-Gateway-Last-Kind"); !strings.Contains(got, "no_available_channel") {
		t.Fatalf("X-Gateway-Last-Kind = %q, want to contain no_available_channel (ste branch must have fired)", got)
	}
}
