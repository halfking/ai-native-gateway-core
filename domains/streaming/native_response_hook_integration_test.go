package streaming

import (
	"context"
	"errors"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	outputhook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
)

type nativePassingChecker struct{}

func (nativePassingChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

type nativeJoinedFailureChecker struct{}

func (nativeJoinedFailureChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	if output == "topsecretAtopsecretB" {
		return nil, errors.New("joined lane check failed")
	}
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

func nativeComplianceDeltaFrame(protocol, text string) []byte {
	quoted := strconv.Quote(text)
	switch protocol {
	case "anthropic-messages":
		return []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":" + quoted + "}}\n\n")
	case "openai-responses":
		return []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":" + quoted + "}\n\n")
	default:
		return []byte("data: {\"choices\":[{\"delta\":{\"content\":" + quoted + "}}]}\n\n")
	}
}

func TestRealComplianceStreamCapAndFlushFailureRenderOneProtocolTerminal(t *testing.T) {
	for _, tc := range []struct{ protocol, terminal string }{
		{"anthropic-messages", "event: error\n"},
		{"openai-responses", "event: response.failed\n"},
		{"openai-completions", "data: [DONE]\n\n"},
	} {
		for _, failure := range []string{"pending cap", "flush check"} {
			t.Run(tc.protocol+"/"+failure, func(t *testing.T) {
				var checker interface {
					Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error)
				} = nativePassingChecker{}
				if failure == "flush check" {
					checker = nativeJoinedFailureChecker{}
				}
				rec := httptest.NewRecorder()
				writer := newInterceptingStreamWriter(rec,
					response.NewInterceptorChain(outputhook.NewOutputComplianceInterceptor(checker, nil)),
					context.Background(), response.StreamMeta{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol})
				if failure == "pending cap" {
					if _, err := writer.Write(nativeComplianceDeltaFrame(tc.protocol, strings.Repeat("sensitive", 140000))); err == nil {
						t.Fatal("pending cap did not stop oversized policy state")
					}
				} else {
					for _, fragment := range []string{"topsecretA", "topsecretB"} {
						if _, err := writer.Write(nativeComplianceDeltaFrame(tc.protocol, fragment)); err != nil {
							t.Fatalf("fragment rejected before flush: %v", err)
						}
					}
				}
				writer.finish()
				if !writer.blocked || writer.writeErr == nil || strings.Count(rec.Body.String(), tc.terminal) != 1 ||
					strings.Contains(rec.Body.String(), "sensitive") || strings.Contains(rec.Body.String(), "topsecret") {
					t.Fatalf("policy failure leaked or terminal missing: %q", rec.Body.String())
				}
			})
		}
	}
}

type nativePhoneChecker struct{}

var nativePhonePattern = regexp.MustCompile(`1[3-9][0-9]{9}`)

func (nativePhoneChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	match := nativePhonePattern.FindStringIndex(output)
	if match == nil {
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
	}
	return &outputcompliance.ComplianceResult{
		Issues:         []outputcompliance.ComplianceIssue{{Type: "pii", Subtype: "phone", Location: "char:" + strconv.Itoa(match[0]) + "-" + strconv.Itoa(match[1])}},
		RedactedOutput: output[:match[0]] + "[PHONE]" + output[match[1]:],
	}, nil
}

