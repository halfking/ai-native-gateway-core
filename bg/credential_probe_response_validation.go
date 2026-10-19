package bg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/internal/emptyoutcome"
)

const credentialProbeBodyLimit = 64 << 10

// Retain bounded classification evidence before shortening operator previews.
// Quota codes can follow long vendor metadata, beyond the old 1KiB read cap.
func credentialProbeHTTPFailure(protocol string, status int, body []byte) string {
	return fmt.Sprintf("%s status %d: %s", protocol, status, errorsx.SanitizeErrorText(body, credentialProbeBodyLimit))
}

func credentialProbeReadFailure(protocol string, status int, err error) string {
	if status >= 400 {
		// Receiving 401 is auth evidence even when its body cannot be read.
		// Ambiguous 403 keeps the existing fail-closed auth classification.
		return credentialProbeHTTPFailure(protocol, status, []byte("response body unavailable: "+err.Error()))
	}
	return protocol + " probe response invalid: " + err.Error()
}

// A low-budget reasoning response can establish neither success nor failure.
// Record only cadence; keep auth/quota evidence, bindings and health untouched.
const credentialProbeObservationSQL = `UPDATE credentials
SET last_probe_at = NOW(), last_probe_success = FALSE
WHERE id = $1 AND COALESCE(manual_disabled, FALSE) = FALSE
  AND (lifecycle_status = 'active' OR
       (lifecycle_status = 'disabled' AND auto_disabled_at IS NOT NULL))`

func writeCredentialProbeObservation(ctx context.Context, db bindingProbeExecer, credID int) (pgconn.CommandTag, error) {
	return db.Exec(ctx, credentialProbeObservationSQL, credID)
}

func credentialProbeInvalidResponse(message string) bool {
	if strings.HasPrefix(message, "chat unreachable (after ") {
		if i := strings.Index(message, "): "); i >= 0 {
			message = message[i+3:]
		}
	}
	for _, protocol := range []string{"chat", "messages", "responses"} {
		if strings.HasPrefix(message, protocol+" probe response invalid: ") {
			return true
		}
	}
	return false
}

// Read the complete bounded response: a truncated JSON prefix cannot prove
// health, and a body read error must not turn a received 200 into success.
func readCredentialProbeBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, credentialProbeBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > credentialProbeBodyLimit {
		return nil, fmt.Errorf("response exceeds %d bytes", credentialProbeBodyLimit)
	}
	return body, nil
}

type credentialProbeContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
	Name    string `json:"name"`
}

func credentialProbeText(parts []credentialProbeContent) bool {
	for _, p := range parts {
		switch p.Type {
		case "", "text", "output_text", "refusal":
			if p.Text != "" || p.Refusal != "" {
				return true
			}
		}
	}
	return false
}

// emptyoutcome's unknown/malformed fallback protects business payloads from
// being discarded. A probe needs positive evidence, so additionally require
// the expected envelope and recognized assistant text or a named tool call.
func credentialProbeHasOutput(protocol string, body []byte) bool {
	format, empty := emptyoutcome.ClassifyBody(body)
	want := map[string]emptyoutcome.Format{"chat": emptyoutcome.FormatChat,
		"messages": emptyoutcome.FormatAnthropic, "responses": emptyoutcome.FormatResponses}[protocol]
	if format == emptyoutcome.FormatUnknown || format != want || empty {
		return false
	}
	var envelope struct {
		Error   json.RawMessage          `json:"error"`
		Type    string                   `json:"type"`
		Status  string                   `json:"status"`
		Content []credentialProbeContent `json:"content"`
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					Function *struct{ Name string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Output []struct {
			Type    string                   `json:"type"`
			Name    string                   `json:"name"`
			Content []credentialProbeContent `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &envelope) != nil || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return false
	}
	switch protocol {
	case "chat":
		for _, choice := range envelope.Choices {
			var text string
			if json.Unmarshal(choice.Message.Content, &text) == nil && text != "" {
				return true
			}
			var parts []credentialProbeContent
			if json.Unmarshal(choice.Message.Content, &parts) == nil && credentialProbeText(parts) {
				return true
			}
			for _, tc := range choice.Message.ToolCalls {
				if tc.Function != nil && tc.Function.Name != "" {
					return true
				}
			}
		}
	case "messages":
		if envelope.Type != "" && envelope.Type != "message" {
			return false
		}
		if credentialProbeText(envelope.Content) {
			return true
		}
		for _, p := range envelope.Content {
			if p.Type == "tool_use" && p.Name != "" {
				return true
			}
		}
	case "responses":
		// This checks reachability and assistant output, not a complete answer:
		// a terminal token-budget stop with real text is sufficient. Reasoning
		// alone is not; failed, cancelled and unfinished executions never are.
		if envelope.Status != "" && envelope.Status != "completed" && envelope.Status != "incomplete" {
			return false
		}
		for _, item := range envelope.Output {
			if (item.Type == "message" && credentialProbeText(item.Content)) ||
				(item.Type == "function_call" && item.Name != "") {
				return true
			}
		}
	}
	return false
}
