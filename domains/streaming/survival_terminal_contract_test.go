package streaming

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// P0-A+（2026-10-14）：survival 终态契约——机器可读 retry_after_seconds /
// attempts；头未刷出时 Retry-After 经 applySurvivalRetryAfterHeader 写入。
// code/type/reason 字节契约不变。

func TestSurvivalRetryAfterSecondsFromDetail(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		want   int
	}{
		{"parses hint", "attempts=7 kinds=rate_limit:3 earliest_recovery_sec=45", 45},
		{"fallback when absent", "attempts=1", 30},
		{"fallback when empty", "", 30},
		{"fallback when zero", "earliest_recovery_sec=0", 30},
		{"fallback when garbage", "earliest_recovery_sec=abc", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := survivalRetryAfterSeconds(TaskDecision{Detail: tc.detail})
			if got != tc.want {
				t.Fatalf("survivalRetryAfterSeconds = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSurvivalAttemptsFromDetail(t *testing.T) {
	if got := survivalAttemptsFromDetail(TaskDecision{Detail: "attempts=7 kinds=x earliest_recovery_sec=3"}); got != 7 {
		t.Fatalf("attempts = %d, want 7", got)
	}
	if got := survivalAttemptsFromDetail(TaskDecision{Detail: ""}); got != 0 {
		t.Fatalf("absent attempts = %d, want 0", got)
	}
}

func TestRenderSurvivalTerminalRetryAfterSecondsThreeProtocols(t *testing.T) {
	detail := "attempts=7 kinds=rate_limit:3,upstream_down:4 earliest_recovery_sec=42"
	decision := TaskDecision{
		Action: TaskActionFailClosed,
		Reason: "deadline_exceeded",
		Detail: detail,
	}

	t.Run("anthropic", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolAnthropic, decision, false)
		var payload struct {
			Error struct {
				RetryAfterSeconds int    `json:"retry_after_seconds"`
				Attempts          int    `json:"attempts"`
				Code              string `json:"code"`
				Reason            string `json:"reason"`
			} `json:"error"`
		}
		raw := f.buf.String()
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "event: error\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		if payload.Error.RetryAfterSeconds != 42 || payload.Error.Attempts != 7 {
			t.Fatalf("retry_after_seconds=%d attempts=%d, want 42/7",
				payload.Error.RetryAfterSeconds, payload.Error.Attempts)
		}
		if payload.Error.Code != "gateway_survival_fail_closed" || payload.Error.Reason != "deadline_exceeded" {
			t.Fatalf("machine fields drifted: code=%q reason=%q", payload.Error.Code, payload.Error.Reason)
		}
	})

	t.Run("openai responses", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIResponses, decision, false)
		var payload struct {
			Response struct {
				Error struct {
					RetryAfterSeconds int `json:"retry_after_seconds"`
					Attempts          int `json:"attempts"`
				} `json:"error"`
			} `json:"response"`
		}
		raw := f.buf.String()
		if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "event: response.failed\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		if payload.Response.Error.RetryAfterSeconds != 42 || payload.Response.Error.Attempts != 7 {
			t.Fatalf("retry_after_seconds=%d attempts=%d, want 42/7",
				payload.Response.Error.RetryAfterSeconds, payload.Response.Error.Attempts)
		}
	})

	t.Run("openai chat", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolOpenAIChat, decision, false)
		raw := f.buf.String()
		dataFrame := strings.TrimSuffix(raw, "data: [DONE]\n\n")
		var payload struct {
			Error struct {
				RetryAfterSeconds int `json:"retry_after_seconds"`
				Attempts          int `json:"attempts"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(dataFrame, "data: ")), &payload); err != nil {
			t.Fatalf("decode: %v (raw=%q)", err, raw)
		}
		if payload.Error.RetryAfterSeconds != 42 || payload.Error.Attempts != 7 {
			t.Fatalf("retry_after_seconds=%d attempts=%d, want 42/7",
				payload.Error.RetryAfterSeconds, payload.Error.Attempts)
		}
	})

	t.Run("absent detail defaults to 30/0", func(t *testing.T) {
		f := &trackingFlusher{}
		renderSurvivalTerminal(NewSerializedStreamWriter(f), ProtocolAnthropic, TaskDecision{
			Action: TaskActionFailClosed,
			Reason: "deadline_exceeded",
		}, false)
		var payload struct {
			Error struct {
				RetryAfterSeconds int `json:"retry_after_seconds"`
				Attempts          int `json:"attempts"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(f.buf.String(), "event: error\ndata: ")), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Error.RetryAfterSeconds != 30 || payload.Error.Attempts != 0 {
			t.Fatalf("defaults = retry_after=%d attempts=%d, want 30/0",
				payload.Error.RetryAfterSeconds, payload.Error.Attempts)
		}
	})
}

func TestApplySurvivalRetryAfterHeader(t *testing.T) {
	// 同层驱动生产入口 applySurvivalRetryAfterHeader（不是测里手写 Set）。
	rec := httptest.NewRecorder()
	decision := TaskDecision{
		Action: TaskActionFailClosed,
		Reason: "deadline_exceeded",
		Detail: "attempts=1 earliest_recovery_sec=17",
	}
	applySurvivalRetryAfterHeader(rec, decision)
	if got := rec.Header().Get("Retry-After"); got != "17" {
		t.Fatalf("Retry-After = %q, want 17", got)
	}
	// nil writer 不 panic。
	applySurvivalRetryAfterHeader(nil, decision)
}