func TestNativeHandlersApplyRealOutputComplianceBeforeNonStreamWrite(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		write          func(*ChatHandler, *httptest.ResponseRecorder, nativeResponseInterception) []byte
	}{
		{"messages", "anthropic-messages", func(ch *ChatHandler, w *httptest.ResponseRecorder, opts nativeResponseInterception) []byte {
			return NewMessagesHandler(ch).writeNonStreamResponse(w, []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"call 13800138000"}]}`), "m", "req", 0, opts)
		}},
		{"responses", "openai-responses", func(ch *ChatHandler, w *httptest.ResponseRecorder, opts nativeResponseInterception) []byte {
			return NewResponsesHandler(ch).writeNonStreamResponse(w, []byte(`{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"call 13800138000"}]}]}`), "m", "req", opts)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil)
			ch := &ChatHandler{responseInterceptor: response.NewInterceptorChain(hook)}
			rec := httptest.NewRecorder()
			body := tc.write(ch, rec, nativeResponseInterception{
				ctx:     context.Background(),
				request: response.InterceptRequest{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol},
			})
			if rec.Code != 200 || rec.Body.String() != string(body) || strings.Contains(string(body), "13800138000") ||
				!strings.Contains(string(body), "[PHONE]") {
				t.Fatalf("real hook did not govern wire bytes: status=%d body=%q returned=%q", rec.Code, rec.Body.String(), body)
			}
		})
	}
}

func TestNativeStreamRealOutputComplianceRedactsCrossFramePhoneBeforeWire(t *testing.T) {
	for _, tc := range []struct{ protocol, event, terminal string }{
		{"anthropic-messages", "content_block_delta", "event: message_stop\n"},
		{"openai-responses", "response.output_text.delta", "event: response.completed\n"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			hook := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil)
			writer := newInterceptingStreamWriter(httptest.NewRecorder(), response.NewInterceptorChain(hook), context.Background(),
				response.StreamMeta{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol})
			rec := writer.w.(*httptest.ResponseRecorder)
			for _, fragment := range []string{"138", "001", "38000 "} {
				var frame string
				if tc.protocol == "anthropic-messages" {
					frame = "event: " + tc.event + "\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + fragment + "\"}}\n\n"
				} else {
					frame = "event: " + tc.event + "\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + fragment + "\"}\n\n"
				}
				_, _ = writer.Write([]byte(frame))
			}
			if tc.protocol == "anthropic-messages" {
				_, _ = writer.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
			} else {
				_, _ = writer.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
			}
			writer.finish()
			if got := rec.Body.String(); strings.Contains(got, "138") || strings.Contains(got, "001") ||
				strings.Contains(got, "38000") || !strings.Contains(got, "[PHONE]") || strings.Count(got, tc.terminal) != 1 {
				t.Fatalf("cross-frame PII leaked or terminal missing: %q", got)
			}
		})
	}
}

func TestRestoredSensitiveFrameIsHeldUntilRealOutputComplianceApproval(t *testing.T) {
	for _, tc := range []struct {
		protocol, first, terminal string
	}{
		{"openai-completions", "data: {\"choices\":[{\"delta\":{\"content\":\"call {SENSITIVE:phone:1}\"}}]}\n\n", "data: [DONE]\n\n"},
		{"anthropic-messages", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"call {SENSITIVE:phone:1}\"}}\n\n", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"openai-responses", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"call {SENSITIVE:phone:1}\"}\n\n", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			mini, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(mini.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			ctx := context.Background()
			const sessionID = "gw-restored-policy"
			if err := rdb.HSet(ctx, sanitize.SanitizeRedisKey(sanitize.HashTenant("tenant-1"), sessionID), "{SENSITIVE:phone:1}", "13800138000").Err(); err != nil {
				t.Fatal(err)
			}
			sanitizer, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
			if err != nil {
				t.Fatal(err)
			}
			restore, err := sanitize.NewSanitizeRestoreInterceptor(sanitizer, rdb, 30*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			chain := response.NewInterceptorChain(restore, outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil))
			rec := httptest.NewRecorder()
			writer := newInterceptingStreamWriter(rec, chain, ctx, response.StreamMeta{SessionID: sessionID, TenantID: "tenant-1", ClientProtocol: tc.protocol})
			if _, err := writer.Write([]byte(tc.first)); err != nil {
				t.Fatalf("first frame rejected: %v", err)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("restored unapproved frame leaked before terminal: %q", rec.Body.String())
			}
			if _, err := writer.Write([]byte(tc.terminal)); err != nil {
				t.Fatalf("terminal frame rejected: %v", err)
			}
			writer.finish()
			got := rec.Body.String()
			if writer.writeErr != nil || strings.Contains(got, "13800138000") || strings.Contains(got, "{SENSITIVE:") || !strings.Contains(got, "[PHONE]") {
				t.Fatalf("restored output was not safely released: err=%v wire=%q", writer.writeErr, got)
			}
		})
	}
}
