// Guard for audit round 243: the OUTPUT side shares the input side's blind
// spot, and 242's request-side finding is therefore a two-sided gap rather than
// a one-sided one.
//
// The structural fact, read on 2026-10-04 from security/sanitize/output_sensitive.go:
//
//	outputSensitiveChecker.Check (:115) calls c.sanitizer.detector.Detect(ctx, text)
//	                                                 ^^^^^^^^^^^^^^^^^^^^^^^^^^^
//	the same detector the request-side sanitizer uses (242 measured that side:
//	a base64-encoded phone/email is forwarded verbatim, 0 placeholders, and no
//	SanitizeInfo at all).
//
// ⇒ the output gate is encoding-blind BY CONSTRUCTION, not by coincidence: it
// is literally the same pattern matcher. Round 242 registered the input side
// (待裁决 105) and explicitly left open the question "does the output side
// catch it anyway". This file is the answer, measured rather than reasoned.
//
// Arms, same chain, one variable each:
//
//	A (control)  model returns a PLAINTEXT phone   -> [REDACTED]        (gate works)
//	B            model returns that phone base64    -> forwarded as-is   (same gap)
//	C (round trip) the exact blob 242 let through the REQUEST side comes
//	              back in the response             -> forwarded as-is
//
// Arm A is a hard precondition. Without it, "arm B was not redacted" is
// indistinguishable from "this chain never redacts anything".
//
// The chain is the production ordering's relevant part: guard before restore,
// as cmd/gateway/goal_control.go:571-586 wires it (236 号 audited that ordering;
// this file is about what the guard can detect, not where it sits).
package sanitize

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

// runOutputGate drives one non-stream response body through a guard+restore
// chain and returns the body the client would receive.
func runOutputGate(t *testing.T, content string) (string, bool) {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{})
	body := `{"choices":[{"message":{"content":` + mustJSONString(t, content) + `}}]}`
	result, err := chain.InterceptNonStream(ctx, &response.InterceptRequest{
		SessionID:    "gw_enc_out",
		TenantID:     "tenant-enc-out",
		ResponseBody: []byte(body),
	})
	if err != nil {
		t.Fatalf("InterceptNonStream: %v", err)
	}
	// ⚠️ Read the contract before trusting an empty field. The chain returns a
	// NON-NIL result with an EMPTY ModifiedBody when nothing was detected
	// (measured: Action and Metadata are empty too), and the caller is expected
	// to keep the original body. Returning that empty slice as "what the client
	// sees" would make a bypass and an emptied response indistinguishable —
	// so this helper resolves the contract and returns the original body.
	if result == nil {
		return body, false
	}
	if len(result.ModifiedBody) == 0 {
		return body, result.ShouldBlock
	}
	return string(result.ModifiedBody), result.ShouldBlock
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestOutputGateSharesTheInputSidesEncodingBlindSpot(t *testing.T) {
	const phone = "13800138000"
	enc := base64.StdEncoding.EncodeToString([]byte(phone))

	// --- arm A: the control. If the gate does not redact a plaintext phone,
	// every conclusion below is about a gate that was never working.
	ctrlBody, _ := runOutputGate(t, "call "+phone)
	if !strings.Contains(ctrlBody, "[REDACTED]") || strings.Contains(ctrlBody, phone) {
		t.Fatalf("判据失效（对照组没被遮蔽）：body=%s —— "+
			"下面关于「编码形态没被遮蔽」的结论全部不成立，请先修夹具。", ctrlBody)
	}

	arms := []struct{ name, content, secret string }{
		{"B_encoded_phone", "call " + enc, enc},
		{"C_round_trip_of_the_blob_242_let_through", enc, enc},
	}
	var unredacted []string
	for _, a := range arms {
		body, blocked := runOutputGate(t, a.content)
		t.Logf("arm %s: blocked=%v client_body=%s", a.name, blocked, body)
		// POSITIVE assertion, not "lacks [REDACTED]": the claim is that the
		// encoded secret REACHES THE CLIENT, and that is only demonstrable by
		// finding it in the body the client receives.
		if strings.Contains(body, a.secret) {
			unredacted = append(unredacted, a.name)
		} else {
			t.Logf("arm %s 已被处置（body 中不再有该串）：%s", a.name, body)
		}
	}

	if len(unredacted) == 0 {
		t.Errorf("✅ 盲区已消失：出向闸不再让编码形态抵达客户端（arm %v）。\n\n"+
			"请接着处置 待裁决 105 的后半段——注意**入向那一侧也要一起确认**，"+
			"因为两侧共用同一个 detector，只修一侧等于把同一个洞留在另一面。\n"+
			"并把本测试改成正向断言。**不要直接删掉本文件。**", unredacted)
		return
	}
	t.Logf("⚠️ 盲区仍在（对照组 arm A 正常遮蔽，证明不是夹具坏了）：%v 的编码串原样抵达客户端。\n"+
		"  arm C 是 242 那个入向缺口的**回环**：入向放行的 base64 blob 被模型原样带回时，"+
		"出向闸同样不拦 ⇒ 同一个洞在链路上是**两次**。\n"+
		"  根因是两侧共用 `c.sanitizer.detector`（output_sensitive.go:119）——"+
		"这不是巧合，是同源。", unredacted)
}

