package admin

// P2-C1 (doc 23 §5): admin-side adaptation to the CO-5 summary-mode digest
// envelope stored in request_logs_bodies_with_current_month. Table-driven
// tests cover the four input classes — envelope, non-envelope, empty and
// broken JSON — plus the acceptance red line: non-envelope (flag-off /
// request_bodies_full=true) behaviour must be completely unchanged.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// mustEnvelopeJSON builds a digest-envelope document shaped exactly like the
// telemetry write path output ({"_gw_body_summary":{...}}).
func mustEnvelopeJSON(t *testing.T, env bodyEnvelope) string {
	t.Helper()
	b, err := json.Marshal(bodyEnvelopeOuter{Summary: &env})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return string(b)
}

const envelopeUserBody = `{"model":"glm-5","messages":[{"role":"user","content":"帮我排查线上网关 502"}]}`

const envelopeRespBody = `{"choices":[{"message":{"role":"assistant","content":"答案：先看上游超时"}}]}`

func TestDetectBodyEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantEnvelope bool
		wantBytes    int
		wantMode     string
	}{
		{
			name:         "digest envelope detected",
			raw:          mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: 42, SHA256: "abc", Head: `{"a":1}`, HeadTruncated: true}),
			wantEnvelope: true,
			wantBytes:    42,
			wantMode:     "digest",
		},
		{
			name:         "plain request body is not an envelope",
			raw:          envelopeUserBody,
			wantEnvelope: false,
		},
		{
			name:         "empty string sentinel passes through",
			raw:          "",
			wantEnvelope: false,
		},
		{
			name:         "null sentinel passes through",
			raw:          "null",
			wantEnvelope: false,
		},
		{
			name:         "empty object sentinel passes through",
			raw:          "{}",
			wantEnvelope: false,
		},
		{
			name:         "broken JSON passes through",
			raw:          `{"messages":[{"role":"user","content":"trunc`,
			wantEnvelope: false,
		},
		{
			name:         "null envelope key passes through",
			raw:          `{"_gw_body_summary":null}`,
			wantEnvelope: false,
		},
		{
			name:         "non-object JSON passes through",
			raw:          `[]`,
			wantEnvelope: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, ok := detectBodyEnvelope(tc.raw)
			if ok != tc.wantEnvelope {
				t.Fatalf("detectBodyEnvelope(%q) ok = %v, want %v", tc.raw, ok, tc.wantEnvelope)
			}
			if ok {
				if env.Bytes != tc.wantBytes {
					t.Fatalf("bytes = %d, want %d", env.Bytes, tc.wantBytes)
				}
				if env.Mode != tc.wantMode {
					t.Fatalf("mode = %q, want %q", env.Mode, tc.wantMode)
				}
			}
		})
	}
}

func TestHeadForExtraction(t *testing.T) {
	plain := envelopeUserBody

	t.Run("non-envelope body returned unchanged", func(t *testing.T) {
		in := plain
		got := headForExtraction(&in)
		if got != &in {
			t.Fatal("non-envelope body must flow through headForExtraction untouched")
		}
	})

	t.Run("envelope with full head yields the head", func(t *testing.T) {
		env := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(plain), Head: plain, HeadTruncated: false})
		got := headForExtraction(strp(env))
		if got == nil || *got != plain {
			t.Fatalf("head = %v, want %q", got, plain)
		}
	})

	t.Run("envelope with truncated head yields nil (preview fallback)", func(t *testing.T) {
		truncated := `{"messages":[{"role":"user","content":"trunc`
		env := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: 4096, Head: truncated, HeadTruncated: true})
		if got := headForExtraction(strp(env)); got != nil {
			t.Fatalf("truncated head must be dropped for preview fallback, got %q", *got)
		}
	})

	t.Run("nil body stays nil", func(t *testing.T) {
		if got := headForExtraction(nil); got != nil {
			t.Fatal("nil body must stay nil")
		}
	})
}

