package sanitize

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func TestSessionReplayKeepsSanitizedCacheIdentity(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	var bodies [][]byte
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
	}))
	original := `{"messages":[{"role":"user","content":"phone 13800138000"}]}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original))
		req.Header.Set("X-Gw-Session-Id", "gw_replay01")
		req = req.WithContext(WithAuthenticatedTenant(context.Background(), "tenant-replay"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Fatal("replayed history changed its sanitized identity")
	}
	diff, err := compression.BuildOutboundMessages(bodies[1], &compression.SessionState{}, bodies[0], "openai")
	if err != nil || !diff.Unchanged || diff.IsNewSess {
		t.Fatalf("replayed sanitized history misses cache: %+v %v", diff, err)
	}
}

func TestReplayedAssistantAndToolSecretsDoNotLeaveGateway(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, time.Minute)
	original := `{"messages":[{"role":"assistant","content":"13800138000","tool_calls":[{"id":"call_1","type":"function","function":{"name":"connect","arguments":"{\"host\":\"192.168.2.3\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"foo@example.com"}]}`
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		for _, secret := range []string{"13800138000", "192.168.2.3", "foo@example.com"} {
			if strings.Contains(string(body), secret) {
				t.Error("replayed sensitive history leaked")
			}
		}
		if !strings.Contains(string(body), "call_1") {
			t.Error("tool identity lost")
		}
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original)))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRecreatedMappingCannotReuseCompressedHistory(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, rdb, time.Minute)
	cache := compression.NewSessionCache(nil, nil)
	compressor := compression.NewSessionCompressor(compression.SessionCompressorDeps{Cache: cache})
	const tenant, session = "tenant-epoch", "gw_epoch01"
	oldBody := []byte(`{"messages":[{"role":"user","content":"phone 13800138000"}]}`)
	first := true
	var generation string
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		info, _ := compression.SanitizeInfoFromContext(r.Context())
		if first {
			generation = info.MapGeneration
			old := []byte(`{"messages":[{"role":"user","content":"[smm_v1:old]\nold summary"},{"role":"user","content":"phone {SENSITIVE:phone:1}"}]}`)
			err := cache.Set(r.Context(), tenant, session, &compression.SessionState{MsgCount: 2, SanitizeMapGeneration: generation}, old)
			if err != nil {
				t.Fatal(err)
			}
			// Prove the fixture would reuse this summary without the generation check.
			unguarded, err := compression.BuildOutboundMessages(body, &compression.SessionState{}, old, "openai")
			if err != nil || !strings.Contains(string(unguarded.Body), "old summary") {
				t.Fatalf("fixture does not exercise stale summary reuse: %v", err)
			}
		} else {
			if info.MapGeneration == generation {
				t.Fatal("recreated map retained its generation")
			}
			result := compressor.Prepare(r.Context(), body, tenant, session, "openai", 0, false)
			effective := result.OutboundBody
			if len(effective) == 0 {
				effective = body
			}
			if strings.Contains(string(effective), "old summary") {
				t.Fatal("expired map reused an old compressed summary")
			}
		}
	}))
	send := func() {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(oldBody)))
		req.Header.Set("X-Gw-Session-Id", session)
		req = req.WithContext(WithAuthenticatedTenant(req.Context(), tenant))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	send()
	if err := rdb.Del(context.Background(), SanitizeRedisKey(HashTenant(tenant), session), SanitizeOffsetRedisKey(HashTenant(tenant), session), sanitizeGenerationKey(tenant, session)).Err(); err != nil {
		t.Fatal(err)
	}
	first = false
	send()
}

func TestRawAndSanitizedSnapshotsKeepDistinctStageIdentity(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	original := []byte(`{"messages":[{"role":"user","content":"13800138000"},{"role":"assistant","content":"13800138000"}]}`)
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		info, ok := compression.SanitizeInfoFromContext(r.Context())
		if !ok || info.RawSnapshot != compression.SnapshotForBody(original) || info.SanitizedSnapshot != compression.SnapshotForBody(body) || info.RawSnapshot.Hash == info.SanitizedSnapshot.Hash {
			t.Fatal("raw and sanitized snapshots lost their stage identity")
		}
		if len(info.MessageRefs) != 2 || info.MessageRefs[0].PlaceholderCount != 1 || info.MessageRefs[1].PlaceholderCount != 1 {
			t.Fatal("reused placeholders lost per-message occurrence counts")
		}
		prepared := compression.NewSessionCompressor(compression.SessionCompressorDeps{}).Prepare(r.Context(), body, "tenant", "", "openai", 0, false)
		if prepared.RawSnapshot != info.RawSnapshot {
			t.Fatal("compression mislabeled sanitized body as raw")
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(original))))
}

func TestLiteralPlaceholderCannotAliasNewSensitiveValue(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	result, err := s.SanitizeInput(context.Background(), "literal {SENSITIVE:phone:1} actual 13800138000")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.SanitizeMap["{SENSITIVE:phone:1}"]; ok {
		t.Fatal("literal placeholder collided with allocated identity")
	}
	if result.SanitizeMap["{SENSITIVE:phone:2}"] != "13800138000" {
		t.Fatal("sensitive value not allocated independently")
	}
}

