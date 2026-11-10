package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// r1038_survival_shape_parity_test.go — 2026-11-10 审计轮（用户报第
// 0/2/3/4/8/9/10 条：gateway_survival_resume_blocked /
// "gateway request survival ended: committed_output" /
// "Partial assistant output was discarded before a streaming retry"）。
//
// 根因（形状缺口，不是语义缺口）：
//
//	客户端读法已被 stream.go:1462 的活证据钉死——它读 error.code（所以
//	provider_code 正确），但 reason / retryable **只从帧根读**。嵌套形态下
//	客户端报 reason=unknown / retryable=false，整轮硬失败。
//
//	stream.go 的四条错误信封（eof_without_done / stream_timeout /
//	upstream_error / openai_empty_response）2026-09-23 都补了根级副本，
//	只有 renderSurvivalTerminal 漏了 ⇒ survival 终态是唯一的漏网之鱼。
//
// 本门守住：survival 终态（三协议）的帧根必须有 code / reason / retryable，
// 且 resume_blocked 的 retryable 必须是 true（该语义修正于 2026-09-23，
// 但因形状缺口从未真正到达客户端）。

// survivalRootFields 是帧根必须携带的机器可读字段。
type survivalRootFields struct {
	Code      string `json:"code"`
	Reason    string `json:"reason"`
	Retryable *bool  `json:"retryable"`
}

// assertSurvivalRootShapeParity 是被测断言本体。它**委托**给纯函数
// survivalRootShapeParityErr（负控复用同一实现 ⇒ 门有没有牙可判定）。
func assertSurvivalRootShapeParity(t *testing.T, raw, wantCode, wantReason string, wantRetryable bool) {
	t.Helper()
	if err := survivalRootShapeParityErr(raw, wantCode, wantReason, wantRetryable); err != nil {
		t.Fatalf("survival terminal shape parity: %v (raw=%q)", err, raw)
	}
}

// TestRenderSurvivalTerminal_ShapeParityThreeProtocols 守三协议的帧根形状对齐。
func TestRenderSurvivalTerminal_ShapeParityThreeProtocols(t *testing.T) {
	decision := TaskDecision{
		Action: TaskActionResumeBlocked,
		Reason: "committed_output",
		Detail: "attempts=3 kinds=upstream_down:3 earliest_recovery_sec=42",
	}

	t.Run("openai chat", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIChat, decision, true)
		assertSurvivalRootShapeParity(t, f.buf.String(),
			"gateway_survival_resume_blocked", "committed_output", true)
	})

	t.Run("anthropic", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolAnthropic, decision, true)
		assertSurvivalRootShapeParity(t, f.buf.String(),
			"gateway_survival_resume_blocked", "committed_output", true)
	})

	t.Run("openai responses", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIResponses, decision, true)
		assertSurvivalRootShapeParity(t, f.buf.String(),
			"gateway_survival_resume_blocked", "committed_output", true)
	})
}

// TestRenderSurvivalTerminal_FailClosedAlsoParity 守另一个终态：形状对齐是
// 终态信封的**通用契约**，不是 resume_blocked 的特例。
func TestRenderSurvivalTerminal_FailClosedAlsoParity(t *testing.T) {
	f := &trackingFlusher{}
	renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIChat, TaskDecision{
		Action: TaskActionFailClosed,
		Reason: "deadline_exceeded",
	}, false)
	assertSurvivalRootShapeParity(t, f.buf.String(),
		"gateway_survival_fail_closed", "deadline_exceeded", false)
}

// TestSurvivalShapeParityGate_HasTeeth 是**负控**：把一条"只有嵌套形态"的
// 帧喂给同一个断言实现，必须判红。没有这条，门有可能因为断言写错而恒绿
// （自证的门不算门）。
func TestSurvivalShapeParityGate_HasTeeth(t *testing.T) {
	nestedOnly := `data: {"error":{"message":"gateway request survival ended: committed_output","type":"server_error","code":"gateway_survival_resume_blocked","reason":"committed_output","retryable":true}}` + "\n\n"

	err := survivalRootShapeParityErr(nestedOnly,
		"gateway_survival_resume_blocked", "committed_output", true)
	if err == nil {
		t.Fatalf("negative control did not fail: the shape-parity gate accepts a nested-only frame, so it has no teeth")
	}
	// 正控：补上根级副本后同一实现必须判绿（证明负控不是因为实现恒红）。
	parity := `data: {"error":{"message":"m","type":"server_error","code":"gateway_survival_resume_blocked","reason":"committed_output","retryable":true},"code":"gateway_survival_resume_blocked","reason":"committed_output","retryable":true}` + "\n\n"
	if err := survivalRootShapeParityErr(parity,
		"gateway_survival_resume_blocked", "committed_output", true); err != nil {
		t.Fatalf("positive control failed: %v — the gate implementation rejects a conforming frame", err)
	}
}

// survivalRootShapeParityErr 是断言的可判定版本（断言本体委托给它）。
func survivalRootShapeParityErr(raw, wantCode, wantReason string, wantRetryable bool) error {
	body := survivalFrameBody(raw)
	var frame map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &frame); err != nil {
		return err
	}
	for _, key := range []string{"code", "reason", "retryable"} {
		if _, ok := frame[key]; !ok {
			return &shapeParityError{"frame root carries no " + key}
		}
	}
	var root survivalRootFields
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return err
	}
	if root.Code != wantCode {
		return &shapeParityError{"root code mismatch"}
	}
	if root.Reason != wantReason {
		return &shapeParityError{"root reason mismatch"}
	}
	if root.Retryable == nil {
		return errMissingRootRetryable
	}
	if *root.Retryable != wantRetryable {
		return &shapeParityError{"root retryable mismatch"}
	}
	return nil
}

var (
	errMissingRootCode      = &shapeParityError{"frame root carries no code"}
	errMissingRootRetryable = &shapeParityError{"frame root carries no retryable"}
	errRootMismatch         = &shapeParityError{"root code/reason/retryable mismatch"}
)

type shapeParityError struct{ msg string }

func (e *shapeParityError) Error() string { return e.msg }

// survivalFrameBody 剥掉 SSE 前缀与 [DONE] 终止帧，返回帧 JSON 文本。
func survivalFrameBody(raw string) string {
	body := raw
	switch {
	case strings.HasPrefix(body, "event: error\ndata: "):
		body = strings.TrimPrefix(body, "event: error\ndata: ")
	case strings.HasPrefix(body, "event: response.failed\ndata: "):
		body = strings.TrimPrefix(body, "event: response.failed\ndata: ")
	case strings.HasPrefix(body, "data: "):
		body = strings.TrimPrefix(body, "data: ")
	}
	if idx := strings.Index(body, "\n\ndata: [DONE]"); idx >= 0 {
		body = body[:idx]
	}
	return strings.TrimSpace(body)
}