func TestExtractTurnDisplay_EnvelopeHeadUsed(t *testing.T) {
	reqEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeUserBody), Head: envelopeUserBody})
	respEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeRespBody), Head: envelopeRespBody})

	d := extractTurnDisplay(strp(reqEnv), strp(respEnv), nil, nil)
	if !strings.Contains(d.UserTurn, "帮我排查线上网关 502") {
		t.Fatalf("user turn must come from the envelope head, got %q", d.UserTurn)
	}
	if !strings.Contains(d.AssistantText, "答案：先看上游超时") {
		t.Fatalf("assistant text must come from the envelope head, got %q", d.AssistantText)
	}
	if !strings.Contains(d.UserTurn, "[已摘要化: 原始 ") || !strings.Contains(d.AssistantText, "[已摘要化: 原始 ") {
		t.Fatalf("envelope-derived content must carry the summary marker, got %q / %q", d.UserTurn, d.AssistantText)
	}
	if strings.Contains(d.UserTurn, "_gw_body_summary") || strings.Contains(d.AssistantText, "_gw_body_summary") {
		t.Fatalf("envelope JSON must never leak as display content, got %q / %q", d.UserTurn, d.AssistantText)
	}
}

func TestExtractTurnDisplay_EnvelopeTruncatedFallsBackToPreview(t *testing.T) {
	truncatedHead := `{"messages":[{"role":"user","content":"被截断的开头`
	reqEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: 9000, Head: truncatedHead, HeadTruncated: true})

	d := extractTurnDisplay(strp(reqEnv), nil, strp("user: 预览里的用户问题"), strp("assistant: 预览回答"))
	if !strings.Contains(d.UserTurn, "预览里的用户问题") {
		t.Fatalf("truncated head must fall back to the stored preview, got %q", d.UserTurn)
	}
	if !strings.Contains(d.UserTurn, "head 已截断") {
		t.Fatalf("fallback content must still carry the truncation marker, got %q", d.UserTurn)
	}
	if strings.Contains(d.UserTurn, "_gw_body_summary") {
		t.Fatalf("envelope JSON must not leak, got %q", d.UserTurn)
	}
}

// TestExtractTurnDisplay_NonEnvelopeUnchanged is the acceptance red line:
// with flag-off / request_bodies_full=true bodies, extractTurnDisplay output
// must be byte-for-byte the pre-P2-C1 result (no marker, no rewiring).
func TestExtractTurnDisplay_NonEnvelopeUnchanged(t *testing.T) {
	tests := []struct {
		name            string
		requestBody     *string
		responseBody    *string
		requestPreview  *string
		responsePreview *string
		wantUserTurn    string
		wantAssistant   string
	}{
		{
			name:            "full bodies extract as before",
			requestBody:     strp(envelopeUserBody),
			responseBody:    strp(envelopeRespBody),
			requestPreview:  strp("user: preview"),
			responsePreview: strp("assistant: preview answer"),
			wantUserTurn:    "帮我排查线上网关 502",
			wantAssistant:   "答案：先看上游超时",
		},
		{
			name:            "nil bodies fall back to previews",
			requestPreview:  strp("user: preview only"),
			responsePreview: strp("assistant: preview answer"),
			wantUserTurn:    "preview only",
			wantAssistant:   "preview answer",
		},
		{
			name:          "nil everything stays empty",
			wantUserTurn:  "",
			wantAssistant: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := extractTurnDisplay(tc.requestBody, tc.responseBody, tc.requestPreview, tc.responsePreview)
			if d.UserTurn != tc.wantUserTurn {
				t.Fatalf("user turn = %q, want %q", d.UserTurn, tc.wantUserTurn)
			}
			if d.AssistantText != tc.wantAssistant {
				t.Fatalf("assistant text = %q, want %q", d.AssistantText, tc.wantAssistant)
			}
			if strings.Contains(d.UserTurn, "已摘要化") || strings.Contains(d.AssistantText, "已摘要化") {
				t.Fatalf("non-envelope path must not carry the envelope marker, got %q / %q", d.UserTurn, d.AssistantText)
			}
		})
	}
}

// TestBuildSummaryCorpus_Envelope covers the logs_summary.go / session_title.go
// / no_topic_session.go corpus path (all three share buildSummaryCorpus):
// envelope bodies must contribute their head content, never the envelope JSON.
func TestBuildSummaryCorpus_Envelope(t *testing.T) {
	now := time.Now().UTC()
	reqEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeUserBody), Head: envelopeUserBody})
	respEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeRespBody), Head: envelopeRespBody})

	logs := []sessionLogForSummary{
		{Ts: now, RequestBody: strp(reqEnv), ResponseBody: strp(respEnv), RequestStatus: "success"},
		{Ts: now.Add(time.Second), RequestPreview: strp("user: 第二轮预览"), RequestStatus: "success"},
	}
	corpus := buildSummaryCorpus(logs)
	if !strings.Contains(corpus, "帮我排查线上网关 502") {
		t.Fatalf("corpus must use the envelope head content, got %q", corpus)
	}
	if !strings.Contains(corpus, "答案：先看上游超时") {
		t.Fatalf("corpus must use the response envelope head content, got %q", corpus)
	}
	if !strings.Contains(corpus, "第二轮预览") {
		t.Fatalf("preview-only turns must be unaffected, got %q", corpus)
	}
	if strings.Contains(corpus, "_gw_body_summary") {
		t.Fatalf("envelope JSON must never become title/summary raw material, got %q", corpus)
	}
}