func TestLiteralMarkerInOtherFieldCannotAliasToolCredential(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	body := `{"messages":[{"role":"assistant","content":"literal \u007bSENSITIVE:secret:1}","tool_calls":[{"function":{"name":"connect","arguments":"{\"password\":\"new value\"}"}}]}]}`
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mapping, _ := SanitizeMapFromContext(r.Context())
		if _, collision := mapping["{SENSITIVE:secret:1}"]; collision || mapping["{SENSITIVE:secret:2}"] != "new value" {
			t.Fatal("field order aliased a literal token to a credential")
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
}

func TestCredentialArrayLeavesAreSanitized(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	body := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"arguments":"{\"password\":[\"short\",\"two words\"]}"}}]}]}`
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "short") || strings.Contains(string(body), "two words") {
			t.Fatal("array credential leaked upstream")
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
}

func TestResponseUsesRequestMappingAfterRedisGenerationChanges(t *testing.T) {
	rdb := setupSaniGuardRedis(t)
	s, _ := NewSanitizer(NewPatternDetector())
	restore, _ := NewSanitizeRestoreInterceptor(s, rdb, time.Minute)
	const tenant, session = "tenant-long", "gw_long01"
	ctx := WithSanitizeMap(context.Background(), SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"})
	ctx = compression.WithSanitizeInfo(ctx, compression.SanitizeInfo{MapGeneration: "old"})
	rdb.HSet(ctx, sanitizeMapKey(tenant, session), "{SENSITIVE:phone:1}", "13900139000")
	rdb.Set(ctx, sanitizeGenerationKey(tenant, session), "new", time.Minute)
	result, err := restore.InterceptNonStream(ctx, &response.InterceptRequest{TenantID: tenant, SessionID: session, ResponseBody: []byte(`{"choices":[{"message":{"content":"{SENSITIVE:phone:1}"}}]}`)})
	if err != nil || result == nil || result.ShouldBlock || !strings.Contains(string(result.ModifiedBody), "13800138000") || strings.Contains(string(result.ModifiedBody), "13900139000") {
		t.Fatal("long-running response used a newer mapping")
	}
	if err := rdb.Del(ctx, sanitizeMapKey(tenant, session)).Err(); err != nil {
		t.Fatal(err)
	}
	result, err = restore.InterceptNonStream(ctx, &response.InterceptRequest{TenantID: tenant, SessionID: session, ResponseBody: []byte(`{"choices":[{"message":{"content":"{SENSITIVE:phone:1}"}}]}`)})
	if err != nil || result == nil || result.ShouldBlock || !strings.Contains(string(result.ModifiedBody), "13800138000") {
		t.Fatal("mapping expiry discarded safe request fallback")
	}
}

func TestStructuredToolCredentialsPreserveEscapesAndRestore(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	original := `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"connect","arguments":"{\"password\":\"two words and \\\"quoted\\\"\",\"username\":\"ops\",\"limit\":9007199254740993}"}}]}]}`
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "two words") || strings.Contains(string(body), "\\\"ops\\\"") {
			t.Fatal("structured credential leaked")
		}
		var input map[string]any
		if err := json.Unmarshal(body, &input); err != nil {
			t.Fatal(err)
		}
		calls := input["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)
		arguments := calls[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
		if !json.Valid([]byte(arguments)) || !strings.Contains(arguments, "9007199254740993") {
			t.Fatal("tool JSON/number corrupted")
		}
		output, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": calls}}}})
		restore, _ := NewSanitizeRestoreInterceptor(s, nil, 0)
		result, err := restore.InterceptNonStream(r.Context(), &response.InterceptRequest{ResponseBody: output})
		if err != nil || result == nil || result.ShouldBlock || !json.Valid(result.ModifiedBody) || !strings.Contains(string(result.ModifiedBody), "two words") {
			t.Fatalf("tool restoration failed: %+v %v", result, err)
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(original)))
}

func TestSummaryGeneratedSensitiveTextIsMaskedBeforeRetry(t *testing.T) {
	s, _ := NewSanitizer(NewPatternDetector())
	mw, _ := NewSanitizeInputMiddleware(s, nil, 0)
	messages := []any{}
	for i := 0; i < 12; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": strings.Repeat("ordinary historical context ", 100)})
	}
	body, _ := json.Marshal(map[string]any{"messages": messages})
	rec := httptest.NewRecorder()
	mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sanitized, _ := io.ReadAll(r.Body)
		rc := compression.NewRecoveryCoordinator(compression.RecoveryDeps{Summarizer: func(context.Context, []byte, string) (string, bool) { return "summary password=NewSummarySecret", true }})
		result := rc.Recover(r.Context(), sanitized, "openai", 5000, "tenant-summary", "gw_summary01", 0)
		if !result.ShouldRetry || result.Strategy != "smart_window_llm" || strings.Contains(string(result.NewBody), "NewSummarySecret") || !strings.Contains(string(result.NewBody), "[REDACTED]") {
			t.Fatalf("generated summary not guarded: %+v", result)
		}
	})).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("middleware status=%d", rec.Code)
	}
}