// runOutputGateStream drives SSE frames through the same chain and returns the
// bytes the client would receive.
func runOutputGateStream(t *testing.T, frames []string) string {
	t.Helper()
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{})
	meta := &response.StreamMeta{SessionID: "gw_enc_stream", TenantID: "tenant-enc-stream", State: response.NewStreamState()}

	var wire strings.Builder
	for i, frame := range frames {
		result, err := chain.InterceptStreamChunk(ctx, []byte(frame), meta)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if result == nil {
			wire.WriteString(frame)
			continue
		}
		if len(result.ModifiedChunk) > 0 {
			wire.Write(result.ModifiedChunk)
		} else if !result.SuppressChunk {
			wire.WriteString(frame)
		}
	}
	return wire.String()
}

// TestOutputGateStreamSharesTheSameBlindSpot covers the streaming lane, which
// the objective names explicitly alongside the non-streaming one. It reuses the
// same control-then-measure shape: a plaintext phone split across frames must
// still be masked, otherwise "the encoded form survived" proves nothing.
func TestOutputGateStreamSharesTheSameBlindSpot(t *testing.T) {
	const phone = "13800138000"
	enc := base64.StdEncoding.EncodeToString([]byte(phone))

	// --- NEUTRAL arm first. The control below proves the gate masks; it does
	// NOT prove that frames pass through untouched when nothing is detected.
	// Measured on 2026-10-04: they do not, under this chain+config — so a
	// "the encoded blob did not appear in the wire" conclusion would have been
	// an artifact of the harness dropping frames, not evidence of a fix.
	// Without this arm the stream measurement is uninterpretable.
	neutral := runOutputGateStream(t, []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello \"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"}}]}\n\n",
		"data: [DONE]\n\n",
	})
	t.Logf("流式中性臂：wire=%q", neutral)

	ctrl := runOutputGateStream(t, []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"call 13800\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"138000\"}}]}\n\n",
		"data: [DONE]\n\n",
	})
	if !strings.Contains(ctrl, "[REDACTED]") || strings.Contains(ctrl, phone) {
		t.Fatalf("判据失效（流式对照组没被遮蔽）：wire=%q —— "+
			"下面关于「编码形态」的读数不成立，请先修夹具。", ctrl)
	}
	t.Logf("流式对照组：明文跨帧被遮蔽 ✅  wire=%s", ctrl)

	if !strings.Contains(neutral, "hello") || !strings.Contains(neutral, "world") {
		t.Logf("⚠️ 本夹具在本配置下**不透传**未检出帧（中性臂 wire=%q）⇒ "+
			"流式 lane 的编码盲区**本轮不可测**，如实登记为诚实边界；"+
			"非流式 lane 的读数（B/C 两臂）不受影响。", neutral)
		return
	}

	encFrames := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"call " + enc[:8] + "\"}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + enc[8:] + "\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	wire := runOutputGateStream(t, encFrames)
	t.Logf("流式编码臂：wire=%q", wire)

	// ⚠️ A first version of this assertion was `strings.Contains(wire, enc)`
	// and it reported the blind spot as GONE. That was wrong, and the way it
	// was wrong matters: the blob is delivered across TWO SSE frames, so the
	// contiguous string `enc` never appears in the wire no matter whether it
	// was redacted. The assertion was observing a property that cannot be
	// observed in that form — a byte-level blob is the right unit for a
	// non-stream body and the wrong unit for a stream.
	//
	// What IS observable, and is the real claim: both halves reach the client
	// unredacted and in order, so concatenating them reconstructs the secret.
	firstHalf := strings.Contains(wire, enc[:8])
	secondHalf := strings.Contains(wire, enc[8:])
	t.Logf("流式编码臂：前半在=%v 后半在=%v（两者皆真 ⇒ 按帧序拼接即可还原出密文）", firstHalf, secondHalf)
	if firstHalf && secondHalf {
		t.Log("⚠️ 盲区在流式 lane 同样存在：编码串跨帧原样抵达客户端（与 105 同源，不另立条目）")
		return
	}
	t.Errorf("✅ 流式 lane 的编码盲区已消失（wire=%q）。请与 105 一并处置，并把本测试改成正向断言。", wire)
}