// TestBuildSessionMessageMap_Envelope covers the no_topic_session.go message
// list path (handleNoTopicSessionMessages → buildSessionMessageMap).
func TestBuildSessionMessageMap_Envelope(t *testing.T) {
	reqEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeUserBody), Head: envelopeUserBody})
	respEnv := mustEnvelopeJSON(t, bodyEnvelope{Mode: "digest", Bytes: len(envelopeRespBody), Head: envelopeRespBody})

	m := requestMessageRow{
		Ts:            time.Now().UTC(),
		RequestID:     "req-1",
		RequestBody:   strp(reqEnv),
		ResponseBody:  strp(respEnv),
		RequestStatus: strp("success"),
	}
	msg := buildSessionMessageMap(m, 1)
	userTurn, _ := msg["user_turn"].(string)
	assistant, _ := msg["assistant_text"].(string)
	if !strings.Contains(userTurn, "帮我排查线上网关 502") || !strings.Contains(userTurn, "[已摘要化: 原始 ") {
		t.Fatalf("user_turn must be head content plus the marker, got %q", userTurn)
	}
	if !strings.Contains(assistant, "答案：先看上游超时") || !strings.Contains(assistant, "[已摘要化: 原始 ") {
		t.Fatalf("assistant_text must be head content plus the marker, got %q", assistant)
	}

	// Red line: non-envelope rows render exactly as before (no marker).
	plain := requestMessageRow{
		Ts:            time.Now().UTC(),
		RequestID:     "req-2",
		RequestBody:   strp(envelopeUserBody),
		ResponseBody:  strp(envelopeRespBody),
		RequestStatus: strp("success"),
	}
	msg2 := buildSessionMessageMap(plain, 2)
	if got, _ := msg2["user_turn"].(string); got != "帮我排查线上网关 502" {
		t.Fatalf("non-envelope user_turn must be unchanged, got %q", got)
	}
	if got, _ := msg2["assistant_text"].(string); got != "答案：先看上游超时" {
		t.Fatalf("non-envelope assistant_text must be unchanged, got %q", got)
	}
}

// TestCompressionStatsEstimatedOrigSQL pins the P2-C1 compression-stats
// adaptation: digest-envelope rows contribute their original byte count
// (bytes), malformed envelopes and non-envelope rows keep the legacy
// LENGTH(...)/4 estimate, and summary-mode rows are counted separately.
func TestCompressionStatsEstimatedOrigSQL(t *testing.T) {
	sql := compressionStatsEstimatedOrigSQL
	for _, want := range []string{
		`rb.request_body ? '_gw_body_summary'`,
		`rb.request_body #>> '{_gw_body_summary,bytes}'`,
		`'^[0-9]+$'`,
		// legacy non-envelope estimate preserved, with the '' literal kept on
		// the text side of the cast (a bare '' beside jsonb resolves to jsonb
		// and errors at runtime on NULL-body rows).
		`LENGTH(COALESCE(COALESCE(rb.request_body, rl.request_body)::text, ''))::numeric`,
		// separate summary-mode row count
		`SUM(CASE WHEN rb.request_body ? '_gw_body_summary' THEN 1 ELSE 0 END)`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("compressionStatsEstimatedOrigSQL missing %q\nSQL:\n%s", want, sql)
		}
	}
}

func TestBodyEnvelopeDisplayNote(t *testing.T) {
	env := &bodyEnvelope{Mode: "digest", Bytes: 1234}
	if got := env.displayNote(); got != "[已摘要化: 原始 1234 bytes]" {
		t.Fatalf("note = %q", got)
	}
	env.HeadTruncated = true
	if got := env.displayNote(); got != "[已摘要化: 原始 1234 bytes, head 已截断]" {
		t.Fatalf("truncated note = %q", got)
	}
	if got := (*bodyEnvelope)(nil).displayNote(); got != "" {
		t.Fatalf("nil envelope note = %q, want empty", got)
	}
}
